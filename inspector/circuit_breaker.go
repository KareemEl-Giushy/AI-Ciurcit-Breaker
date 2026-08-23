package inspector

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Failure classification constants
const (
	ClassAIdenticalLoop     = "CLASS_A_IDENTICAL_TOOL_CALL_LOOP"
	ClassBErrorAccumulation = "CLASS_B_ERROR_ACCUMULATION"
)

// SREIncident defines the structured SRE incident payload emitted when a circuit breaker trips.
type SREIncident struct {
	IncidentID      string    `json:"incident_id"`
	Timestamp       time.Time `json:"timestamp"`
	CircuitState    string    `json:"circuit_state"` // e.g. "OPEN_TRIPPED", "VELOCITY_BLOCKED"
	FailureClass    string    `json:"failure_class"` // e.g. "CLASS_A_IDENTICAL_TOOL_CALL_LOOP"
	ToolName        string    `json:"tool_name,omitempty"`
	Fingerprint     string    `json:"fingerprint,omitempty"`
	SimHashHex      string    `json:"simhash,omitempty"`
	HammingDistance int       `json:"hamming_distance,omitempty"`
	SimilarityScore float64   `json:"similarity_score,omitempty"`
	ObservedCount   int       `json:"observed_count"`
	Threshold       int       `json:"threshold"`
	Mitigation      string    `json:"mitigation"`
	ActionRequired  string    `json:"action_required"`
}

// SREErrorResponse formats the top-level SRE JSON error response.
type SREErrorResponse struct {
	SREIncident SREIncident `json:"sre_incident"`
}

// CircuitBreakerConfig sets thresholds for repetition and error accumulation.
type CircuitBreakerConfig struct {
	WindowDuration     time.Duration // Time window for sliding evaluation (default: 60s)
	ClassAMaxIdentical int           // Max allowed identical/similar tool calls before tripping (default: 3)
	ClassBMaxErrors    int           // Max allowed tool errors in window before tripping (default: 4)
	MaxHammingDistance int           // Max SimHash Hamming distance to treat tool calls as identical/near-duplicate (default: 4)
	JaccardThreshold   float64       // Jaccard similarity threshold for near-duplicate tool calls (default: 0.85)
	Enabled            bool          // Toggle circuit breaker protection
}

// ToolCallEntry tracks metadata for an evaluated tool invocation.
type ToolCallEntry struct {
	Timestamp   time.Time
	ToolName    string
	Args        string
	Fingerprint string
	SimHash     uint64
	IsError     bool
	ErrorMsg    string
}

