package inspector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"circuit-breaker-proxy/utils"
)

// ChatCompletionChoice represents a choice in a non-streaming OpenAI chat completion response.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatCompletionResponse represents a standard OpenAI non-streaming chat response.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
}

// ChunkDelta represents the delta message content in a streaming chunk.
type ChunkDelta struct {
	Role         string        `json:"role,omitempty"`
	Content      string        `json:"content,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
}

// ChunkChoice represents a choice in an OpenAI streaming chunk.
type ChunkChoice struct {
	Index        int        `json:"index"`
	Delta        ChunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

// ChatCompletionChunk represents an SSE chunk from an OpenAI streaming response.
type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

// RealtimeResponseReader wraps an io.ReadCloser (upstream response body)
// to stream and pretty-print LLM text and tool calls directly to stdout in real-time,
// calculate zero-allocation Shannon token/byte entropy on the fly,
// enforce active circuit breaking (tool repetition loops / error accumulation),
// and invoke a callback upon completion with the captured response.
type RealtimeResponseReader struct {
	src               io.ReadCloser
	contentType       string
	logger            *slog.Logger
	requestPath       string
	circuitBreaker    *CircuitBreakerEngine
	showSlidingWindow bool
	onComplete        func(RecordedResponse)

	mu              sync.Mutex
	isSSE           bool
	lineBuffer      bytes.Buffer
	fullBuffer      bytes.Buffer
	accumulatedSSE  strings.Builder
	toolCallsAcc    map[int]*ToolCall
	streamStarted   bool
	modelName       string
	entropy         EntropyCalculator
	trippedIncident *SREIncident
	injectedError   []byte
	injectedRead    bool
	completed       bool
	closed          bool
}

// NewRealtimeResponseReader creates a new RealtimeResponseReader with pretty console streaming and circuit breaker protection.
func NewRealtimeResponseReader(src io.ReadCloser, contentType string, logger *slog.Logger, requestPath string) *RealtimeResponseReader {
	if logger == nil {
		logger = slog.Default()
	}

	isSSE := strings.Contains(strings.ToLower(contentType), "text/event-stream")

	return &RealtimeResponseReader{
		src:          src,
		contentType:  contentType,
		logger:       logger,
		requestPath:  requestPath,
		isSSE:        isSSE,
		toolCallsAcc: make(map[int]*ToolCall),
		entropy:      NewEntropyCalculator(),
	}
}

// SetCircuitBreaker attaches the CircuitBreakerEngine for active stream termination.
func (r *RealtimeResponseReader) SetCircuitBreaker(cb *CircuitBreakerEngine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.circuitBreaker = cb
}

// SetShowSlidingWindow sets whether to suppress console streaming in favor of sliding window display.
func (r *RealtimeResponseReader) SetShowSlidingWindow(show bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.showSlidingWindow = show
}

// SetOnCompleteCallback registers a callback to receive the structured RecordedResponse.
func (r *RealtimeResponseReader) SetOnCompleteCallback(cb func(RecordedResponse)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onComplete = cb
}

// Read reads from the underlying response body, updates entropy, and intercepts circuit breaker trips.
func (r *RealtimeResponseReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	// If circuit breaker injected a terminal SRE error event, serve it
	if r.injectedError != nil && !r.injectedRead {
		r.injectedRead = true
		n := copy(p, r.injectedError)
		r.mu.Unlock()
		return n, nil
	}
	if r.trippedIncident != nil {
		r.mu.Unlock()
		return 0, io.EOF
	}
	r.mu.Unlock()

	n, err := r.src.Read(p)
	if n > 0 {
		r.mu.Lock()
		chunk := p[:n]
		r.fullBuffer.Write(chunk)
		r.entropy.AddBytes(chunk)

		if r.isSSE {
			r.processSSEChunk(chunk)
		}
		r.mu.Unlock()
	}

	if err == io.EOF {
		r.mu.Lock()
		r.onCompleteHandler()
		r.mu.Unlock()
	}

	return n, err
}

func (r *RealtimeResponseReader) ensureHeader() {
	if !r.streamStarted {
		r.streamStarted = true
		if !r.showSlidingWindow {
			utils.PrintStreamHeader(r.requestPath, r.modelName)
		}
	}
}

// processSSEChunk handles incoming SSE stream bytes, printing tokens & tool calls live,
// and evaluates active circuit breaking on completed tool calls.
func (r *RealtimeResponseReader) processSSEChunk(chunk []byte) {
	r.lineBuffer.Write(chunk)

	for {
		lineBytes, err := r.lineBuffer.ReadBytes('\n')
		if err != nil {
			r.lineBuffer.Write(lineBytes)
			break
		}

		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataPayload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataPayload == "[DONE]" {
				continue
			}

			var chunkObj ChatCompletionChunk
			if jsonErr := json.Unmarshal([]byte(dataPayload), &chunkObj); jsonErr == nil {
				if chunkObj.Model != "" && r.modelName == "" {
					r.modelName = chunkObj.Model
				}

				for _, choice := range chunkObj.Choices {
					// 1. Handle text tokens
					if choice.Delta.Content != "" {
						r.ensureHeader()
						r.accumulatedSSE.WriteString(choice.Delta.Content)
						if !r.showSlidingWindow {
							utils.PrintStreamToken(choice.Delta.Content)
						}

						// Evaluate circuit breaker stream entropy / model degeneration in real-time
						if r.circuitBreaker != nil {
							curEntropy := r.entropy.CumulativeEntropy()
							if incident, tripped := r.circuitBreaker.CheckStreamEntropy(curEntropy, r.entropy.TotalBytes()); tripped {
								r.trippedIncident = incident
								r.logger.Warn("circuit breaker tripped during streaming - low entropy model degeneration detected",
									slog.String("failure_class", incident.FailureClass),
									slog.Float64("entropy", incident.Entropy),
								)

								sreResp := SREErrorResponse{SREIncident: *incident}
								sreBytes, _ := json.Marshal(sreResp)
								r.injectedError = fmt.Appendf(nil, "\nevent: error\ndata: %s\n\n", string(sreBytes))

								_ = r.src.Close()
								r.onCompleteHandler()
								return
							}
						}
					}

					// 2. Handle streaming tool calls
					if len(choice.Delta.ToolCalls) > 0 {
						r.ensureHeader()
						for _, tc := range choice.Delta.ToolCalls {
							idx := 0
							if tc.Index != nil {
								idx = *tc.Index
							}

							acc, exists := r.toolCallsAcc[idx]
							if !exists {
								acc = &ToolCall{
									ID:   tc.ID,
									Type: tc.Type,
								}
								r.toolCallsAcc[idx] = acc
							}

							if tc.ID != "" {
								acc.ID = tc.ID
							}
							if tc.Function.Name != "" {
								acc.Function.Name += tc.Function.Name
								if !r.showSlidingWindow {
									utils.PrintStreamToolCallHeader(acc.Function.Name)
								}
							}
							if tc.Function.Arguments != "" {
								acc.Function.Arguments += tc.Function.Arguments
								if !r.showSlidingWindow {
									utils.PrintStreamToolCallArg(tc.Function.Arguments)
								}
							}
						}
					}

					// 3. If choice finished with tool_calls, evaluate circuit breaker
					if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" && r.circuitBreaker != nil {
						for _, acc := range r.toolCallsAcc {
							incident, tripped := r.circuitBreaker.RecordToolCall(acc.Function.Name, acc.Function.Arguments, false, "")
							if tripped {
								r.trippedIncident = incident
								r.logger.Warn("circuit breaker tripped during streaming - executing active stream termination",
									slog.String("failure_class", incident.FailureClass),
									slog.String("tool", incident.ToolName),
									slog.Int("count", incident.ObservedCount),
								)

								// Format structured SRE JSON error event
								sreResp := SREErrorResponse{SREIncident: *incident}
								sreBytes, _ := json.Marshal(sreResp)
								r.injectedError = fmt.Appendf(nil, "\nevent: error\ndata: %s\n\n", string(sreBytes))

								// Actively terminate upstream stream
								_ = r.src.Close()
								r.onCompleteHandler()
								return
							}
						}
					}
				}
			}
		}
	}
}

// onCompleteHandler pretty-prints completion and notifies callback.
func (r *RealtimeResponseReader) onCompleteHandler() {
	if r.completed {
		return
	}
	r.completed = true

	var recorded RecordedResponse
	streamEntropy := r.entropy.CumulativeEntropy()

	if r.isSSE {
		if r.streamStarted && !r.showSlidingWindow {
			utils.PrintStreamFooter(len(r.toolCallsAcc) > 0, streamEntropy)
		}

		if r.trippedIncident != nil {
			var violatingCalls []utils.SREViolatingCall
			for _, vc := range r.trippedIncident.ViolatingCalls {
				violatingCalls = append(violatingCalls, utils.SREViolatingCall{
					ToolName:  vc.ToolName,
					Arguments: vc.Arguments,
					Error:     vc.Error,
					Distance:  vc.HammingDistance,
					Score:     vc.SimilarityScore,
					Entropy:   vc.Entropy,
				})
			}

			utils.PrintSREIncident("CIRCUIT BREAKER TRIPPED", utils.SREIncidentSummary{
				IncidentID:      r.trippedIncident.IncidentID,
				FailureClass:    r.trippedIncident.FailureClass,
				ToolName:        r.trippedIncident.ToolName,
				ToolArguments:   r.trippedIncident.ToolArguments,
				ViolatingCalls:  violatingCalls,
				SimHashHex:      r.trippedIncident.SimHashHex,
				HammingDistance: r.trippedIncident.HammingDistance,
				SimilarityScore: r.trippedIncident.SimilarityScore,
				Entropy:         r.trippedIncident.Entropy,
				ObservedCount:   r.trippedIncident.ObservedCount,
				Threshold:       r.trippedIncident.Threshold,
				Mitigation:      r.trippedIncident.Mitigation,
				ActionRequired:  r.trippedIncident.ActionRequired,
			})
		}

		fullText := r.accumulatedSSE.String()

		var toolCalls []ToolCall
		for _, tc := range r.toolCallsAcc {
			toolCalls = append(toolCalls, *tc)
		}

		recorded = RecordedResponse{
			Model:     r.modelName,
			Content:   fullText,
			ToolCalls: toolCalls,
			Streamed:  true,
			Entropy:   streamEntropy,
		}

		r.logger.Info("llm stream finished",
			slog.String("path", r.requestPath),
			slog.String("model", r.modelName),
			slog.Int("text_length", len(fullText)),
			slog.Int("tool_calls", len(toolCalls)),
			slog.Float64("entropy_bits_per_byte", streamEntropy),
		)
	} else {
		fullBytes := r.fullBuffer.Bytes()
		if len(fullBytes) > 0 {
			var respObj ChatCompletionResponse
			if err := json.Unmarshal(fullBytes, &respObj); err == nil && len(respObj.Choices) > 0 {
				choice := respObj.Choices[0]
				content := choice.Message.ContentString()

				recorded = RecordedResponse{
					Model:     respObj.Model,
					Content:   content,
					ToolCalls: choice.Message.ToolCalls,
					Streamed:  false,
					Entropy:   streamEntropy,
				}

				if !r.showSlidingWindow {
					var printTools []utils.PrintToolCall
					for _, tc := range choice.Message.ToolCalls {
						printTools = append(printTools, utils.PrintToolCall{
							ID:        tc.ID,
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						})
					}

					utils.PrintNonStreamingResponse(r.requestPath, respObj.Model, content, printTools, streamEntropy)
				}

				r.logger.Info("llm response completed",
					slog.String("path", r.requestPath),
					slog.String("model", respObj.Model),
					slog.Int("length", len(content)),
					slog.Int("tool_calls", len(choice.Message.ToolCalls)),
					slog.Float64("entropy_bits_per_byte", streamEntropy),
				)
			}
		}
	}

	if r.onComplete != nil {
		r.onComplete(recorded)
	}
}

// Close closes the underlying response body and finalizes the stream.
func (r *RealtimeResponseReader) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.onCompleteHandler()
	}
	r.mu.Unlock()
	return r.src.Close()
}
