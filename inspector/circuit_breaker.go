package inspector

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Failure classification constants for circuit breaker trip causes.
const (
	FailureClassToolLoop          = "TOOL_CALL_REPETITION_LOOP"
	FailureClassErrorAccumulation = "TOOL_ERROR_ACCUMULATION"
	FailureClassLowEntropy        = "MODEL_DEGENERATION_LOW_ENTROPY"
)

// ViolatingToolCall details an individual tool invocation or error that participated in a circuit breaker violation.
type ViolatingToolCall struct {
	ToolName        string    `json:"tool_name"`
	Arguments       string    `json:"arguments"`
	HammingDistance int       `json:"hamming_distance,omitempty"`
	SimilarityScore float64   `json:"similarity_score,omitempty"`
	Entropy         float64   `json:"entropy,omitempty"`
	Error           string    `json:"error,omitempty"`
	Timestamp       time.Time `json:"timestamp,omitempty"`
}

// SREIncident defines the structured SRE incident payload emitted when a circuit breaker trips.
type SREIncident struct {
	IncidentID      string              `json:"incident_id"`
	Timestamp       time.Time           `json:"timestamp"`
	CircuitState    string              `json:"circuit_state"` // e.g. "OPEN_TRIPPED", "VELOCITY_BLOCKED"
	FailureClass    string              `json:"failure_class"` // e.g. "TOOL_CALL_REPETITION_LOOP", "TOOL_ERROR_ACCUMULATION", "MODEL_DEGENERATION_LOW_ENTROPY"
	ToolName        string              `json:"tool_name,omitempty"`
	ToolArguments   string              `json:"tool_arguments,omitempty"`
	ViolatingCalls  []ViolatingToolCall `json:"violating_calls,omitempty"`
	SimHashHex      string              `json:"simhash,omitempty"`
	HammingDistance int                 `json:"hamming_distance,omitempty"`
	SimilarityScore float64             `json:"similarity_score,omitempty"`
	Entropy         float64             `json:"entropy,omitempty"`
	ObservedCount   int                 `json:"observed_count"`
	Threshold       int                 `json:"threshold"`
	Mitigation      string              `json:"mitigation"`
	ActionRequired  string              `json:"action_required"`
}

// SREErrorResponse formats the top-level SRE JSON error response.
type SREErrorResponse struct {
	SREIncident SREIncident `json:"sre_incident"`
}

// CircuitBreakerConfig sets thresholds for tool call repetition loops, error accumulation, and entropy degeneration.
type CircuitBreakerConfig struct {
	WindowDuration      time.Duration // Time window for sliding evaluation (default: 60s)
	MaxToolRepeats      int           // Max allowed identical/similar tool calls before tripping (default: 3)
	MaxToolErrors       int           // Max allowed tool errors in window before tripping (default: 4)
	MaxHammingDistance  int           // Max SimHash Hamming distance to treat tool calls as identical/near-duplicate (default: 3)
	JaccardThreshold    float64       // Jaccard similarity threshold for near-duplicate tool calls (default: 0.85)
	MinEntropyThreshold float64       // Min Shannon entropy (bits/byte) threshold below which model degeneration trips breaker (default: 1.5, 0 to disable)
	Enabled             bool          // Toggle circuit breaker protection
}

// ToolCallEntry tracks metadata for an evaluated tool invocation.
type ToolCallEntry struct {
	Timestamp time.Time
	ToolName  string
	Args      string
	SimHash   uint64
	Entropy   float64
	IsError   bool
	ErrorMsg  string
}

// CircuitBreakerEngine maintains a sliding-window tracker for tool calls, errors, and entropy quality.
type CircuitBreakerEngine struct {
	mu      sync.RWMutex
	config  CircuitBreakerConfig
	entries []ToolCallEntry
}

