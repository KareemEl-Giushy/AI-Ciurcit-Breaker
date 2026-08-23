package inspector

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecordedResponse captures the model's output (text and tool calls).
type RecordedResponse struct {
	Model     string     `json:"model,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Streamed  bool       `json:"streamed"`
	Entropy   float64    `json:"entropy,omitempty"`
}

// ConversationRecord captures the entire request/response transaction in structured format.
type ConversationRecord struct {
	ID         string           `json:"id"`
	Timestamp  time.Time        `json:"timestamp"`
	Path       string           `json:"path"`
	ClientIP   string           `json:"client_ip"`
	DurationMs int64            `json:"duration_ms"`
	StatusCode int              `json:"status_code"`
	Request    *OpenAIRequest   `json:"request"`
	Response   RecordedResponse `json:"response"`
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("conv_%s_%s", time.Now().Format("20060102_150405"), hex.EncodeToString(b))
}

// SaveConversationRecord writes a ConversationRecord to an indented JSON file in the specified directory.
func SaveConversationRecord(record *ConversationRecord, dir string) (string, error) {
	if record == nil {
		return "", fmt.Errorf("cannot save nil conversation record")
	}

	if dir == "" {
		dir = "./conversations"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if record.ID == "" {
		record.ID = generateID()
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}

	filePath := filepath.Join(dir, fmt.Sprintf("%s.json", record.ID))

	jsonData, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal conversation record: %w", err)
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", filePath, err)
	}

	return filePath, nil
}
