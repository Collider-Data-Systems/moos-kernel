# P9 SPIKE — WebTransport / HTTP-3 datagram data-plane

**Status:** spike. Feature-flagged, **OFF by default**, synthetic. Not a surface.
**Branch:** `feat/webtransport`.

## Why this exists (and why it is NOT justified as a real surface)

The Phase-5 measurement (`scripts/bench-frame-read.mjs`) clocked the **reliable**
MCP / SSE frame-read path at **~61 Hz p95** — comfortably above any realistic
single-user UI refresh rate. In a single-user, read-only pilot there is **no
organic high-rate producer** that would benefit from a lossy datagram pipe.

So this spike is deliberately narrow: it **exercises and proves** the
WebTransport datagram pipe using a **synthetic** ~20 Hz stream, so we know the
transport works end-to-end **if** an organic high-rate surface ever appears. It
does **not** claim to beat, or be needed over, the reliable path.

> **Re-measure before any adoption.** If a real high-rate surface is ever
> proposed, benchmark this datagram path against the SSE/reliable baseline
> (`bench-frame-read.mjs`) first. The spike proves the pipe is *possible*, not
> that it is *warranted*.

## Isolation invariants

- Started **only** when `--wt-addr` is non-empty. Empty (default) ⇒ the listener
  does not exist; the reliable `:8000` HTTP, `:8080` MCP, and the M10
  `--quic-addr` HTTP/3 mirror all run untouched.
- Separate listener/port from every reliable path. Serves **only**
  `/wt/presence`.
- Touches **no HG state**: no ADD/LINK/MUTATE/UNLINK, no log, no fold, no
  registry, no §M11/§M12 path. It emits fabricated datagrams and nothing else.

## Flag & wiring

| Flag | Default | Meaning |
|------|---------|---------|
| `--wt-addr` | (empty) | UDP address for the synthetic WebTransport listener, e.g. `:8443`. Empty ⇒ not started. |
| `--tls-cert` / `--tls-key` | (empty) | Reused if provided. If **either** is empty, an **ephemeral in-memory** self-signed localhost cert is generated at startup. |

`--wt-addr` is a **new, dedicated flag** — it is intentionally NOT `--quic-addr`.
`--quic-addr` already runs the M10 HTTP/3 *reliable* mirror of the HTTP API;
reusing it would either replace a shipped reliable path or couple the spike to
it, both of which violate "reliable paths byte-for-byte unchanged." The spike
gets its own flag and its own listener.

Run it (dev):

```bash
go run ./cmd/moos --wt-addr :8443
# reliable :8000 / :8080 still start exactly as before; add --wt-addr to opt in.
```

## Certificate handling (no secret committed)

- With `--tls-cert`/`--tls-key`: loaded from disk (operator-supplied).
- Without: an **ephemeral ECDSA P-256** self-signed cert for
  `localhost` / `127.0.0.1` / `::1` is minted in memory at startup. The private
  key lives only in process memory — it is **never written to disk and never
  committed**. Dev-only, localhost-only. Clients dial with `InsecureSkipVerify`
  (or pin the ephemeral leaf out-of-band).

Implementation: `internal/transport/devcert.go` (`DevTLSConfig`).

## Client contract (build the pilot client against this)

A pilot WebTransport client dials, over HTTP/3 (Extended CONNECT):

```
https://<host>:<wt-addr-port>/wt/presence
```

- ALPN: `h3`. QUIC datagrams must be enabled by the client.
- The server accepts the session and pushes one **QUIC DATAGRAM** every ~50 ms
  (~20 Hz), **drop-not-block** (lossy by design — gaps are expected).

Each datagram is a small JSON object (`internal/transport/webtransport.go`,
`syntheticPresenceFrame`):

```json
{
  "seq":   1,
  "t_ms":  1737300000000,
  "value": 0.5498,
  "kind":  "synthetic.presence"
}
```

| Field | Type | Meaning |
|-------|------|---------|
| `seq` | uint64 | Per-session monotonic counter, starts at 1. Strictly increasing; **gaps allowed** (datagrams are lossy). |
| `t_ms` | int64 | Server wall-clock ms (`UnixMilli`) at emit time. |
| `value` | float64 | **Synthetic** signal in `[0,1]` (`0.5 + 0.5·sin(seq/10)`) — a stand-in for e.g. an HDC-similarity delta. Not derived from HG. |
| `kind` | string | Always `"synthetic.presence"` — marks the payload as fabricated. |

Cadence can be verified purely from the payload: `Δt_ms / Δseq ≈ 50 ms`,
independent of loss/jitter.

## Dependency added (flagged for review)

- **`github.com/quic-go/webtransport-go` v0.10.0** — a **quic-go sibling** built
  on quic-go. It is a *new module*, but stays in-family and, at v0.10.0, pins
  **quic-go v0.59.0** (already the kernel's dep) with **no bump** to quic-go, the
  Go directive (`1.24`), or any `golang.org/x/*` version.
- Transitively adds **`github.com/dunglas/httpsfv` v1.1.0** (structured-field
  values, required by webtransport-go's capsule/protocol handling).

Nothing heavier was added. See the PR body for the reviewer checklist.

## Test

`internal/transport/webtransport_test.go`:

- `TestWebTransportSyntheticDatagramsLoopback` — isolated ephemeral loopback UDP
  port + ephemeral in-memory cert (no `--log`, no `--ontology`, no live `:8000`
  instance), real webtransport-go client, asserts ≥12 datagrams received,
  strictly monotonic `seq`, and ~50 ms/tick cadence. No network egress, no
  on-disk cert.
- `TestWebTransportAbsentFromReliableMux` — the reliable HTTP mux 404s
  `/wt/presence` (proves off-by-default isolation).
- `TestDevTLSConfigEphemeral` — ephemeral cert has an in-memory key and h3 ALPN.

`go test ./...` is green with no special network/cert setup.
