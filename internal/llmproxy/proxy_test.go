package llmproxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func testLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}

// syncBuffer guards a bytes.Buffer with a mutex so it's safe to write from
// the handler goroutine while the test goroutine polls it for content.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestNewHandlerRoutesByPrefixAndForwardsHeaders(t *testing.T) {
	var gotPath, gotAPIKey, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	handler, err := NewHandler(map[string]string{
		"/anthropic": upstream.URL,
		"/openai":    upstream.URL,
	}, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("x-api-key", "secret-key")
	req.Header.Set("User-Agent", "claude-cli/2.0.1 (external, cli)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream saw path %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "secret-key" {
		t.Fatalf("upstream saw x-api-key %q, want secret-key", gotAPIKey)
	}

	req2, err := http.NewRequest(http.MethodPost, server.URL+"/openai/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req2.Header.Set("Authorization", "Bearer token-123")
	req2.Header.Set("Originator", "codex_cli_rs")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream saw path %q, want /v1/responses", gotPath)
	}
	if gotAuth != "Bearer token-123" {
		t.Fatalf("upstream saw Authorization %q, want Bearer token-123", gotAuth)
	}
}

func TestNewHandlerUnknownRoute(t *testing.T) {
	handler, err := NewHandler(DefaultUpstreams, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/unknown")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestNewHandlerRejectsBadInput(t *testing.T) {
	if _, err := NewHandler(nil, testLogger(), ""); err == nil {
		t.Fatal("NewHandler(nil) should error on empty upstream map")
	}
	if _, err := NewHandler(map[string]string{"anthropic": "https://api.anthropic.com"}, testLogger(), ""); err == nil {
		t.Fatal("NewHandler() should error when prefix doesn't start with /")
	}
	if _, err := NewHandler(map[string]string{"/anthropic": "://bad-url"}, testLogger(), ""); err == nil {
		t.Fatal("NewHandler() should error on unparsable upstream")
	}
}

func TestNewHandlerStreamsResponseBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n"} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL}, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.1 (external, cli)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString() error = %v", err)
	}
	if first != "data: one\n" {
		t.Fatalf("first line = %q, want %q", first, "data: one\n")
	}
}

