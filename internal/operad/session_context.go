package operad

import (
	"moos/kernel/internal/graph"
)

// Session-context and system-internal classifiers for §M11 (liveness) and
// §M12 (admin capability) kernel gating. Pure functions over state +
// envelope; no locking, no IO. Kept in the operad package so both the
// kernel.Runtime gate path and future PR 4 admin-gate path share one
// implementation.

// ResolveSessionResult describes how the envelope's session context was
// resolved (or why it could not be). Callers use the Kind field to shape
// error messages; SessionURN and Actor carry the resolved context when
// the resolution succeeds.
type ResolveSessionResult struct {
	Kind       ResolveSessionKind
	SessionURN graph.URN
	// Candidates is populated only when Kind == ResolveSessionAmbiguous;
	// it lists the session URNs the actor currently occupies so the caller
	// can surface them in the error message.
	Candidates []graph.URN
}

// ResolveSessionKind enumerates the outcomes of ResolveSessionForEnvelope.
type ResolveSessionKind int

const (
	// ResolveSessionExplicit: env.SessionURN was set and verified to be an
	// occupied session with has-occupant pointing at env.Actor. The
	// envelope is safe to pass §M11 gating.
	ResolveSessionExplicit ResolveSessionKind = iota

	// ResolveSessionInferred: env.SessionURN was empty, and exactly one
	// session in the state has has-occupant -> env.Actor. Single-session
	// occupants fall into this convenience path.
	ResolveSessionInferred

	// ResolveSessionActorIsSession: env.Actor is itself a session node.
	// The session context is the actor's own URN. No has-occupant hop is
	// needed; caller should use SessionURN == env.Actor.
	ResolveSessionActorIsSession

	// ResolveSessionExplicitMismatch: env.SessionURN was set, but the
	// named session is unknown, not of type session, unoccupied, or its
	// has-occupant does not point at env.Actor. Fail closed.
	ResolveSessionExplicitMismatch

	// ResolveSessionAmbiguous: env.SessionURN was empty and env.Actor
	// occupies more than one session. Caller must set SessionURN
	// explicitly; the resolver refuses to guess.
	ResolveSessionAmbiguous

	// ResolveSessionAbsent: env.SessionURN was empty and env.Actor
	// occupies no session at all. No valid context; fail closed under
	// §M11 unless the envelope matches the system-internal allowlist.
	ResolveSessionAbsent
)

// ResolveSessionForEnvelope determines the session context for a rewrite
// envelope per the §M11 resolver rules:
//
//  1. If env.Actor is a node of type "session", the actor IS the session;
//     return ResolveSessionActorIsSession with SessionURN == env.Actor —
//     but ONLY if that session itself has a canonical has-occupant relation
//     pointing at a principal (user or agent). An unoccupied session as
//     actor is not §M11-compliant: there is no seated principal to gate
//     against, so the resolver returns ResolveSessionAbsent rather than
//     silently passing. This mirrors CheckAdminCapability's hop-through-
//     has-occupant pattern in §M12.
//  2. If env.SessionURN is set, verify it: must name a session node, that
//     session must have has-occupant -> env.Actor. Success returns
//     ResolveSessionExplicit. Any failure returns ResolveSessionExplicitMismatch.
//  3. If env.SessionURN is empty, walk reverse has-occupant from env.Actor.
//     Exactly one match -> ResolveSessionInferred. Zero -> ResolveSessionAbsent.
//     More than one -> ResolveSessionAmbiguous (Candidates populated).
//
// The function reads state; it does not enforce §M11. Enforcement lives in
// kernel.Runtime and consults this resolver plus SystemInternalEnvelope
// for allowlisting.
func ResolveSessionForEnvelope(state graph.GraphState, env graph.Envelope) ResolveSessionResult {
	// Case 1 — actor is itself a session node. Session identity is its own
	// resolution, but the session must still be occupied — an unoccupied
	// session cannot represent any principal and must not bypass §M11.
	if actorNode, ok := state.Nodes[env.Actor]; ok && actorNode.TypeID == "session" {
		if !sessionHasAnyOccupant(state, env.Actor) {
			return ResolveSessionResult{Kind: ResolveSessionAbsent, SessionURN: env.Actor}
		}
		return ResolveSessionResult{Kind: ResolveSessionActorIsSession, SessionURN: env.Actor}
	}

	// Case 2 — explicit SessionURN wins when provided.
	if env.SessionURN != "" {
		sessionNode, ok := state.Nodes[env.SessionURN]
		if !ok || sessionNode.TypeID != "session" {
			return ResolveSessionResult{Kind: ResolveSessionExplicitMismatch, SessionURN: env.SessionURN}
		}
		if !sessionHasOccupantTarget(state, env.SessionURN, env.Actor) {
			return ResolveSessionResult{Kind: ResolveSessionExplicitMismatch, SessionURN: env.SessionURN}
		}
		return ResolveSessionResult{Kind: ResolveSessionExplicit, SessionURN: env.SessionURN}
	}

	// Case 3 — reverse-lookup. Walk has-occupant from the actor side.
	candidates := sessionsOccupyingActor(state, env.Actor)
	switch len(candidates) {
	case 0:
		return ResolveSessionResult{Kind: ResolveSessionAbsent}
	case 1:
		return ResolveSessionResult{Kind: ResolveSessionInferred, SessionURN: candidates[0]}
	default:
		return ResolveSessionResult{Kind: ResolveSessionAmbiguous, Candidates: candidates}
	}
}

