// Package llmproxy implements a local reverse proxy that forwards Claude and
// Codex API traffic to their real upstreams, so every client on this machine
// (Claude Code CLI/Desktop/VS Code, Codex CLI/VS Code) can be pointed at one
// place instead of each managing its own base URL and headers.
//
// This proxy is deliberately scoped to those CLI/Desktop/VS-Code-extension
// clients only — see isKnownCLIClient. Raw API-key/SDK traffic (a bare curl
// call, a direct Anthropic/OpenAI SDK integration) is rejected with 403,
// even though its credentials would otherwise be forwarded correctly; this
// is a policy choice about what this proxy is *for*, not a claim that the
// check is a security boundary.
package llmproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// DefaultUpstreams maps a local path prefix to the real API host it proxies
// requests to. x-api-key (Anthropic) and Authorization: Bearer (OpenAI) are
// forwarded to their upstream unchanged — this proxy never inspects or
// rewrites credentials, only routes by path prefix.
var DefaultUpstreams = map[string]string{
	"/anthropic": "https://api.anthropic.com",
	"/openai":    "https://api.openai.com",
}

// exampleBodyLimit caps how much of a request/response body gets sampled
// into an example payload file — enough to see the shape of a call without
// storing large uploads or long SSE streams.
const exampleBodyLimit = 8 * 1024

// redactedHeaders never has its value written to an example payload or log
// line, matching the "never mirror credentials, OAuth tokens, or session
// auth" rule: these are exactly the headers DefaultUpstreams' doc comment
// says are forwarded to the upstream unchanged.
var redactedHeaders = map[string]bool{
	"authorization": true,
	"x-api-key":     true,
}

// anthropicHelloPath is an undocumented but consistently-observed part of
// Claude Code's startup: every invocation fires a credential-free, fire-
// and-forget HEAD (or GET) to "${ANTHROPIC_BASE_URL}/api/hello" to warm up
// the TCP connection, and doesn't wait for or care about the response
// (equivalent to `fetch(...).catch(() => {})` client-side). Forwarding it
// through the full reverse-proxy round-trip to the real upstream only
// invites exactly the failure mode this was built to avoid: the client
// abandons the request before the round-trip finishes, which used to show
// up as a "context canceled" warning on every single session start even
// though nothing was actually broken. Answering it locally — the same fix
// independently converged on by several other Claude-Code-compatible
// proxies — makes it instant and removes the upstream round-trip (and the
// log noise) entirely.
const anthropicHelloPath = "/api/hello"

func isAnthropicHelloProbe(prefix string, r *http.Request) bool {
	return prefix == "/anthropic" &&
		r.URL.Path == prefix+anthropicHelloPath &&
		(r.Method == http.MethodHead || r.Method == http.MethodGet)
}

