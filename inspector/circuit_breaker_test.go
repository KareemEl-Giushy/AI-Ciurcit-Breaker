package inspector

import (
	"testing"
	"time"
)

func TestComputeToolFingerprint(t *testing.T) {
	// Canonical JSON normalization test: different whitespace and key ordering should produce identical hash
	args1 := `{"city": "Tokyo", "unit": "celsius"}`
	args2 := `{"unit":"celsius","city":"Tokyo"}`

	fp1 := ComputeToolFingerprint("get_weather", args1)
	fp2 := ComputeToolFingerprint("get_weather", args2)

	if fp1 != fp2 {
		t.Errorf("expected identical fingerprints for normalized JSON args, got %s vs %s", fp1, fp2)
	}

	fp3 := ComputeToolFingerprint("get_weather", `{"city": "Paris"}`)
	if fp1 == fp3 {
		t.Errorf("expected different fingerprints for different args")
	}
}

func TestSimHash(t *testing.T) {
	// Exact match -> Hamming distance 0
	h1 := ComputeSimHash("get_weather", `{"city": "Tokyo", "unit": "celsius"}`)
	h2 := ComputeSimHash("get_weather", `{"unit": "celsius", "city": "Tokyo"}`)
	if dist := HammingDistance(h1, h2); dist != 0 {
		t.Errorf("expected Hamming distance 0 for canonical match, got %d", dist)
	}

	// Minor perturbation (mutating prompt slightly: attempt 0 vs attempt 1)
	prompt1 := `Let me try again (attempt 0) to search database for user records in production`
	prompt2 := `Let me try again (attempt 1) to search database for user records in production`
	sh1 := ComputeSimHash("search_db", prompt1)
	sh2 := ComputeSimHash("search_db", prompt2)

	dist := HammingDistance(sh1, sh2)
	sim := SimHashSimilarity(sh1, sh2)

	if dist > 4 {
		t.Errorf("expected small Hamming distance (<= 4) for slightly mutated prompt, got %d (sim: %f)", dist, sim)
	}

	similar, d, s := AreToolCallsSimilarSimHash("search_db", prompt1, "search_db", prompt2, 4)
	if !similar {
		t.Errorf("expected tool calls to be similar via SimHash, dist: %d, sim: %f", d, s)
	}

	// Completely different tool and query -> large Hamming distance
	diffToolHash := ComputeSimHash("delete_all_records", `{"confirm": true}`)
	if distDiff := HammingDistance(sh1, diffToolHash); distDiff < 10 {
		t.Errorf("expected large Hamming distance for completely different tool call, got %d", distDiff)
	}
}

func TestJaccardSimilarity(t *testing.T) {
	// Exact match
	if score := JaccardSimilarity(`{"query": "find users"}`, `{"query": "find users"}`); score != 1.0 {
		t.Errorf("expected 1.0 for identical strings, got %f", score)
	}

	// Completely disjoint
	if score := JaccardSimilarity("abcdef", "123456"); score != 0.0 {
		t.Errorf("expected 0.0 for disjoint strings, got %f", score)
	}

	// Near duplicate with minor perturbation
	s1 := `{"query": "search for latest devops logs in us-east-1 cluster"}`
	s2 := `{"query": "search for latest devops logs in us-east-2 cluster"}`
	score := JaccardSimilarity(s1, s2)
	if score < 0.80 {
		t.Errorf("expected high similarity (>= 0.80) for near duplicates, got %f", score)
	}

	// AreToolCallsSimilar helper
	similar, s := AreToolCallsSimilar("search_logs", s1, "search_logs", s2, 0.80)
	if !similar {
		t.Errorf("expected tool calls to be similar, score: %f", s)
	}

	// Different tools should never be similar
	differentTool, _ := AreToolCallsSimilar("search_logs", s1, "delete_logs", s1, 0.50)
	if differentTool {
		t.Errorf("expected different tool names not to be similar")
	}
}

