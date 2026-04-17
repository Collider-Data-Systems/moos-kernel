package transport

// Twin-kernel support (M9): adjoint sync protocol.
//
// Two kernels K, K' are twins when K eagerly forwards rewrites to K' via POST /twin/ingest.
// K' receives, applies, and does NOT re-forward (loop prevention by convention: configure
// at most one side of a twin pair as sync_mode=eager; the other as sync_mode=read-only).
//
// Architecture:
//   RunTwinSync — goroutine; subscribes to broadcast; forwards to eager twin_link endpoints.
//   POST /twin/ingest — receives forwarded rewrites from a peer; applies via ApplyProgram.
//   GET  /twin/status — returns active twin_link nodes and their configuration.

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"moos/kernel/internal/graph"
)

// handleTwinIngest receives a batch of envelopes from a peer kernel (the G functor, M9)
// and applies them to the local graph. Fire-and-forget from the peer side; we return results.
//
// Loop prevention: configure receiving twins as sync_mode=read-only or sync_mode=lazy in
// their own twin_link nodes so they do not re-forward back to the originating kernel.
func (s *Server) handleTwinIngest(w http.ResponseWriter, r *http.Request) {
	var envelopes []graph.Envelope
	if err := json.NewDecoder(r.Body).Decode(&envelopes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(envelopes) == 0 {
		writeJSON(w, http.StatusOK, []graph.EvalResult{})
		return
	}
	results, err := s.write.ApplyProgram(envelopes)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// handleTwinStatus returns all twin_link nodes with their configuration summary.
// Useful for operators to verify twin wiring without reading the full graph state.
func (s *Server) handleTwinStatus(w http.ResponseWriter, r *http.Request) {
	nodes := s.inspect.Nodes()
	type twinEntry struct {
		URN            string `json:"urn"`
		LocalURN       string `json:"local_urn"`
		RemoteURN      string `json:"remote_urn"`
		RemoteEndpoint string `json:"remote_endpoint"`
		SyncMode       string `json:"sync_mode"`
		Status         string `json:"status"`
	}
	var twins []twinEntry
	for _, n := range nodes {
		if n.TypeID != "twin_link" {
			continue
		}
		twins = append(twins, twinEntry{
			URN:            n.URN.String(),
			LocalURN:       twinPropStr(n, "local_urn"),
			RemoteURN:      twinPropStr(n, "remote_urn"),
			RemoteEndpoint: twinPropStr(n, "remote_endpoint"),
			SyncMode:       twinPropStr(n, "sync_mode"),
			Status:         twinPropStr(n, "status"),
		})
	}
	if twins == nil {
		twins = []twinEntry{}
	}
	writeJSON(w, http.StatusOK, twins)
}

// RunTwinSync subscribes to the kernel broadcast channel and forwards each rewrite
// to all active twin_link nodes configured with sync_mode=eager (the F functor, M9).
// This is a blocking call — run it as a goroutine.
//
// Each forward is fire-and-forget: failures are logged but do not affect local state.
// The goroutine exits when the subscription channel is closed (kernel shutdown).
func (s *Server) RunTwinSync() {
	subID := "twin-sync"
	ch := s.observe.Subscribe(subID)
	defer s.observe.Unsubscribe(subID)
	log.Printf("transport: twin sync goroutine started")
	for pr := range ch {
		s.forwardToEagerTwins(pr)
	}
	log.Printf("transport: twin sync goroutine stopped")
}

// forwardToEagerTwins inspects current twin_link nodes and forwards the rewrite
// to any that are status=active and sync_mode=eager.
func (s *Server) forwardToEagerTwins(pr graph.PersistedRewrite) {
	nodes := s.inspect.Nodes()
	for _, n := range nodes {
		if n.TypeID != "twin_link" {
			continue
		}
		if twinPropStr(n, "sync_mode") != "eager" {
			continue
		}
		if twinPropStr(n, "status") != "active" {
			continue
		}
		endpoint := twinPropStr(n, "remote_endpoint")
		if endpoint == "" {
			continue
		}
		go twinPost(endpoint, pr.Envelope)
	}
}

// twinPost POSTs a single envelope to a remote twin's /twin/ingest endpoint.
// Fire-and-forget: errors are logged, not propagated.
func twinPost(endpoint string, env graph.Envelope) {
	body, err := json.Marshal([]graph.Envelope{env})
	if err != nil {
		log.Printf("transport: twin post marshal: %v", err)
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(endpoint+"/twin/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("transport: twin post to %s: %v", endpoint, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("transport: twin post to %s: HTTP %d", endpoint, resp.StatusCode)
	}
}

// twinPropStr reads a string property from a node, returning "" if absent.
func twinPropStr(n graph.Node, field string) string {
	p, ok := n.Properties[field]
	if !ok {
		return ""
	}
	s, _ := p.Value.(string)
	return s
}
