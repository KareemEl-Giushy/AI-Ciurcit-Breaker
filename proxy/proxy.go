package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"circuit-breaker-proxy/inspector"
	"circuit-breaker-proxy/utils"
)

type contextKey string

const reqInfoKey contextKey = "proxy_req_info"

type requestInfo struct {
	openAIReq *inspector.OpenAIRequest
	startTime time.Time
	clientIP  string
	path      string
}

// Config defines the configuration settings for the proxy server.
type Config struct {
	TargetURL         *url.URL
	Logger            *slog.Logger
	Inspector         *inspector.SlidingWindow
	CircuitBreaker    *inspector.CircuitBreakerEngine
	Velocity          *inspector.VelocityDetector
	SaveConversations bool
	SaveDir           string
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code and bytes written.
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (rw *responseWriterWrapper) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriterWrapper) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// getSessionKey resolves a unique session identifier for rate and velocity tracking.
func getSessionKey(r *http.Request) string {
	if sessionID := r.Header.Get("X-Session-ID"); sessionID != "" {
		return sessionID
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		return auth
	}
	// Fallback to client remote address (strip port if possible)
	remote := r.RemoteAddr
	if idx := strings.LastIndex(remote, ":"); idx != -1 {
		return remote[:idx]
	}
	return remote
}

