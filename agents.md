# AGENTS.md

Welcome! This document provides context, architectural guidelines, and development workflows for AI agents working on the **Circuit Breaker Proxy** codebase.

---

## 1. Project Overview

This repository provides an HTTP Reverse Proxy written in Go designed to:
1. Intercept incoming requests on a configured port across any URL path and query parameters.
2. Inspect the HTTP request body by parsing it as an **OpenAI-compliant JSON payload** (`ChatCompletionRequest`).
3. Track and aggregate usage metrics (requests, token estimates, model distribution) in a **thread-safe Sliding Window**.
4. Optionally enforce rate/token limits (returning `429 Too Many Requests`) or act as a sliding window circuit breaker.
5. Log LLM responses (streaming tokens via SSE and standard JSON completions) in **real-time**.
6. Seamlessly forward requests and response streams to the backend target with immediate chunk flushing (`FlushInterval = -1`).
7. Rely entirely on Go's standard library (zero third-party runtime dependencies).

---

## 2. Directory Structure & Key Files

```text
.
├── main.go                       # Application entrypoint, CLI flags, env parsing, graceful shutdown
├── proxy/
│   ├── proxy.go                  # Reverse proxy handler, request logging middleware, body buffering, SSE flushing
│   └── proxy_test.go             # Integration & unit tests for proxy forwarding, SSE streaming, rate limiting
├── inspector/
│   ├── circuit_breaker.go        # CircuitBreakerEngine with tool repetition and error accumulation detection
│   ├── circuit_breaker_test.go   # Unit tests for fingerprinting, SimHash, tool loop, and error accumulation
│   ├── entropy.go                # Zero-allocation Shannon entropy engine on incoming chunk streams
│   ├── entropy_test.go           # Unit tests & benchmarks for 0 B/op stream entropy calculation
│   ├── fingerprint.go            # 64-bit SimHash (LSH) & SHA256 tool fingerprinting and Jaccard similarity
│   ├── openai.go                 # OpenAI structs (OpenAIRequest, ChatMessage, ToolCall), token estimation
│   ├── response.go               # RealtimeResponseReader for live token & tool calls streaming with active termination
│   ├── response_test.go          # Unit tests for SSE chunk processing, tool calls, and conversation formatting
│   ├── sliding_window.go         # Thread-safe SlidingWindow inspector, rolling time window eviction, metrics
│   ├── sliding_window_test.go    # Unit tests for JSON unmarshaling, sliding eviction, concurrency
│   ├── storage.go                # Multi-turn conversation persistence grouped and appended by client IP
│   ├── storage_test.go           # Unit tests for multi-turn client IP persistence and session isolation
│   ├── velocity.go               # Session VelocityDetector enforcing Max RPS and Endpoint Repeat limits
│   └── velocity_test.go          # Unit tests for session velocity rate limits and endpoint repeat tracking
├── utils/
│   ├── colors.go                 # Shared ANSI terminal color and styling escape codes
│   ├── config.go                 # YAML configuration parsing, layered resolution, and validation
│   ├── config_test.go            # Unit tests for YAML configuration parsing and validation
│   ├── env.go                    # Shared environment variable parsing utilities (string, int, bool, duration)
│   ├── env_test.go               # Unit tests for environment variable parsers
│   ├── printer.go                # Shared terminal formatting, banners, access logs, stream & SRE alerts
│   └── printer_test.go           # Unit tests for terminal formatting and printing functions
├── start.sh                      # Executable startup shell script
├── go.mod                        # Go module definition (pure Go standard library)
└── README.md                     # User-facing documentation
```

---

## 3. Core Architectural Concepts & Invariants

### A. Body Stream Preservation
- HTTP request bodies in Go are single-read streams (`io.ReadCloser`).
- When inspecting request bodies in `proxy/proxy.go`, read into memory and **always replace `r.Body`**:
  ```go
  bodyBytes, _ := io.ReadAll(r.Body)
  r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
  ```
- This ensures downstream target services receive the complete payload unmodified.

