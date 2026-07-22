package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidBearer(t *testing.T) {
	cases := []struct {
		header string
		token  string
		want   bool
	}{
		{"Bearer s3cret", "s3cret", true},
		{"bearer s3cret", "s3cret", true},   // scheme is case-insensitive
		{"Bearer  s3cret ", "s3cret", true},  // surrounding whitespace trimmed
		{"Bearer wrong", "s3cret", false},
		{"s3cret", "s3cret", false}, // missing scheme
		{"", "s3cret", false},
		{"Bearer ", "s3cret", false},
		{"Bearer s3cretX", "s3cret", false},
	}
	for _, c := range cases {
		if got := validBearer(c.header, c.token); got != c.want {
			t.Errorf("validBearer(%q, %q) = %v, want %v", c.header, c.token, got, c.want)
		}
	}
}

// TestWriteGate_TokenConfigured: when a token is set, the gate rejects requests
// without a valid bearer (401, handler not run) and admits valid ones, and it
// always strips the permissive Access-Control-Allow-Origin header.
func TestWriteGate_TokenConfigured(t *testing.T) {
	s := &Server{authToken: "s3cret"}
	var called bool
	h := s.writeGate(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// No credential -> 401, handler not invoked, CORS wildcard stripped.
	rr := httptest.NewRecorder()
	rr.Header().Set("Access-Control-Allow-Origin", "*") // as corsMiddleware would have set
	h(rr, httptest.NewRequest(http.MethodPost, "/rewrites", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated write: got %d, want 401", rr.Code)
	}
	if called {
		t.Fatal("handler ran despite missing bearer")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin not stripped on write route: %q", got)
	}

	// Valid credential -> handler runs.
	called = false
	rr2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rewrites", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h(rr2, req)
	if !called {
		t.Fatal("handler not invoked with valid bearer")
	}
	if rr2.Code != http.StatusOK {
		t.Fatalf("authenticated write: got %d, want 200", rr2.Code)
	}
}

// TestWriteGate_NoTokenConfigured: backward-compatible open mode still runs the
// handler, but the CORS wildcard is stripped on writes regardless.
func TestWriteGate_NoTokenConfigured(t *testing.T) {
	s := &Server{} // authToken == ""
	var called bool
	h := s.writeGate(func(w http.ResponseWriter, r *http.Request) { called = true })

	rr := httptest.NewRecorder()
	rr.Header().Set("Access-Control-Allow-Origin", "*")
	h(rr, httptest.NewRequest(http.MethodPost, "/rewrites", nil))
	if !called {
		t.Fatal("open mode should invoke the handler")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin should be stripped on writes even without a token: %q", got)
	}
}

// TestLLMGate covers the scope-split matrix (t263): /llm/* accepts EITHER the
// write token OR the llm token; gating is active when at least one is set; the
// llm token must NEVER be accepted by writeGate (that is the whole point of
// the split).
func TestLLMGate(t *testing.T) {
	cases := []struct {
		name      string
		authToken string
		llmToken  string
		header    string
		wantPass  bool
	}{
		{"both empty: open", "", "", "", true},
		{"write token only: write token passes", "w-tok", "", "Bearer w-tok", true},
		{"write token only: no header 401", "w-tok", "", "", false},
		{"llm token only: llm token passes", "", "l-tok", "Bearer l-tok", true},
		{"llm token only: no header 401", "", "l-tok", "", false},
		{"both set: write token passes", "w-tok", "l-tok", "Bearer w-tok", true},
		{"both set: llm token passes", "w-tok", "l-tok", "Bearer l-tok", true},
		{"both set: wrong token 401", "w-tok", "l-tok", "Bearer nope", false},
		{"both set: no header 401", "w-tok", "l-tok", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{authToken: c.authToken, llmToken: c.llmToken}
			var called bool
			h := s.llmGate(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			rr := httptest.NewRecorder()
			rr.Header().Set("Access-Control-Allow-Origin", "*")
			req := httptest.NewRequest(http.MethodPost, "/llm/gemini/chat/completions", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			h(rr, req)
			if called != c.wantPass {
				t.Fatalf("handler called = %v, want %v", called, c.wantPass)
			}
			if !c.wantPass && rr.Code != http.StatusUnauthorized {
				t.Fatalf("blocked case: got %d, want 401", rr.Code)
			}
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("CORS wildcard not stripped on /llm/*: %q", got)
			}
		})
	}
}

// TestWriteGate_RejectsLLMToken: the scope boundary itself — a client holding
// only the llm token must get 401 on mutating routes.
func TestWriteGate_RejectsLLMToken(t *testing.T) {
	s := &Server{authToken: "w-tok", llmToken: "l-tok"}
	var called bool
	h := s.writeGate(func(w http.ResponseWriter, r *http.Request) { called = true })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rewrites", nil)
	req.Header.Set("Authorization", "Bearer l-tok")
	h(rr, req)
	if called {
		t.Fatal("llm token must not open a mutating route")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rr.Code)
	}
}
