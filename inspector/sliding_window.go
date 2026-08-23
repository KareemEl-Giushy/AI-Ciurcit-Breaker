package inspector

import (
	"sync"
	"time"
)

// SlidingWindowConfig holds the configuration for the sliding window mechanism.
type SlidingWindowConfig struct {
	WindowDuration time.Duration // e.g. 60 * time.Second
	MaxRequests    int           // Maximum requests allowed in the window (0 for unlimited)
	MaxTokens      int           // Maximum estimated tokens allowed in the window (0 for unlimited)
	EnforceLimits  bool          // If true, reject requests when limits are exceeded
}

// RequestEntry tracks metadata for an inspected request within the sliding window.
type RequestEntry struct {
	Timestamp       time.Time
	Model           string
	MessageCount    int
	EstimatedTokens int
	ClientIP        string
}

// WindowStats contains aggregated metrics for the current sliding window.
type WindowStats struct {
	TotalRequests  int            `json:"total_requests"`
	TotalTokens    int            `json:"total_tokens"`
	ModelCounts    map[string]int `json:"model_counts"`
	WindowDuration time.Duration  `json:"window_duration"`
	LimitExceeded  bool           `json:"limit_exceeded"`
	LimitReason    string         `json:"limit_reason,omitempty"`
}

// SlidingWindow maintains a thread-safe sliding time window of request history.
type SlidingWindow struct {
	mu      sync.RWMutex
	config  SlidingWindowConfig
	entries []RequestEntry
}

// NewSlidingWindow creates and initializes a new SlidingWindow inspector.
func NewSlidingWindow(cfg SlidingWindowConfig) *SlidingWindow {
	if cfg.WindowDuration <= 0 {
		cfg.WindowDuration = 60 * time.Second
	}
	return &SlidingWindow{
		config:  cfg,
		entries: make([]RequestEntry, 0),
	}
}

// evictExpired removes entries that fall outside the current sliding time window.
// Must be called with lock held.
func (sw *SlidingWindow) evictExpired(now time.Time) {
	threshold := now.Add(-sw.config.WindowDuration)
	validIdx := 0
	for i, entry := range sw.entries {
		if entry.Timestamp.After(threshold) {
			validIdx = i
			break
		}
		if i == len(sw.entries)-1 {
			// All entries expired
			sw.entries = sw.entries[:0]
			return
		}
	}
	if validIdx > 0 {
		sw.entries = sw.entries[validIdx:]
	}
}

// Record inspects and records a new OpenAI request into the sliding window.
// It returns the updated WindowStats and a boolean 'allowed' (false if limits are enforced and breached).
func (sw *SlidingWindow) Record(req *OpenAIRequest, clientIP string) (WindowStats, bool) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	sw.evictExpired(now)

	var model string
	var msgCount int
	var tokens int

	if req != nil {
		model = req.Model
		msgCount = len(req.Messages)
		tokens = req.EstimateTokens()
	}

	entry := RequestEntry{
		Timestamp:       now,
		Model:           model,
		MessageCount:    msgCount,
		EstimatedTokens: tokens,
		ClientIP:        clientIP,
	}

	// Calculate what totals would be if this entry is admitted
	currentRequests := len(sw.entries) + 1
	currentTokens := tokens
	for _, e := range sw.entries {
		currentTokens += e.EstimatedTokens
	}

	stats := WindowStats{
		TotalRequests:  currentRequests,
		TotalTokens:    currentTokens,
		ModelCounts:    make(map[string]int),
		WindowDuration: sw.config.WindowDuration,
	}

	for _, e := range sw.entries {
		if e.Model != "" {
			stats.ModelCounts[e.Model]++
		}
	}
	if model != "" {
		stats.ModelCounts[model]++
	}

	// Check limits
	allowed := true
	if sw.config.MaxRequests > 0 && currentRequests > sw.config.MaxRequests {
		stats.LimitExceeded = true
		stats.LimitReason = "request limit exceeded in sliding window"
		if sw.config.EnforceLimits {
			allowed = false
		}
	} else if sw.config.MaxTokens > 0 && currentTokens > sw.config.MaxTokens {
		stats.LimitExceeded = true
		stats.LimitReason = "token limit exceeded in sliding window"
		if sw.config.EnforceLimits {
			allowed = false
		}
	}

	// If allowed, append the entry
	if allowed {
		sw.entries = append(sw.entries, entry)
	} else {
		// Revert stats to actual recorded entries
		stats.TotalRequests = len(sw.entries)
		stats.TotalTokens = currentTokens - tokens
		if model != "" {
			stats.ModelCounts[model]--
			if stats.ModelCounts[model] == 0 {
				delete(stats.ModelCounts, model)
			}
		}
	}

	return stats, allowed
}

// GetStats returns current snapshot of metrics within the sliding window.
func (sw *SlidingWindow) GetStats() WindowStats {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	sw.evictExpired(now)

	stats := WindowStats{
		TotalRequests:  len(sw.entries),
		ModelCounts:    make(map[string]int),
		WindowDuration: sw.config.WindowDuration,
	}

	for _, e := range sw.entries {
		stats.TotalTokens += e.EstimatedTokens
		if e.Model != "" {
			stats.ModelCounts[e.Model]++
		}
	}

	return stats
}

// Config returns the current sliding window configuration.
func (sw *SlidingWindow) Config() SlidingWindowConfig {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return sw.config
}

// Reset clears all entries from the sliding window.
func (sw *SlidingWindow) Reset() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.entries = sw.entries[:0]
}
