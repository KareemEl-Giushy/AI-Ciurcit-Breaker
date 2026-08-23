package inspector

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RecordedResponse captures the model's output (text, tool calls, entropy, stream status).
type RecordedResponse struct {
	Model     string     `json:"model,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Streamed  bool       `json:"streamed"`
	Entropy   float64    `json:"entropy,omitempty"`
}

// ConversationTurn captures an individual request-response exchange within a conversation session.
type ConversationTurn struct {
	TurnIndex  int              `json:"turn_index"`
	Timestamp  time.Time        `json:"timestamp"`
	Path       string           `json:"path"`
	DurationMs int64            `json:"duration_ms"`
	StatusCode int              `json:"status_code"`
	Request    *OpenAIRequest   `json:"request"`
	Response   RecordedResponse `json:"response"`
}

// ConversationRecord captures the entire multi-turn conversation session for a client IP.
type ConversationRecord struct {
	ID        string             `json:"conversation_id"`
	ClientIP  string             `json:"client_ip"`
	StartedAt time.Time          `json:"started_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	TurnCount int                `json:"turn_count"`
	Turns     []ConversationTurn `json:"turns"`

	// Convenience / top-level snapshot fields for the latest turn
	Timestamp  time.Time        `json:"timestamp,omitempty"`
	Path       string           `json:"path,omitempty"`
	DurationMs int64            `json:"duration_ms,omitempty"`
	StatusCode int              `json:"status_code,omitempty"`
	Request    *OpenAIRequest   `json:"request,omitempty"`
	Response   RecordedResponse `json:"response,omitempty"`
}

type clientSession struct {
	record   *ConversationRecord
	filePath string
	lastSeen time.Time
}

// ConversationStorageManager manages persistent recording of conversations grouped by client IP.
type ConversationStorageManager struct {
	mu             sync.Mutex
	saveDir        string
	sessionTimeout time.Duration
	sessions       map[string]*clientSession // key: sanitized client IP
}

var (
	defaultStorageManager     *ConversationStorageManager
	defaultStorageManagerOnce sync.Once
)

// GetDefaultStorageManager returns the shared ConversationStorageManager singleton.
func GetDefaultStorageManager(saveDir string) *ConversationStorageManager {
	defaultStorageManagerOnce.Do(func() {
		defaultStorageManager = NewConversationStorageManager(saveDir, 30*time.Minute)
	})
	if saveDir != "" && defaultStorageManager.saveDir != saveDir {
		defaultStorageManager.saveDir = saveDir
	}
	return defaultStorageManager
}

// NewConversationStorageManager creates an initialized ConversationStorageManager.
func NewConversationStorageManager(saveDir string, sessionTimeout time.Duration) *ConversationStorageManager {
	if saveDir == "" {
		saveDir = "./conversations"
	}
	if sessionTimeout <= 0 {
		sessionTimeout = 30 * time.Minute
	}
	return &ConversationStorageManager{
		saveDir:        saveDir,
		sessionTimeout: sessionTimeout,
		sessions:       make(map[string]*clientSession),
	}
}

// SanitizeClientIP removes port numbers and converts IP characters suitable for file naming.
func SanitizeClientIP(rawIP string) string {
	clean := strings.TrimSpace(rawIP)
	if host, _, err := net.SplitHostPort(clean); err == nil {
		clean = host
	} else if idx := strings.LastIndex(clean, ":"); idx != -1 && !strings.Contains(clean, "]") {
		clean = clean[:idx]
	}
	if clean == "" {
		clean = "unknown_client"
	}
	safe := strings.ReplaceAll(clean, ".", "_")
	safe = strings.ReplaceAll(safe, ":", "_")
	safe = strings.ReplaceAll(safe, "/", "_")
	return safe
}

// CleanIPAddress returns the clean IP address without ephemeral port.
func CleanIPAddress(rawIP string) string {
	clean := strings.TrimSpace(rawIP)
	if host, _, err := net.SplitHostPort(clean); err == nil {
		return host
	} else if idx := strings.LastIndex(clean, ":"); idx != -1 && !strings.Contains(clean, "]") {
		return clean[:idx]
	}
	if clean == "" {
		return "127.0.0.1"
	}
	return clean
}

