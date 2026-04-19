# moos-kernel

Ontology: `ffs0/kb/superset/ontology.json` (**v3.11.0** — 52 node types, 20 WFs / WF01–WF20)
Canonical reference: `ffs0/kb/research/20260408-foundation-t158.md`
T=164 delta: `ffs0/kb/research/session/20260414-t164-session-channel-purpose.md`
T=168–169 delta: `ffs0/kb/research/t169_conv_sum_claude_HPlap.md` (this conversation's summary)

---

## The Rule

Nothing happens except rewrites. Four operations only:

```
ADD    — create a new node with typed properties
LINK   — create a new relation (hyperedge) connecting two nodes
MUTATE — change one typed property value on one existing node
UNLINK — remove one relation
```

state(t) = fold(log[0..t]). Log is truth. State is derived.

---

## Nomenclature (enforced)

| Use | Not this |
|-----|----------|
| node | object, element, vertex |
| relation | binding, edge, wire, association |
| rewrite | morphism, update, mutation |
| rewrite_category (WF01–WF20) | named relation, UML association |
| property | field, payload, attribute |
| operad | schema, grammar |
| interaction_node | transition, event, message |
| `_urn` / `_urns` suffix | `_ref` / `_refs` |

Relations are truth. Properties never duplicate topology.

---

## Package Structure

```
internal/graph      — pure types (no IO): Node, Relation, Property, Rewrite, Envelope,
                      GraphState (+ NodesByType / RelationsBySrc / RelationsByTgt indexes,
                      T=169), URN, Stratum, RewriteCategory
internal/fold       — pure catamorphism: Evaluate, Replay, EvaluateProgram
                      (maintains GraphState indexes on ADD/LINK/UNLINK)
internal/operad     — type system: Registry (WF01–WF20), ValidateLINK,
                      ValidateMUTATE, loader (loads external ontology.json);
                      session-occupancy helpers (ResolveSessionOccupant,
                      CheckAdminCapability) per §M11/§M12/§M19
internal/kernel     — effect layer: Runtime, Store, LogStore, MemStore;
                      sweep loop (RunTimedSweep / SweepTick) emitting WF13
                      governance_proposals per t_hook.firing_state transitions
internal/reactive   — predicate evaluator (EvaluateThookPredicate with 10+
                      §M14 kinds incl. when_capability), Watch/React/Guard engine
internal/transport  — HTTP adapter: state/log/rewrites/programs/operad/hdc,
                      plus /t-hook/evaluate (GET + POST batch), /t-cone,
                      /twin/ingest, /fold (+ SSE stream), /healthz
internal/hdc        — Hyperdimensional Computation: spectral, fiber,
                      crosswalk, encode, live_index
internal/mcp        — MCP JSON-RPC 2.0 (SSE + stdio + Streamable HTTP)
internal/tday       — shared T-day epoch (T0 + Now + At), consumed by
                      transport/server.go and kernel/sweep.go (T=169)
cmd/moos            — entry point
```

---

## Running

```bash
go run ./cmd/moos \
  --ontology ../../ffs0/kb/superset/ontology.json \
  --log /tmp/moos.log \
  --seed
```

### CLI Flags

| Flag | Default | Purpose |
|------|---------|--------|
| `--ontology` | (none) | Path to ontology.json; omit for no type validation |
| `--log` | (none) | JSONL log path; omit for in-memory (non-persistent) |
| `--listen` | `:8000` | HTTP transport address |
| `--mcp-addr` | `:8080` | MCP SSE server address |
| `--mcp-stdio` | false | Also run MCP on stdin/stdout |
| `--stdio-only` | false | MCP stdio only — no HTTP/SSE |
| `--seed` | false | Seed infrastructure nodes (idempotent) |
| `--seed-user` | `sam` | Username for seed node |
| `--seed-ws` | `hp-laptop` | Workstation name for seed node |
| `--sweep-interval` | `30s` | T-hook sweep tick (0 disables). Walks pending hooks, emits governance_proposal + firing_state MUTATE per firing. |
| `--quic-addr` | (none) | UDP address for HTTP/3 QUIC listener. Requires `--tls-cert` and `--tls-key`. |
| `--tls-cert` | (none) | TLS certificate (PEM) for QUIC listener. |
| `--tls-key` | (none) | TLS private key (PEM) for QUIC listener. |

---

## Testing

```bash
go test ./...
```

Test files (growing list; stdlib-only, no external deps):

| Package | Files |
|---------|-------|
| `internal/fold` | `fold_test.go` |
| `internal/graph` | `state_test.go` |
| `internal/hdc` | `hdc_test.go`, `spectral_test.go`, `fiber_test.go`, `crosswalk_test.go` |
| `internal/kernel` | `runtime_reactive_test.go`, `sweep_test.go` |
| `internal/operad` | `occupancy_test.go` |
| `internal/reactive` | `engine_test.go`, `predicate_test.go`, `predicate_extended_test.go` |
| `internal/tday` | `tday_test.go` |
| `internal/transport` | `thook_test.go`, `tcone_test.go` |

---

## Development Conventions

- **No external dependencies** beyond quic-go (required for `--quic-addr`). Stdlib-first; stdlib-only for everything except the QUIC transport.
- **Layer discipline**: `graph` has no IO; `fold` is pure; only `kernel` has effects.
- **Immutability**: `Registry` is read-only after load. `GraphState` is replaced on each rewrite, never mutated in place.
- **Operad validation before fold**: `operad` validates type/port/authority; `fold` enforces structural invariants and maintains the secondary indexes.
- **Reactive is read-only**: `reactive.Engine` never mutates graph state directly; returns proposed `[]Envelope` for the caller to apply. The sweep runs on its own goroutine (started from `cmd/moos/main.go`) and applies through the normal `ApplyProgram` path.
- **HDC is derived**: `hdc.LiveIndex` is recomputed from state after each apply.
- **Single T-day epoch**: `internal/tday` is the one source of `T0` + `Now()`. Never redefine the epoch elsewhere — fixes a real day-boundary drift bug that existed pre-T=169.
- **Firing semantics**: sweep proposes via WF13 governance; never auto-applies. `t_hook.firing_state` is the idempotency state machine (`pending → proposed → approved|rejected → applied / closed`). Approver reactor (proposed → applied) is not yet built.

---

## Safety

- Never commit `ffs0/secrets/` files.
- Never embed API keys in code.
- `ffs0/` is a sibling repo — not part of this module.
- Build artifacts (`moos-kernel*.exe`, `moos-kernel.exe~`) and ephemeral state snapshots are gitignored; do not commit them.
