package kernel

import (
	"testing"

	"moos/kernel/internal/graph"
	"moos/kernel/internal/operad"
)

// §M13 tests — bumpSessionLocalT must increment session.local_t for the
// session that the kernel resolved this envelope to, regardless of whether
// the actor is the session itself, an agent inferring via has-occupant, or
// an explicit env.SessionURN. Closes the session-actor-agent-lookup
// sub-program from T=168 (surfaced concretely T=174 ~00:45 by Guido during
// Phase A: 24 acknowledged Cowork rewrites left local_t=0 because the
// agent-actor inferred path skipped the bump).

// readLocalT pulls the local_t value off the session node, normalizing
// whichever numeric type the property happens to carry.
func readLocalT(t *testing.T, rt *Runtime, sessionURN graph.URN) int64 {
	t.Helper()
	node, ok := rt.state.Nodes[sessionURN]
	if !ok {
		t.Fatalf("session %s not in state", sessionURN)
	}
	prop, ok := node.Properties["local_t"]
	if !ok {
		return 0
	}
	switch v := prop.Value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	t.Fatalf("local_t has unexpected type %T", prop.Value)
	return 0
}

// m13TestRegistry extends the liveness registry with the local_t property
// declaration on `session`, and declares WF19 so UNLINK envelopes
// targeting has-occupant relations validate cleanly. Without local_t in
// the operad, bumpSessionLocalT's MUTATE can't inject a PropertySpec via
// injectPropertySpec → fold rejects. Without WF19 declared, UNLINK
// validation fails with "unknown rewrite_category".
func m13TestRegistry() *operad.Registry {
	reg := operad.EmptyRegistry()
	for _, typeID := range []graph.TypeID{"session", "user", "agent", "workstation", "kernel", "program"} {
		spec := operad.NodeTypeSpec{ID: typeID, Stratum: "S2"}
		if typeID == "session" {
			spec.Properties = map[string]operad.PropertySpec{
				"local_t": {
					Mutability:     "mutable",
					AuthorityScope: "kernel",
					Type:           "int",
				},
			}
		}
		reg.NodeTypes[typeID] = spec
	}
	// Declare WF19 with the has-occupant/is-occupant-of port pair so the
	// rotation test's UNLINK passes ValidateUNLINK. Mirrors the v3.10+
	// shape (D19.1 grammar_fragment) but minimal — TgtTypes restricted to
	// agent so the rotation test's session→agent UNLINK validates.
	reg.RewriteCategories[graph.WF19] = operad.RewriteCategorySpec{
		ID:              graph.WF19,
		Name:            "WF19",
		AllowedRewrites: []graph.RewriteType{graph.LINK, graph.UNLINK},
		SrcTypes:        []graph.TypeID{"session"},
		TgtTypes:        []graph.TypeID{"agent", "user"},
		SrcPort:         "has-occupant",
		TgtPort:         "is-occupant-of",
		Authority:       "kernel",
	}
	return reg
}

func newM13Runtime(t *testing.T) *Runtime {
	t.Helper()
	return &Runtime{
		state:       graph.NewGraphState(),
		store:       NewMemStore(),
		registry:    m13TestRegistry(),
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}
}

// TestApply_BumpsLocalT_ActorIsSession — regression check: the original
// path (actor == session URN) still ticks local_t.
func TestApply_BumpsLocalT_ActorIsSession(t *testing.T) {
	rt := newM13Runtime(t)
	sessionURN := graph.URN("urn:moos:session:sam.solo")
	injectOccupancy(rt, sessionURN, "urn:moos:agent:claude", "agent")

	env := graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       sessionURN,
		NodeURN:     "urn:moos:program:x",
		TypeID:      "program",
	}
	if _, err := rt.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readLocalT(t, rt, sessionURN); got != 1 {
		t.Errorf("session.local_t = %d, want 1 (session-actor path)", got)
	}
}

