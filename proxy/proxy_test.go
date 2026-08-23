package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"circuit-breaker-proxy/inspector"
)

func TestProxyForwardsPathAndQuery(t *testing.T) {
	var capturedPath string
	var capturedQuery string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-response"))
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("failed to parse backend URL: %v", err)
	}

	handler := NewServerHandler(Config{
		TargetURL: backendURL,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	testPath := "/api/v1/users/42"
	testQuery := "filter=active&sort=desc"
	targetURL := fmt.Sprintf("%s%s?%s", proxyServer.URL, testPath, testQuery)

	resp, err := proxyServer.Client().Get(targetURL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if string(body) != "backend-response" {
		t.Errorf("expected body 'backend-response', got '%s'", string(body))
	}

	if capturedPath != testPath {
		t.Errorf("expected path '%s', got '%s'", testPath, capturedPath)
	}

	if capturedQuery != testQuery {
		t.Errorf("expected query '%s', got '%s'", testQuery, capturedQuery)
	}
}

func TestProxyForwardsOpenAIJSONRequestWithConversation(t *testing.T) {
	tempSaveDir := t.TempDir()

	var receivedBody []byte
	var receivedContentType string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Response to conversation"))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	handler := NewServerHandler(Config{
		TargetURL:         backendURL,
		SaveConversations: true,
		SaveDir:           tempSaveDir,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	jsonPayload := `{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant."},
			{"role": "user", "content": "Fetch user data from DB."},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_123", "type": "function", "function": {"name": "query_db", "arguments": "{\"user_id\": 42}"}}]},
			{"role": "tool", "tool_call_id": "call_123", "content": "{\"name\": \"Alice\", \"role\": \"admin\"}"},
			{"role": "user", "content": "Summarize that."}
		],
		"temperature": 0.7,
		"max_tokens": 150
	}`

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(jsonPayload))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	if string(receivedBody) != jsonPayload {
		t.Errorf("expected backend to receive exact JSON payload, got '%s'", string(receivedBody))
	}

	if receivedContentType != "application/json" {
		t.Errorf("expected content type 'application/json', got '%s'", receivedContentType)
	}

	// Verify conversation record file was saved to tempSaveDir and contains all 5 messages
	files, err := os.ReadDir(tempSaveDir)
	if err != nil {
		t.Fatalf("failed to read save directory: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 saved conversation file, found %d", len(files))
	}

	savedBytes, err := os.ReadFile(tempSaveDir + "/" + files[0].Name())
	if err != nil {
		t.Fatalf("failed to read saved conversation file: %v", err)
	}

	var savedRecord inspector.ConversationRecord
	if err := json.Unmarshal(savedBytes, &savedRecord); err != nil {
		t.Fatalf("failed to unmarshal saved conversation: %v", err)
	}
	if len(savedRecord.Request.Messages) != 5 {
		t.Errorf("expected saved JSON to contain all 5 messages, got %d", len(savedRecord.Request.Messages))
	}
}

func TestProxyCircuitBreaker_ClassA_Rejection(t *testing.T) {
	backendCalled := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	cbEngine := inspector.NewCircuitBreakerEngine(inspector.CircuitBreakerConfig{
		ClassAMaxIdentical: 3,
		Enabled:            true,
	})

	handler := NewServerHandler(Config{
		TargetURL:      backendURL,
		CircuitBreaker: cbEngine,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	// 3 identical tool calls in conversation history
	payload := `{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "Find info."},
			{"role": "assistant", "tool_calls": [{"id":"1","type":"function","function":{"name":"search","arguments":"{\"query\":\"users\"}"}}]},
			{"role": "tool", "tool_call_id":"1", "content":"{}"},
			{"role": "assistant", "tool_calls": [{"id":"2","type":"function","function":{"name":"search","arguments":"{\"query\":\"users\"}"}}]},
			{"role": "tool", "tool_call_id":"2", "content":"{}"},
			{"role": "assistant", "tool_calls": [{"id":"3","type":"function","function":{"name":"search","arguments":"{\"query\":\"users\"}"}}]}
		]
	}`

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 Too Many Requests, got %d", resp.StatusCode)
	}

	if backendCalled {
		t.Errorf("expected backend NOT to be called when circuit breaker is tripped")
	}

	body, _ := io.ReadAll(resp.Body)
	var sreResp inspector.SREErrorResponse
	if err := json.Unmarshal(body, &sreResp); err != nil {
		t.Fatalf("failed to unmarshal SRE JSON error response: %v", err)
	}

	if sreResp.SREIncident.FailureClass != inspector.ClassAIdenticalLoop {
		t.Errorf("expected failure class %s, got %s", inspector.ClassAIdenticalLoop, sreResp.SREIncident.FailureClass)
	}
	if sreResp.SREIncident.CircuitState != "OPEN_TRIPPED" {
		t.Errorf("expected circuit state 'OPEN_TRIPPED', got '%s'", sreResp.SREIncident.CircuitState)
	}
}

func TestProxyVelocity_MaxRPS_Rejection(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	vd := inspector.NewVelocityDetector(inspector.VelocityConfig{
		MaxRPS:             3.0,
		MaxEndpointRepeats: 100,
		RepeatWindow:       10 * time.Second,
		Enabled:            true,
	})

	handler := NewServerHandler(Config{
		TargetURL: backendURL,
		Velocity:  vd,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	client := proxyServer.Client()

	// Send 3 requests with custom session ID header -> succeed
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, proxyServer.URL+"/api/test", nil)
		req.Header.Set("X-Session-ID", "agent-session-42")
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d failed: %v / status %d", i+1, err, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// 4th request from same session -> should breach 3.0 RPS limit and return 429
	req, _ := http.NewRequest(http.MethodGet, proxyServer.URL+"/api/test", nil)
	req.Header.Set("X-Session-ID", "agent-session-42")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request 4 failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 Too Many Requests on velocity RPS breach, got %d", resp.StatusCode)
	}
}

func TestProxyVelocity_MaxEndpointRepeats_Rejection(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	vd := inspector.NewVelocityDetector(inspector.VelocityConfig{
		MaxRPS:             0, // disabled
		MaxEndpointRepeats: 3,
		RepeatWindow:       5 * time.Second,
		Enabled:            true,
	})

	handler := NewServerHandler(Config{
		TargetURL: backendURL,
		Velocity:  vd,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	client := proxyServer.Client()

	// 3 requests to /repeat-endpoint -> succeed
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, proxyServer.URL+"/repeat-endpoint", nil)
		req.Header.Set("X-Session-ID", "agent-session-99")
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d failed: %v / status %d", i+1, err, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// 4th request to /repeat-endpoint -> should be blocked by repeat limit
	req, _ := http.NewRequest(http.MethodGet, proxyServer.URL+"/repeat-endpoint", nil)
	req.Header.Set("X-Session-ID", "agent-session-99")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request 4 failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 on endpoint repeat limit, got %d", resp.StatusCode)
	}
}

func TestProxyRealtimeStreamingSSE_WithToolCalls(t *testing.T) {
	tempSaveDir := t.TempDir()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("expected flusher")
			return
		}

		chunks := []string{
			`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_tokyo","type":"function","function":{"name":"query_weather","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Tokyo\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprint(w, chunk)
			flusher.Flush()
		}
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	handler := NewServerHandler(Config{
		TargetURL:         backendURL,
		SaveConversations: true,
		SaveDir:           tempSaveDir,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqPayload := `{"model":"gpt-4","messages":[{"role":"user","content":"Weather in Tokyo?"}]}`
	req, _ := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(reqPayload))
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if !strings.Contains(string(body), "query_weather") || !strings.Contains(string(body), "[DONE]") {
		t.Errorf("expected body to contain SSE stream chunks, got: %s", string(body))
	}

	// Verify conversation record file was saved to tempSaveDir for streaming SSE request
	files, err := os.ReadDir(tempSaveDir)
	if err != nil {
		t.Fatalf("failed to read save directory: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 saved streaming conversation file, found %d", len(files))
	}
}

func TestProxySlidingWindowRateLimiting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	windowInspector := inspector.NewSlidingWindow(inspector.SlidingWindowConfig{
		WindowDuration: 1 * time.Minute,
		MaxRequests:    2,
		EnforceLimits:  true,
	})

	handler := NewServerHandler(Config{
		TargetURL: backendURL,
		Inspector: windowInspector,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	client := proxyServer.Client()

	// 1st request - should pass
	resp1, err := client.Get(proxyServer.URL + "/test1")
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected request 1 to succeed, got %v / status %d", err, resp1.StatusCode)
	}
	resp1.Body.Close()

	// 2nd request - should pass
	resp2, err := client.Get(proxyServer.URL + "/test2")
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected request 2 to succeed, got %v / status %d", err, resp2.StatusCode)
	}
	resp2.Body.Close()

	// 3rd request - should be blocked by sliding window (429 Too Many Requests)
	resp3, err := client.Get(proxyServer.URL + "/test3")
	if err != nil {
		t.Fatalf("failed to make request 3: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 Too Many Requests, got %d", resp3.StatusCode)
	}
}

func TestProxyForwardingHeadersAndHttpMethods(t *testing.T) {
	var capturedForwardedFor string
	var capturedForwardedProto string
	var capturedMethod string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedForwardedFor = r.Header.Get("X-Forwarded-For")
		capturedForwardedProto = r.Header.Get("X-Forwarded-Proto")
		capturedMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	handler := NewServerHandler(Config{
		TargetURL: backendURL,
	})

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	req, _ := http.NewRequest(http.MethodDelete, proxyServer.URL+"/items/123", nil)
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("failed to execute delete request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, resp.StatusCode)
	}
	if capturedMethod != http.MethodDelete {
		t.Errorf("expected method DELETE, got %s", capturedMethod)
	}
	if !strings.Contains(capturedForwardedFor, "127.0.0.1") {
		t.Errorf("expected X-Forwarded-For containing 127.0.0.1, got %s", capturedForwardedFor)
	}
	if capturedForwardedProto != "http" {
		t.Errorf("expected X-Forwarded-Proto 'http', got %s", capturedForwardedProto)
	}
}

func TestProxyBackendUnreachableReturns502(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	backendURL, _ := url.Parse(backend.URL)
	backend.Close()

	handler := NewServerHandler(Config{
		TargetURL: backendURL,
	})
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := proxyServer.Client().Get(proxyServer.URL + "/health")
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected status %d (Bad Gateway), got %d", http.StatusBadGateway, resp.StatusCode)
	}
}
