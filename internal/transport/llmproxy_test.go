package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockKeyFetcher returns keys from a sequence (one per fetch call, clamping to
// the last), counting calls. If err is set it always fails.
type mockKeyFetcher struct {
	keys  []string
	err   error
	calls int
}

func (m *mockKeyFetcher) fetchKey(_ context.Context) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	i := m.calls
	if i >= len(m.keys) {
		i = len(m.keys) - 1
	}
	m.calls++
	return m.keys[i], nil
}

// newTestProxy builds a geminiProxy pointed at an arbitrary upstream URL with a
// mocked key source — no gcloud, no real Gemini.
func newTestProxy(upstreamURL string, fetch keyFetcher, defaultModel string) *geminiProxy {
	return &geminiProxy{
		defaultModel: defaultModel,
		upstreamURL:  upstreamURL,
		client:       &http.Client{},
		fetch:        fetch,
	}
}

func postProxyJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestGeminiProxyForwardsAndInjectsTrimmedBearer asserts the proxy forwards the
// body verbatim, injects a trimmed Bearer key, and returns the upstream
// status + body verbatim.
func TestGeminiProxyForwardsAndInjectsTrimmedBearer(t *testing.T) {
	const reqBody = `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`
	const upstreamBody = `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`

	var gotAuth, gotBody, gotContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	// Key is deliberately padded with whitespace/newline — must be trimmed.
	p := newTestProxy(upstream.URL, &mockKeyFetcher{keys: []string{"  sk-test-123\n"}}, "")

	rr := postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/llm/gemini/chat/completions", reqBody)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != upstreamBody {
		t.Errorf("response body = %q, want verbatim upstream %q", got, upstreamBody)
	}
	if gotAuth != "Bearer sk-test-123" {
		t.Errorf("upstream Authorization = %q, want trimmed %q", gotAuth, "Bearer sk-test-123")
	}
	if gotBody != reqBody {
		t.Errorf("forwarded body = %q, want verbatim %q", gotBody, reqBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("upstream Content-Type = %q, want application/json", gotContentType)
	}
}

// TestGeminiProxyReturnsUpstreamStatusVerbatim asserts a non-200 upstream
// status + body is passed through unchanged (e.g. a 400 from Gemini).
func TestGeminiProxyReturnsUpstreamStatusVerbatim(t *testing.T) {
	const upstreamBody = `{"error":{"message":"bad request"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	p := newTestProxy(upstream.URL, &mockKeyFetcher{keys: []string{"k"}}, "")
	rr := postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/llm/gemini/chat/completions", `{"model":"x"}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (verbatim)", rr.Code)
	}
	if rr.Body.String() != upstreamBody {
		t.Errorf("body = %q, want verbatim %q", rr.Body.String(), upstreamBody)
	}
}

// TestGeminiProxyRefreshesKeyOn401 asserts the proxy refetches the key on an
// upstream 401 and retries once with the fresh key.
func TestGeminiProxyRefreshesKeyOn401(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	fetch := &mockKeyFetcher{keys: []string{"stale-key", "good-key"}}
	p := newTestProxy(upstream.URL, fetch, "")
	rr := postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/llm/gemini/chat/completions", `{"model":"x"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after key refresh; body=%s", rr.Code, rr.Body.String())
	}
	if fetch.calls != 2 {
		t.Errorf("fetchKey calls = %d, want 2 (initial + refresh-on-401)", fetch.calls)
	}
}

// TestGeminiProxyKeyFetchFailureIs503 asserts a key-fetch failure yields 503
// with a non-secret message.
func TestGeminiProxyKeyFetchFailureIs503(t *testing.T) {
	p := newTestProxy("http://127.0.0.1:0/never", &mockKeyFetcher{err: errors.New("gcloud boom")}, "")
	rr := postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/llm/gemini/chat/completions", `{"model":"x"}`)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "gcloud boom") {
		t.Errorf("503 body leaked underlying error: %s", rr.Body.String())
	}
}

// TestGeminiProxyUpstreamUnreachableIs502 asserts an unreachable upstream
// yields 502.
func TestGeminiProxyUpstreamUnreachableIs502(t *testing.T) {
	// Port 1 is not listening — client.Do errors.
	p := newTestProxy("http://127.0.0.1:1/v1beta/openai/chat/completions",
		&mockKeyFetcher{keys: []string{"k"}}, "")
	rr := postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/llm/gemini/chat/completions", `{"model":"x"}`)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}

// TestGeminiProxyInjectsDefaultModel asserts the default model is injected only
// when the request omits one.
func TestGeminiProxyInjectsDefaultModel(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		if v, ok := m["model"].(string); ok {
			gotModel = v
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	// No model in body + default configured → injected.
	p := newTestProxy(upstream.URL, &mockKeyFetcher{keys: []string{"k"}}, "gemini-2.5-flash")
	postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/x", `{"messages":[]}`)
	if gotModel != "gemini-2.5-flash" {
		t.Errorf("injected model = %q, want gemini-2.5-flash", gotModel)
	}

	// Explicit model + default configured → request model wins (verbatim).
	gotModel = ""
	postProxyJSON(t, http.HandlerFunc(p.handleChatCompletions), "/x", `{"model":"gemini-pro","messages":[]}`)
	if gotModel != "gemini-pro" {
		t.Errorf("model = %q, want request's gemini-pro (default must not override)", gotModel)
	}
}

// TestLLMProxyRouteAbsentWhenDisabled asserts the endpoint 404s when the proxy
// was never enabled (flag off).
func TestLLMProxyRouteAbsentWhenDisabled(t *testing.T) {
	s := NewServer(nil, nil, 0)
	h := s.Handler()
	rr := postProxyJSON(t, h, "/llm/gemini/chat/completions", `{"model":"x"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when --enable-llm-proxy is off", rr.Code)
	}
}

// TestLLMProxyRoutePresentWhenEnabled asserts the endpoint is routed through
// the full mux (incl. CORS middleware) when enabled.
func TestLLMProxyRoutePresentWhenEnabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"routed":true}`)
	}))
	defer upstream.Close()

	s := NewServer(nil, nil, 0)
	// Inject a fully-mocked proxy (no gcloud, no real Gemini) then build Handler.
	s.llmProxy = newTestProxy(upstream.URL, &mockKeyFetcher{keys: []string{"k"}}, "")
	h := s.Handler()

	rr := postProxyJSON(t, h, "/llm/gemini/chat/completions", `{"model":"x"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 routed through mux; body=%s", rr.Code, rr.Body.String())
	}
	// CORS header from corsMiddleware must be present (mirrors /fold).
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

// TestLLMProxyOptionsPreflight asserts the CORS preflight (OPTIONS) is handled
// by the shared middleware for the proxy path.
func TestLLMProxyOptionsPreflight(t *testing.T) {
	s := NewServer(nil, nil, 0)
	s.llmProxy = newTestProxy("http://unused", &mockKeyFetcher{keys: []string{"k"}}, "")
	h := s.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/llm/gemini/chat/completions", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if !strings.Contains(rr.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("Allow-Methods = %q, want it to include POST", rr.Header().Get("Access-Control-Allow-Methods"))
	}
}