func respondAnthropicHello(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"hello"}`))
}

// isKnownCLIClient decides whether a request came from an actual Claude or
// Codex client this proxy is meant to serve, as opposed to a raw API-key/SDK
// caller (curl, a direct Anthropic/OpenAI SDK integration, a generic HTTP
// client). This proxy is deliberately scoped to CLI/Desktop/VS-Code-extension
// traffic only — it is not a general-purpose Anthropic/OpenAI API gateway.
//
// Each vendor identifies its first-party clients differently:
//   - Claude Code CLI and the Claude Code VS Code extension share the same
//     underlying CLI and both send a User-Agent starting with "claude-cli/"
//     or "claude-code/" (the version segment changes every release, so this
//     matches on the stable prefix). Claude Desktop is an Electron app and
//     sends a User-Agent containing both "Claude" and "Electron" — this is
//     the pattern third-party enterprise network-filtering guides use to
//     tell Claude's CLI, Desktop, and Web clients apart at the HTTP layer
//     (e.g. Netskope's Claude client policy guide), since Anthropic doesn't
//     publish it directly.
//   - Codex CLI and the Codex VS Code extension send an "Originator" header
//     of "codex_cli_rs" and "codex_vscode" respectively — OpenAI's own
//     client-identification convention for Responses API requests.
//
// This is a *policy* gate, not a security boundary: every signal checked
// here is an ordinary, client-supplied HTTP header that anything (curl
// included) can set to claim to be one of these tools. It adds no
// protection beyond whatever x-api-key/Authorization already provide —
// its only job is keeping this proxy's traffic scoped to real CLI/Desktop/
// VS-Code-extension usage and rejecting everything else.
func isKnownCLIClient(prefix string, r *http.Request) bool {
	switch prefix {
	case "/anthropic":
		ua := r.Header.Get("User-Agent")
		return strings.HasPrefix(ua, "claude-cli/") ||
			strings.HasPrefix(ua, "claude-code/") ||
			(strings.Contains(ua, "Claude") && strings.Contains(ua, "Electron"))
	case "/openai":
		switch r.Header.Get("Originator") {
		case "codex_cli_rs", "codex_vscode":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func respondClientNotAllowed(w http.ResponseWriter) {
	http.Error(w,
		"this proxy only serves Claude Code / Codex traffic from the CLI, Desktop, or VS Code extension clients — direct API-key/SDK usage is not supported",
		http.StatusForbidden)
}

// NewHandler builds an http.Handler that reverse-proxies each configured
// prefix to its upstream. Request and response bodies are streamed, not
// buffered: FlushInterval is set to flush every write immediately, which
// matters for Anthropic/OpenAI's SSE streaming responses — buffering would
// make a client wait for the whole reply instead of seeing tokens arrive
// incrementally.
//
// When exampleDir is non-empty, a sanitized sample of the most recent
// request and response for each route is written under it as
// "<route>-request.json"/"<route>-response.json" — credentials redacted,
// bodies capped at exampleBodyLimit — for operators who want to see what a
// route's traffic actually looks like. Pass "" to disable.
func NewHandler(upstreams map[string]string, logger zerolog.Logger, exampleDir string) (http.Handler, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams configured")
	}
	prefixes := make([]string, 0, len(upstreams))
	proxies := make(map[string]*httputil.ReverseProxy, len(upstreams))
	for prefix, upstream := range upstreams {
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("route prefix %q must start with /", prefix)
		}
		target, err := url.Parse(upstream)
		if err != nil {
			return nil, fmt.Errorf("parse upstream %q: %w", upstream, err)
		}
		proxies[prefix] = newReverseProxy(prefix, target, logger, exampleDir)
		prefixes = append(prefixes, prefix)
	}
	// Longest prefix first, so "/anthropic/v1" (if ever configured) would be
	// tried before the shorter "/anthropic".
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range prefixes {
			if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
				if isAnthropicHelloProbe(prefix, r) {
					logger.Debug().Str("route", prefix).Msg("answered Claude Code connectivity probe locally")
					respondAnthropicHello(w)
					return
				}
				if !isKnownCLIClient(prefix, r) {
					logger.Warn().Str("route", prefix).Str("user_agent", r.UserAgent()).Str("originator", r.Header.Get("Originator")).
						Msg("rejected request: not a recognized Claude/Codex CLI, Desktop, or VS Code extension client")
					respondClientNotAllowed(w)
					return
				}
				logger.Info().Str("method", r.Method).Str("path", r.URL.Path).Str("route", prefix).Msg("proxy request")
				proxies[prefix].ServeHTTP(w, r)
				return
			}
		}
		logger.Warn().Str("path", r.URL.Path).Msg("unknown route")
		http.Error(w, "unknown route", http.StatusNotFound)
	}), nil
}

func newReverseProxy(prefix string, target *url.URL, logger zerolog.Logger, exampleDir string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if !strings.HasPrefix(req.URL.Path, "/") {
			req.URL.Path = "/" + req.URL.Path
		}
		baseDirector(req)
		// The default director leaves req.Host as the client's original
		// "127.0.0.1:PORT" Host header; without this, the upstream would
		// receive that instead of its own hostname and could reject or
		// misroute the request.
		req.Host = target.Host

		captureRequestExample(exampleDir, prefix, req, logger)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		captureResponseExample(exampleDir, prefix, resp, logger)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// r here is the already-rewritten outbound request (Director has
		// run), so r.URL.Path is the *upstream* path, not the path the
		// client originally requested — e.g. for a client request to
		// "/anthropic/api/hello" this logs path=/api/hello. That's not a
		// sign the route was dropped; the "proxy request" line logged above
		// already recorded the original client path and which route prefix
		// matched it. Log the route here too so the two lines are easy to
		// correlate.
		//
		// A canceled client request (it disconnected, or hit a short
		// client-side timeout) surfaces here as "context canceled" even
		// though the proxy correctly matched the route and attempted the
		// upstream call — it's not a proxy bug, so it's logged at Debug
		// instead of Warn to avoid looking like one.
		if r.Context().Err() != nil && errors.Is(err, context.Canceled) {
			logger.Debug().Err(err).Str("route", prefix).Str("upstream_path", r.URL.Path).Str("upstream_host", target.Host).
				Msg("client canceled request before upstream responded")
		} else {
			logger.Warn().Err(err).Str("route", prefix).Str("upstream_path", r.URL.Path).Str("upstream_host", target.Host).
				Msg("upstream request failed")
		}
		w.WriteHeader(http.StatusBadGateway)
	}
	// -1 flushes on every write instead of batching on a timer, so SSE
	// tokens reach the client as soon as the upstream sends them.
	proxy.FlushInterval = -1
	return proxy
}

// examplePayload is the sanitized shape written to an example payload file:
// enough to see what a route's traffic looks like without ever including
// credentials or a full large/streamed body.
type examplePayload struct {
	CapturedAt string            `json:"captured_at"`
	Route      string            `json:"route"`
	Method     string            `json:"method,omitempty"`
	Path       string            `json:"path"`
	Status     int               `json:"status,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
}

