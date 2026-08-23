package inspector

import (
	"sync"
	"testing"
	"time"
)

func TestParseOpenAIJSON_Valid(t *testing.T) {
	jsonData := []byte(`{
		"model": "gpt-4",
		"temperature": 0.7,
		"max_tokens": 150,
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "What is the weather in Cairo?"}
		]
	}`)

	req, err := ParseOpenAIJSON(jsonData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got '%s'", req.Model)
	}
	if len(req.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "You are a helpful assistant." {
		t.Errorf("unexpected message 0: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "What is the weather in Cairo?" {
		t.Errorf("unexpected message 1: %+v", req.Messages[1])
	}

	tokens := req.EstimateTokens()
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestParseOpenAIJSON_Invalid(t *testing.T) {
	invalidJSON := []byte(`{invalid json`)
	_, err := ParseOpenAIJSON(invalidJSON)
	if err == nil {
		t.Errorf("expected error for invalid json")
	}

	emptyStructure := []byte(`{"other_field": "test"}`)
	_, err = ParseOpenAIJSON(emptyStructure)
	if err == nil {
		t.Errorf("expected error for non-openai structure")
	}
}

func TestSlidingWindow_Eviction(t *testing.T) {
	windowDuration := 50 * time.Millisecond
	sw := NewSlidingWindow(SlidingWindowConfig{
		WindowDuration: windowDuration,
	})

	req := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello world"},
		},
	}

	stats, allowed := sw.Record(req, "127.0.0.1")
	if !allowed {
		t.Fatalf("expected allowed=true")
	}
	if stats.TotalRequests != 1 {
		t.Errorf("expected 1 total request, got %d", stats.TotalRequests)
	}

	// Sleep longer than the window duration
	time.Sleep(70 * time.Millisecond)

	currentStats := sw.GetStats()
	if currentStats.TotalRequests != 0 {
		t.Errorf("expected 0 requests after window expiry, got %d", currentStats.TotalRequests)
	}
}

func TestSlidingWindow_EnforceLimits(t *testing.T) {
	sw := NewSlidingWindow(SlidingWindowConfig{
		WindowDuration: 1 * time.Second,
		MaxRequests:    2,
		EnforceLimits:  true,
	})

	req := &OpenAIRequest{Model: "gpt-4"}

	// 1st request
	_, allowed1 := sw.Record(req, "127.0.0.1")
	if !allowed1 {
		t.Errorf("expected request 1 to be allowed")
	}

	// 2nd request
	_, allowed2 := sw.Record(req, "127.0.0.1")
	if !allowed2 {
		t.Errorf("expected request 2 to be allowed")
	}

	// 3rd request should be blocked
	stats3, allowed3 := sw.Record(req, "127.0.0.1")
	if allowed3 {
		t.Errorf("expected request 3 to be rejected")
	}
	if !stats3.LimitExceeded {
		t.Errorf("expected LimitExceeded to be true")
	}
}

func TestSlidingWindow_Concurrency(t *testing.T) {
	sw := NewSlidingWindow(SlidingWindowConfig{
		WindowDuration: 1 * time.Second,
	})

	req := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Concurreny test"},
		},
	}

	var wg sync.WaitGroup
	workers := 20
	requestsPerWorker := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < requestsPerWorker; j++ {
				sw.Record(req, "127.0.0.1")
			}
		}()
	}

	wg.Wait()

	stats := sw.GetStats()
	expectedRequests := workers * requestsPerWorker
	if stats.TotalRequests != expectedRequests {
		t.Errorf("expected %d requests, got %d", expectedRequests, stats.TotalRequests)
	}
}
