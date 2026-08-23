# 📘 The Complete Beginner's Guide to the Circuit Breaker Proxy

Welcome! This document is designed for anyone new to **Go (Golang)** or distributed systems who wants to understand **every single file, concept, and line of logic** in this project.

By the end of this guide, you will:
1. Understand how an AI Circuit Breaker Reverse Proxy works from scratch.
2. Understand every file, package, algorithm, and subsystem in this repository.
3. Understand core Go concepts used (Pointers, Mutexes, Interfaces, Streams, Zero-Allocation arrays, Locality-Sensitive Hashing).
4. Understand how YAML configuration, Environment Variables, and CLI Flags are layered and validated.
5. Know exactly **where and how to add your own custom code**.

---

# Table of Contents
1. [What is This Project Doing? (The Big Picture)](#1-what-is-this-project-doing-the-big-picture)
2. [Go Fundamentals Used in This Codebase](#2-go-fundamentals-used-in-this-codebase)
3. [Architecture & Request Lifecycle](#3-architecture--request-lifecycle)
4. [File-by-File Detailed Breakdown](#4-file-by-file-detailed-breakdown)
   - [A. Entrypoint: `main.go`](#a-entrypoint-maingo)
   - [B. Configuration Engine: `utils/config.go`](#b-configuration-engine-utilsconfiggo)
   - [C. Reverse Proxy Engine: `proxy/proxy.go`](#c-reverse-proxy-engine-proxyproxygo)
   - [D. Tool Fingerprinting & SimHash: `inspector/fingerprint.go`](#d-tool-fingerprinting--simhash-inspectorfingerprintgo)
   - [E. Circuit Breaker Engine: `inspector/circuit_breaker.go`](#e-circuit-breaker-engine-inspectorcircuit_breakergo)
   - [F. Session Velocity Detector: `inspector/velocity.go`](#f-session-velocity-detector-inspectorvelocitygo)
   - [G. Zero-Allocation Entropy Engine: `inspector/entropy.go`](#g-zero-allocation-entropy-engine-inspectorentropygo)
   - [H. Live Stream Interceptor: `inspector/response.go`](#h-live-stream-interceptor-inspectorresponsego)
   - [I. Sliding Window Tracker: `inspector/sliding_window.go`](#i-sliding-window-tracker-inspectorsliding_windowgo)
   - [J. JSON Storage Engine: `inspector/storage.go`](#j-json-storage-engine-inspectorstoragego)
   - [K. Shared Utilities: `utils/colors.go`, `utils/env.go`, `utils/printer.go`](#k-shared-utilities-utilscolorsgo-utilsenvgo-utilsprintergo)
5. [Configuration Reference (`config.yaml`)](#5-configuration-reference-configyaml)
6. [How to Add Your Own Code (Cookbook)](#6-how-to-add-your-own-code-cookbook)
7. [Commands Reference](#7-commands-reference)

---

# 1. What is This Project Doing? (The Big Picture)

Imagine you are building an AI Agent application. Your agent talks to an LLM provider (like OpenAI or a local model) to perform actions using **tools** (e.g. database queries, weather searches, bash commands).

Modern LLMs frequently suffer from dangerous runtime failure modes:
1. **Tool-Call Hallucination Loops (Class A)**:
   The model gets stuck calling the exact same tool or slightly mutated versions of it (e.g. `(attempt 0)` vs `(attempt 1)`).
2. **Error Accumulation Loops (Class B)**:
   A tool returns an error, and the agent blindly retries in a storm without resolving the root cause.
3. **Anomalous Request Velocity**:
   A runaway agent issues hundreds of requests per second, hammering specific endpoints or exceeding downstream rate limits.
4. **Model Token Degeneration**:
   The model degenerates into repetitive character spam (`! ! ! ! !`), draining token budgets.

This project sits **in between** your client and the LLM backend as an **Intelligent Circuit Breaker Reverse Proxy**:

```text
┌─────────────┐       HTTP Request        ┌───────────────────────┐       HTTP Request        ┌─────────────┐
│             │ ────────────────────────> │                       │ ────────────────────────> │             │
│ Client / AI │                           │ Circuit Breaker Proxy │                           │ LLM Backend │
│ Application │ <──────────────────────── │      (This App)       │ <──────────────────────── │  (OpenAI)   │
└─────────────┘     Streaming Response    └───────────────────────┘     Streaming Response    └─────────────┘
                                                      │
                                                      ├── 🔍 Inspects Conversation & Tokens
                                                      ├── 🛡️ SimHash (LSH) & Jaccard Loop Breaker
                                                      ├── ⚡ Velocity Detector (Max RPS & Repeats)
                                                      ├── 📊 0-Allocation Shannon Stream Entropy
                                                      ├── ⏱️ Rolling Sliding Window Metrics
                                                      └── 💾 Structured JSON Audit Persistence
```

---

# 2. Go Fundamentals Used in This Codebase

If you are new to Go, here are the essential concepts used throughout the codebase:

### 1. Pointers (`*Type` and `&variable`)
- In Go, variables are passed by **value** (copied) by default.
- Using a pointer `*OpenAIRequest` allows functions to modify the original struct without copying all its data in memory.
- `&myStruct` takes the memory address of `myStruct`.
- In [`utils/config.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/utils/config.go), pointers (`*int`, `*bool`, `*float64`) are used to distinguish between an **unset YAML field** (`nil`) and an explicit `0` or `false`.

### 2. Slices (`[]byte`) vs Fixed Arrays (`[256]uint32`)
- **Slice (`[]byte`)**: Dynamically sized, allocated on the **heap** (involves memory allocation overhead).
- **Array (`[256]uint32`)**: Fixed size, allocated on the **stack** (zero heap allocations, blazing fast!).
- We use fixed arrays in `inspector/entropy.go` to guarantee **`0 B/op` zero-allocation performance**.

### 3. Thread Safety: Mutexes (`sync.Mutex` and `sync.RWMutex`)
- When thousands of requests hit the proxy simultaneously, they run concurrently in separate **goroutines** (lightweight threads).
- `sync.RWMutex` prevents **race conditions** (two goroutines reading and writing to the same map or slice at the same time).
- `mu.Lock()` / `mu.Unlock()` gives exclusive write access; `mu.RLock()` / `mu.RUnlock()` allows multiple concurrent readers.

### 4. Interfaces: `io.Reader` and `io.ReadCloser`
- In Go, HTTP bodies are **single-read streams** implementing `io.ReadCloser`.
- You can read a request body **only once**. To inspect it and still forward it, we read the bytes into memory and restore the stream using `io.NopCloser(bytes.NewReader(bodyBytes))`.

---

# 3. Architecture & Request Lifecycle

When a request arrives at `http://localhost:8080/v1/chat/completions`:

```text
1. Client Sends Request (JSON with conversation messages)
   │
2. [proxy/proxy.go] Resolves session key (X-Session-ID / Authorization / IP)
   │
3. [inspector/velocity.go] Evaluates Session Velocity (Max RPS & Max Endpoint Repeats)
   ├─► IF EXCEEDED: Emits SRE Alert + Returns HTTP 429 Too Many Requests
   └─► IF OK: Continues
   │
4. [proxy/proxy.go] Intercepts & Buffers r.Body
   │
5. [inspector/openai.go] Parses JSON into structured OpenAIRequest
   │
6. [inspector/circuit_breaker.go] Checks conversation history for SimHash/Jaccard loops (Class A) or errors (Class B)
   ├─► IF TRIPPED: Returns HTTP 429 + Structured SRE Incident JSON error response
   └─► IF OK: Continues
   │
7. [inspector/sliding_window.go] Ingests request tokens into rolling window metrics
   ├─► IF LIMIT ENFORCEMENT ON & EXCEEDED: Returns HTTP 429
   └─► IF OK: Continues
   │
8. [inspector/openai.go] Pretty-prints latest interaction turn to terminal
   │
9. [proxy/proxy.go] Forwards request to Target LLM Server
   │
10. [inspector/response.go] Intercepts Upstream LLM Response Stream
    ├── Live streams text tokens and tool calls to terminal
    ├── [inspector/entropy.go] Computes Shannon entropy on-the-fly (0 B/op allocations)
    ├── [inspector/fingerprint.go] Computes SimHash 64-bit LSH on tool calls
    └── Checks Circuit Breaker during streaming:
        └─► IF TRIPPED: Actively cuts off LLM stream & injects SRE JSON error
   │
11. [inspector/storage.go] Saves complete request + response + entropy metadata to ./conversations/
   │
12. Downstream Client receives response stream
```

---

# 4. File-by-File Detailed Breakdown

---

### A. Entrypoint: `main.go`
**Location**: [`main.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/main.go)

#### What it does:
1. Calls `utils.ParseAndValidateConfig(os.Args[1:])` to resolve configuration from YAML, environment variables, and CLI flags.
2. Initializes the `SlidingWindow`, `CircuitBreakerEngine`, `VelocityDetector`, and structured `Logger`.
3. Starts the HTTP server and prints the startup banner.
4. Handles graceful shutdown on `SIGINT` / `SIGTERM`.

---

### B. Configuration Engine: `utils/config.go`
**Location**: [`utils/config.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/utils/config.go)

#### What it does:
1. **`LoadYAMLConfigFile(path string)`**: Reads and unmarshals `config.yaml` using `gopkg.in/yaml.v3`.
2. **`ParseAndValidateConfig(args []string)`**: Enforces the 4-tier configuration hierarchy:
   $$\text{Code Defaults} \longrightarrow \text{YAML File} \longrightarrow \text{Environment Variables} \longrightarrow \text{CLI Flags}$$
3. **`AppConfig.Validate()`**: Ensures URLs are valid with scheme/host, durations are positive, and thresholds are within valid mathematical bounds.

---

### C. Reverse Proxy Engine: `proxy/proxy.go`
**Location**: [`proxy/proxy.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/proxy/proxy.go)

#### What it does:
1. **Velocity Gatekeeper**: Checks `velocityDetector.RecordRequest(sessionKey, path)` before processing bodies.
2. **Request Interception**: Reads `r.Body`, unmarshals OpenAI JSON, and replaces `r.Body` with `io.NopCloser`.
3. **Edge Loop Rejection**: Runs `cbEngine.CheckRequestHistory(messages)` to reject looping prompts before reaching LLM backends.
4. **Response Hook (`ModifyResponse`)**: Attaches `inspector.NewRealtimeResponseReader` to `resp.Body` for live stream analysis.
5. **Immediate SSE Flushing**: Uses `FlushInterval = -1` for real-time token streaming.

---

### D. Tool Fingerprinting & SimHash: `inspector/fingerprint.go`
**Location**: [`inspector/fingerprint.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/fingerprint.go)

#### What it does:
- **Exact Hash**: `ComputeToolFingerprint` computes `SHA256(tool_name + ":" + canonical_json)`.
- **Fuzzy SimHash (LSH)**: `ComputeSimHash` uses `github.com/go-dedup/simhash` to generate a 64-bit Locality Sensitive Hash.
  - When prompts mutate slightly (e.g. attempt 0 vs attempt 1), the **Hamming distance** ($D_H$) is small ($\le 3$ bits).
- **Jaccard Similarity**: Computes 3-gram $k$-shingle set overlap $J(A, B) = \frac{|A \cap B|}{|A \cup B|}$.

---

### E. Circuit Breaker Engine: `inspector/circuit_breaker.go`
**Location**: [`inspector/circuit_breaker.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/circuit_breaker.go)

#### What it does:
Maintains thread-safe rolling history of tool calls:
- **Class A Loop (`CLASS_A_IDENTICAL_TOOL_CALL_LOOP`)**: Trips when identical or SimHash near-duplicate tool calls ($D_H \le 3$) reach `ClassAMaxIdentical` (default: 3).
- **Class B Errors (`CLASS_B_ERROR_ACCUMULATION`)**: Trips when accumulated tool failures reach `ClassBMaxErrors` (default: 4).
- **Structured SRE Incident**: Emits structured JSON with `IncidentID`, `FailureClass`, `SimHashHex`, `HammingDistance`, `Mitigation`, and `ActionRequired`.

---

### F. Session Velocity Detector: `inspector/velocity.go`
**Location**: [`inspector/velocity.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/velocity.go)

#### What it does:
Enforces maximum throughput rules per session:
1. **Max RPS (`max_rps: 5.0`)**: Rejects sessions exceeding 5 requests per second.
2. **Max Endpoint Repeats (`max_endpoint_repeats: 20` in `10s`)**: Rejects sessions that hammer the same endpoint $\ge 20$ times in 10 seconds.

---

### G. Zero-Allocation Entropy Engine: `inspector/entropy.go`
**Location**: [`inspector/entropy.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/entropy.go)

#### What it does:
Calculates **Shannon Information Entropy** ($H(X) = -\sum p(x) \log_2 p(x)$) on byte streams:
- Normal text: $\approx 3.5 - 5.0$ bits/byte.
- Repetition collapse (`AAAAA...`): $\rightarrow 0.0$ bits/byte.
- Uses stack-allocated fixed arrays (`[256]uint32` and `[1024]byte`) for **`0 B/op` zero-allocation performance**.

---

### H. Live Stream Interceptor: `inspector/response.go`
**Location**: [`inspector/response.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/response.go)

#### What it does:
Wraps `resp.Body`:
- Prints streaming tokens and tool calls live to stdout.
- Computes stream entropy on-the-fly.
- Actively closes the upstream connection and injects SRE incident JSON if the circuit breaker trips mid-stream.

---

### I. Sliding Window Tracker: `inspector/sliding_window.go`
**Location**: [`inspector/sliding_window.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/sliding_window.go)

#### What it does:
Tracks aggregate proxy metrics over a rolling window (e.g. 60 seconds):
- Request throughput, token estimates, and model usage distributions.
- Optional sliding window enforcement (`-enforce-limits=true`).

---

### J. JSON Storage Engine: `inspector/storage.go`
**Location**: [`inspector/storage.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/storage.go)

#### What it does:
Saves structured JSON conversation audit logs to `./conversations/conv_<timestamp>_<id>.json`.

---

### K. Shared Utilities: `utils/`
**Location**: [`utils/`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/utils/)

- **`colors.go`**: ANSI styling codes.
- **`env.go`**: Safe environment variable parsers.
- **`config.go`**: YAML loader and configuration validator.
- **`printer.go`**: Centralized terminal formatting, banners, access logs, stream typing, and SRE alert boxes.

---

# 5. Configuration Reference (`config.yaml`)

Every setting can be specified in [`config.yaml`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/config.yaml):

```yaml
server:
  port: "8080"
  target_url: "http://localhost:3000"
  log_level: "INFO"

sliding_window:
  window_duration: "60s"
  max_requests: 0
  max_tokens: 0
  enforce_limits: false

circuit_breaker:
  enabled: true
  class_a_limit: 3
  class_b_limit: 4
  max_hamming_distance: 3
  jaccard_threshold: 0.85

velocity:
  enabled: true
  max_rps: 5.0
  max_endpoint_repeats: 20
  repeat_window: "10s"

storage:
  save_conversations: true
  conversations_dir: "./conversations"
```

---

# 6. How to Add Your Own Code (Cookbook)

### Scenario A: Adding a Custom Circuit Breaker Rule
Open [`inspector/circuit_breaker.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/inspector/circuit_breaker.go):
```go
// Block dangerous tool commands
if strings.Contains(strings.ToLower(args), "rm -rf") {
    return &SREIncident{
        IncidentID:   generateIncidentID(),
        Timestamp:    time.Now(),
        CircuitState: "OPEN_TRIPPED",
        FailureClass: "SECURITY_VIOLATION",
        Mitigation:   "Blocked dangerous command",
    }, true
}
```

### Scenario B: Custom Auth Filter
Open [`proxy/proxy.go`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/proxy/proxy.go):
```go
if r.Header.Get("Authorization") == "" {
    w.WriteHeader(http.StatusUnauthorized)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
    return
}
```

---

# 7. Commands Reference

```bash
# Start server with default config.yaml
./start.sh

# Start server with custom flags
./proxy-server -port 8080 -target http://localhost:3000 -cb-class-a-limit 3

# Run all unit and integration tests
go test -v ./...

# Run concurrency race detector
go test -race ./...

# Run zero-allocation benchmarks
go test -bench=BenchmarkEntropy -benchmem ./inspector

# Static analysis
go vet ./...

# Build binary
go build -o proxy-server main.go
```
