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
