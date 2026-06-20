# AGENTS.md — moos-kernel (Go runtime)

> **Mirror of `ffs0/AGENTS.md`** (project SOT) · manual · don't edit except emergency de-rot.
> This repo carries runtime-deltas only — it does not restate the seat table or the full fleet brief.

## Orientation
This repository is the **Go runtime** for mo:os — the rewrite engine (log/fold/operad/kernel/
transport/mcp). It is the OS-facing runtime function program, **not** an application domain. The
**docs/KB SOT lives in the `ffs0` repo** (`ffs0/AGENTS.md` + `kb/superset/running-state.md` +
`kb/superset/ontology.json`); the **semantic SOT is the folded HG + live `/healthz`** readback. A
Markdown file is never final truth — if a doc and the readback disagree, the readback wins.

## The rule (non-negotiable)
Four rewrites only: **ADD · LINK · MUTATE · UNLINK**. **Log is truth. State is derived.**
Nomenclature — use: node · relation · rewrite · property · operad · port · rewrite_category
WF01..WF21 · `_urn`/`_urns`. Never: edge · wire · field · schema · mutation · association ·
binding · `_ref`. Authoritative vocabulary: `ontology.json` in ffs0.

## Branching
Runtime repos branch **`feat/<purpose-slug>`** (trunk-first on `main` only for verified
single-lane, non-colliding work). Merge with provenance; commit trailer
`authored-by: <agent-urn> / <session-urn> / <purpose-slug>`.

## Safety
Never commit `secrets/`, API tokens, or build artifacts (`moos-kernel*.exe`, `*.bak-*`, `*~`,
ephemeral state). `ffs0/` is a **sibling** repo, not part of this module.

## Pointers
- Project SOT / seat table / fleet brief → **`ffs0/AGENTS.md`**.
- Live round-to-round state → `ffs0/kb/superset/running-state.md`.
- Kernel internals, gates, package map, CLI → this repo's `README.md` and `CLAUDE.md`.

---
authored-by: agent:claude-cowork.hp-z440 / session:sam.z440-cowork-workspace / config-overhaul