func captureRequestExample(exampleDir, prefix string, req *http.Request, logger zerolog.Logger) {
	if exampleDir == "" {
		return
	}
	example := examplePayload{
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Route:      prefix,
		Method:     req.Method,
		Path:       req.URL.Path,
		Headers:    snapshotHeaders(req.Header),
	}
	body, truncated, err := peekBody(&req.Body, exampleBodyLimit)
	if err != nil {
		logger.Warn().Err(err).Str("route", prefix).Msg("failed to sample request body for example payload")
	} else {
		decoded, decodeTruncated := decodeBodyForExample(body, req.Header.Get("Content-Encoding"), exampleBodyLimit)
		example.Body = string(decoded)
		example.Truncated = truncated || decodeTruncated
	}
	writeExample(exampleDir, routeFileName(prefix)+"-request.json", example, logger)
}

func captureResponseExample(exampleDir, prefix string, resp *http.Response, logger zerolog.Logger) {
	if exampleDir == "" {
		return
	}
	example := examplePayload{
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Route:      prefix,
		Status:     resp.StatusCode,
		Headers:    snapshotHeaders(resp.Header),
	}
	if resp.Request != nil {
		example.Path = resp.Request.URL.Path
	}
	body, truncated, err := peekBody(&resp.Body, exampleBodyLimit)
	if err != nil {
		logger.Warn().Err(err).Str("route", prefix).Msg("failed to sample response body for example payload")
	} else {
		decoded, decodeTruncated := decodeBodyForExample(body, resp.Header.Get("Content-Encoding"), exampleBodyLimit)
		example.Body = string(decoded)
		example.Truncated = truncated || decodeTruncated
	}
	writeExample(exampleDir, routeFileName(prefix)+"-response.json", example, logger)
}

// snapshotHeaders copies h into a plain map for JSON encoding, replacing any
// redactedHeaders value with a clearly-labeled placeholder instead of
// omitting the key outright — the same pattern examples/statusline-input.json
// uses for its transcript_path field.
func snapshotHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for key, values := range h {
		if redactedHeaders[strings.ToLower(key)] {
			out[key] = "REDACTED"
			continue
		}
		out[key] = strings.Join(values, ", ")
	}
	return out
}

// peekBody samples up to limit bytes from *body without consuming them for
// the real caller: it reads limit+1 bytes, then replaces *body with a reader
// that replays exactly what was read followed by whatever remains of the
// original stream, so the proxied request/response is unaffected.
func peekBody(body *io.ReadCloser, limit int) ([]byte, bool, error) {
	if *body == nil {
		return nil, false, nil
	}
	original := *body
	buf := make([]byte, limit+1)
	n, err := io.ReadFull(original, buf)
	switch {
	case err == nil:
		// Filled the buffer exactly; there may be more left unread.
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		err = nil
	default:
		return nil, false, err
	}
	peeked := buf[:n]
	truncated := n > limit
	sample := peeked
	if truncated {
		sample = peeked[:limit]
	}
	*body = struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(bytes.NewReader(peeked), original),
		Closer: original,
	}
	return sample, truncated, err
}

// decodeBodyForExample gzip-decodes body when contentEncoding says the real
// caller will treat it as gzip — otherwise peekBody's sample is exactly the
// compressed bytes on the wire, which serialize into the example JSON file
// as unreadable binary noise instead of the request/response text it's meant
// to document. body may itself be a truncated sample of a larger compressed
// stream, so a decode error after some bytes were already produced is
// expected, not a failure: it returns whatever was decoded before the error.
// The decoded result is capped at limit bytes, same as peekBody's own cap.
func decodeBodyForExample(body []byte, contentEncoding string, limit int) ([]byte, bool) {
	if !strings.EqualFold(strings.TrimSpace(contentEncoding), "gzip") {
		return body, false
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body, false
	}
	defer func() { _ = reader.Close() }()
	buf := make([]byte, limit+1)
	n, err := io.ReadFull(reader, buf)
	if n == 0 && err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return body, false
	}
	truncated := n > limit
	if truncated {
		n = limit
	}
	return buf[:n], truncated
}

// routeFileName turns a route prefix like "/anthropic" into a filesystem-safe
// stem like "anthropic".
func routeFileName(prefix string) string {
	name := strings.ReplaceAll(strings.Trim(prefix, "/"), "/", "-")
	if name == "" {
		name = "root"
	}
	return name
}

func writeExample(dir, name string, payload examplePayload, logger zerolog.Logger) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn().Err(err).Str("file", name).Msg("failed to marshal example payload")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn().Err(err).Str("dir", dir).Msg("failed to create examples directory")
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		logger.Warn().Err(err).Str("file", path).Msg("failed to write example payload")
	}
}
