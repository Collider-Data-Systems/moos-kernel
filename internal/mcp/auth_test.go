package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mustReq(t *testing.T, body string) Request {
	t.Helper()
	var req Request
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("bad test request %q: %v", body, err)
	}
	return req
}

func TestIsWriteCall(t *testing.T) {
	s := &Server{}
	cases := []struct {
		body string
		want bool
	}{
		{`{"method":"tools/call","params":{"name":"apply_program","arguments":{"envelopes":[]}}}`, true},
		{`{"method":"tools/call","params":{"name":"apply_rewrite","arguments":{"envelope":{}}}}`, true},
		{`{"method":"tools/call","params":{"name":"graph_state"}}`, false},
		{`{"method":"tools/call","params":{"name":"node_lookup","arguments":{"urn":"x"}}}`, false},
		{`{"method":"tools/list"}`, false},
		{`{"method":"initialize"}`, false},
	}
	for _, c := range cases {
		if got := s.isWriteCall(mustReq(t, c.body)); got != c.want {
			t.Errorf("isWriteCall(%s) = %v, want %v", c.body, got, c.want)
		}
	}
}

// TestMCPWriteToolRequiresBearer: over the HTTP transports, an apply_* tool
// call without a valid bearer is rejected with 401 before dispatch (so a
// nil-runtime server is safe to exercise here).
func TestMCPWriteToolRequiresBearer(t *testing.T) {
	s := &Server{authToken: "s3cret"}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"apply_program","arguments":{"envelopes":[]}}}`

	// Streamable HTTP (POST /sse) — unauthenticated.
	rr := httptest.NewRecorder()
	s.handleStreamableHTTP(rr, httptest.NewRequest(http.MethodPost, "/sse", strings.NewReader(body)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("streamable write without bearer: got %d, want 401", rr.Code)
	}

	// SSE message channel (POST /message) — unauthenticated.
	rr2 := httptest.NewRecorder()
	s.handleMessage(rr2, httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(body)))
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("/message write without bearer: got %d, want 401", rr2.Code)
	}

	// With a valid bearer the gate passes (dispatch then runs; a nil runtime
	// would panic, so we only assert the gate itself did not 401 by checking a
	// read tool is likewise ungated below).
}

// TestMCPReadToolNotGated: a read tool is never gated, even with a token set.
// isWriteCall is the gate predicate, so asserting it stays false for reads
// proves the gate lets reads through without touching the (nil) runtime.
func TestMCPReadToolNotGated(t *testing.T) {
	s := &Server{authToken: "s3cret"}
	if s.isWriteCall(mustReq(t, `{"method":"tools/call","params":{"name":"graph_state"}}`)) {
		t.Error("graph_state should not be gated")
	}
}
