package transport

// P9 WebTransport / HTTP-3 datagram data-plane SPIKE — feature-flagged,
// SYNTHETIC, OFF by default.
//
// WHY THIS IS A SPIKE, NOT A SURFACE
// ----------------------------------
// The Phase-5 measurement (scripts/bench-frame-read.mjs) clocked the reliable
// MCP / SSE frame-read path at ~61 Hz p95 — comfortably above any realistic
// single-user UI refresh rate. So a lossy datagram data-plane is NOT justified
// for a real surface in a single-user, read-only pilot. This endpoint exists
// only to EXERCISE and PROVE the WebTransport datagram pipe with a SYNTHETIC
// high-rate stream. There is no organic high-rate producer in the kernel to
// attach it to; the stream below is fabricated on purpose. Before any real
// adoption this path MUST be re-measured against the SSE/reliable baseline
// (see docs/spike-p9-webtransport.md) — the spike proves the pipe works, NOT
// that it beats or is needed over the reliable path.
//
// ISOLATION INVARIANTS (do not regress)
// -------------------------------------
//   - This listener is SEPARATE from the reliable :8000 HTTP server, the :8080
//     MCP server, and the M10 HTTP/3 mirror (--quic-addr). It is started only
//     when --wt-addr is non-empty; with the flag empty (the default) it does
//     not exist and the reliable paths run untouched.
//   - It serves ONLY /wt/presence. It touches NO HG state: no ADD/LINK/MUTATE/
//     UNLINK, no log, no fold, no registry, no session/liveness path. It just
//     emits synthetic datagrams.

import (
	"crypto/tls"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// WebTransportPresencePath is the WebTransport session path a pilot client
// dials with an Extended CONNECT. The datagram wire contract is
// syntheticPresenceFrame below and is documented in
// docs/spike-p9-webtransport.md.
const WebTransportPresencePath = "/wt/presence"

// wtEmitInterval is the synthetic datagram cadence (~20 Hz).
const wtEmitInterval = 50 * time.Millisecond

// syntheticPresenceFrame is the JSON payload carried by each datagram — the
// WIRE CONTRACT a pilot WebTransport client decodes.
//
// It is SYNTHETIC: Seq is a per-session monotonic counter (starts at 1),
// TsUnixMs is the server wall clock at emit time, and Value is a fabricated
// oscillating signal standing in for something like an HDC-similarity delta or
// a presence heartbeat. None of it is derived from HG state.
type syntheticPresenceFrame struct {
	Seq      uint64  `json:"seq"`   // monotonic per session, starts at 1
	TsUnixMs int64   `json:"t_ms"`  // server wall-clock ms at emit time
	Value    float64 `json:"value"` // synthetic signal in [0,1]
	Kind     string  `json:"kind"`  // always "synthetic.presence" — marks this as fabricated
}

// ServeWebTransport starts the SPIKE WebTransport/HTTP-3 listener on addr and
// blocks (call it in a goroutine from main.go, ONLY when --wt-addr is set).
//
// The receiver s is present purely for API symmetry with ServeQUIC (M10); no
// HG accessor on s is touched anywhere in this file — the spike is read-only
// with respect to the graph and, in fact, reads nothing from it at all.
func (s *Server) ServeWebTransport(addr string, tlsConf *tls.Config) error {
	wtSrv := buildWebTransportServer(addr, tlsConf)
	log.Printf("wt-spike: SYNTHETIC WebTransport datagram listener on %s%s (~20 Hz; feature-flagged, off by default; touches no HG state)", addr, WebTransportPresencePath)
	return wtSrv.ListenAndServe()
}

// buildWebTransportServer wires the http3.Server (datagrams + WebTransport
// SETTINGS via ConfigureHTTP3Server), a minimal mux exposing ONLY
// /wt/presence, and the synthetic emitter. Split out from ServeWebTransport so
// tests can drive it over a caller-owned UDP PacketConn (to learn the ephemeral
// port) via wtSrv.Serve.
func buildWebTransportServer(addr string, tlsConf *tls.Config) *webtransport.Server {
	h3 := &http3.Server{
		Addr:      addr,
		TLSConfig: tlsConf,
	}
	// Enable HTTP/3 datagrams + the WebTransport SETTINGS bit, and thread the
	// QUIC conn into the request context (required by Server.Upgrade).
	webtransport.ConfigureHTTP3Server(h3)

	wtSrv := &webtransport.Server{
		H3: h3,
		// Dev/localhost synthetic spike: accept any origin. This listener has
		// no HG reach and emits only fabricated data, so CSRF-style origin
		// pinning is not meaningful here; the reliable paths keep their own
		// policy. Tighten this before any non-dev use.
		CheckOrigin: func(*http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc(WebTransportPresencePath, func(w http.ResponseWriter, r *http.Request) {
		sess, err := wtSrv.Upgrade(w, r)
		if err != nil {
			log.Printf("wt-spike: upgrade rejected: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pushSyntheticPresence(sess)
	})
	h3.Handler = mux
	return wtSrv
}

// pushSyntheticPresence emits synthetic presence datagrams at ~20 Hz until the
// session ends.
//
// Datagrams are LOSSY by design (QUIC DATAGRAM, RFC 9221): SendDatagram is
// fire-and-forget and never blocks on a slow client. On any send error (slow /
// closed client, datagram too large) we DROP the frame and keep ticking —
// drop-not-block. Nothing here reads or writes HG state.
func pushSyntheticPresence(sess *webtransport.Session) {
	ctx := sess.Context()
	ticker := time.NewTicker(wtEmitInterval)
	defer ticker.Stop()

	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			seq++
			frame := syntheticPresenceFrame{
				Seq:      seq,
				TsUnixMs: time.Now().UnixMilli(),
				Value:    syntheticValue(seq),
				Kind:     "synthetic.presence",
			}
			b, err := json.Marshal(frame)
			if err != nil {
				continue
			}
			if err := sess.SendDatagram(b); err != nil {
				// Slow/closed client or datagram too large: drop, don't block.
				// Only stop when the session itself is gone.
				if ctx.Err() != nil {
					return
				}
				continue
			}
		}
	}
}

// syntheticValue fabricates a bounded oscillating signal in [0,1]. Purely
// synthetic — a stand-in for e.g. an HDC-similarity delta so the pilot client
// has a non-constant value to render while proving the pipe. It is NOT computed
// from any graph state.
func syntheticValue(seq uint64) float64 {
	return 0.5 + 0.5*math.Sin(float64(seq)/10.0)
}