// NewCircuitBreakerEngine creates an initialized CircuitBreakerEngine.
func NewCircuitBreakerEngine(cfg CircuitBreakerConfig) *CircuitBreakerEngine {
	if cfg.WindowDuration <= 0 {
		cfg.WindowDuration = 60 * time.Second
	}
	if cfg.MaxToolRepeats <= 0 {
		cfg.MaxToolRepeats = 3
	}
	if cfg.MaxToolErrors <= 0 {
		cfg.MaxToolErrors = 4
	}
	if cfg.MaxHammingDistance <= 0 {
		cfg.MaxHammingDistance = 3
	}
	if cfg.JaccardThreshold <= 0 || cfg.JaccardThreshold > 1.0 {
		cfg.JaccardThreshold = 0.85
	}
	if cfg.MinEntropyThreshold <= 0 {
		cfg.MinEntropyThreshold = 1.5
	}
	return &CircuitBreakerEngine{
		config:  cfg,
		entries: make([]ToolCallEntry, 0),
	}
}

func generateIncidentID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("inc_%s_%s", time.Now().Format("20060102_150405"), hex.EncodeToString(b))
}

// evictExpired removes entries outside the sliding time window. Must be called with lock held.
func (cb *CircuitBreakerEngine) evictExpired(now time.Time) {
	threshold := now.Add(-cb.config.WindowDuration)
	validIdx := 0
	for i, entry := range cb.entries {
		if entry.Timestamp.After(threshold) {
			validIdx = i
			break
		}
		if i == len(cb.entries)-1 {
			cb.entries = cb.entries[:0]
			return
		}
	}
	if validIdx > 0 {
		cb.entries = cb.entries[validIdx:]
	}
}

