package inspector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveConversationRecord(t *testing.T) {
	tempDir := t.TempDir()

	record := &ConversationRecord{
		ID:         "test_conv_123",
		Timestamp:  time.Now(),
		Path:       "/v1/chat/completions",
		ClientIP:   "127.0.0.1",
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

	expectedFile := filepath.Join(tempDir, "test_conv_123.json")
	if savedPath != expectedFile {
		t.Errorf("expected path %s, got %s", expectedFile, savedPath)
	}

	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}

	var readRecord ConversationRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("failed to unmarshal saved JSON: %v", err)
	}

	if readRecord.ID != "test_conv_123" {
		t.Errorf("expected ID 'test_conv_123', got '%s'", readRecord.ID)
	}
	if readRecord.Response.Content != "Hello Human!" {
		t.Errorf("expected response 'Hello Human!', got '%s'", readRecord.Response.Content)
	}
	if len(readRecord.Request.Messages) != 1 {
		t.Errorf("expected 1 request message, got %d", len(readRecord.Request.Messages))
	}
}