// TestApply_BumpsLocalT_ActorIsAgent_Inferred — the §M13 fix path. Single-
// session agent emits with no SessionURN; reverse-lookup resolves the
// session; local_t must tick.
func TestApply_BumpsLocalT_ActorIsAgent_Inferred(t *testing.T) {
	rt := newM13Runtime(t)
	sessionURN := graph.URN("urn:moos:session:sam.solo")
	agentURN := graph.URN("urn:moos:agent:claude")
	injectOccupancy(rt, sessionURN, agentURN, "agent")

	env := graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       agentURN,
		NodeURN:     "urn:moos:program:x",
		TypeID:      "program",
	}
	if _, err := rt.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readLocalT(t, rt, sessionURN); got != 1 {
		t.Errorf("session.local_t = %d, want 1 (inferred-session path — the §M13 fix)", got)
	}
}

// TestApply_BumpsLocalT_ActorIsAgent_Explicit — agent emits with an
// explicit env.SessionURN that matches has-occupant. Path is
// ResolveSessionExplicit; local_t ticks.
func TestApply_BumpsLocalT_ActorIsAgent_Explicit(t *testing.T) {
	rt := newM13Runtime(t)
	sessionURN := graph.URN("urn:moos:session:sam.solo")
	agentURN := graph.URN("urn:moos:agent:claude")
	injectOccupancy(rt, sessionURN, agentURN, "agent")

	env := graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       agentURN,
		SessionURN:  sessionURN,
		NodeURN:     "urn:moos:program:x",
		TypeID:      "program",
	}
	if _, err := rt.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readLocalT(t, rt, sessionURN); got != 1 {
		t.Errorf("session.local_t = %d, want 1 (explicit-session path)", got)
	}
}

// TestApply_NoBump_Ambiguous — agent occupying TWO sessions emits without
// explicit SessionURN. §M11 gate rejects upstream (returns Ambiguous);
// neither session ticks. Defensive double-check at the bump path.
func TestApply_NoBump_Ambiguous(t *testing.T) {
	rt := newM13Runtime(t)
	agentURN := graph.URN("urn:moos:agent:claude")
	sessionA := graph.URN("urn:moos:session:sam.a")
	sessionB := graph.URN("urn:moos:session:sam.b")
	injectOccupancy(rt, sessionA, agentURN, "agent")
	injectOccupancy(rt, sessionB, agentURN, "agent")

	env := graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       agentURN,
		NodeURN:     "urn:moos:program:x",
		TypeID:      "program",
	}
	if _, err := rt.Apply(env); err == nil {
		t.Fatalf("ambiguous liveness should reject; got nil error")
	}
	if got := readLocalT(t, rt, sessionA); got != 0 {
		t.Errorf("sessionA.local_t = %d, want 0 (rejected envelope must not tick)", got)
	}
	if got := readLocalT(t, rt, sessionB); got != 0 {
		t.Errorf("sessionB.local_t = %d, want 0 (rejected envelope must not tick)", got)
	}
}

// TestApply_NoBump_KernelActor — kernel-actor system-internal allowlist
// emissions are not session-scoped; no session should tick. Verifies the
// resolveSessionForBump returns ok=false on Absent.
func TestApply_NoBump_KernelActor(t *testing.T) {
	rt := newM13Runtime(t)
	sessionURN := graph.URN("urn:moos:session:sam.solo")
	injectOccupancy(rt, sessionURN, "urn:moos:agent:claude", "agent")

	// Kernel-actor envelope, NOT seated to any session. Allowlist path.
	env := graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:kernel:hp-z440.primary",
		NodeURN:     "urn:moos:workstation:hp-z440",
		TypeID:      "workstation", // infrastructure type → allowlist
	}
	if _, err := rt.Apply(env); err != nil {
		t.Fatalf("kernel-actor allowlist envelope should pass; got %v", err)
	}
	if got := readLocalT(t, rt, sessionURN); got != 0 {
		t.Errorf("session.local_t = %d, want 0 (kernel-actor envelope is not session-scoped)", got)
	}
}