func TestCircuitBreaker_ClassA_IdenticalToolCall(t *testing.T) {
	cb := NewCircuitBreakerEngine(CircuitBreakerConfig{
		WindowDuration:     1 * time.Minute,
		ClassAMaxIdentical: 3,
		Enabled:            true,
	})

	tool := "search_db"
	args := `{"query": "SELECT * FROM users"}`

	// 1st call
	incident, tripped := cb.RecordToolCall(tool, args, false, "")
	if tripped || incident != nil {
		t.Fatalf("expected call 1 not to trip circuit breaker")
	}

	// 2nd call
	incident, tripped = cb.RecordToolCall(tool, args, false, "")
	if tripped || incident != nil {
		t.Fatalf("expected call 2 not to trip circuit breaker")
	}

	// 3rd call -> should trip Class A!
	incident, tripped = cb.RecordToolCall(tool, args, false, "")
	if !tripped || incident == nil {
		t.Fatalf("expected call 3 to trip Class A circuit breaker")
	}

	if incident.FailureClass != ClassAIdenticalLoop {
		t.Errorf("expected failure class %s, got %s", ClassAIdenticalLoop, incident.FailureClass)
	}
	if incident.ObservedCount != 3 {
		t.Errorf("expected observed count 3, got %d", incident.ObservedCount)
	}
}

func TestCircuitBreaker_ClassA_SimHash_NearDuplicateLoop(t *testing.T) {
	cb := NewCircuitBreakerEngine(CircuitBreakerConfig{
		WindowDuration:     1 * time.Minute,
		ClassAMaxIdentical: 3,
		MaxHammingDistance: 3,
		Enabled:            true,
	})

	tool := "fetch_logs"
	// 3 slight mutations that LLMs generate in loops
	args1 := `{"prompt": "Please fetch the latest container logs for service auth-service (attempt 1)"}`
	args2 := `{"prompt": "Please fetch the latest container logs for service auth-service (attempt 2)"}`
	args3 := `{"prompt": "Please fetch the latest container logs for service auth-service (attempt 3)"}`

	// 1st call
	_, tripped := cb.RecordToolCall(tool, args1, false, "")
	if tripped {
		t.Fatalf("expected call 1 not to trip")
	}

	// 2nd call
	_, tripped = cb.RecordToolCall(tool, args2, false, "")
	if tripped {
		t.Fatalf("expected call 2 not to trip")
	}

	// 3rd call -> should trip Class A via SimHash LSH!
	incident, tripped := cb.RecordToolCall(tool, args3, false, "")
	if !tripped || incident == nil {
		t.Fatalf("expected call 3 to trip Class A circuit breaker via SimHash")
	}

	if incident.FailureClass != ClassAIdenticalLoop {
		t.Errorf("expected %s, got %s", ClassAIdenticalLoop, incident.FailureClass)
	}
	if incident.ObservedCount != 3 {
		t.Errorf("expected observed count 3, got %d", incident.ObservedCount)
	}
	if incident.SimHashHex == "" {
		t.Errorf("expected non-empty SimHashHex in incident")
	}
}

func TestCircuitBreaker_ClassA_JaccardSimilarity_NearDuplicateLoop(t *testing.T) {
	cb := NewCircuitBreakerEngine(CircuitBreakerConfig{
		WindowDuration:     1 * time.Minute,
		ClassAMaxIdentical: 3,
		JaccardThreshold:   0.80,
		Enabled:            true,
	})

	tool := "search_db"
	args1 := `{"query": "SELECT id, name FROM users WHERE active = 1"}`
	args2 := `{"query": "SELECT id, name FROM users WHERE active = 2"}`
	args3 := `{"query": "SELECT id, name FROM users WHERE active = 3"}`

	// 1st call
	_, tripped := cb.RecordToolCall(tool, args1, false, "")
	if tripped {
		t.Fatalf("expected call 1 not to trip")
	}

	// 2nd call
	_, tripped = cb.RecordToolCall(tool, args2, false, "")
	if tripped {
		t.Fatalf("expected call 2 not to trip")
	}

	// 3rd call -> should trip Class A on near duplicate repetition!
	incident, tripped := cb.RecordToolCall(tool, args3, false, "")
	if !tripped || incident == nil {
		t.Fatalf("expected call 3 to trip Class A circuit breaker via Jaccard similarity")
	}

	if incident.FailureClass != ClassAIdenticalLoop {
		t.Errorf("expected %s, got %s", ClassAIdenticalLoop, incident.FailureClass)
	}
	if incident.ObservedCount != 3 {
		t.Errorf("expected observed count 3, got %d", incident.ObservedCount)
	}
	if incident.SimilarityScore < 0.80 {
		t.Errorf("expected similarity score >= 0.80, got %f", incident.SimilarityScore)
	}
}

