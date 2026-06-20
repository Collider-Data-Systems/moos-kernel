# mo:os kernel

A categorical hypergraph rewriting kernel for distributed knowledge work. Go, stdlib-first.

This repository is the OS-facing **runtime function program** for mo:os — the rewrite engine. It is
separate from application/domain groups such as `my-tiny-data-collider`, which run *on* the HG through
kernels and may own websites, DNS, servers, Calendar/GitHub/Workspace surfaces, and projection targets.
The semantic source of truth is the folded hypergraph and the live `/healthz` readback, not this file.

## The rule

Nothing happens except rewrites. **Four operations only:**

```
ADD    — create a node with typed properties
LINK   — create a relation (hyperedge) between two nodes via a typed port pair
MUTATE — change one property value on one existing node
UNLINK — remove a relation
```

`state(t) = fold(log[0..t])`. **Log is truth. State is derived.** The log is append-only; the kernel
is a fold (catamorphism) over it. Replay is prospective-only.

Nomenclature is enforced: node · relation · rewrite · property · operad · port · rewrite_category
WF01..WF21 · `_urn`/`_urns`. Never edge/wire/field/schema/mutation/association/binding/`_ref`.

## Layering

The kernel is a strict tower; effects live only at the top.

```
graph    (pure types, no IO)
  └─ fold     (pure catamorphism: state = fold(log))
       └─ operad   (registry: WF01..WF21, port pairs, validation — read-only after load)
            └─ kernel  (the ONLY mutator: Runtime, Store, §M11/§M12 gates, sweep)
                 └─ transport / mcp   (adapters: HTTP+SSE, MCP JSON-RPC)
```

| Layer | Path | Role |
|---|---|---|
| `internal/graph` | pure types | `Node`, `Relation`, `Property`, `Rewrite`, `Envelope` (+ optional `SessionURN` for explicit §M11 context), `GraphState` (with `NodesByType` / `RelationsBySrc` / `RelationsByTgt` indexes), `URN`, `Stratum`, `RewriteCategory`. No IO. |
| `internal/fold` | pure catamorphism | `Evaluate`, `Replay`, `EvaluateProgram`. Maintains state indexes on ADD/LINK/UNLINK. |
| `internal/operad` | type system | `Registry` (ontology **v3.16.2** — 53 node types, WF01–WF21) with `Version` for `/healthz`; strict port-pair `ValidateLINK` (pair must be declared; src/tgt type enforcement), `ValidateMUTATE`; `AdminScopeRewrite`, `SystemInternalEnvelope`, `ResolveSessionForEnvelope`; occupancy + admin-capability walks. |
| `internal/kernel` | **effect layer** | `Runtime`, `Store`, `LogStore` / `MemStore`. §M11 session-liveness + §M12 admin-capability gates (`liveness.go`). Sweep loop emits WF13 governance proposals on `t_hook.firing_state` transitions. The only place state mutates. |
| `internal/reactive` | predicate evaluator | Watch / React / Guard engine; `EvaluateThookPredicate` covers 10+ §M14 predicate kinds (incl. `when_capability`). Read-only — returns proposed `[]Envelope`, never mutates. |
| `internal/transport` | HTTP + SSE | the REST surface below. Reports `ontology_version` on `/healthz`. |
| `internal/hdc` | hyperdimensional computing | spectral, fiber, crosswalk, encode, live-index. Derived from state after each apply. |
| `internal/mcp` | MCP JSON-RPC 2.0 | SSE + stdio + Streamable HTTP. |
| `internal/tday` | T-day epoch | single source of `T0` + `Now()` + `At()`. |
| `cmd/moos` | entry point | flag parsing, registry/store load, seed, server wiring, sweep, graceful shutdown. |

**Layer discipline:** `graph` has no IO; `fold` is pure; only `kernel` has effects. `Registry` is
read-only after load; `GraphState` is replaced on each rewrite, never mutated in place.

## Runtime gates (live since T=171)

Every envelope passes two gates in `Runtime.Apply` / `Runtime.ApplyProgram` **before fold**:

- **§M11 liveness** (`checkLivenessM11`) — the emitter must have a session context. Explicit
  `env.session_urn` wins; fallback is a reverse `has-occupant` lookup from `env.actor` (unambiguous
  single session = pass; ambiguous or absent = reject). Runs against batch-initial state.
- **§M12 admin-capability** (`checkLivenessM12`) — admin-scope rewrites must hold WF02 superadmin.
  Admin scope = ADD/MUTATE on ontology-governed types (`system_instruction`, `gate`, `twin_link`,
  `transport_binding`, `kernel`) OR MUTATE of a property with `authority_scope: "kernel"` on a
  non-kernel node, classified by `operad.(*Registry).AdminScopeRewrite`. Runs against working-state
  inside the ApplyProgram loop, so it catches an intra-batch ADD-then-MUTATE bypass.

A **system-internal allowlist** (`operad.SystemInternalEnvelope` — kernel-URN actors emitting
infrastructure types `user` / `workstation` / `kernel`) precedes both gates. `SeedIfAbsent`
additionally bypasses §M11 structurally for bootstrap. **Replay is prospective-only**: `fold.Replay`
does not re-check liveness, so pre-gate persisted logs rebuild identically.

Operad validation (type / port-pair / authority) runs ahead of the gates; `fold` then enforces
structural invariants and maintains indexes.

## HTTP API surface

Served on `--listen` (default `:8000`), CORS-wrapped.

