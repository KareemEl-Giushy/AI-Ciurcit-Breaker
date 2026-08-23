package inspector

import (
	"encoding/json"
	"fmt"
	"strings"

	"circuit-breaker-proxy/utils"
)

// FunctionCall represents a legacy or nested function call.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// ChatMessage represents a single message in an OpenAI chat completion conversation.
type ChatMessage struct {
	Role         string        `json:"role"`
	Content      any           `json:"content"` // string, []any (multimodal), or null
	Name         string        `json:"name,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
}

// ContentString returns a string representation of the message content.
func (m *ChatMessage) ContentString() string {
	if m.Content == nil {
		return ""
	}
	switch c := m.Content.(type) {
	case string:
		return c
	default:
		b, err := json.Marshal(c)
		if err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", c)
	}
}

// OpenAIRequest represents a standard OpenAI-compliant JSON request payload.
type OpenAIRequest struct {
	Model            string        `json:"model"`
	Messages         []ChatMessage `json:"messages,omitempty"`
	Prompt           any           `json:"prompt,omitempty"` // string or []string
	Tools            []any         `json:"tools,omitempty"`
	Functions        []any         `json:"functions,omitempty"`
	Temperature      *float64      `json:"temperature,omitempty"`
	TopP             *float64      `json:"top_p,omitempty"`
	N                *int          `json:"n,omitempty"`
	Stream           *bool         `json:"stream,omitempty"`
	MaxTokens        *int          `json:"max_tokens,omitempty"`
	PresencePenalty  *float64      `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64      `json:"frequency_penalty,omitempty"`
	User             string        `json:"user,omitempty"`
}

// ParseOpenAIJSON parses a raw JSON byte slice into an OpenAIRequest.
func ParseOpenAIJSON(data []byte) (*OpenAIRequest, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty payload")
	}

	var req OpenAIRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Basic sanity check to ensure it has OpenAI-like structure
	if req.Model == "" && len(req.Messages) == 0 && req.Prompt == nil {
		return nil, fmt.Errorf("payload does not contain recognizable OpenAI request fields")
	}

	return &req, nil
}

// EstimateTokens calculates an approximate token count for the request payload.
func (req *OpenAIRequest) EstimateTokens() int {
	if req == nil {
		return 0
	}

	totalChars := 0

	for _, msg := range req.Messages {
		contentStr := msg.ContentString()
		totalChars += len(msg.Role) + len(msg.Name) + len(contentStr) + len(msg.ToolCallID) + 4
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.ID) + len(tc.Function.Name) + len(tc.Function.Arguments) + 8
		}
	}

	if req.Prompt != nil {
		switch p := req.Prompt.(type) {
		case string:
			totalChars += len(p)
		case []any:
			for _, item := range p {
				totalChars += len(fmt.Sprint(item))
			}
		case []string:
			for _, item := range p {
				totalChars += len(item)
			}
		}
	}

	estimatedTokens := totalChars / 4
	if estimatedTokens == 0 && totalChars > 0 {
		estimatedTokens = 1
	}

	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		estimatedTokens += *req.MaxTokens
	}

	return estimatedTokens
}

// getLastInteractionMessages extracts only the messages belonging to the most recent turn.
// If the conversation ends with tool outputs, it includes the assistant call that prompted them.
// If it ends with a user message, it returns the user message.
func getLastInteractionMessages(messages []ChatMessage) (int, []ChatMessage) {
	n := len(messages)
	if n <= 1 {
		return 0, messages
	}

	lastRole := strings.ToLower(messages[n-1].Role)

	// If the last message is a tool/function response, find the assistant message that made the tool calls
	if lastRole == "tool" || lastRole == "function" {
		startIdx := n - 1
		for i := n - 1; i >= 0; i-- {
			if strings.ToLower(messages[i].Role) == "assistant" && (len(messages[i].ToolCalls) > 0 || messages[i].FunctionCall != nil) {
				startIdx = i
				break
			}
		}
		return startIdx, messages[startIdx:]
	}

	// Otherwise, return just the last message (e.g. user prompt)
	return n - 1, messages[n-1:]
}

// PrintConversation formats and pretty-prints the recent interaction turn to stdout.
func PrintConversation(req *OpenAIRequest, path string) {
	if req == nil || len(req.Messages) == 0 {
		return
	}

	startIdx, recentMessages := getLastInteractionMessages(req.Messages)
	hiddenCount := startIdx

	printMsgs := make([]utils.PrintMessage, len(recentMessages))
	for i, m := range recentMessages {
		var toolCalls []utils.PrintToolCall
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, utils.PrintToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}

		funcName := ""
		funcArgs := ""
		if m.FunctionCall != nil {
			funcName = m.FunctionCall.Name
			funcArgs = m.FunctionCall.Arguments
		}

		printMsgs[i] = utils.PrintMessage{
			Role:         m.Role,
			Content:      m.ContentString(),
			ToolCallID:   m.ToolCallID,
			ToolCalls:    toolCalls,
			FunctionName: funcName,
			FunctionArgs: funcArgs,
		}
	}

	utils.PrintConversation(path, req.Model, hiddenCount, printMsgs)
}