// CircuitBreakerEngine maintains a sliding-window tracker for tool calls and errors.
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
	if cfg.ClassAMaxIdentical <= 0 {
		cfg.ClassAMaxIdentical = 3
	}
	if cfg.ClassBMaxErrors <= 0 {
		cfg.ClassBMaxErrors = 4
	}
	if cfg.MaxHammingDistance <= 0 {
		cfg.MaxHammingDistance = 3
	}
	if cfg.JaccardThreshold <= 0 || cfg.JaccardThreshold > 1.0 {
		cfg.JaccardThreshold = 0.85
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
		`"failed"`, `exception:`, `traceback`, `permission denied`,
		`not found`, `timed out`, `internal server error`,
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// RecordToolCall records a tool execution and evaluates whether Class A (identical/SimHash similar calls)
// or Class B (error accumulation) thresholds are breached.
func (cb *CircuitBreakerEngine) RecordToolCall(toolName, args string, isError bool, errorMsg string) (*SREIncident, bool) {
	if !cb.config.Enabled {
		return nil, false
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	cb.evictExpired(now)

	fingerprint := ComputeToolFingerprint(toolName, args)
	simhash := ComputeSimHash(toolName, args)

	entry := ToolCallEntry{
		Timestamp:   now,
		ToolName:    toolName,
		Args:        args,
		Fingerprint: fingerprint,
		SimHash:     simhash,
		IsError:     isError,
		ErrorMsg:    errorMsg,
	}
	cb.entries = append(cb.entries, entry)

	// 1. Evaluate Class A: Identical or SimHash/Jaccard similar tool-call repetition in active window
	similarCount := 0
	minHammingDist := 64
	maxSimilarity := 0.0

	for _, e := range cb.entries {
		if strings.TrimSpace(strings.ToLower(e.ToolName)) != strings.TrimSpace(strings.ToLower(toolName)) {
			continue
		}

		dist := HammingDistance(e.SimHash, simhash)
		sim := SimHashSimilarity(e.SimHash, simhash)

		if e.Fingerprint == fingerprint || dist <= cb.config.MaxHammingDistance {
			similarCount++
			if dist < minHammingDist {
				minHammingDist = dist
			}
			if sim > maxSimilarity {
				maxSimilarity = sim
			}
		} else if isSimilar, jaccardSim := AreToolCallsSimilar(e.ToolName, e.Args, toolName, args, cb.config.JaccardThreshold); isSimilar {
			similarCount++
			if jaccardSim > maxSimilarity {
				maxSimilarity = jaccardSim
			}
		}
	}

	if similarCount >= cb.config.ClassAMaxIdentical {
		incident := &SREIncident{
			IncidentID:      generateIncidentID(),
			Timestamp:       now,
			CircuitState:    "OPEN_TRIPPED",
			FailureClass:    ClassAIdenticalLoop,
			ToolName:        toolName,
			Fingerprint:     fingerprint,
			SimHashHex:      SimHashHexString(simhash),
			HammingDistance: minHammingDist,
			SimilarityScore: maxSimilarity,
			ObservedCount:   similarCount,
			Threshold:       cb.config.ClassAMaxIdentical,
			Mitigation:      "Active stream termination to prevent recursive tool looping",
			ActionRequired:  "Inspect agent prompt reasoning to break identical/similar tool call recursion",
		}
		return incident, true
	}

	// 2. Evaluate Class B: Error accumulation in active window
	errorCount := 0
	for _, e := range cb.entries {
		if e.IsError {
			errorCount++
		}
	}

	if errorCount >= cb.config.ClassBMaxErrors {
		incident := &SREIncident{
			IncidentID:     generateIncidentID(),
			Timestamp:      now,
			CircuitState:   "OPEN_TRIPPED",
			FailureClass:   ClassBErrorAccumulation,
			ToolName:       toolName,
			Fingerprint:    fingerprint,
			SimHashHex:     SimHashHexString(simhash),
			ObservedCount:  errorCount,
			Threshold:      cb.config.ClassBMaxErrors,
			Mitigation:     "Circuit breaker opened due to sustained tool execution failures",
			ActionRequired: "Check downstream tool health and resolve underlying tool error conditions",
		}
		return incident, true
	}

	return nil, false
}

// CheckRequestHistory analyzes the entire conversation history in an incoming request
// for existing Class A identical/similar loops or Class B error accumulation.
func (cb *CircuitBreakerEngine) CheckRequestHistory(messages []ChatMessage) (*SREIncident, bool) {
	if !cb.config.Enabled || len(messages) == 0 {
		return nil, false
	}

	type recordedCall struct {
		name        string
		args        string
		fingerprint string
		simhash     uint64
	}

	var recordedCalls []recordedCall
	errorCount := 0

	for _, msg := range messages {
		// Check assistant tool calls for Class A (exact match, SimHash, or Jaccard similarity)
		for _, tc := range msg.ToolCalls {
			fp := ComputeToolFingerprint(tc.Function.Name, tc.Function.Arguments)
			sh := ComputeSimHash(tc.Function.Name, tc.Function.Arguments)

			current := recordedCall{
				name:        tc.Function.Name,
				args:        tc.Function.Arguments,
				fingerprint: fp,
				simhash:     sh,
			}

			// Count occurrences matching current tool call
			similarCount := 1
			minDist := 0
			maxSim := 1.0

			for _, prev := range recordedCalls {
				if strings.TrimSpace(strings.ToLower(prev.name)) != strings.TrimSpace(strings.ToLower(current.name)) {
					continue
				}

				dist := HammingDistance(prev.simhash, current.simhash)
				sim := SimHashSimilarity(prev.simhash, current.simhash)

				if prev.fingerprint == fp || dist <= cb.config.MaxHammingDistance {
					similarCount++
					minDist = dist
					maxSim = sim
				} else if isSimilar, jaccardSim := AreToolCallsSimilar(prev.name, prev.args, current.name, current.args, cb.config.JaccardThreshold); isSimilar {
					similarCount++
					maxSim = jaccardSim
				}
			}

			recordedCalls = append(recordedCalls, current)

			if similarCount >= cb.config.ClassAMaxIdentical {
				return &SREIncident{
					IncidentID:      generateIncidentID(),
					Timestamp:       time.Now(),
					CircuitState:    "OPEN_TRIPPED",
					FailureClass:    ClassAIdenticalLoop,
					ToolName:        tc.Function.Name,
					Fingerprint:     fp,
					SimHashHex:      SimHashHexString(sh),
					HammingDistance: minDist,
					SimilarityScore: maxSim,
					ObservedCount:   similarCount,
					Threshold:       cb.config.ClassAMaxIdentical,
					Mitigation:      "Request rejected at edge - repetitive tool call loop detected via SimHash LSH",
					ActionRequired:  "Modify prompt or conversation context to eliminate repetitive tool loops",
				}, true
			}
		}

		// Check tool responses for Class B errors
		if strings.ToLower(msg.Role) == "tool" || strings.ToLower(msg.Role) == "function" {
			if IsToolResponseError(msg.ContentString()) {
				errorCount++
				if errorCount >= cb.config.ClassBMaxErrors {
					return &SREIncident{
						IncidentID:     generateIncidentID(),
						Timestamp:      time.Now(),
						CircuitState:   "OPEN_TRIPPED",
						FailureClass:   ClassBErrorAccumulation,
						ObservedCount:  errorCount,
						Threshold:      cb.config.ClassBMaxErrors,
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
