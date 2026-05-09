# mo:os kernel

A categorical hypergraph rewriting kernel for distributed knowledge work. Go, stdlib-first.

As of T189, this repository is the OS-facing runtime function program for mo:os. It is separate from application/domain groups such as `my-tiny-data-collider`, which run on the HG through kernels and may own websites, DNS, servers, Calendar/GitHub/Workspace surfaces, and public/private projection targets.

## What it is

mo:os is a **semantic functorial network over distributed compute**, where all components — hardware and programs — are considered categorically. The hypergraph (HG) is the source of truth; state is derived from a log of four primitive rewrites:

```
ADD    — create a node with typed properties
LINK   — create a relation (hyperedge) between two nodes via a typed port pair
MUTATE — change one property value on one existing node
UNLINK — remove a relation
```

`state(t) = fold(log[0..t])`. The log is append-only; the kernel is a fold over it. Replay is prospective-only.

The session is the centerpiece. A session is a 5-facet scoped context: `(scope, purpose) x (host, owner, occupant)`. Its relations make it findable by G-direction ingest from surfaces like Drive, Gmail, Calendar, GitHub, websites, and DNS, and make it capable of writing programs that pull explicit actuator leaves in the F direction. Leaves are boundary actions with graph-derived arguments, not hidden side effects.

## What's in the box

| Layer | Path | Role |
|---|---|---|
| `internal/graph` | pure types | `Node`, `Relation`, `Property`, `Rewrite`, `Envelope`, `GraphState` (with indexes), `URN`, `Stratum`, `RewriteCategory`. No IO. |
| `internal/fold` | pure catamorphism | `Evaluate`, `Replay`, `EvaluateProgram`. Maintains state indexes on ADD/LINK/UNLINK. |
| `internal/operad` | type system | `Registry` (current ontology v3.16.1: 53 node types; WFs WF01–WF21), strict port-pair `ValidateLINK`, `ValidateMUTATE`, occupancy resolution, session-context resolution, admin-capability walks. |
| `internal/kernel` | effect layer | `Runtime`, `Store`, `LogStore`/`MemStore`. §M11 session-liveness + §M12 admin-capability gates. Sweep loop emits WF13 governance proposals on `t_hook.firing_state` transitions. |
| `internal/reactive` | predicate evaluator | Watch / React / Guard engine; `EvaluateThookPredicate` covers 10+ §M14 predicate kinds. |
| `internal/transport` | HTTP + SSE | `/state/*` (e.g. `/state/nodes`, `/state/relations`), `/log`, `/rewrites`, `/programs`, `/operad/*` (e.g. `/operad/node-types`), `/hdc/*`, `/t-hook/evaluate`, `/t-cone`, `/twin/ingest`, `/fold` (with SSE), `/healthz`. |
| `internal/hdc` | hyperdimensional computing | spectral, fiber, crosswalk, encode, live-index. |
| `internal/mcp` | MCP JSON-RPC | SSE + stdio + Streamable HTTP. |

Layer discipline: `graph` has no IO; `fold` is pure; only `kernel` has effects.

## Running

```bash
go run ./cmd/moos \
  --ontology ../ffs0/kb/superset/ontology.json \
  --log /tmp/moos.log \
  --listen :8000 \
  --mcp-addr :8080 \
  --seed
```

Key flags:

| Flag | Default | Purpose |
|---|---|---|
| `--ontology` | (none) | path to `ontology.json`; without it, ontology-backed type validation is disabled and liveness/admin gates are bypassed because there is no registry-backed operad |
| `--log` | (none) | JSONL log path; in-memory if omitted |
| `--listen` | `:8000` | HTTP transport |
| `--mcp-addr` | `:8080` | MCP SSE |
| `--sweep-interval` | `30s` | t-hook sweep cadence (0 disables) |
| `--quic-addr` | (none) | UDP/HTTP3 listener (requires `--tls-cert` + `--tls-key`) |

Direct dependencies are stdlib-only except `quic-go` (gated behind `--quic-addr`); transitive dependencies include `golang.org/x/net` and `golang.org/x/crypto` via `quic-go`.

## Testing

