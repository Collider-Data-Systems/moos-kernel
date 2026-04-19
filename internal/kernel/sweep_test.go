package kernel

import (
	"strings"
	"testing"
	"time"

	"moos/kernel/internal/graph"
)

// sweepState returns a graph state with:
//   - urn:moos:program:sam.prog  (status=active)
//   - urn:moos:t_hook:sam.hook-a (fires_at=200, firing_state absent = pending)
//   - urn:moos:t_hook:sam.hook-b (fires_at=300, firing_state absent)
//   - urn:moos:t_hook:sam.hook-c (fires_at=180, firing_state="proposed" — should be skipped by the v3.11 idempotency check)
func sweepState() graph.GraphState {
	return graph.GraphState{
		Nodes: map[graph.URN]graph.Node{
			"urn:moos:program:sam.prog": {
				URN:    "urn:moos:program:sam.prog",
				TypeID: "program",
				Properties: map[string]graph.Property{
					"status": {Value: "active", Mutability: "mutable"},
				},
			},
			"urn:moos:t_hook:sam.hook-a": {
				URN:    "urn:moos:t_hook:sam.hook-a",
				TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn": {Value: "urn:moos:program:sam.prog", Mutability: "immutable"},
					"predicate": {Value: map[string]any{"kind": "fires_at", "t": 200}, Mutability: "immutable"},
					"react_template": {
						Value:      map[string]any{"rewrite_type": "MUTATE", "target_urn": "urn:moos:program:sam.prog", "field": "status", "new_value": "done"},
						Mutability: "mutable",
					},
				},
			},
			"urn:moos:t_hook:sam.hook-b": {
				URN:    "urn:moos:t_hook:sam.hook-b",
				TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn": {Value: "urn:moos:program:sam.prog", Mutability: "immutable"},
					"predicate": {Value: map[string]any{"kind": "fires_at", "t": 300}, Mutability: "immutable"},
				},
			},
			"urn:moos:t_hook:sam.hook-c": {
				URN:    "urn:moos:t_hook:sam.hook-c",
				TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn":    {Value: "urn:moos:program:sam.prog", Mutability: "immutable"},
					"predicate":    {Value: map[string]any{"kind": "fires_at", "t": 180}, Mutability: "immutable"},
					"firing_state": {Value: "proposed", Mutability: "mutable", AuthorityScope: "kernel"},
				},
			},
			// The old-style paired governance_proposal stays in the fixture.
			// Under v3.11 semantics it's ignored by the sweep's idempotency
			// check — which now looks at firing_state on the hook itself —
			// but we keep it so the TestSweepOnce_AllFire case still has a
			// realistic "hook already processed" shape in state.
			"urn:moos:proposal:kernel.hook-c-t180-seq0": {
				URN:    "urn:moos:proposal:kernel.hook-c-t180-seq0",
				TypeID: "governance_proposal",
				Properties: map[string]graph.Property{
					"title":             {Value: "Fire t_hook urn:moos:t_hook:sam.hook-c at T=180", Mutability: "immutable"},
					"source_t_hook_urn": {Value: "urn:moos:t_hook:sam.hook-c", Mutability: "immutable"},
					"fires_at_t":        {Value: 180, Mutability: "immutable"},
				},
			},
		},
		Relations: map[graph.URN]graph.Relation{},
	}
}

// findProposalFor returns the first ADD governance_proposal envelope
// whose source_t_hook_urn matches hookURN, plus the paired MUTATE
// firing_state envelope that should follow it. Helper for the post-v3.11
// sweep tests where each firing hook produces two envelopes.
func findProposalFor(envs []graph.Envelope, hookURN string) (add, mut *graph.Envelope) {
	for i := range envs {
		e := &envs[i]
		if e.RewriteType == graph.ADD && e.TypeID == "governance_proposal" {
			if s, _ := e.Properties["source_t_hook_urn"].Value.(string); s == hookURN {
				add = e
				// The MUTATE pairing is the next envelope (emitted together).
				if i+1 < len(envs) && envs[i+1].RewriteType == graph.MUTATE && string(envs[i+1].TargetURN) == hookURN && envs[i+1].Field == "firing_state" {
					mut = &envs[i+1]
				}
				return
			}
		}
	}
	return nil, nil
}