func generateConversationID(clientIP string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	safeIP := SanitizeClientIP(clientIP)
	return fmt.Sprintf("conv_%s_%s_%s", time.Now().Format("20060102_150405"), safeIP, hex.EncodeToString(b))
}

// RecordTurn records a new interaction turn for the given client IP.
// If the client IP already has an active conversation file within the session timeout,
// the turn is appended to that file. Otherwise, a new conversation file is created.
func (mgr *ConversationStorageManager) RecordTurn(clientIP, path string, durationMs int64, statusCode int, req *OpenAIRequest, resp RecordedResponse) (string, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if err := os.MkdirAll(mgr.saveDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create conversations directory %s: %w", mgr.saveDir, err)
	}

	cleanIP := CleanIPAddress(clientIP)
	sessionKey := SanitizeClientIP(cleanIP)
	now := time.Now()

	sess, exists := mgr.sessions[sessionKey]
	if exists && now.Sub(sess.lastSeen) <= mgr.sessionTimeout {
		// Existing active session for this client IP -> Append turn to same conversation file
		sess.lastSeen = now
		sess.record.UpdatedAt = now
		turnIndex := len(sess.record.Turns) + 1
		sess.record.TurnCount = turnIndex

		turn := ConversationTurn{
			TurnIndex:  turnIndex,
			Timestamp:  now,
			Path:       path,
			DurationMs: durationMs,
			StatusCode: statusCode,
			Request:    req,
			Response:   resp,
		}
		sess.record.Turns = append(sess.record.Turns, turn)

		// Update latest turn snapshot
		sess.record.Timestamp = now
		sess.record.Path = path
		sess.record.DurationMs = durationMs
		sess.record.StatusCode = statusCode
		sess.record.Request = req
		sess.record.Response = resp

		jsonData, err := json.MarshalIndent(sess.record, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal updated conversation record: %w", err)
		}

		if err := os.WriteFile(sess.filePath, jsonData, 0644); err != nil {
			return "", fmt.Errorf("failed to write updated conversation file %s: %w", sess.filePath, err)
		}

		return sess.filePath, nil
	}

	// New client session or expired session -> Create new conversation file
	convID := generateConversationID(cleanIP)
	filePath := filepath.Join(mgr.saveDir, fmt.Sprintf("%s.json", convID))

	turn := ConversationTurn{
		TurnIndex:  1,
		Timestamp:  now,
		Path:       path,
		DurationMs: durationMs,
		StatusCode: statusCode,
		Request:    req,
		Response:   resp,
	}

	record := &ConversationRecord{
		ID:         convID,
		ClientIP:   cleanIP,
		StartedAt:  now,
		UpdatedAt:  now,
		TurnCount:  1,
		Turns:      []ConversationTurn{turn},
		Timestamp:  now,
		Path:       path,
		DurationMs: durationMs,
		StatusCode: statusCode,
		Request:    req,
		Response:   resp,
	}

	jsonData, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal new conversation record: %w", err)
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return "", fmt.Errorf("failed to write new conversation file %s: %w", filePath, err)
	}

	mgr.sessions[sessionKey] = &clientSession{
		record:   record,
		filePath: filePath,
		lastSeen: now,
	}

	return filePath, nil
}

// Reset clears in-memory active session tracking (useful for tests).
func (mgr *ConversationStorageManager) Reset() {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	mgr.sessions = make(map[string]*clientSession)
}

// SaveConversationRecord writes or appends a ConversationRecord to a JSON file.
// It integrates seamlessly with both individual records and multi-turn managers.
func SaveConversationRecord(record *ConversationRecord, dir string) (string, error) {
	if record == nil {
		return "", fmt.Errorf("cannot save nil conversation record")
	}

	if dir == "" {
		dir = "./conversations"
	}

	mgr := GetDefaultStorageManager(dir)
	return mgr.RecordTurn(
		record.ClientIP,
		record.Path,
		record.DurationMs,
		record.StatusCode,
		record.Request,
		record.Response,
	)
}
