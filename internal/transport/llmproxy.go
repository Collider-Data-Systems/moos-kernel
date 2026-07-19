package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// --- Gemini LLM proxy (read-only, flag-gated) ------------------------------
//
// This is a PURE PROXY. It does NOT touch the HG log / fold / any rewrite path
// (no §M11/§M12 interaction — it is not a rewrite). It is read-only w.r.t.
// mo:os state: no ADD/LINK/MUTATE/UNLINK ever originates here. The only new
// outbound network is the Gemini forward; the only new inbound is the single
// flag-gated endpoint POST /llm/gemini/chat/completions.
//
// The Gemini API key is never held by the extension and is never logged. The
// kernel reads it from GCP Secret Manager (via the gcloud CLI + ADC), caches
// the trimmed value in memory, and refreshes it on an upstream 401/403.

// geminiUpstreamURL is the OpenAI-compatible Gemini chat/completions endpoint.
const geminiUpstreamURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"

// maxProxyBody bounds an inbound request body (defensive; chat payloads with
// tool schemas + context are large but not unbounded).
const maxProxyBody = 20 << 20 // 20 MiB

// keyFetcher resolves the Gemini API key from a secret source. It is an
// interface so the handler can be unit-tested with a mocked source instead of
// hitting GCP Secret Manager.
type keyFetcher interface {
	fetchKey(ctx context.Context) (string, error)
}

// gcloudKeyFetcher fetches the key via the gcloud CLI (Option A: zero new Go
// deps). Runtime dependency: the gcloud CLI + ADC must be available for the
// kernel's user. The secret value is returned on stdout ONLY; any error wraps
// stderr (diagnostics), never stdout, so the key never lands in a log line.
type gcloudKeyFetcher struct {
	project string
	secret  string
}

func (g gcloudKeyFetcher) fetchKey(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gcloud", "secrets", "versions", "access", "latest",
		"--secret="+g.secret, "--project="+g.project)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// errBuf is gcloud diagnostics, not the secret (which is on stdout).
		return "", fmt.Errorf("gcloud secret access failed: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	key := strings.TrimSpace(out.String())
	if key == "" {
		return "", fmt.Errorf("gcloud returned an empty secret for %s/%s", g.project, g.secret)
	}
	return key, nil
}

// geminiProxy holds the proxy config and an in-memory cached key. Safe for
// concurrent use.
type geminiProxy struct {
	defaultModel string
	upstreamURL  string
	client       *http.Client
	fetch        keyFetcher

	mu  sync.Mutex
	key string // cached, already TrimSpace'd; "" means "not yet fetched"
}

// newGeminiProxy builds a proxy backed by the gcloud key fetcher. project /
// secretName select the Secret Manager secret; defaultModel (may be "") is
// injected only when a proxied request omits a model.
func newGeminiProxy(project, secretName, defaultModel string) *geminiProxy {
	return &geminiProxy{
		defaultModel: defaultModel,
		upstreamURL:  geminiUpstreamURL,
		// LLM tool-calling round-trips can be slow; keep a generous timeout.
		client: &http.Client{Timeout: 120 * time.Second},
		fetch:  gcloudKeyFetcher{project: project, secret: secretName},
	}
}

// getKey returns the cached key, fetching (or, if forceRefresh, re-fetching)
// from the secret source when needed. The returned value is always trimmed.
func (p *geminiProxy) getKey(ctx context.Context, forceRefresh bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key != "" && !forceRefresh {
		return p.key, nil
	}
	k, err := p.fetch.fetchKey(ctx)
	if err != nil {
		return "", err
	}
	// A stored secret may carry trailing whitespace/newline — trim before use.
	p.key = strings.TrimSpace(k)
	if p.key == "" {
		return "", fmt.Errorf("secret source returned an empty key")
	}
	return p.key, nil
}

// applyDefaultModel injects p.defaultModel into the request body iff a default
// is configured AND the body is a JSON object with no "model" key. Otherwise it
// returns the body verbatim (the contract is "forward essentially verbatim").
func (p *geminiProxy) applyDefaultModel(body []byte) []byte {
	if p.defaultModel == "" {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body // not an object we understand — forward verbatim
	}
	if _, ok := m["model"]; ok {
		return body
	}
	modelJSON, err := json.Marshal(p.defaultModel)
	if err != nil {
		return body
	}
	m["model"] = modelJSON
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// forward POSTs body to the Gemini upstream with a Bearer key. The caller owns
// closing the returned response body.
func (p *geminiProxy) forward(ctx context.Context, body []byte, key string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// key is already trimmed by getKey; trim again defensively so a mocked or
	// future key source can't slip trailing whitespace into the header.
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	return p.client.Do(req)
}

// handleChatCompletions proxies POST /llm/gemini/chat/completions to Gemini's
// OpenAI-compatible endpoint. It forwards the body essentially verbatim,
// injects the Bearer key, and returns the upstream status + body verbatim so
// the caller parses choices[0].message.tool_calls (or .content) exactly as it
// does for Ollama. CORS + OPTIONS preflight are handled by corsMiddleware,
// mirroring the /fold handler.
func (p *geminiProxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	body = p.applyDefaultModel(body)

	key, err := p.getKey(r.Context(), false)
	if err != nil {
		// Non-secret, actionable message. NEVER include the key or raw secret.
		writeError(w, http.StatusServiceUnavailable, "gemini proxy: API key unavailable from Secret Manager")
		return
	}

	resp, err := p.forward(r.Context(), body, key)
	if err != nil {
		writeError(w, http.StatusBadGateway, "gemini proxy: upstream unreachable")
		return
	}

	// On an auth failure the cached key may be stale (rotated) — refresh once
	// and retry a single time.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		if key, err = p.getKey(r.Context(), true); err != nil {
			writeError(w, http.StatusServiceUnavailable, "gemini proxy: API key unavailable from Secret Manager")
			return
		}
		if resp, err = p.forward(r.Context(), body, key); err != nil {
			writeError(w, http.StatusBadGateway, "gemini proxy: upstream unreachable")
			return
		}
	}
	defer resp.Body.Close()

	// Return the upstream status + body verbatim.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