```bash
go test ./...
```

Race-detector requires cgo. Integration tests gated behind `MOOS_INTEGRATION=1` (read sibling `ffs0/kb/superset/ontology.json`).

## Runtime gates (live since T=171 round 11)

Every envelope passes two gates before fold:

- **§M11 liveness** — emitter must have a session context. Explicit `env.session_urn` wins; fallback is reverse-`has-occupant` lookup from `env.actor` (single-session = pass; ambiguous or absent = reject).
- **§M12 admin-capability** — admin-scope rewrites (ADDs on ontology-governed types, MUTATEs of `authority_scope: kernel` properties on non-kernel nodes) walk `WF02 governs` from actor through `role:superadmin` to verify capability.

System-internal allowlist (kernel-URN actors emitting infrastructure types) bypasses both. `SeedIfAbsent` additionally bypasses §M11 structurally for bootstrap.

Fold-time replay does **not** re-check liveness — pre-gate persisted logs rebuild identically.

## Ontology

[`ffs0/kb/superset/ontology.json`](https://github.com/Collider-Data-Systems/ffs0) (private workspace) — v3.16.1 — 53 node types, WFs WF01–WF21. Loaded at boot; immutable thereafter.

Selected node types: `session`, `program`, `purpose`, `agent`, `kernel`, `channel`, `knowledge_item`, `claim`, `derivation`, `calendar_event`, `clock`, `t_hook`, `reactor`, `watcher`, `tool_call`, `tool_result`, `external_op`, `compute`, `storage`, `harness`, `workflow`, `group`, `role`, `gate`, `system_instruction`, `transport_binding`, `twin_link`, `governance_proposal`, `grammar_fragment`.

WF categories include WF01 owns/owned-by, WF02 governs/delegates-to, WF12 provides-kb/kb-source, WF13 governance-proposes/proposed-by, WF18 composition/dependency/scheduling pairs, WF19 opens-on/has-occupant/has-purpose/pins-urn/filtering/tool-mount pairs, WF20 grammar-promotion ceremony, and WF21 causes/caused-by.

## Kernel, Host OS, And Applications

The kernel's job is runtime substrate: log/fold, operad validation, session liveness, admin capability, transport, HDC derivation, and explicit actuator boundaries. Longer-term OS integration may include a Rust host extension on Linux/Windows for local watchers, credentials, policy, and user-approved data channels. That host extension should still be substrate/runtime code, separate from application code.

Applications are HG groups/program families that use the runtime. `my-tiny-data-collider` is one such application/domain: websites, DNS, content/data servers, Calendar, GitHub, Workspace, and other projection surfaces can belong to that group without becoming part of `moos-kernel`.

## Federation

Twin kernels ride the same hypervisor host via `twin_link` edges and the WF16 router (`Collider-Data-Systems/moos-router`). Cross-machine federation rides Cloudflare tunnels (Z440 ↔ hp-laptop). The router shards by URN prefix; each kernel keeps its own sovereign log.

## Status

Current hp-laptop runtime context at T189: ontology v3.16.1, `session:sam.governance` live, Calendar/time-fabric program family pinned into governance session scope, and the local ffs0 projection dashboard reporting `warn` with 12 pass, 2 warn, 0 fail.

Near-term kernel work remains correctness-focused: keep the log/fold/operad/session/authority contract small and dependable while projection/ingest applications develop in `ffs0` and application-specific repos.

## Repository layout

```
cmd/moos          # entry point
internal/...      # see "What's in the box" table
go.mod            # stdlib + quic-go only
```

## Companion repositories

- **moos-router** — WF16 federation gateway; URN-prefix and type routing across kernels.
- **ffs0** — private workspace, ontology source, skills, projection planners, reports, and operator dashboards.
- **Application groups** — e.g. `my-tiny-data-collider`, modeled as HG group/purpose/program/channel families, not as kernel code.

## License

(TBD — assignment in progress)

## Contact

Maintained by [@Collider-Data-Systems](https://github.com/Collider-Data-Systems). Two teams: **Sam** (kernels + delegates + most sessions), **Moos** (multimodal diary curation).