func TestCircuitBreaker_ClassB_ErrorAccumulation(t *testing.T) {
	cb := NewCircuitBreakerEngine(CircuitBreakerConfig{
		WindowDuration:  1 * time.Minute,
		ClassBMaxErrors: 3,
		Enabled:         true,
	})

	// 1st error
	_, tripped := cb.RecordToolCall("tool1", `{"a":1}`, true, "timeout")
	if tripped {
		t.Fatalf("expected error 1 not to trip")
	}

	// 2nd error
	_, tripped = cb.RecordToolCall("tool2", `{"b":2}`, true, "permission denied")
	if tripped {
		t.Fatalf("expected error 2 not to trip")
	}

	// 3rd error -> trips Class B!
	incident, tripped := cb.RecordToolCall("tool3", `{"c":3}`, true, "connection refused")
	if !tripped || incident == nil {
		t.Fatalf("expected error 3 to trip Class B circuit breaker")
	}

	if incident.FailureClass != ClassBErrorAccumulation {
		t.Errorf("expected failure class %s, got %s", ClassBErrorAccumulation, incident.FailureClass)
	}
	if incident.ObservedCount != 3 {
		t.Errorf("expected observed count 3, got %d", incident.ObservedCount)
	}
}

func TestCircuitBreaker_CheckRequestHistory(t *testing.T) {
	cb := NewCircuitBreakerEngine(CircuitBreakerConfig{
		ClassAMaxIdentical: 3,
		ClassBMaxErrors:    3,
		MaxHammingDistance: 4,
		JaccardThreshold:   0.80,
		Enabled:            true,
	})

	// Case 1: 3 near-duplicate tool calls in conversation history (SimHash / Jaccard similarity)
	messagesWithClassA := []ChatMessage{
		{Role: "user", Content: "Run search."},
		{Role: "assistant", ToolCalls: []ToolCall{{Function: FunctionCall{Name: "search", Arguments: `{"query":"search for all active servers in us-east-1 datacenter"}`}}}},
		{Role: "tool", Content: `{"result": "none"}`},
		{Role: "assistant", ToolCalls: []ToolCall{{Function: FunctionCall{Name: "search", Arguments: `{"query":"search for all active servers in us-east-2 datacenter"}`}}}},
		{Role: "tool", Content: `{"result": "none"}`},
		{Role: "assistant", ToolCalls: []ToolCall{{Function: FunctionCall{Name: "search", Arguments: `{"query":"search for all active servers in us-east-3 datacenter"}`}}}},
	}

	incident, tripped := cb.CheckRequestHistory(messagesWithClassA)
	if !tripped || incident == nil {
		t.Fatalf("expected Class A trip in history check with SimHash/Jaccard similarity")
	}
	if incident.FailureClass != ClassAIdenticalLoop {
		t.Errorf("expected ClassAIdenticalLoop, got %s", incident.FailureClass)
	}

	// Case 2: 3 error tool responses in conversation history
	messagesWithClassB := []ChatMessage{
		{Role: "user", Content: "Start process."},
		{Role: "tool", Content: `{"status":"error", "message":"connection timeout"}`},
		{Role: "tool", Content: `{"error":"permission denied"}`},
		{Role: "tool", Content: `{"failed": true, "reason":"resource locked"}`},
	}

	incidentB, trippedB := cb.CheckRequestHistory(messagesWithClassB)
	if !trippedB || incidentB == nil {
		t.Fatalf("expected Class B trip in history check")
	}
	if incidentB.FailureClass != ClassBErrorAccumulation {
		t.Errorf("expected ClassBErrorAccumulation, got %s", incidentB.FailureClass)
	}
}