// NewServerHandler creates an HTTP handler that inspects OpenAI JSON payloads,
// detects Class A / Class B repetition loops via SimHash LSH, enforces session velocity rules,
// prints conversation history, logs streams, and proxies requests to the target destination.
func NewServerHandler(cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	saveDir := cfg.SaveDir
	if saveDir == "" {
		saveDir = "./conversations"
	}

	windowInspector := cfg.Inspector
	if windowInspector == nil {
		windowInspector = inspector.NewSlidingWindow(inspector.SlidingWindowConfig{
			WindowDuration: 60 * time.Second,
		})
	}

	cbEngine := cfg.CircuitBreaker
	if cbEngine == nil {
		cbEngine = inspector.NewCircuitBreakerEngine(inspector.CircuitBreakerConfig{
			WindowDuration:     60 * time.Second,
			ClassAMaxIdentical: 3,
			ClassBMaxErrors:    4,
			MaxHammingDistance: 3,
			JaccardThreshold:   0.85,
			Enabled:            true,
		})
	}

	velocityDetector := cfg.Velocity
	if velocityDetector == nil {
		velocityDetector = inspector.NewVelocityDetector(inspector.VelocityConfig{
			MaxRPS:             5.0,
			MaxEndpointRepeats: 20,
			RepeatWindow:       10 * time.Second,
			Enabled:            true,
		})
	}

	reverseProxy := &httputil.ReverseProxy{
		FlushInterval: -1, // Real-time immediate streaming
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(cfg.TargetURL)
			pr.SetXForwarded()
			pr.Out.Host = cfg.TargetURL.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.Body != nil {
				reqPath := "/"
				var info *requestInfo
				if resp.Request != nil {
					if resp.Request.URL != nil {
						reqPath = resp.Request.URL.Path
					}
					if v, ok := resp.Request.Context().Value(reqInfoKey).(*requestInfo); ok {
						info = v
					}
				}

				reader := inspector.NewRealtimeResponseReader(
					resp.Body,
					resp.Header.Get("Content-Type"),
					logger,
					reqPath,
				)
				reader.SetCircuitBreaker(cbEngine)

				// Register callback to save structured conversation JSON record
				if cfg.SaveConversations && info != nil && info.openAIReq != nil {
					reader.SetOnCompleteCallback(func(recorded inspector.RecordedResponse) {
						record := &inspector.ConversationRecord{
							Timestamp:  info.startTime,
							Path:       info.path,
							ClientIP:   info.clientIP,
							DurationMs: time.Since(info.startTime).Milliseconds(),
							StatusCode: resp.StatusCode,
							Request:    info.openAIReq,
							Response:   recorded,
						}
						savedPath, err := inspector.SaveConversationRecord(record, saveDir)
						if err != nil {
							logger.Error("failed to save conversation record", slog.String("error", err.Error()))
						} else {
							utils.PrintSavedJSON(savedPath)
						}
					})
				}

				resp.Body = reader
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("proxy forward failed",
				slog.String("method", r.Method),
				slog.String("path", r.URL.RequestURI()),
				slog.String("target", cfg.TargetURL.String()),
				slog.String("error", err.Error()),
			)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "Bad Gateway",
				"message": fmt.Sprintf("Failed to reach target destination: %v", err),
			})
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sessionKey := getSessionKey(r)

		// 1. Check Velocity Detector (Max RPS & Max Endpoint Repeats per session)
		if allowed, velocityIncident := velocityDetector.RecordRequest(sessionKey, r.URL.Path); !allowed && velocityIncident != nil {
			utils.PrintSREIncident("VELOCITY LIMIT EXCEEDED", utils.SREIncidentSummary{
				IncidentID:     velocityIncident.IncidentID,
				FailureClass:   velocityIncident.FailureClass,
				ObservedCount:  velocityIncident.ObservedCount,
				Threshold:      velocityIncident.Threshold,
				Mitigation:     velocityIncident.Mitigation,
				ActionRequired: velocityIncident.ActionRequired,
			})

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(inspector.SREErrorResponse{SREIncident: *velocityIncident})
			return
		}

		var bodyBytes []byte
		var openAIReq *inspector.OpenAIRequest

		if r.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(r.Body)
			if err != nil {
				logger.Warn("failed to read request body", slog.String("error", err.Error()))
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		if len(bodyBytes) > 0 {
			parsed, err := inspector.ParseOpenAIJSON(bodyBytes)
			if err == nil {
				openAIReq = parsed
			}
		}

		// 2. Check Circuit Breaker for Class A (SimHash/Jaccard loops) or Class B (error accumulation) in request history
		if openAIReq != nil && len(openAIReq.Messages) > 0 {
			if incident, tripped := cbEngine.CheckRequestHistory(openAIReq.Messages); tripped {
				utils.PrintSREIncident("CIRCUIT BREAKER OPEN (REQUEST REJECTED)", utils.SREIncidentSummary{
					IncidentID:      incident.IncidentID,
					FailureClass:    incident.FailureClass,
					ToolName:        incident.ToolName,
					Fingerprint:     incident.Fingerprint,
					SimHashHex:      incident.SimHashHex,
					HammingDistance: incident.HammingDistance,
					SimilarityScore: incident.SimilarityScore,
					ObservedCount:   incident.ObservedCount,
					Threshold:       incident.Threshold,
					Mitigation:      incident.Mitigation,
					ActionRequired:  incident.ActionRequired,
				})

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(inspector.SREErrorResponse{SREIncident: *incident})
				return
			}
		}

		// Pretty-print full conversation history if present
		if openAIReq != nil && len(openAIReq.Messages) > 0 {
			inspector.PrintConversation(openAIReq, r.URL.Path)
		}

		// Store request info in context for response recorder
		info := &requestInfo{
			openAIReq: openAIReq,
			startTime: start,
			clientIP:  r.RemoteAddr,
			path:      r.URL.Path,
		}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey, info))

		stats, allowed := windowInspector.Record(openAIReq, r.RemoteAddr)

		if !allowed {
			utils.PrintBlockedSummary(r.Method, r.URL.RequestURI(), stats.LimitReason)

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":           "Too Many Requests",
				"message":         "Sliding window threshold exceeded",
				"reason":          stats.LimitReason,
				"window_requests": stats.TotalRequests,
				"window_tokens":   stats.TotalTokens,
			})
			return
		}

		rw := &responseWriterWrapper{ResponseWriter: w}
		reverseProxy.ServeHTTP(rw, r)
		duration := time.Since(start)

		// Print request summary using centralized utils printer
		model := ""
		msgCount := 0
		tokens := 0
		if openAIReq != nil {
			model = openAIReq.Model
			msgCount = len(openAIReq.Messages)
			tokens = openAIReq.EstimateTokens()
		}
		utils.PrintRequestSummary(r.Method, r.URL.RequestURI(), rw.statusCode, duration, model, msgCount, tokens, stats.TotalRequests, stats.TotalTokens)
	})
}