// IsToolResponseError inspects a tool response payload to determine if it indicates failure.
func IsToolResponseError(content string) bool {
	lower := strings.ToLower(content)
	indicators := []string{
		`"error":`, `"status":"error"`, `"status": "error"`,
		`"failed"`, `exception:`, `"exception":`, `traceback`, `permission denied`,
		`not found`, `timed out`, `internal server error`,
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// CheckStreamEntropy evaluates live stream Shannon entropy and returns an SRE incident if model degeneration is detected.
func (cb *CircuitBreakerEngine) CheckStreamEntropy(entropy float64, totalBytes uint32) (*SREIncident, bool) {
	if !cb.config.Enabled || cb.config.MinEntropyThreshold <= 0 {
		return nil, false
	}

	// Only evaluate after a minimum sample size (e.g. 64 bytes) to avoid false positives on stream beginnings
	if totalBytes >= 64 && entropy < cb.config.MinEntropyThreshold {
		now := time.Now()
		incident := &SREIncident{
			IncidentID:     generateIncidentID(),
			Timestamp:      now,
			CircuitState:   "OPEN_TRIPPED",
			FailureClass:   FailureClassLowEntropy,
			Entropy:        entropy,
			ObservedCount:  int(totalBytes),
			Threshold:      int(cb.config.MinEntropyThreshold * 100),
			Mitigation:     fmt.Sprintf("Active stream termination: Shannon entropy (%.2f bits/byte) dropped below minimum threshold (%.2f bits/byte)", entropy, cb.config.MinEntropyThreshold),
			ActionRequired: "Detect token repetition spam or model degeneration; modify temperature or prompt",
		}
		return incident, true
	}

	return nil, false
}

// RecordToolCall records a tool execution and evaluates tool repetition loops,
// error accumulation, and low-entropy parameter degeneration in the active window.
func (cb *CircuitBreakerEngine) RecordToolCall(toolName, args string, isError bool, errorMsg string) (*SREIncident, bool) {
	if !cb.config.Enabled {
		return nil, false
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	cb.evictExpired(now)

	simhash := ComputeSimHash(toolName, args)
	argsEntropy := CalculateStringEntropy(args)
	normArgs := NormalizeJSONArgs(args)

	entry := ToolCallEntry{
		Timestamp: now,
		ToolName:  toolName,
		Args:      args,
		SimHash:   simhash,
		Entropy:   argsEntropy,
		IsError:   isError,
		ErrorMsg:  errorMsg,
	}
	cb.entries = append(cb.entries, entry)

	// 1. Evaluate Low Entropy / Model Degeneration in Tool Arguments
	if cb.config.MinEntropyThreshold > 0 && len(args) >= 30 && argsEntropy < cb.config.MinEntropyThreshold {
		incident := &SREIncident{
			IncidentID:     generateIncidentID(),
			Timestamp:      now,
			CircuitState:   "OPEN_TRIPPED",
			FailureClass:   FailureClassLowEntropy,
			ToolName:       toolName,
			ToolArguments:  args,
			Entropy:        argsEntropy,
			ObservedCount:  1,
			Threshold:      int(cb.config.MinEntropyThreshold * 100),
			Mitigation:     fmt.Sprintf("Tool call blocked: Shannon entropy (%.2f bits/byte) dropped below minimum threshold (%.2f bits/byte)", argsEntropy, cb.config.MinEntropyThreshold),
			ActionRequired: "Inspect prompt for repetitive degeneration or token collapse patterns",
			ViolatingCalls: []ViolatingToolCall{
				{
					ToolName:  toolName,
					Arguments: args,
					Entropy:   argsEntropy,
					Timestamp: now,
					Error:     fmt.Sprintf("Low entropy detected: %.2f bits/byte", argsEntropy),
				},
			},
		}
		return incident, true
	}

	// 2. Evaluate Tool Call Repetition Loop: SimHash and Jaccard similarity in active window
	similarCount := 0
	minHammingDist := 64
	maxSimilarity := 0.0
	var violatingCalls []ViolatingToolCall

	cleanToolName := strings.TrimSpace(strings.ToLower(toolName))

	for _, e := range cb.entries {
		if strings.TrimSpace(strings.ToLower(e.ToolName)) != cleanToolName {
			continue
		}

		dist := HammingDistance(e.SimHash, simhash)
		simHashScore := SimHashSimilarity(e.SimHash, simhash)
		jaccardScore := JaccardSimilarity(normArgs, NormalizeJSONArgs(e.Args))

		isSimHashMatch := dist <= cb.config.MaxHammingDistance
		isJaccardMatch := jaccardScore >= cb.config.JaccardThreshold

		if isSimHashMatch || isJaccardMatch {
			similarCount++
			if dist < minHammingDist {
				minHammingDist = dist
			}

			score := simHashScore
			if jaccardScore > score {
				score = jaccardScore
			}
			if score > maxSimilarity {
				maxSimilarity = score
			}

			violatingCalls = append(violatingCalls, ViolatingToolCall{
				ToolName:        e.ToolName,
				Arguments:       e.Args,
				HammingDistance: dist,
				SimilarityScore: score,
				Entropy:         e.Entropy,
				Timestamp:       e.Timestamp,
			})
		}
	}

	if similarCount >= cb.config.MaxToolRepeats {
		incident := &SREIncident{
			IncidentID:      generateIncidentID(),
			Timestamp:       now,
			CircuitState:    "OPEN_TRIPPED",
			FailureClass:    FailureClassToolLoop,
			ToolName:        toolName,
			ToolArguments:   args,
			ViolatingCalls:  violatingCalls,
			SimHashHex:      SimHashHexString(simhash),
			HammingDistance: minHammingDist,
			SimilarityScore: maxSimilarity,
			Entropy:         argsEntropy,
			ObservedCount:   similarCount,
			Threshold:       cb.config.MaxToolRepeats,
			Mitigation:      "Active stream termination to prevent recursive tool looping",
			ActionRequired:  "Inspect agent prompt reasoning to break identical/similar tool call recursion",
		}
		return incident, true
	}

	// 3. Evaluate Tool Error Accumulation in active window
	errorCount := 0
	var errorViolations []ViolatingToolCall
	for _, e := range cb.entries {
		if e.IsError {
			errorCount++
			errorViolations = append(errorViolations, ViolatingToolCall{
				ToolName:  e.ToolName,
				Arguments: e.Args,
				Entropy:   e.Entropy,
				Error:     e.ErrorMsg,
				Timestamp: e.Timestamp,
			})
		}
	}

	if errorCount >= cb.config.MaxToolErrors {
		incident := &SREIncident{
			IncidentID:     generateIncidentID(),
			Timestamp:      now,
			CircuitState:   "OPEN_TRIPPED",
			FailureClass:   FailureClassErrorAccumulation,
			ToolName:       toolName,
			ToolArguments:  args,
			ViolatingCalls: errorViolations,
			SimHashHex:     SimHashHexString(simhash),
			Entropy:        argsEntropy,
			ObservedCount:  errorCount,
			Threshold:      cb.config.MaxToolErrors,
			Mitigation:     "Circuit breaker opened due to sustained tool execution failures",
			ActionRequired: "Check downstream tool health and resolve underlying tool error conditions",
		}
		return incident, true
	}

	return nil, false
}

// CheckRequestHistory analyzes the entire conversation history in an incoming request
// for existing tool repetition loops, low-entropy degeneration, or tool error accumulation.
func (cb *CircuitBreakerEngine) CheckRequestHistory(messages []ChatMessage) (*SREIncident, bool) {
	if !cb.config.Enabled || len(messages) == 0 {
		return nil, false
	}

	type recordedCall struct {
		name     string
		args     string
		normArgs string
		simhash  uint64
		entropy  float64
	}

	var recordedCalls []recordedCall
	errorCount := 0

	for _, msg := range messages {
		// 1. Check for Low Entropy / Model Degeneration in conversation turns
		msgContent := msg.ContentString()
		if cb.config.MinEntropyThreshold > 0 && len(msgContent) >= 64 {
			contentEntropy := CalculateStringEntropy(msgContent)
			if contentEntropy < cb.config.MinEntropyThreshold {
				return &SREIncident{
					IncidentID:     generateIncidentID(),
					Timestamp:      time.Now(),
					CircuitState:   "OPEN_TRIPPED",
					FailureClass:   FailureClassLowEntropy,
					Entropy:        contentEntropy,
					ObservedCount:  len(msgContent),
					Threshold:      int(cb.config.MinEntropyThreshold * 100),
					Mitigation:     fmt.Sprintf("Request rejected at edge: message content entropy (%.2f bits/byte) dropped below minimum threshold (%.2f bits/byte)", contentEntropy, cb.config.MinEntropyThreshold),
					ActionRequired: "Remove repetitive degenerate text or spam patterns from conversation context",
				}, true
			}
		}

		// 2. Check assistant tool calls for repetition (SimHash or Jaccard similarity) and low entropy
		for _, tc := range msg.ToolCalls {
			sh := ComputeSimHash(tc.Function.Name, tc.Function.Arguments)
			tcEntropy := CalculateStringEntropy(tc.Function.Arguments)
			normArgs := NormalizeJSONArgs(tc.Function.Arguments)

			// Check tool arguments for low entropy degeneration
			if cb.config.MinEntropyThreshold > 0 && len(tc.Function.Arguments) >= 30 && tcEntropy < cb.config.MinEntropyThreshold {
				return &SREIncident{
					IncidentID:     generateIncidentID(),
					Timestamp:      time.Now(),
					CircuitState:   "OPEN_TRIPPED",
					FailureClass:   FailureClassLowEntropy,
					ToolName:       tc.Function.Name,
					ToolArguments:  tc.Function.Arguments,
					Entropy:        tcEntropy,
					ObservedCount:  1,
					Threshold:      int(cb.config.MinEntropyThreshold * 100),
					Mitigation:     fmt.Sprintf("Request rejected at edge: tool argument entropy (%.2f bits/byte) dropped below minimum threshold (%.2f bits/byte)", tcEntropy, cb.config.MinEntropyThreshold),
					ActionRequired: "Inspect prompt for repetitive degeneration or token collapse patterns",
				}, true
			}

			current := recordedCall{
				name:     tc.Function.Name,
				args:     tc.Function.Arguments,
				normArgs: normArgs,
				simhash:  sh,
				entropy:  tcEntropy,
			}

			cleanCurrentName := strings.TrimSpace(strings.ToLower(current.name))

			// Count occurrences matching current tool call and collect violating calls
			similarCount := 1
			minDist := 0
			maxSim := 1.0
			var violatingCalls []ViolatingToolCall

			for _, prev := range recordedCalls {
				if strings.TrimSpace(strings.ToLower(prev.name)) != cleanCurrentName {
					continue
				}

				dist := HammingDistance(prev.simhash, current.simhash)
				simHashScore := SimHashSimilarity(prev.simhash, current.simhash)
				jaccardScore := JaccardSimilarity(prev.normArgs, current.normArgs)

				isSimHashMatch := dist <= cb.config.MaxHammingDistance
				isJaccardMatch := jaccardScore >= cb.config.JaccardThreshold

				if isSimHashMatch || isJaccardMatch {
					similarCount++
					if dist < minDist {
						minDist = dist
					}
					score := simHashScore
					if jaccardScore > score {
						score = jaccardScore
					}
					if score > maxSim {
						maxSim = score
					}
					violatingCalls = append(violatingCalls, ViolatingToolCall{
						ToolName:        prev.name,
						Arguments:       prev.args,
						HammingDistance: dist,
						SimilarityScore: score,
						Entropy:         prev.entropy,
					})
				}
			}

			// Append triggering tool call to violating calls
			violatingCalls = append(violatingCalls, ViolatingToolCall{
				ToolName:        current.name,
				Arguments:       current.args,
				HammingDistance: 0,
				SimilarityScore: 1.0,
				Entropy:         current.entropy,
			})

			recordedCalls = append(recordedCalls, current)

			if similarCount >= cb.config.MaxToolRepeats {
				return &SREIncident{
					IncidentID:      generateIncidentID(),
					Timestamp:       time.Now(),
					CircuitState:    "OPEN_TRIPPED",
					FailureClass:    FailureClassToolLoop,
					ToolName:        tc.Function.Name,
					ToolArguments:   tc.Function.Arguments,
					ViolatingCalls:  violatingCalls,
					SimHashHex:      SimHashHexString(sh),
					HammingDistance: minDist,
					SimilarityScore: maxSim,
					Entropy:         tcEntropy,
					ObservedCount:   similarCount,
					Threshold:       cb.config.MaxToolRepeats,
					Mitigation:      "Request rejected at edge - repetitive tool call loop detected via SimHash / Jaccard similarity",
					ActionRequired:  "Modify prompt or conversation context to eliminate repetitive tool loops",
				}, true
			}
		}

		// 3. Check tool responses for error accumulation
		if strings.ToLower(msg.Role) == "tool" || strings.ToLower(msg.Role) == "function" {
			if IsToolResponseError(msg.ContentString()) {
				errorCount++
				if errorCount >= cb.config.MaxToolErrors {
					var errorCalls []ViolatingToolCall
					for _, m := range messages {
						if (strings.ToLower(m.Role) == "tool" || strings.ToLower(m.Role) == "function") && IsToolResponseError(m.ContentString()) {
							name := m.ToolCallID
							if name == "" {
								name = "tool_response"
							}
							errorCalls = append(errorCalls, ViolatingToolCall{
								ToolName:  name,
								Arguments: m.ContentString(),
								Entropy:   CalculateStringEntropy(m.ContentString()),
								Error:     "Error indicator found in tool output",
							})
						}
					}

					return &SREIncident{
						IncidentID:     generateIncidentID(),
						Timestamp:      time.Now(),
						CircuitState:   "OPEN_TRIPPED",
						FailureClass:   FailureClassErrorAccumulation,
						ViolatingCalls: errorCalls,
						ObservedCount:  errorCount,
						Threshold:      cb.config.MaxToolErrors,
						Mitigation:     "Request rejected at edge - accumulated tool errors threshold exceeded",
						ActionRequired: "Resolve failing tool endpoints before continuing conversation",
					}, true
				}
			}
		}
	}

	return nil, false
}

// Reset clears all historical entries in the circuit breaker engine.
func (cb *CircuitBreakerEngine) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.entries = cb.entries[:0]
}