// sessionHasAnyOccupant returns true when sessionURN has at least one
// canonical (has-occupant, is-occupant-of) relation whose target is a
// recognised principal (user or agent). Mirrors ResolveSessionOccupant
// but without caring which principal — just that one exists. Used by
// case 1 of ResolveSessionForEnvelope to block unoccupied-session-as-
// actor bypass.
func sessionHasAnyOccupant(state graph.GraphState, sessionURN graph.URN) bool {
	_, ok := ResolveSessionOccupant(state, sessionURN)
	return ok
}

// sessionHasOccupantTarget returns true when sessionURN has a canonical
// (has-occupant, is-occupant-of) relation pointing at actor.
func sessionHasOccupantTarget(state graph.GraphState, sessionURN, actor graph.URN) bool {
	for _, relURN := range state.RelationsFrom(sessionURN) {
		rel, ok := state.Relations[relURN]
		if !ok {
			continue
		}
		if rel.SrcPort != hasOccupantSrcPort || rel.TgtPort != isOccupantOfTgtPort {
			continue
		}
		if rel.TgtURN == actor {
			return true
		}
	}
	return false
}

// sessionsOccupyingActor walks reverse has-occupant relations (via
// state.RelationsTo on the actor side) and returns the session URNs whose
// canonical has-occupant points at actor. Stable order is not guaranteed;
// callers that rely on determinism should sort before comparing.
func sessionsOccupyingActor(state graph.GraphState, actor graph.URN) []graph.URN {
	var out []graph.URN
	for _, relURN := range state.RelationsTo(actor) {
		rel, ok := state.Relations[relURN]
		if !ok {
			continue
		}
		if rel.SrcPort != hasOccupantSrcPort || rel.TgtPort != isOccupantOfTgtPort {
			continue
		}
		// Verify the source is actually a session (defensive — the port-pair
		// check is already strong, but type-check guards against future pairs
		// that reuse the ports).
		srcNode, ok := state.Nodes[rel.SrcURN]
		if !ok || srcNode.TypeID != "session" {
			continue
		}
		out = append(out, rel.SrcURN)
	}
	return out
}

// SystemInternalEnvelope reports whether an envelope is exempt from §M11
// (liveness) and §M12 (admin capability) runtime gates. Gates are for
// user-space rewrites; system-internal emissions are below the governance
// line and must be allowed through so the kernel's own housekeeping does
// not brick itself.
//
// Classifier:
//
//   - Sweep WF13 governance_proposal emissions. Actor is a kernel URN
//     (DefaultSweepActor or a configured per-kernel sweep actor), rewrite
//     category is WF13. These are emitted by the sweep goroutine every
//     tick and must never be session-gated.
//   - Any envelope whose Actor is a kernel URN. This covers: sweep, HDC
//     drift claims emitted via applyReactiveLocked, admin-executed
//     kernel-authority MUTATEs (context_urn, local_t, status). The kernel
//     is the substrate; its own rewrites cannot be subject to §M11 at
//     the risk of a self-referential deadlock.
//   - ADD of infrastructure types (kernel, workstation, user) — these
//     exist to bootstrap the hypergraph itself. Even with a user actor,
//     they are below the liveness line. Note: SeedIfAbsent additionally
//     bypasses liveness structurally; this classifier catches the same
//     envelopes if they arrive via the normal Apply path.
//
// Keep this classifier conservative. Adding to the allowlist weakens
// §M11; removing items requires coordinated runtime + test changes.
func SystemInternalEnvelope(env graph.Envelope) bool {
	if isKernelActor(env.Actor) {
		return true
	}
	if env.RewriteType == graph.ADD && isInfrastructureType(env.TypeID) {
		return true
	}
	return false
}