// TestApplyProgram_DedupesBySessionNotActor — atomic batch where two
// different actors both occupy the SAME session. Bump should fire once
// for the session, not twice. Dedupe key is the resolved session URN.
func TestApplyProgram_DedupesBySessionNotActor(t *testing.T) {
	rt := newM13Runtime(t)
	sessionURN := graph.URN("urn:moos:session:sam.shared")
	agentA := graph.URN("urn:moos:agent:alice")
	agentB := graph.URN("urn:moos:agent:bob")
	injectOccupancy(rt, sessionURN, agentA, "agent")
	injectOccupancy(rt, sessionURN, agentB, "agent")

	envs := []graph.Envelope{
		{RewriteType: graph.ADD, Actor: agentA, NodeURN: "urn:moos:program:x1", TypeID: "program"},
		{RewriteType: graph.ADD, Actor: agentB, NodeURN: "urn:moos:program:x2", TypeID: "program"},
	}
	if _, err := rt.ApplyProgram(envs); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}
	if got := readLocalT(t, rt, sessionURN); got != 1 {
		t.Errorf("session.local_t = %d, want 1 (one tick per session per batch, not per actor)", got)
	}
}

// TestApply_BumpsLocalT_RotatesOccupancy — regression test for the Copilot-
// flagged correctness bug on PR #33. An UNLINK envelope that removes the
// only has-occupant edge between actor and session must still tick the
// originating session's local_t. This works only because session resolution
// is captured against the PRE-apply state (matching §M11 preflight). If
// resolution ran against post-apply state, the resolver would return
// Absent (the edge is gone) and local_t would not tick — silently dropping
// the heartbeat for the very envelope that closed the seat.
func TestApply_BumpsLocalT_RotatesOccupancy(t *testing.T) {
	rt := newM13Runtime(t)
	sessionURN := graph.URN("urn:moos:session:sam.solo")
	agentURN := graph.URN("urn:moos:agent:claude")
	injectOccupancy(rt, sessionURN, agentURN, "agent")

	// Capture the relation URN that injectOccupancy created so we can
	// UNLINK it. injectOccupancy writes a deterministic URN.
	relURN := graph.URN("urn:moos:rel:" + string(sessionURN) + ".has-occupant." + string(agentURN))

	// Sanity check the pre-state has the relation.
	if _, ok := rt.state.Relations[relURN]; !ok {
		t.Fatalf("pre-state missing has-occupant relation %s", relURN)
	}

	// UNLINK the has-occupant edge — actor's only seat — emitted by the
	// agent itself. §M11 passes against pre-state (one session occupied);
	// post-state has zero has-occupant relations.
	env := graph.Envelope{
		RewriteType:     graph.UNLINK,
		Actor:           agentURN,
		RelationURN:     relURN,
		RewriteCategory: graph.WF19,
	}
	if _, err := rt.Apply(env); err != nil {
		t.Fatalf("Apply UNLINK: %v", err)
	}

	// Post-apply: relation gone (the rotation), but local_t must still
	// have ticked because resolution captured pre-state.
	if _, ok := rt.state.Relations[relURN]; ok {
		t.Errorf("post-state should not contain has-occupant relation; UNLINK didn't apply")
	}
	if got := readLocalT(t, rt, sessionURN); got != 1 {
		t.Errorf("session.local_t = %d, want 1 — rotation envelope should still tick the originating session", got)
	}
}

// TestApplyProgram_DedupesAcrossAgents — atomic batch where two agents
// each occupy their own session. Both sessions tick once.
func TestApplyProgram_DedupesAcrossAgents(t *testing.T) {
	rt := newM13Runtime(t)
	sessionA := graph.URN("urn:moos:session:sam.a")
	sessionB := graph.URN("urn:moos:session:sam.b")
	agentA := graph.URN("urn:moos:agent:alice")
	agentB := graph.URN("urn:moos:agent:bob")
	injectOccupancy(rt, sessionA, agentA, "agent")
	injectOccupancy(rt, sessionB, agentB, "agent")

	envs := []graph.Envelope{
		{RewriteType: graph.ADD, Actor: agentA, NodeURN: "urn:moos:program:x1", TypeID: "program"},
		{RewriteType: graph.ADD, Actor: agentB, NodeURN: "urn:moos:program:x2", TypeID: "program"},
	}
	if _, err := rt.ApplyProgram(envs); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}
	if got := readLocalT(t, rt, sessionA); got != 1 {
		t.Errorf("sessionA.local_t = %d, want 1", got)
	}
	if got := readLocalT(t, rt, sessionB); got != 1 {
		t.Errorf("sessionB.local_t = %d, want 1", got)
	}
}
