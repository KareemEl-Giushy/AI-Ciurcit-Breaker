# Go HTTP Reverse Proxy & Circuit Breaker Inspector

A high-performance HTTP reverse proxy written in Go with an integrated **AI Circuit Breaker**, **64-bit SimHash (LSH) & Jaccard Repetition Breaker**, **Session Velocity Detector**, **Sliding Window Rate & Token Inspector**, **Zero-Allocation Stream Entropy Engine**, and **OpenAI JSON conversation visualizer**.

> 💡 **New to Go?** Check out the [Complete Beginner's Guide](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/BEGINNER_GUIDE.md) for a line-by-line explanation of the entire codebase and instructions on adding your own code!

It intercepts requests across any URL path and query string, reads and parses OpenAI-compliant JSON request bodies, tracks real-time usage metrics across a sliding time window (requests, tokens, models), detects prompt mutation loops and error accumulation, enforces session velocity limits, and transparently proxies requests to the target destination.

---

## Features

- **Full URL Path & Query Preservation**: Forwards any path (e.g. `/v1/chat/completions`) and query parameters intact.
- **SimHash 64-bit Locality Sensitive Hashing (LSH)**:
  - Uses `github.com/go-dedup/simhash` to detect mutated prompt loops (e.g. `attempt 0` vs `attempt 1`) where cryptographic hashes fail.
  - Configurable Hamming distance threshold (default: `3` bits, corresponding to ~95.3% similarity).
- **Jaccard 3-Gram Similarity**: Evaluates argument token set overlap ($J(A, B) = \frac{|A \cap B|}{|A \cup B|}$) to detect near-duplicate tool calls.
- **Session Velocity Detection**:
  - **Max RPS (`max_rps: 5.0`)**: Enforces maximum requests per second per session over a 1-second rolling window.
  - **Max Endpoint Repeats (`max_endpoint_repeats: 20` in `10s`)**: Blocks runaway recursion or retry storms hammering a single endpoint.
- **Sliding-Window Repetition & Error Detection**:
  - **Class A**: Identical / SimHash near-duplicate tool-call loops (default limit: `3`).
  - **Class B**: Tool error accumulation loops (default limit: `4`).
- **Active Stream Termination & Structured SRE JSON**: Immediately halts runaway streams and returns structured SRE incident JSON with root-cause diagnostics, observed count, and mitigation instructions.
- **Zero-Allocation Stream Entropy Engine**: Computes real-time Shannon entropy ($0.0 - 8.0$ bits/byte) on incoming token and byte streams with $0\text{ B/op}$ heap allocations to detect model degeneration.
- **Conversation History Visualizer**: Pretty-prints the most recent interaction turn with color-coded role badges (⚙️ System, 👤 User, 🤖 Assistant, 🛠️ Tool output).
- **Structured JSON Persistence**: Automatically logs and saves each full transaction into structured indented JSON files (`./conversations/conv_*.json`).
- **Thread-Safe Sliding Window**:
  - Tracks request counts, token estimates, and model distributions across a rolling time window (e.g. last 60 seconds).
  - Automatically evicts expired requests as the window slides.
  - Optional rate limiting / circuit breaking (`-enforce-limits`) returning `429 Too Many Requests` with `Retry-After`.
- **4-Tier Configuration Hierarchy**:
  $$\text{Code Defaults} \longrightarrow \text{YAML Config File} \longrightarrow \text{Environment Variables} \longrightarrow \text{CLI Flags}$$
- **Resilient Error Handling**: Returns clean JSON HTTP `502 Bad Gateway` if the backend destination is unreachable.
- **Graceful Shutdown**: Handles `SIGINT` / `SIGTERM` signals cleanly.

---

## Getting Started

### Quick Start with `start.sh`

```bash
# Run with defaults (port 8080 -> http://localhost:3000, loading config.yaml if present)
./start.sh

# Run with custom environment variables
PORT=9000 TARGET_URL=http://localhost:4000 ./start.sh

# Or pass CLI flags directly
./start.sh -port 9000 -target http://localhost:4000 -enforce-limits=true
```

### Run with `go run` or compiled binary

```bash
# Run with default config.yaml
go run main.go

# Run with specific YAML configuration file
go run main.go -config /path/to/custom-config.yaml

# Custom sliding window with request enforcement
go run main.go \
  -port 8080 \
  -target http://localhost:3000 \
  -window-duration 60s \
  -cb-max-hamming-dist 3 \
  -velocity-max-rps 5.0 \
  -enforce-limits=true
```

### Build Binary

```bash
go build -o proxy-server main.go
./proxy-server -config config.yaml
```

---

## Configuration Reference

All settings can be specified via YAML in [`config.yaml`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/config.yaml) (see [`config.example.yaml`](file:///home/kareem/Documents/DevOps%20Hackathon/Circuit%20Breaker/config.example.yaml) for full documentation), environment variables, or CLI flags:

| Flag | Environment Variable | Default | Description |
|---|---|---|---|
| `-config` | `CONFIG_FILE` | `"config.yaml"` | Path to optional YAML configuration file |
| `-port` | `PORT` | `8080` | Port for the proxy server to listen on |
| `-target` | `TARGET_URL` | `http://localhost:3000` | Upstream destination base URL |
| `-log-level` | `LOG_LEVEL` | `INFO` | Logging level (`DEBUG`, `INFO`, `WARN`, `ERROR`) |
| `-window-duration` | `WINDOW_DURATION` | `60s` | Rolling sliding window duration (e.g. `60s`, `5m`) |
| `-window-max-requests` | `WINDOW_MAX_REQUESTS` | `0` | Max requests allowed in sliding window (0 = unlimited) |
| `-window-max-tokens` | `WINDOW_MAX_TOKENS` | `0` | Max estimated tokens in sliding window (0 = unlimited) |
| `-enforce-limits` | `ENFORCE_LIMITS` | `false` | If `true`, rejects requests exceeding window thresholds with HTTP 429 |
| `-cb-enabled` | `CB_ENABLED` | `true` | Enable tool-call loop and error accumulation circuit breaker |
| `-cb-class-a-limit` | `CB_CLASS_A_LIMIT` | `3` | Max identical/similar tool calls allowed in window |
| `-cb-class-b-limit` | `CB_CLASS_B_LIMIT` | `4` | Max accumulated tool execution errors allowed in window |
| `-cb-max-hamming-dist` | `CB_MAX_HAMMING_DIST` | `3` | Max SimHash Hamming distance for near-duplicate loop detection (0-64) |
| `-cb-jaccard-threshold` | `CB_JACCARD_THRESHOLD` | `0.85` | Jaccard similarity threshold for near-duplicate tool calls (0.0 to 1.0) |
| `-velocity-enabled` | `VELOCITY_ENABLED` | `true` | Enable session-based velocity detection |
| `-velocity-max-rps` | `VELOCITY_MAX_RPS` | `5.0` | Max allowed requests per second per session (0 = disabled) |
| `-velocity-max-endpoint-repeats` | `VELOCITY_MAX_ENDPOINT_REPEATS` | `20` | Max hits to same endpoint in window (0 = disabled) |
| `-velocity-repeat-window` | `VELOCITY_REPEAT_WINDOW` | `10s` | Time window for endpoint repeat tracking |
| `-save-conversations` | `SAVE_CONVERSATIONS` | `true` | Save structured JSON conversation audit records |
| `-save-dir` | `CONVERSATIONS_DIR` | `./conversations` | Directory to store conversation JSON records |

---

## Example: Sending OpenAI JSON Payload

1. Start proxy targeting your upstream backend:
   ```bash
   ./start.sh -port 8080 -target https://api.openai.com
   ```

2. Send an OpenAI JSON chat completion request:
   ```bash
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer $OPENAI_API_KEY" \
     -d '{
       "model": "gpt-4",
       "temperature": 0.7,
       "messages": [
         {"role": "system", "content": "You are a helpful assistant."},
         {"role": "user", "content": "Explain SimHash and Locality Sensitive Hashing."}
       ]
     }'
   ```

---

## Testing & Quality Assurance

Run all unit, integration, and race-detector tests:

```bash
# Unit & Integration Tests
go test -v ./...

# Concurrency Race Detector
go test -race ./...

# Zero-Allocation Stream Entropy Benchmarks
go test -bench=BenchmarkEntropy -benchmem ./inspector

# Static Analysis
go vet ./...
```
