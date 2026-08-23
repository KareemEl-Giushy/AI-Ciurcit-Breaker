package inspector

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestRealtimeResponseReader_SSE(t *testing.T) {
	sseData := `data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}

data: [DONE]

`
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	src := io.NopCloser(strings.NewReader(sseData))
	reader := NewRealtimeResponseReader(src, "text/event-stream", logger, "/v1/chat/completions")

	var completedRecord RecordedResponse
	reader.SetOnCompleteCallback(func(r RecordedResponse) {
		completedRecord = r
	})

	buf := make([]byte, 64)
	var output bytes.Buffer
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			output.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
	}
	_ = reader.Close()

	if output.String() != sseData {
		t.Errorf("expected output to match sseData exactly")
	}

	if completedRecord.Content != "Hello world!" {
		t.Errorf("expected completed record content 'Hello world!', got '%s'", completedRecord.Content)
	}
	if completedRecord.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got '%s'", completedRecord.Model)
	}
	if !completedRecord.Streamed {
		t.Errorf("expected streamed to be true")
	}
}

func TestRealtimeResponseReader_SSE_ToolCalls(t *testing.T) {
	sseData := `data: {"id":"chatcmpl-2","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"Tokyo\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	src := io.NopCloser(strings.NewReader(sseData))
	reader := NewRealtimeResponseReader(src, "text/event-stream", logger, "/v1/chat/completions")

	var completedRecord RecordedResponse
	reader.SetOnCompleteCallback(func(r RecordedResponse) {
		completedRecord = r
	})

	_, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	_ = reader.Close()

	if len(completedRecord.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(completedRecord.ToolCalls))
	}
	if completedRecord.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected function name 'get_weather', got '%s'", completedRecord.ToolCalls[0].Function.Name)
	}
	if completedRecord.ToolCalls[0].Function.Arguments != `{"location":"Tokyo"}` {
		t.Errorf("expected function arguments '{\"location\":\"Tokyo\"}', got '%s'", completedRecord.ToolCalls[0].Function.Arguments)
	}
}

func TestRealtimeResponseReader_JSON_ToolCalls(t *testing.T) {
	jsonData := `{
		"id": "chatcmpl-3",
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_abc999",
					"type": "function",
					"function": {
						"name": "search_database",
						"arguments": "{\"query\": \"users\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	src := io.NopCloser(strings.NewReader(jsonData))
	reader := NewRealtimeResponseReader(src, "application/json", logger, "/v1/chat/completions")

	var completedRecord RecordedResponse
	reader.SetOnCompleteCallback(func(r RecordedResponse) {
		completedRecord = r
	})

	_, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	_ = reader.Close()

	if len(completedRecord.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(completedRecord.ToolCalls))
	}
	if completedRecord.ToolCalls[0].Function.Name != "search_database" {
		t.Errorf("expected function name 'search_database', got '%s'", completedRecord.ToolCalls[0].Function.Name)
	}
}

func TestPrintConversation_LastInteraction(t *testing.T) {
	// 4 messages: system, user1, assistant (tool_calls), tool output
	req := &OpenAIRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Initial greeting."},
			{
				Role:    "assistant",
				Content: nil,
				ToolCalls: []ToolCall{
					{
						ID:   "call_lon1",
						Type: "function",
						Function: FunctionCall{
							Name:      "fetch_weather",
							Arguments: `{"city":"London"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_lon1", Content: `{"temp":"18C"}`},
		},
	}

	// Verify PrintConversation executes cleanly to stdout without errors
	PrintConversation(req, "/v1/chat/completions")

	startIdx, recent := getLastInteractionMessages(req.Messages)
	if startIdx != 2 {
		t.Errorf("expected startIdx 2, got %d", startIdx)
	}
	if len(recent) != 2 {
		t.Errorf("expected 2 recent messages, got %d", len(recent))
	}
}