// isKernelActor returns true when the URN is a kernel node URN.
// Kernel actors are the kernel itself (including the sweep sub-identity)
// and are authorised to emit below-the-line housekeeping rewrites.
func isKernelActor(actor graph.URN) bool {
	s := string(actor)
	const prefix = "urn:moos:kernel:"
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// isInfrastructureType returns true for the small fixed set of node types
// that bootstrap the graph: user, workstation, kernel. These ADDs are the
// substrate on which all other sessions and rewrites depend, and must be
// admitted even without an occupied session.
func isInfrastructureType(t graph.TypeID) bool {
	switch t {
	case "user", "workstation", "kernel":
		return true
	}
	return false
}

// ontologyGovernedTypes is the set of node types whose ADD or MUTATE
// requires superadmin capability (§M12 Q3 case 2 per ffs0#33 Guido answers).
// Any change to these types influences how the graph itself is grammared —
// they are S2 infrastructure artifacts whose authority must route through
// WF02 governs → role:superadmin, not ordinary occupant authority.
//
//   - system_instruction: S4 context overlay, shapes how downstream
//     reads interpret the graph.
//   - gate: fail-closed flow primitive (§M8); wrong gates brick Apply.
//   - twin_link: kernel-replication pairing (§M9); wrong pairing
//     corrupts the adjoint.
//   - transport_binding: wire-protocol declaration; wrong binding
//     breaks federation.
//   - kernel: ADD kernel creates a new sovereign substrate — as
//     ontology-adjacent as it gets (Guido flag on the M11/M12 plan).
var ontologyGovernedTypes = map[graph.TypeID]struct{}{
	"system_instruction":  {},
	"gate":                {},
	"twin_link":           {},
	"transport_binding":   {},
	"kernel":              {},
}

// AdminScopeRewrite classifies whether an envelope touches admin-scope
// surface per §M12. The admin scope covers (per Guido's answers on
// ffs0#33 and the M11/M12 implementation plan):
//
//  1. ADDs or MUTATEs of ontology-governed node types (system_instruction,
//     gate, twin_link, transport_binding, kernel). Any change to the
//     grammar of the graph itself flows through superadmin.
//  2. MUTATEs of properties with authority_scope == "kernel" on non-kernel
//     nodes. The authority declaration on the ontology says "only kernel
//     URNs may change this", and §M12 extends that to "or a superadmin-
//     capable actor". Non-kernel actors without superadmin fail closed.
//  3. (Reserved for §M16) MUTATEs to the ontology file / version. Stays
//     off until an ontology_publication node type lands.
//
// System-internal envelopes (kernel actors, infrastructure ADDs, sweep
// WF13) are allowlisted by the §M11 gate BEFORE §M12 runs — a kernel
// URN emitting an ontology-governed rewrite bypasses §M12 by design.
// The gate (checkLiveness in kernel package) is where the allowlist
// precedes the admin check.
//
// The method takes registry access (vs. the PR 3 package-level form)
// because case 2 requires looking up the type-spec authority_scope for
// additive MUTATEs where the field is not yet on the node. For fields
// already on the node, the existing Property.AuthorityScope is consulted
// directly.
func (r *Registry) AdminScopeRewrite(env graph.Envelope, state graph.GraphState) bool {
	if r == nil {
		return false
	}

	// Case 1 — ADD of ontology-governed type.
	if env.RewriteType == graph.ADD {
		if _, gov := ontologyGovernedTypes[env.TypeID]; gov {
			return true
		}
		return false
	}

	// Case 1+2 — MUTATE on a node. Look up the node's type first.
	if env.RewriteType == graph.MUTATE {
		node, ok := state.Nodes[env.TargetURN]
		if !ok {
			// Target missing from state — fold will reject with ErrNodeNotFound
			// at apply time. Not our problem to enforce admin-scope on a
			// non-existent node.
			return false
		}

		// Case 1 — target node is of an ontology-governed type.
		if _, gov := ontologyGovernedTypes[node.TypeID]; gov {
			return true
		}

		// Case 2 — kernel-authority property MUTATE on a non-kernel node.
		// Check existing node properties first (cheap map lookup), then fall
		// back to the registry type spec for additive MUTATE (field not yet
		// present on the node).
		if scope, ok := authorityScopeForField(r, node, env.Field); ok {
			if scope == "kernel" && node.TypeID != "kernel" {
				return true
			}
		}
		return false
	}

	// LINK / UNLINK are not admin-scope-gated in §M12 v1. A future PR could
	// extend this (e.g. LINK to superadmin role requires superadmin), but
	// that's outside the current doctrine scope.
	return false
}

// authorityScopeForField returns the AuthorityScope for a field on a node,
// preferring the live property record (reflects what actually landed) and
// falling back to the type-spec declaration (for additive MUTATEs where
// the field is not yet on the node). Returns "", false when the field is
// unknown to both sources.
func authorityScopeForField(r *Registry, node graph.Node, field string) (string, bool) {
	if prop, ok := node.Properties[field]; ok {
		return prop.AuthorityScope, true
	}
	typeSpec, hasType := r.NodeTypes[node.TypeID]
	if !hasType {
		return "", false
	}
	pspec, hasPspec := typeSpec.Properties[field]
	if !hasPspec {
		return "", false
	}
	return pspec.AuthorityScope, true
}