| Route | Method | Purpose |
|---|---|---|
| `/healthz` | GET | liveness + `ontology_version`, `t_day`, `log_len` |
| `/state/nodes`, `/state/nodes/{urn}` | GET | folded node state |
| `/state/relations`, `/state/relations/src/{urn}`, `/state/relations/tgt/{urn}` | GET | folded relations by side |
| `/log`, `/log/stream` | GET | raw rewrite log (+ SSE tail) |
| `/fold`, `/fold/stream` | GET | full folded state (+ SSE) |
| `/rewrites` | POST | submit one rewrite envelope (through both gates) |
| `/programs` | POST | submit an atomic batch of envelopes (all-or-nothing) |
| `/operad/node-types`, `/operad/rewrite-categories`, `/operad/port-colors` | GET | registry introspection |
| `/hdc/*` | GET | derived HDC views (similarity-matrix, eigenvalues, fiedler, fiber, crosswalk, …) |
| `/twin/ingest`, `/twin/status` | POST / GET | M9 twin-kernel adjoint sync |
| `/t-hook/evaluate/{urn}`, `/t-hook/evaluate` | GET / POST | §M14 predicate evaluation (single + batch) |
| `/t-cone` | GET | §M15 occupant's view of nodes with open hooks |

## MCP server

Served on `--mcp-addr` (default `:8080`) over SSE; also stdio (`--mcp-stdio` / `--stdio-only`) and
Streamable HTTP. JSON-RPC 2.0. Five tools:

- **Writes** — `apply_rewrite` (one envelope), `apply_program` (atomic batch). Both go through the
  same §M11/§M12 gates as the HTTP `/rewrites` and `/programs` routes.
- **Reads** — `graph_state`, `node_lookup` (by URN), `operad_registry` (points at the `/operad/*`
  HTTP routes for full introspection).

## Sweep

`Runtime.RunTimedSweep` / `SweepTick` runs on its own goroutine every `--sweep-interval`
(default `30s`, `0` disables). Each tick walks all pending `t_hook`s and, for every hook whose
predicate fires, **proposes** a WF13 `governance_proposal` (status `pending`). The sweep **never
auto-applies** — proposals await admin ratification. `t_hook.firing_state` state machine:
`pending → proposed → approved | rejected → applied | closed`. The sweep actor defaults to
`urn:moos:kernel:sweep`, overridden to `urn:moos:kernel:<ws>.primary` when `--seed` is set.

## Seed mode

`--seed` idempotently seeds the S2 core via `SeedIfAbsent` (liveness-bypassed): a `user` node, a
`workstation` node (with detected OS/arch), a `kernel` node, plus WF01 `owns` (user→workstation) and
WF03 `hosts` (workstation→kernel) relations. Names come from `--seed-user` (default `sam`) and
`--seed-ws` (default `hp-laptop`). Safe to run on every restart.

## Build / Run / Test

```bash
go build ./...

go run ./cmd/moos \
  --ontology ../ffs0/kb/superset/ontology.json \
  --log /tmp/moos.log \
  --seed
```

```bash
go test ./...
MOOS_INTEGRATION=1 go test ./...   # integration tests read sibling ffs0/kb/superset/ontology.json
```

Race-detector run requires cgo/gcc.

### CLI flags

| Flag | Default | Purpose |
|---|---|---|
| `--ontology` | (none) | path to `ontology.json`; omit → no type validation, gates bypass (registry-less) |
| `--log` | (none) | JSONL log path; omit → in-memory (non-persistent) |
| `--listen` | `:8000` | HTTP transport address |
| `--mcp-addr` | `:8080` | MCP SSE address |
| `--mcp-stdio` | false | also run MCP on stdin/stdout |
| `--stdio-only` | false | MCP stdio only — no HTTP/SSE (Desktop integration) |
| `--seed` | false | seed infrastructure nodes (idempotent, liveness-bypassed) |
| `--seed-user` | `sam` | username for seed node |
| `--seed-ws` | `hp-laptop` | workstation name for seed node |
| `--sweep-interval` | `30s` | t-hook sweep tick (`0` disables) |
| `--quic-addr` | (none) | UDP HTTP/3 QUIC listener; requires `--tls-cert` + `--tls-key` |
| `--tls-cert` / `--tls-key` | (none) | PEM cert/key for the QUIC listener |

## Dependencies

Stdlib-first. The only direct dependency is `github.com/quic-go/quic-go` (gated behind
`--quic-addr`); transitive deps (`golang.org/x/net`, `golang.org/x/crypto`, …) arrive via quic-go.

## Ontology

Canonical at [`ffs0/kb/superset/ontology.json`](../ffs0/kb/superset/ontology.json) — **v3.16.2**,
53 node types, WF01–WF21. Loaded at boot, immutable thereafter. The kernel reports its loaded version
on `/healthz`; if a doc and the readback disagree, the readback wins.

WF families include WF01 owns/owned-by, WF02 governs/delegates-to, WF03 hosts/hosted-on,
WF12 provides-kb/kb-source, WF13 governance-proposes/proposed-by, WF18 composition/dependency/
scheduling pairs, WF19 opens-on/has-occupant/has-purpose/pins-urn pairs, WF20 grammar-promotion,
WF21 causes/caused-by.

## Companion repositories

- **moos-router** — WF16 federation gateway; URN-prefix + type routing across kernels.
- **ffs0** — private control/research workspace: ontology source, skills, projection planners,
  doctrine notes, running-state. The docs/KB SOT lives there (`ffs0/AGENTS.md`).
- **Application groups** — e.g. `my-tiny-data-collider`, modeled as HG group/purpose/program/channel
  families, **not** as kernel code.

---
*Mirror note: this README is a hand-authored projection. The semantic SOT is the folded HG +
`/healthz`; the project brief SOT is `ffs0/AGENTS.md`. See `AGENTS.md` (this repo) for the thin mirror.*
