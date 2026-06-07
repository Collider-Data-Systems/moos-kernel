# Runtime lane — `kernel` → `instance` vocab migration (T=218)

> **Status: BRANCH ANCHOR / intent note. No code rename in this commit.**
> This branch (`feat/manifold-instances-vocab`) is the authoring lane for the runtime side of the
> 4.0 `kernel→instance` rename (delta **D6**). Authored by the Z440 VS Code lead
> (`agent:vscode.hp-z440.primary` / `session:sam.z440-vscode-projection-lead`), 2026-06-07.
> Glued to `ffs0` branch `z440-vscode-lead/manifold-instances-vocab` and its design doc
> `dev/design/20260607-t218-manifold-instance-vocab-delta.md` via the shared purpose-slug
> **`manifold-instances-vocab`**.

## Why this branch exists

Per T218 branching doctrine, runtime work in `moos-kernel` always lands on `feat/<purpose-slug>`, keyed
to a feature session, and merges to `master` only when the build gate passes. This lane tracks the
`kernel → instance` rename so it converges as one reviewed unit instead of leaking across trunk commits.

`instance := fold(log)` exposed at an endpoint — the same object the README already describes as "the
kernel is a fold over the log". The rename is a **label** change; §M11/§M12 authority semantics and the
rewrite set (ADD/LINK/MUTATE/UNLINK) are unchanged.

## Runtime footprint to migrate (survey — NOT yet changed)

- `internal/kernel/` package name + all import paths.
- `/healthz` and `/state/*` payload field names that say `kernel`.
- `cmd/moos` flags / log lines referring to "kernel".
- README.md / CLAUDE.md prose.
- URN handling for `urn:moos:kernel:<ws>.<name>`.

## Migration discipline

- **Alias-first.** The runtime must accept both `kernel` and `instance` forms simultaneously across the
  transition so federation/readback and the router never break mid-migration. URN form stays readable as
  `urn:moos:kernel:<ws>.<name>` until a reviewed URN migration ships.
- **Build gate = apply gate (T218).** No rename merges to `master` until `Doctor` + `go test ./...` pass
  on this branch.
- **Boundary.** This branch carries data (this note). Its existence emits no HG rewrite, no deploy, no
  rename. It is S0 substrate until reviewed and merged with a provenance trailer:
  `authored-by: agent:vscode.hp-z440.primary / session:sam.z440-vscode-projection-lead / manifold-instances-vocab`.