// TestSweepOnce_FiresMaturedHook — at T=250, hook-a (fires_at=200) fires
// and produces a pair: (ADD governance_proposal, MUTATE firing_state).
// hook-b (fires_at=300) doesn't fire. hook-c has firing_state=proposed so
// the v3.11 idempotency check skips it.
func TestSweepOnce_FiresMaturedHook(t *testing.T) {
	state := sweepState()
	actor := graph.URN("urn:moos:kernel:test.sweep")
	envelopes := SweepOnce(state, 250, actor, 0, time.Unix(1700000000, 0))

	// v3.11: one firing → two envelopes (ADD proposal + MUTATE firing_state).
	if len(envelopes) != 2 {
		t.Fatalf("expected exactly 2 envelopes (ADD proposal + MUTATE firing_state for hook-a); got %d:\n%v", len(envelopes), envelopes)
	}
	add, mut := findProposalFor(envelopes, "urn:moos:t_hook:sam.hook-a")
	if add == nil || mut == nil {
		t.Fatalf("expected hook-a ADD + MUTATE pair; got envelopes=%v", envelopes)
	}
	if add.TypeID != "governance_proposal" {
		t.Errorf("expected governance_proposal, got %s", add.TypeID)
	}
	if add.Actor != actor {
		t.Errorf("expected actor %s on ADD, got %s", actor, add.Actor)
	}
	if mut.Actor != actor {
		t.Errorf("expected actor %s on MUTATE, got %s", actor, mut.Actor)
	}
	// fires_at_t should be 250 (the T we evaluated at).
	if fireT, _ := add.Properties["fires_at_t"].Value.(int); fireT != 250 {
		t.Errorf("expected fires_at_t=250, got %v", add.Properties["fires_at_t"].Value)
	}
	// MUTATE transitions firing_state → proposed on the source hook.
	if mut.Field != "firing_state" {
		t.Errorf("expected MUTATE field=firing_state, got %s", mut.Field)
	}
	if mut.NewValue != "proposed" {
		t.Errorf("expected MUTATE new_value=proposed, got %v", mut.NewValue)
	}
	// URN pattern check (urn:moos:proposal:<user>.<slug>).
	if !strings.HasPrefix(string(add.NodeURN), "urn:moos:proposal:") {
		t.Errorf("expected node_urn to start with urn:moos:proposal:, got %s", add.NodeURN)
	}
	// Must mention hook slug for traceability.
	if !strings.Contains(string(add.NodeURN), "hook-a") {
		t.Errorf("expected node_urn to contain hook-a slug, got %s", add.NodeURN)
	}
}

// TestSweepOnce_IdempotencyViaFiringState — v3.11 idempotency. hook-c has
// firing_state="proposed" so the sweep must NOT re-propose it even though
// its predicate (fires_at=180) is satisfied at T=200.
func TestSweepOnce_IdempotencyViaFiringState(t *testing.T) {
	state := sweepState()
	// Use T=200 so hook-a fires freshly; hook-c has firing_state=proposed.
	envelopes := SweepOnce(state, 200, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))

	for _, env := range envelopes {
		if s, _ := env.Properties["source_t_hook_urn"].Value.(string); s == "urn:moos:t_hook:sam.hook-c" {
			t.Errorf("expected hook-c to be skipped (firing_state=proposed); got envelope %+v", env)
		}
		if env.RewriteType == graph.MUTATE && env.TargetURN == "urn:moos:t_hook:sam.hook-c" {
			t.Errorf("expected no MUTATE on hook-c (already proposed); got %+v", env)
		}
	}
}

// TestSweepOnce_FiringStateTransitionsPendingToProposed — the source
// hook starts with firing_state absent (= pending) and the sweep's MUTATE
// envelope sets it to proposed. Locks in the state-machine transition.
func TestSweepOnce_FiringStateTransitionsPendingToProposed(t *testing.T) {
	state := sweepState()
	envelopes := SweepOnce(state, 250, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))

	// Expect a MUTATE on hook-a.firing_state with new_value="proposed".
	var found bool
	for _, env := range envelopes {
		if env.RewriteType == graph.MUTATE &&
			env.TargetURN == "urn:moos:t_hook:sam.hook-a" &&
			env.Field == "firing_state" &&
			env.NewValue == "proposed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected MUTATE hook-a.firing_state → proposed; envelopes=%v", envelopes)
	}
}

