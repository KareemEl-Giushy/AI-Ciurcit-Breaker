package inspector

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestSaveConversationRecord(t *testing.T) {
	tempDir := t.TempDir()

	record := &ConversationRecord{
		Path:       "/v1/chat/completions",
		ClientIP:   "127.0.0.1:41730",
		DurationMs: 250,
		StatusCode: 200,
		Request: &OpenAIRequest{
			Model: "gpt-4",
			Messages: []ChatMessage{
				{Role: "user", Content: "Hello AI"},
			},
		},
		Response: RecordedResponse{
			Model:    "gpt-4",
			Content:  "Hello Human!",
			Streamed: false,
		},
	}

	savedPath, err := SaveConversationRecord(record, tempDir)
	if err != nil {
		t.Fatalf("failed to save conversation record: %v", err)
	}

	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}

	var readRecord ConversationRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("failed to unmarshal saved JSON: %v", err)
	}

	if readRecord.ClientIP != "127.0.0.1" {
		t.Errorf("expected clean client IP '127.0.0.1', got '%s'", readRecord.ClientIP)
	}
	if readRecord.TurnCount != 1 {
		t.Errorf("expected turn_count 1, got %d", readRecord.TurnCount)
	}
	if len(readRecord.Turns) != 1 {
		t.Fatalf("expected 1 turn in turns array, got %d", len(readRecord.Turns))
	}
	if readRecord.Turns[0].Response.Content != "Hello Human!" {
		t.Errorf("expected response 'Hello Human!', got '%s'", readRecord.Turns[0].Response.Content)
	}
}

func TestConversationStorageManager_MultiTurnSameClientIP(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewConversationStorageManager(tempDir, 10*time.Minute)

	// Turn 1 from client IP 192.168.1.50 with ephemeral port 45100
	req1 := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "What is the capital of France?"},
		},
	}
	resp1 := RecordedResponse{
		Model:   "gpt-4",
		Content: "The capital of France is Paris.",
		Entropy: 4.5,
	}

	path1, err := mgr.RecordTurn("192.168.1.50:45100", "/v1/chat/completions", 150, 200, req1, resp1)
	if err != nil {
		t.Fatalf("failed to record turn 1: %v", err)
	}

	// Turn 2 from SAME client IP with a NEW ephemeral port 45102 (even if messages didn't specify older conversation)
	req2 := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "And what is its population?"},
		},
	}
	resp2 := RecordedResponse{
		Model:   "gpt-4",
		Content: "The population of Paris is approximately 2.1 million.",
		Entropy: 4.2,
	}

	path2, err := mgr.RecordTurn("192.168.1.50:45102", "/v1/chat/completions", 180, 200, req2, resp2)
	if err != nil {
		t.Fatalf("failed to record turn 2: %v", err)
	}

	// Turn 3 from SAME client IP with yet another ephemeral port 45104
	req3 := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Tell me a fun fact about it."},
		},
	}
	resp3 := RecordedResponse{
		Model:   "gpt-4",
		Content: "Paris has only one stop sign in the entire city!",
		Entropy: 4.6,
	}

	path3, err := mgr.RecordTurn("192.168.1.50:45104", "/v1/chat/completions", 210, 200, req3, resp3)
	if err != nil {
		t.Fatalf("failed to record turn 3: %v", err)
	}

	// Verify that ALL 3 turns were written to the EXACT SAME file path!
	if path1 != path2 {
		t.Fatalf("expected path 1 (%s) to match path 2 (%s)", path1, path2)
	}
	if path2 != path3 {
		t.Fatalf("expected path 2 (%s) to match path 3 (%s)", path2, path3)
	}

	// Read and verify file contents
	data, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("failed to read conversation file: %v", err)
	}

	var rec ConversationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("failed to unmarshal conversation record: %v", err)
	}

	if rec.ClientIP != "192.168.1.50" {
		t.Errorf("expected ClientIP '192.168.1.50', got '%s'", rec.ClientIP)
	}
	if rec.TurnCount != 3 {
		t.Errorf("expected TurnCount 3, got %d", rec.TurnCount)
	}
	if len(rec.Turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(rec.Turns))
	}

	// Check turn details
	if rec.Turns[0].TurnIndex != 1 || rec.Turns[0].Request.Messages[0].Content != "What is the capital of France?" {
		t.Errorf("unexpected turn 1 data: %+v", rec.Turns[0])
	}
	if rec.Turns[1].TurnIndex != 2 || rec.Turns[1].Request.Messages[0].Content != "And what is its population?" {
		t.Errorf("unexpected turn 2 data: %+v", rec.Turns[1])
	}
	if rec.Turns[2].TurnIndex != 3 || rec.Turns[2].Response.Content != "Paris has only one stop sign in the entire city!" {
		t.Errorf("unexpected turn 3 data: %+v", rec.Turns[2])
	}
}

func TestConversationStorageManager_DifferentClientsDifferentFiles(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewConversationStorageManager(tempDir, 10*time.Minute)

	req := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello from client"},
		},
	}
	resp := RecordedResponse{
		Model:   "gpt-4",
		Content: "Hello!",
	}

	// Client A
	pathA, err := mgr.RecordTurn("10.0.0.1:40001", "/v1/chat/completions", 100, 200, req, resp)
	if err != nil {
		t.Fatalf("client A failed: %v", err)
	}

	// Client B
	pathB, err := mgr.RecordTurn("10.0.0.2:40002", "/v1/chat/completions", 100, 200, req, resp)
	if err != nil {
		t.Fatalf("client B failed: %v", err)
	}

	if pathA == pathB {
		t.Fatalf("expected different files for different client IPs, got same path: %s", pathA)
	}
}

func TestConversationStorageManager_SessionExpiry(t *testing.T) {
	tempDir := t.TempDir()
	// Short session timeout of 20ms
	mgr := NewConversationStorageManager(tempDir, 20*time.Millisecond)

	req := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Turn 1"},
		},
	}
	resp := RecordedResponse{
		Model:   "gpt-4",
		Content: "Response 1",
	}

	path1, err := mgr.RecordTurn("192.168.1.99:50000", "/v1/chat/completions", 100, 200, req, resp)
	if err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	// Wait for session to expire
	time.Sleep(30 * time.Millisecond)

	// Turn 2 after expiry
	req2 := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Turn 2 new session"},
		},
	}
	resp2 := RecordedResponse{
		Model:   "gpt-4",
		Content: "Response 2",
	}

	path2, err := mgr.RecordTurn("192.168.1.99:50001", "/v1/chat/completions", 100, 200, req2, resp2)
	if err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	if path1 == path2 {
		t.Fatalf("expected different files after session timeout, got same path: %s", path1)
	}
}
