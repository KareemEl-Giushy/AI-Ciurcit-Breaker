package inspector

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Velocity failure classification constants
const (
	ClassVelocityMaxRPS             = "VELOCITY_MAX_RPS_EXCEEDED"
	ClassVelocityMaxEndpointRepeats = "VELOCITY_MAX_ENDPOINT_REPEATS_EXCEEDED"
)

// VelocityConfig defines the thresholds for high-frequency velocity enforcement per session.
type VelocityConfig struct {
	MaxRPS             float64       // Max allowed requests per second (e.g. 5.0). 0 = unlimited.
	MaxEndpointRepeats int           // Max allowed hits to the same endpoint within RepeatWindow (e.g. 20). 0 = unlimited.
	RepeatWindow       time.Duration // Time window for endpoint repeat tracking (default: 10s).
	Enabled            bool          // Enable/disable velocity checking.
}

type sessionRecord struct {
	mu               sync.Mutex
	requests         []time.Time
	endpointRequests map[string][]time.Time
	lastActivity     time.Time
}

func newSessionRecord() *sessionRecord {
	return &sessionRecord{
		requests:         make([]time.Time, 0, 16),
		endpointRequests: make(map[string][]time.Time),
		lastActivity:     time.Now(),
	}
}

// VelocityDetector monitors session request throughput to prevent high-frequency anomalous loops and endpoint hammering.
type VelocityDetector struct {
	mu       sync.RWMutex
	config   VelocityConfig
	sessions map[string]*sessionRecord
}

// NewVelocityDetector creates an initialized VelocityDetector.
func NewVelocityDetector(cfg VelocityConfig) *VelocityDetector {
	if cfg.MaxRPS < 0 {
		cfg.MaxRPS = 5.0
	}
	if cfg.MaxEndpointRepeats < 0 {
		cfg.MaxEndpointRepeats = 20
	}
	if cfg.RepeatWindow <= 0 {
		cfg.RepeatWindow = 10 * time.Second
	}

	return &VelocityDetector{
		config:   cfg,
		sessions: make(map[string]*sessionRecord),
	}
}

func (vd *VelocityDetector) getOrCreateSession(sessionID string) *sessionRecord {
	vd.mu.RLock()
	s, exists := vd.sessions[sessionID]
	vd.mu.RUnlock()

	if exists {
		return s
	}

	vd.mu.Lock()
	defer vd.mu.Unlock()

	s, exists = vd.sessions[sessionID]
	if !exists {
		s = newSessionRecord()
		vd.sessions[sessionID] = s
	}
	return s
}

// RecordRequest checks if a request from sessionID to endpoint violates velocity rules.
// Returns (allowed, incident). If allowed is false, an SRE incident is returned.
func (vd *VelocityDetector) RecordRequest(sessionID, endpoint string) (bool, *SREIncident) {
	if !vd.config.Enabled {
		return true, nil
	}

	if sessionID == "" {
		sessionID = "default"
	}
	// Strip query params for endpoint grouping
	cleanEndpoint := strings.Split(endpoint, "?")[0]
	if cleanEndpoint == "" {
		cleanEndpoint = "/"
	}

	session := vd.getOrCreateSession(sessionID)
	session.mu.Lock()
	defer session.mu.Unlock()

	now := time.Now()
	session.lastActivity = now

	// 1. Evaluate Max Requests Per Second (RPS) over rolling 1-second window
	if vd.config.MaxRPS > 0 {
		rpsThreshold := now.Add(-1 * time.Second)
		validRPSIdx := 0
		for i, t := range session.requests {
			if t.After(rpsThreshold) {
				validRPSIdx = i
				break
			}
			if i == len(session.requests)-1 {
				validRPSIdx = len(session.requests)
			}
		}
		session.requests = session.requests[validRPSIdx:]

		if float64(len(session.requests)) >= vd.config.MaxRPS {
			incident := &SREIncident{
				IncidentID:     generateIncidentID(),
				Timestamp:      now,
				CircuitState:   "VELOCITY_BLOCKED",
				FailureClass:   ClassVelocityMaxRPS,
				ObservedCount:  len(session.requests) + 1,
				Threshold:      int(vd.config.MaxRPS),
				Mitigation:     fmt.Sprintf("Request blocked: session exceeded maximum throughput velocity of %.1f req/s", vd.config.MaxRPS),
				ActionRequired: "Throttle client request rate or reduce agent execution concurrency",
			}
			return false, incident
		}
	}

	// 2. Evaluate Max Endpoint Repeats over RepeatWindow (e.g. 20 hits in 10s)
	if vd.config.MaxEndpointRepeats > 0 {
		repeatThreshold := now.Add(-vd.config.RepeatWindow)
		history := session.endpointRequests[cleanEndpoint]
		validEndpointIdx := 0
		for i, t := range history {
			if t.After(repeatThreshold) {
				validEndpointIdx = i
				break
			}
			if i == len(history)-1 {
				validEndpointIdx = len(history)
			}
		}
		history = history[validEndpointIdx:]
		session.endpointRequests[cleanEndpoint] = history

		if len(history) >= vd.config.MaxEndpointRepeats {
			incident := &SREIncident{
				IncidentID:     generateIncidentID(),
				Timestamp:      now,
				CircuitState:   "VELOCITY_BLOCKED",
				FailureClass:   ClassVelocityMaxEndpointRepeats,
				ObservedCount:  len(history) + 1,
				Threshold:      vd.config.MaxEndpointRepeats,
				Mitigation:     fmt.Sprintf("Request blocked: endpoint %s hit %d times in %s (limit: %d)", cleanEndpoint, len(history)+1, vd.config.RepeatWindow, vd.config.MaxEndpointRepeats),
				ActionRequired: "Check client for runaway request recursion or retry storm targeting this endpoint",
			}
			return false, incident
		}
	}

	// Record timestamp for both global requests and endpoint
	session.requests = append(session.requests, now)
	session.endpointRequests[cleanEndpoint] = append(session.endpointRequests[cleanEndpoint], now)

	return true, nil
}

// Cleanup removes stale sessions inactive for longer than maxAge.
func (vd *VelocityDetector) Cleanup(maxAge time.Duration) {
	vd.mu.Lock()
	defer vd.mu.Unlock()

	threshold := time.Now().Add(-maxAge)
	for id, s := range vd.sessions {
		s.mu.Lock()
		if s.lastActivity.Before(threshold) {
			delete(vd.sessions, id)
		}
		s.mu.Unlock()
	}
}