// TestSweepOnce_NoFiresNoEnvelopes — at T=0 no hook has matured, so the
// sweep returns an empty envelope slice.
func TestSweepOnce_NoFiresNoEnvelopes(t *testing.T) {
	state := sweepState()
	envelopes := SweepOnce(state, 0, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))

	if len(envelopes) != 0 {
		t.Errorf("expected 0 envelopes at T=0; got %d:\n%v", len(envelopes), envelopes)
	}
}

// TestSweepOnce_AllFire — at T=1000 hook-a and hook-b mature and fire.
// hook-c is skipped via firing_state=proposed. Four envelopes expected
// total (two per firing hook).
func TestSweepOnce_AllFire(t *testing.T) {
	state := sweepState()
	envelopes := SweepOnce(state, 1000, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))

	hooks := map[string]bool{}
	for _, env := range envelopes {
		if env.RewriteType == graph.ADD {
			s, _ := env.Properties["source_t_hook_urn"].Value.(string)
			hooks[s] = true
		}
	}

	if !hooks["urn:moos:t_hook:sam.hook-a"] {
		t.Errorf("expected proposal for hook-a at T=1000")
	}
	if !hooks["urn:moos:t_hook:sam.hook-b"] {
		t.Errorf("expected proposal for hook-b at T=1000")
	}
	if hooks["urn:moos:t_hook:sam.hook-c"] {
		t.Errorf("hook-c must be skipped (firing_state=proposed)")
	}
	// 2 firing hooks × 2 envelopes each = 4.
	if len(envelopes) != 4 {
		t.Errorf("expected 4 envelopes (2 hooks × (ADD+MUTATE)), got %d", len(envelopes))
	}
}

// TestSweepOnce_ProposalCarriesReactTemplate — the proposal envelope
// carries the source hook's react_template so approvers can see exactly
// what rewrite would apply if they approve.
func TestSweepOnce_ProposalCarriesReactTemplate(t *testing.T) {
	state := sweepState()
	envelopes := SweepOnce(state, 250, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))

	if len(envelopes) == 0 {
		t.Fatalf("expected at least one envelope")
	}
	env := envelopes[0]
	rtProp, has := env.Properties["proposed_envelope"]
	if !has {
		t.Fatalf("expected proposed_envelope property")
	}
	tmpl, ok := rtProp.Value.(map[string]any)
	if !ok {
		t.Fatalf("expected proposed_envelope to be a map[string]any, got %T", rtProp.Value)
	}
	if tmpl["rewrite_type"] != "MUTATE" {
		t.Errorf("expected proposed_envelope.rewrite_type=MUTATE, got %v", tmpl["rewrite_type"])
	}
	if tmpl["target_urn"] != "urn:moos:program:sam.prog" {
		t.Errorf("expected proposed_envelope.target_urn=prog, got %v", tmpl["target_urn"])
	}
}

// TestSweepOnce_SkipsHooksWithoutPredicate — a t_hook node missing the
// `predicate` property is silently skipped (no envelope, no error).
func TestSweepOnce_SkipsHooksWithoutPredicate(t *testing.T) {
	state := graph.GraphState{
		Nodes: map[graph.URN]graph.Node{
			"urn:moos:t_hook:sam.naked": {
				URN:    "urn:moos:t_hook:sam.naked",
				TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn": {Value: "urn:moos:program:sam.x", Mutability: "immutable"},
				},
			},
		},
		Relations: map[graph.URN]graph.Relation{},
	}

	envelopes := SweepOnce(state, 1000, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))
	if len(envelopes) != 0 {
		t.Errorf("expected 0 envelopes (hook has no predicate); got %d", len(envelopes))
	}
}