func TestNewHandlerSavesRedactedRequestAndResponseExamples(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"reply":"hi"}`))
	}))
	defer upstream.Close()

	exampleDir := t.TempDir()
	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL}, testLogger(), exampleDir)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/anthropic/v1/messages", strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("x-api-key", "super-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0.1 (external, cli)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain response body: %v", err)
	}

	reqData, err := os.ReadFile(filepath.Join(exampleDir, "anthropic-request.json"))
	if err != nil {
		t.Fatalf("read request example: %v", err)
	}
	var reqExample map[string]any
	if err := json.Unmarshal(reqData, &reqExample); err != nil {
		t.Fatalf("unmarshal request example: %v", err)
	}
	if reqExample["body"] != `{"prompt":"hello"}` {
		t.Fatalf("request example body = %v, want the request body", reqExample["body"])
	}
	headers, ok := reqExample["headers"].(map[string]any)
	if !ok {
		t.Fatalf("request example headers missing or wrong type: %v", reqExample["headers"])
	}
	if headers["X-Api-Key"] != "REDACTED" {
		t.Fatalf("request example x-api-key = %v, want REDACTED", headers["X-Api-Key"])
	}

	respData, err := os.ReadFile(filepath.Join(exampleDir, "anthropic-response.json"))
	if err != nil {
		t.Fatalf("read response example: %v", err)
	}
	var respExample map[string]any
	if err := json.Unmarshal(respData, &respExample); err != nil {
		t.Fatalf("unmarshal response example: %v", err)
	}
	if respExample["body"] != `{"reply":"hi"}` {
		t.Fatalf("response example body = %v, want the response body", respExample["body"])
	}
	if respExample["status"] != float64(http.StatusCreated) {
		t.Fatalf("response example status = %v, want %d", respExample["status"], http.StatusCreated)
	}
}

func TestNewHandlerSavesGzipResponseExampleDecoded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var gzipped bytes.Buffer
		gz := gzip.NewWriter(&gzipped)
		if _, err := gz.Write([]byte(`{"reply":"hi"}`)); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gzipped.Bytes())
	}))
	defer upstream.Close()

	exampleDir := t.TempDir()
	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL}, testLogger(), exampleDir)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.1 (external, cli)")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain response body: %v", err)
	}

	respData, err := os.ReadFile(filepath.Join(exampleDir, "anthropic-response.json"))
	if err != nil {
		t.Fatalf("read response example: %v", err)
	}
	var respExample map[string]any
	if err := json.Unmarshal(respData, &respExample); err != nil {
		t.Fatalf("unmarshal response example: %v", err)
	}
	if respExample["body"] != `{"reply":"hi"}` {
		t.Fatalf("response example body = %v, want the decoded response body", respExample["body"])
	}
}

func TestNewHandlerAnswersAnthropicHelloProbeLocally(t *testing.T) {
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL, "/openai": upstream.URL}, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, err := http.NewRequest(method, server.URL+"/anthropic/api/hello", nil)
		if err != nil {
			t.Fatalf("NewRequest(%s) error = %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do(%s) error = %v", method, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", method, resp.StatusCode)
		}
		if method == http.MethodGet && string(body) != `{"message":"hello"}` {
			t.Fatalf("%s body = %q, want the hello payload", method, body)
		}
	}
	if upstreamHit {
		t.Fatal("the hello probe should be answered locally, never forwarded upstream")
	}

	// The same path under /openai is not a known Claude Code convention and
	// must still be forwarded like any other request.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/openai/api/hello", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Originator", "codex_cli_rs")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	if !upstreamHit {
		t.Fatal("/openai/api/hello should be forwarded upstream like any other request")
	}
}

func TestNewHandlerRejectsClientsWithoutAKnownCLISignature(t *testing.T) {
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL, "/openai": upstream.URL}, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{name: "anthropic with no headers at all", path: "/anthropic/v1/messages"},
		{
			name: "anthropic with a raw SDK user agent",
			path: "/anthropic/v1/messages",
			headers: map[string]string{
				"User-Agent": "anthropic-sdk-python/0.45.0",
				"x-api-key":  "sk-ant-does-not-matter",
			},
		},
		{name: "openai with no headers at all", path: "/openai/v1/responses"},
		{
			name: "openai with a raw SDK client and no Originator",
			path: "/openai/v1/responses",
			headers: map[string]string{
				"Authorization": "Bearer sk-does-not-matter",
			},
		},
		{
			name: "openai with an unrecognized Originator value",
			path: "/openai/v1/responses",
			headers: map[string]string{
				"Originator": "some-other-tool",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstreamHit = false
			req, err := http.NewRequest(http.MethodPost, server.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
			if upstreamHit {
				t.Fatal("a rejected client must never reach the upstream")
			}
		})
	}
}

func TestNewHandlerAllowsKnownClaudeAndCodexClients(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL, "/openai": upstream.URL}, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	cases := []struct {
		name   string
		path   string
		header string
		value  string
	}{
		{name: "Claude Code CLI", path: "/anthropic/v1/messages", header: "User-Agent", value: "claude-cli/2.0.1 (external, cli)"},
		{name: "Claude Code VS Code extension", path: "/anthropic/v1/messages", header: "User-Agent", value: "claude-code/2.1.89 (cli)"},
		{name: "Claude Desktop (Electron)", path: "/anthropic/v1/messages", header: "User-Agent", value: "Claude/1.2.3 Chrome/128.0.0.0 Electron/32.0.0 Safari/537.36"},
		{name: "Codex CLI", path: "/openai/v1/responses", header: "Originator", value: "codex_cli_rs"},
		{name: "Codex VS Code extension", path: "/openai/v1/responses", header: "Originator", value: "codex_vscode"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			req.Header.Set(tc.header, tc.value)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (request should have reached the upstream)", resp.StatusCode)
			}
		})
	}
}

func TestNewHandlerLogsClientCancellationBelowWarn(t *testing.T) {
	block := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer upstream.Close()
	defer close(block)

	logBuf := &syncBuffer{}
	handler, err := NewHandler(map[string]string{"/anthropic": upstream.URL}, zerolog.New(logBuf), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.1 (external, cli)")

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, doErr := http.DefaultClient.Do(req)
		if doErr == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = logBuf.String()
		if strings.Contains(got, "client canceled request") || strings.Contains(got, "upstream request failed") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !strings.Contains(got, "client canceled request") {
		t.Fatalf("expected a client-cancellation log line, got %q", got)
	}
	if strings.Contains(got, "upstream request failed") {
		t.Fatalf("client cancellation should not log as an upstream failure: %q", got)
	}
}
