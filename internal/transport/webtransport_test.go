package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go"
)

// TestWebTransportSyntheticDatagramsLoopback stands up the SPIKE WebTransport
// listener on an isolated ephemeral loopback UDP port with an ephemeral
// in-memory cert (NO --log / --ontology / live :8000 instance involved), dials
// it with a real webtransport-go CLIENT, and asserts:
//   - it receives >= wantFrames synthetic datagrams,
//   - the seq field is strictly monotonically increasing (gaps allowed — the
//     transport is lossy by design),
//   - the emit cadence is ~20 Hz (~50 ms/tick), derived from the server's own
//     t_ms/seq so the check is immune to network jitter/loss.
//
// No network egress and no on-disk cert are required: the socket is loopback
// and the cert is generated in memory (see DevTLSConfig("","")).
func TestWebTransportSyntheticDatagramsLoopback(t *testing.T) {
	tlsConf, err := DevTLSConfig("", "")
	if err != nil {
		t.Fatalf("DevTLSConfig: %v", err)
	}

	// Bind our own UDP socket so we learn the ephemeral port before serving.
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	port := udpConn.LocalAddr().(*net.UDPAddr).Port

	wtSrv := buildWebTransportServer("", tlsConf)
	go func() { _ = wtSrv.Serve(udpConn) }()
	defer wtSrv.Close()

	dialer := &webtransport.Dialer{
		// Self-signed ephemeral cert + h3 ALPN. InsecureSkipVerify is correct
		// here: this is a localhost dev spike with a throwaway cert.
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h3"},
		},
	}
	defer dialer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://127.0.0.1:%d%s", port, WebTransportPresencePath)
	resp, sess, err := dialer.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Dial status = %d, want 200", resp.StatusCode)
	}
	defer sess.CloseWithError(0, "test done")

	const wantFrames = 12
	frames := make([]syntheticPresenceFrame, 0, 32)
	for len(frames) < 32 {
		rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
		b, err := sess.ReceiveDatagram(rctx)
		rcancel()
		if err != nil {
			break
		}
		var f syntheticPresenceFrame
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("datagram not valid syntheticPresenceFrame JSON: %v (raw=%q)", err, string(b))
		}
		if f.Kind != "synthetic.presence" {
			t.Fatalf("frame.kind = %q, want synthetic.presence", f.Kind)
		}
		frames = append(frames, f)
		if len(frames) >= wantFrames {
			// enough to assert on; keep a couple more for a stable cadence span
			if len(frames) >= wantFrames+4 {
				break
			}
		}
	}

	if len(frames) < wantFrames {
		t.Fatalf("received %d datagrams, want >= %d", len(frames), wantFrames)
	}

	// seq strictly increasing (lossy: gaps allowed, order must hold).
	for i := 1; i < len(frames); i++ {
		if frames[i].Seq <= frames[i-1].Seq {
			t.Fatalf("seq not strictly increasing at %d: %d then %d",
				i, frames[i-1].Seq, frames[i].Seq)
		}
	}

	// Cadence: server-side t_ms/seq span gives the true ticker period,
	// independent of network jitter or dropped datagrams.
	first, last := frames[0], frames[len(frames)-1]
	spanSeq := int64(last.Seq - first.Seq)
	if spanSeq <= 0 {
		t.Fatalf("non-positive seq span: %d", spanSeq)
	}
	perTickMs := float64(last.TsUnixMs-first.TsUnixMs) / float64(spanSeq)
	// Nominal 50 ms/tick (~20 Hz). Wide band to absorb OS timer coarseness
	// (Windows ~15 ms) without asserting a false-precise rate.
	if perTickMs < 25 || perTickMs > 150 {
		t.Fatalf("emit cadence %.1f ms/tick out of [25,150] (want ~50 ms / ~20 Hz)", perTickMs)
	}
	t.Logf("received %d synthetic datagrams; cadence %.1f ms/tick (~%.0f Hz)",
		len(frames), perTickMs, 1000.0/perTickMs)
}

// TestWebTransportAbsentFromReliableMux proves the spike is OFF by default: the
// reliable HTTP server's mux (the one bound to :8000) carries NO /wt/presence
// route, so with --wt-addr unset there is no WebTransport surface at all.
func TestWebTransportAbsentFromReliableMux(t *testing.T) {
	h := (&Server{}).Handler()
	req := httptest.NewRequest(http.MethodGet, WebTransportPresencePath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reliable mux serves %s with %d, want 404 (spike route must not be on the reliable server)",
			WebTransportPresencePath, rec.Code)
	}
}

// TestDevTLSConfigEphemeral checks the ephemeral in-memory cert path: a cert is
// generated with an in-memory private key (nothing written to disk) and the h3
// ALPN is advertised.
func TestDevTLSConfigEphemeral(t *testing.T) {
	cfg, err := DevTLSConfig("", "")
	if err != nil {
		t.Fatalf("DevTLSConfig: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want 1", len(cfg.Certificates))
	}
	if cfg.Certificates[0].PrivateKey == nil {
		t.Fatal("ephemeral cert has no in-memory private key")
	}
	if len(cfg.Certificates[0].Certificate) == 0 {
		t.Fatal("ephemeral cert has no DER bytes")
	}
	foundH3 := false
	for _, p := range cfg.NextProtos {
		if p == "h3" {
			foundH3 = true
		}
	}
	if !foundH3 {
		t.Fatalf("NextProtos = %v, want to include h3", cfg.NextProtos)
	}
}