### B. Reverse Proxy Routing (Go `httputil.ReverseProxy` Rewrite Hook)
- The proxy uses `httputil.ReverseProxy.Rewrite`:
  - `pr.SetURL(targetURL)`: Rewrites scheme, host, and joins base path with incoming path and query string.
  - `pr.SetXForwarded()`: Sets standard `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`.
  - `pr.Out.Host = targetURL.Host`: Ensures HTTP Host header matches the destination host.

### C. Sliding Window Inspector (`inspector/sliding_window.go`)
- Uses `sync.RWMutex` for concurrent safety under high request loads.
- On each record or stats query, stale entries older than `now - WindowDuration` are evicted.
- In **Enforce Mode** (`-enforce-limits`), if `MaxRequests` or `MaxTokens` is exceeded within the active window, the proxy halts forwarding and returns `429 Too Many Requests` with `Retry-After`.

---

## 4. Development & Testing Commands

### Build Binary
```bash
go build -o proxy-server main.go
```

### Run Tests
Always execute tests across all packages before committing:
```bash
go test -v ./...
```

### Static Analysis
```bash
go vet ./...
```

### Start Server
```bash
./start.sh
# Or with options:
./start.sh -port 8080 -target http://localhost:3000 -window-duration 60s
```

---

## 5. Environment Variables & CLI Flags

| Flag | Env Variable | Default | Purpose |
|---|---|---|---|
| `-port` | `PORT` | `8080` | Server listening port |
| `-target` | `TARGET_URL` | `http://localhost:3000` | Target destination base URL |
| `-log-level` | `LOG_LEVEL` | `INFO` | Logging level (`DEBUG`, `INFO`, `WARN`, `ERROR`) |
| `-window-duration` | `WINDOW_DURATION` | `60s` | Sliding window rolling time duration |
| `-window-max-requests` | `WINDOW_MAX_REQUESTS` | `0` | Max requests allowed in sliding window (0 = disabled) |
| `-window-max-tokens` | `WINDOW_MAX_TOKENS` | `0` | Max estimated tokens allowed in sliding window (0 = disabled) |
| `-show-sliding-window` | `SHOW_SLIDING_WINDOW` | `false` | Display sliding window terminal dashboard instead of conversation turns |
| `-cb-max-tool-repeats` | `CB_MAX_TOOL_REPEATS` | `3` | Max repeated/mutated tool calls allowed in window |
| `-cb-max-tool-errors` | `CB_MAX_TOOL_ERRORS` | `4` | Max accumulated tool execution errors allowed in window |
| `-cb-max-hamming-dist` | `CB_MAX_HAMMING_DIST` | `3` | Max SimHash Hamming distance for near-duplicate loop detection (0-64) |
| `-cb-jaccard-threshold` | `CB_JACCARD_THRESHOLD` | `0.85` | Jaccard similarity threshold for near-duplicate tool calls (0.0 to 1.0) |
| `-velocity-enabled` | `VELOCITY_ENABLED` | `true` | Enable session-based velocity detection |
| `-velocity-max-rps` | `VELOCITY_MAX_RPS` | `5.0` | Max allowed requests per second per session (0 = disabled) |
| `-velocity-max-endpoint-repeats` | `VELOCITY_MAX_ENDPOINT_REPEATS` | `20` | Max hits to same endpoint in window (0 = disabled) |
| `-velocity-repeat-window` | `VELOCITY_REPEAT_WINDOW` | `10s` | Time window for endpoint repeat tracking |
| `-save-conversations` | `SAVE_CONVERSATIONS` | `true` | Save structured JSON conversation audit records grouped by client IP |
| `-save-dir` | `CONVERSATIONS_DIR` | `./conversations` | Directory to store conversation JSON records |

---

## 6. Guidance for Future Agents

- **Circuit Breaker Extensions**: If adding failure-rate based circuit breaking, track downstream HTTP 5xx responses and connection errors in a sliding window ring buffer.
- **Zero External Dependencies**: Keep the repository purely standard library based.
- **Error Responses**: Return JSON error responses with clear `error` and `message` attributes.