// TestSweepOnce_HandlesCompoundPredicate — a t_hook with an all_of
// compound predicate fires only when every sub-predicate holds. Mirrors
// the round-8 v310-delivery.startable hook shape.
func TestSweepOnce_HandlesCompoundPredicate(t *testing.T) {
	state := graph.GraphState{
		Nodes: map[graph.URN]graph.Node{
			"urn:moos:program:sam.anchor": {
				URN:    "urn:moos:program:sam.anchor",
				TypeID: "program",
				Properties: map[string]graph.Property{
					"status": {Value: "active", Mutability: "mutable"},
				},
			},
			"urn:moos:t_hook:sam.compound": {
				URN:    "urn:moos:t_hook:sam.compound",
				TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn": {Value: "urn:moos:program:sam.downstream", Mutability: "immutable"},
					"predicate": {
						Value: map[string]any{
							"kind": "all_of",
							"predicates": []any{
								map[string]any{"kind": "fires_at", "t": 220},
								map[string]any{
									"kind":  "after_urn",
									"urn":   "urn:moos:program:sam.anchor",
									"prop":  "status",
									"value": "completed",
								},
							},
						},
						Mutability: "immutable",
					},
				},
			},
		},
		Relations: map[graph.URN]graph.Relation{},
	}

	// At T=220 with anchor still active → compound is false → no fire.
	if envelopes := SweepOnce(state, 220, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0)); len(envelopes) != 0 {
		t.Errorf("compound predicate should be false (anchor.status=active != completed); got %d envelopes", len(envelopes))
	}

	// Flip anchor to completed; both clauses now true → compound fires.
	anchor := state.Nodes["urn:moos:program:sam.anchor"]
	anchor.Properties["status"] = graph.Property{Value: "completed", Mutability: "mutable"}
	state.Nodes["urn:moos:program:sam.anchor"] = anchor

	envelopes := SweepOnce(state, 220, "urn:moos:kernel:test.sweep", 0, time.Unix(1700000000, 0))
	// 1 firing hook × 2 envelopes (ADD + MUTATE firing_state) = 2.
	if len(envelopes) != 2 {
		t.Fatalf("compound predicate satisfied; expected 2 envelopes (ADD+MUTATE), got %d", len(envelopes))
	}
	add, mut := findProposalFor(envelopes, "urn:moos:t_hook:sam.compound")
	if add == nil {
		t.Fatalf("expected ADD proposal for compound hook, got %v", envelopes)
	}
	if mut == nil {
		t.Errorf("expected paired firing_state MUTATE for compound hook")
	}
}

// TestSweepOnce_UniqueProposalURNsAcrossHooks — when two hooks fire in
// the same tick they must get distinct proposal URNs. Uses logSeq offset
// inside the URN so ApplyProgram's all-or-nothing atomicity doesn't see
// duplicate ADDs.
func TestSweepOnce_UniqueProposalURNsAcrossHooks(t *testing.T) {
	state := graph.GraphState{
		Nodes: map[graph.URN]graph.Node{
			"urn:moos:program:sam.p": {
				URN: "urn:moos:program:sam.p", TypeID: "program",
				Properties: map[string]graph.Property{
					"status": {Value: "active", Mutability: "mutable"},
				},
			},
			"urn:moos:t_hook:sam.h1": {
				URN: "urn:moos:t_hook:sam.h1", TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn": {Value: "urn:moos:program:sam.p", Mutability: "immutable"},
					"predicate": {Value: map[string]any{"kind": "fires_at", "t": 100}, Mutability: "immutable"},
				},
			},
			"urn:moos:t_hook:sam.h2": {
				URN: "urn:moos:t_hook:sam.h2", TypeID: "t_hook",
				Properties: map[string]graph.Property{
					"owner_urn": {Value: "urn:moos:program:sam.p", Mutability: "immutable"},
					"predicate": {Value: map[string]any{"kind": "fires_at", "t": 100}, Mutability: "immutable"},
				},
			},
		},
		Relations: map[graph.URN]graph.Relation{},
	}

	envelopes := SweepOnce(state, 200, "urn:moos:kernel:test.sweep", 100, time.Unix(1700000000, 0))
	// 2 matured hooks × 2 envelopes each (ADD + MUTATE) = 4.
	if len(envelopes) != 4 {
		t.Fatalf("expected 4 envelopes for 2 matured hooks (ADD+MUTATE pairs); got %d", len(envelopes))
	}
	// Collect ADD proposals and verify URNs are distinct.
	var addURNs []graph.URN
	for _, env := range envelopes {
		if env.RewriteType == graph.ADD {
			addURNs = append(addURNs, env.NodeURN)
		}
	}
	if len(addURNs) != 2 {
		t.Fatalf("expected 2 ADD proposals; got %d", len(addURNs))
	}
	if addURNs[0] == addURNs[1] {
		t.Errorf("expected distinct proposal URNs; got duplicates: %s", addURNs[0])
	}
}
