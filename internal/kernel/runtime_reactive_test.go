package kernel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"moos/kernel/internal/graph"
	"moos/kernel/internal/hdc"
)

// TestRuntime_ReactiveChain verifies that Apply triggers the reactive engine
// and applies reactor proposals when a matching watcher+reactor is active.
func TestRuntime_ReactiveChain(t *testing.T) {
	// nil registry: validate() returns nil for all calls — pure structural test.
	rt := &Runtime{
		state:       graph.NewGraphState(),
		store:       NewMemStore(),
		registry:    nil,
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}

	now := time.Now().UTC()

	// Seed the graph directly via SeedIfAbsent to set up watcher, reactor, KI.
	seeds := []graph.Envelope{
		// A knowledge_item to be triggered on.
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:ki:test-item",
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"status": {Value: "raw", Mutability: "mutable"},
				"title":  {Value: "Test KI", Mutability: "immutable"},
			},
		},
		// Watcher: fires on MUTATE of knowledge_item.
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:watcher:test-watch",
			TypeID:      "watcher",
			Properties: map[string]graph.Property{
				"name":               {Value: "test-watch", Mutability: "immutable"},
				"created_at":         {Value: now.Format(time.RFC3339), Mutability: "immutable"},
				"match_rewrite_type": {Value: "MUTATE", Mutability: "mutable"},
				"match_type_id":      {Value: "knowledge_item", Mutability: "mutable"},
				"status":             {Value: "active", Mutability: "mutable"},
			},
		},
		// Reactor: emits MUTATE → status = "claim-pending".
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:reactor:test-react",
			TypeID:      "reactor",
			Properties: map[string]graph.Property{
				"name":        {Value: "test-react", Mutability: "immutable"},
				"created_at":  {Value: now.Format(time.RFC3339), Mutability: "immutable"},
				"action_type": {Value: "rewrite", Mutability: "immutable"},
				"status":      {Value: "active", Mutability: "mutable"},
				"template": {
					Value: map[string]any{
						"rewrite_type": "MUTATE",
						"actor":        "$actor",
						"target_urn":   "$matched_urn",
						"field":        "status",
						"new_value":    "claim-pending",
					},
					Mutability: "mutable",
				},
			},
		},
		// LINK: watcher triggers reactor (WF17).
		{
			RewriteType:     graph.LINK,
			Actor:           "urn:moos:kernel",
			RelationURN:     "urn:moos:rel:watch.triggers.react",
			RewriteCategory: "WF17",
			SrcURN:          "urn:moos:watcher:test-watch",
			SrcPort:         "triggers",
			TgtURN:          "urn:moos:reactor:test-react",
			TgtPort:         "triggered-by",
		},
	}
	for _, env := range seeds {
		if err := rt.SeedIfAbsent(env); err != nil {
			t.Fatalf("seed failed: %v", err)
		}
	}

	logBefore := rt.LogLen()

	// Fire the MUTATE — should trigger watcher → reactor → reactive MUTATE.
	_, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:test-item",
		Field:       "status",
		NewValue:    "raw",
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	logAfter := rt.LogLen()
	if logAfter != logBefore+2 {
		t.Errorf("expected log to grow by 2 (trigger + reactive), got %d → %d", logBefore, logAfter)
	}

	// Verify the KI status was changed by the reactor.
	ki, ok := rt.Node("urn:moos:ki:test-item")
	if !ok {
		t.Fatal("KI not found after apply")
	}
	status, _ := ki.Properties["status"].Value.(string)
	if status != "claim-pending" {
		t.Errorf("expected KI status=claim-pending, got %q", status)
	}
}

// newTHookTestRuntime seeds a runtime with a knowledge_item and an active
// t_hook on it whose react_template would MUTATE the owner's status to
// "hook-fired" on any MUTATE. Shared by the M6 staging tests.
func newTHookTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := &Runtime{
		state:       graph.NewGraphState(),
		store:       NewMemStore(),
		registry:    nil,
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}

	now := time.Now().UTC()

	seeds := []graph.Envelope{
		// Target node that the t_hook watches.
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:ki:hook-target",
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"status": {Value: "raw", Mutability: "mutable"},
				"title":  {Value: "Hook Target", Mutability: "immutable"},
			},
		},
		// T-hook: matches any MUTATE of the owner node.
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:t_hook:hook-target.on-mutate",
			TypeID:      "t_hook",
			Properties: map[string]graph.Property{
				"owner_urn":  {Value: "urn:moos:ki:hook-target", Mutability: "immutable"},
				"status":     {Value: "active", Mutability: "mutable"},
				"created_at": {Value: now.Format(time.RFC3339), Mutability: "immutable"},
				"event_shape": {
					Value:      map[string]any{"rewrite_type": "MUTATE"},
					Mutability: "mutable",
				},
				"react_template": {
					Value: map[string]any{
						"rewrite_type": "MUTATE",
						"actor":        "$actor",
						"target_urn":   "$matched_urn",
						"field":        "status",
						"new_value":    "hook-fired",
					},
					Mutability: "mutable",
				},
			},
		},
	}
	for _, env := range seeds {
		if err := rt.SeedIfAbsent(env); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return rt
}

// TestRuntime_THookStagesProposal verifies the governed M6 event pathway
// (issue #53, parity with the TIME sweep): a matching MUTATE makes the
// t_hook STAGE a governance_proposal — it does NOT apply the react_template.
func TestRuntime_THookStagesProposal(t *testing.T) {
	rt := newTHookTestRuntime(t)
	logBefore := rt.LogLen()

	// Trigger: MUTATE the owner node.
	_, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:hook-target",
		Field:       "status",
		NewValue:    "intermediate",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Log grows by 3: trigger MUTATE + proposal ADD + firing_state MUTATE.
	if got := rt.LogLen(); got != logBefore+3 {
		t.Errorf("expected log +3 (trigger + proposal ADD + firing_state MUTATE), got %d → %d", logBefore, got)
	}

	// The react_template must NOT have applied: status stays what the
	// trigger set, never "hook-fired".
	ki, ok := rt.Node("urn:moos:ki:hook-target")
	if !ok {
		t.Fatal("KI not found after Apply")
	}
	if status, _ := ki.Properties["status"].Value.(string); status != "intermediate" {
		t.Errorf("expected status=intermediate (template staged, not applied), got %q", status)
	}

	// Exactly one governance_proposal exists, carrying the SUBSTITUTED
	// template and the source hook URN.
	state := rt.State()
	proposals := state.NodesOfType("governance_proposal")
	if len(proposals) != 1 {
		t.Fatalf("expected exactly 1 governance_proposal, got %d", len(proposals))
	}
	prop := state.Nodes[proposals[0]]
	if src, _ := prop.Properties["source_t_hook_urn"].Value.(string); src != "urn:moos:t_hook:hook-target.on-mutate" {
		t.Errorf("source_t_hook_urn = %q, want the firing hook", src)
	}
	pe, ok := prop.Properties["proposed_envelope"]
	if !ok {
		t.Fatal("proposal missing proposed_envelope")
	}
	// CI-4 Go-value parity: the live value must be the JSON map form (what
	// replay reconstructs), never a typed graph.Envelope struct.
	if _, isMap := pe.Value.(map[string]any); !isMap {
		t.Errorf("proposed_envelope live value is %T, want map[string]any (replay parity)", pe.Value)
	}
	// The event pathway stages the substituted envelope — $matched_urn and
	// $actor must be resolved to the trigger's values.
	raw, err := json.Marshal(pe.Value)
	if err != nil {
		t.Fatalf("marshal proposed_envelope: %v", err)
	}
	var staged graph.Envelope
	if err := json.Unmarshal(raw, &staged); err != nil {
		t.Fatalf("unmarshal proposed_envelope: %v", err)
	}
	if staged.TargetURN != "urn:moos:ki:hook-target" {
		t.Errorf("staged target_urn = %q, want substituted urn:moos:ki:hook-target", staged.TargetURN)
	}
	if staged.Actor != "urn:moos:user:sam" {
		t.Errorf("staged actor = %q, want substituted urn:moos:user:sam", staged.Actor)
	}
	if staged.NewValue != "hook-fired" {
		t.Errorf("staged new_value = %v, want hook-fired", staged.NewValue)
	}

	// The hook parked at firing_state=proposed.
	hook, ok := rt.Node("urn:moos:t_hook:hook-target.on-mutate")
	if !ok {
		t.Fatal("t_hook not found after Apply")
	}
	if fs := firingStateOf(hook); fs != "proposed" {
		t.Errorf("hook firing_state = %q, want proposed", fs)
	}
}

// TestRuntime_THookExplicitPendingStages verifies staging works when the
// hook was ADDed with an EXPLICIT firing_state="pending" property — the
// standard (non-additive) fold MUTATE path. Adversarial-review finding:
// routing the staging MUTATE through ValidateMUTATE rejected this case
// (empty rewrite_category) and produced one duplicate proposal per trigger.
func TestRuntime_THookExplicitPendingStages(t *testing.T) {
	rt := newTHookTestRuntime(t)

	// Second hook with firing_state explicitly authored.
	if err := rt.SeedIfAbsent(graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:kernel",
		NodeURN:     "urn:moos:t_hook:hook-target.explicit-pending",
		TypeID:      "t_hook",
		Properties: map[string]graph.Property{
			"owner_urn":    {Value: "urn:moos:ki:hook-target", Mutability: "immutable"},
			"status":       {Value: "active", Mutability: "mutable"},
			"created_at":   {Value: time.Now().UTC().Format(time.RFC3339), Mutability: "immutable"},
			"firing_state": {Value: "pending", Mutability: "mutable", AuthorityScope: "kernel"},
			"event_shape": {
				Value:      map[string]any{"rewrite_type": "MUTATE"},
				Mutability: "mutable",
			},
			"react_template": {
				Value: map[string]any{
					"rewrite_type": "MUTATE",
					"actor":        "$actor",
					"target_urn":   "$matched_urn",
					"field":        "status",
					"new_value":    "hook-fired",
				},
				Mutability: "mutable",
			},
		},
	}); err != nil {
		t.Fatalf("seed explicit-pending hook: %v", err)
	}

	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:hook-target",
		Field:       "status",
		NewValue:    "intermediate",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// BOTH hooks (implicit + explicit pending) stage: log = trigger + 2×(ADD+MUTATE).
	hook, ok := rt.Node("urn:moos:t_hook:hook-target.explicit-pending")
	if !ok {
		t.Fatal("explicit-pending hook not found")
	}
	if fs := firingStateOf(hook); fs != "proposed" {
		t.Errorf("explicit-pending hook firing_state = %q, want proposed (standard-path MUTATE must land)", fs)
	}
	state := rt.State()
	if n := len(state.NodesOfType("governance_proposal")); n != 2 {
		t.Errorf("expected 2 governance_proposals (one per hook), got %d", n)
	}
}

// TestRuntime_THookGateBlocksStaging verifies M8 fail-closed parity with the
// sweep: a gate node with a failing predicate guarding the t_hook blocks the
// ENTIRE staging pair — no proposal ADD, no firing_state transition.
func TestRuntime_THookGateBlocksStaging(t *testing.T) {
	rt := newTHookTestRuntime(t)

	// Gate with a failing predicate (node_property on a value that doesn't match),
	// LINKed gate —guards→ t_hook.
	seeds := []graph.Envelope{
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:gate:freeze-hook",
			TypeID:      "gate",
			Properties: map[string]graph.Property{
				"predicate_type": {Value: "node_property", Mutability: "immutable"},
				"target_urn":     {Value: "urn:moos:ki:hook-target", Mutability: "immutable"},
				"field":          {Value: "status", Mutability: "immutable"},
				"expected_value": {Value: "never-this-value", Mutability: "immutable"},
			},
		},
		{
			RewriteType: graph.LINK,
			Actor:       "urn:moos:kernel",
			RelationURN: "urn:moos:rel:gate.freeze-hook.guards",
			SrcURN:      "urn:moos:gate:freeze-hook",
			SrcPort:     "guards",
			TgtURN:      "urn:moos:t_hook:hook-target.on-mutate",
			TgtPort:     "guarded-by",
		},
	}
	for _, env := range seeds {
		if err := rt.SeedIfAbsent(env); err != nil {
			t.Fatalf("seed gate: %v", err)
		}
	}

	logBefore := rt.LogLen()
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:hook-target",
		Field:       "status",
		NewValue:    "intermediate",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Only the trigger lands: the frozen hook stages NOTHING.
	if got := rt.LogLen(); got != logBefore+1 {
		t.Errorf("expected log +1 (gate blocks staging pair fail-closed), got %d → %d", logBefore, got)
	}
	state := rt.State()
	if n := len(state.NodesOfType("governance_proposal")); n != 0 {
		t.Errorf("expected 0 governance_proposals (gate frozen), got %d", n)
	}
	hook, _ := rt.Node("urn:moos:t_hook:hook-target.on-mutate")
	if fs := firingStateOf(hook); fs != "" {
		t.Errorf("hook firing_state = %q, want unset (staging blocked)", fs)
	}
}

// TestRuntime_THookSameSlugNoCollision verifies the per-firing URN
// discriminator: two hooks on one owner whose URNs share the final slug
// segment both stage on a single trigger (adversarial-review finding: the
// URN collided and the loser was silently dropped with no retry clock).
func TestRuntime_THookSameSlugNoCollision(t *testing.T) {
	rt := newTHookTestRuntime(t)

	// The fixture hook is urn:moos:t_hook:hook-target.on-mutate — add a
	// second whose final colon-delimited segment is IDENTICAL.
	if err := rt.SeedIfAbsent(graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:kernel",
		NodeURN:     "urn:moos:t_hook:other:hook-target.on-mutate",
		TypeID:      "t_hook",
		Properties: map[string]graph.Property{
			"owner_urn":  {Value: "urn:moos:ki:hook-target", Mutability: "immutable"},
			"status":     {Value: "active", Mutability: "mutable"},
			"created_at": {Value: time.Now().UTC().Format(time.RFC3339), Mutability: "immutable"},
			"event_shape": {
				Value:      map[string]any{"rewrite_type": "MUTATE"},
				Mutability: "mutable",
			},
			"react_template": {
				Value: map[string]any{
					"rewrite_type": "MUTATE",
					"actor":        "$actor",
					"target_urn":   "$matched_urn",
					"field":        "status",
					"new_value":    "hook-fired-2",
				},
				Mutability: "mutable",
			},
		},
	}); err != nil {
		t.Fatalf("seed same-slug hook: %v", err)
	}

	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:hook-target",
		Field:       "status",
		NewValue:    "intermediate",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	state := rt.State()
	if n := len(state.NodesOfType("governance_proposal")); n != 2 {
		t.Fatalf("expected 2 governance_proposals (no URN collision), got %d", n)
	}
	for _, hookURN := range []string{"urn:moos:t_hook:hook-target.on-mutate", "urn:moos:t_hook:other:hook-target.on-mutate"} {
		hook, ok := rt.Node(graph.URN(hookURN))
		if !ok {
			t.Fatalf("hook %s not found", hookURN)
		}
		if fs := firingStateOf(hook); fs != "proposed" {
			t.Errorf("hook %s firing_state = %q, want proposed", hookURN, fs)
		}
	}
}

// TestRuntime_THookEventIdempotency verifies the firing_state filter on the
// event pathway (parity with SweepOnce): once a hook is proposed, further
// matching triggers do NOT stage duplicate proposals.
func TestRuntime_THookEventIdempotency(t *testing.T) {
	rt := newTHookTestRuntime(t)

	// First trigger: stages (log +3), parks the hook at proposed.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:hook-target",
		Field:       "status",
		NewValue:    "intermediate",
	}); err != nil {
		t.Fatalf("Apply 1: %v", err)
	}

	logBefore := rt.LogLen()

	// Second matching trigger: hook is proposed — only the trigger lands.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:hook-target",
		Field:       "status",
		NewValue:    "final",
	}); err != nil {
		t.Fatalf("Apply 2: %v", err)
	}

	if got := rt.LogLen(); got != logBefore+1 {
		t.Errorf("expected log +1 (trigger only, no re-staging), got %d → %d", logBefore, got)
	}
	state := rt.State()
	if n := len(state.NodesOfType("governance_proposal")); n != 1 {
		t.Errorf("expected 1 governance_proposal after second trigger, got %d", n)
	}
}

// TestRuntime_THookEventShapeFilter verifies that a t_hook with an event_shape
// filter does NOT fire on non-matching rewrites (LINK ≠ MUTATE).
func TestRuntime_THookEventShapeFilter(t *testing.T) {
	rt := &Runtime{
		state:       graph.NewGraphState(),
		store:       NewMemStore(),
		registry:    nil,
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}

	now := time.Now().UTC()

	seeds := []graph.Envelope{
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:ki:filter-target",
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"status": {Value: "raw", Mutability: "mutable"},
				"title":  {Value: "Filter Target", Mutability: "immutable"},
			},
		},
		// T-hook: fires ONLY on LINK rewrites (not MUTATE).
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:t_hook:filter-target.on-link",
			TypeID:      "t_hook",
			Properties: map[string]graph.Property{
				"owner_urn":  {Value: "urn:moos:ki:filter-target", Mutability: "immutable"},
				"status":     {Value: "active", Mutability: "mutable"},
				"created_at": {Value: now.Format(time.RFC3339), Mutability: "immutable"},
				"event_shape": {
					Value:      map[string]any{"rewrite_type": "LINK"},
					Mutability: "mutable",
				},
				"react_template": {
					Value: map[string]any{
						"rewrite_type": "MUTATE",
						"actor":        "$actor",
						"target_urn":   "$matched_urn",
						"field":        "status",
						"new_value":    "should-not-be-set",
					},
					Mutability: "mutable",
				},
			},
		},
	}
	for _, env := range seeds {
		if err := rt.SeedIfAbsent(env); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	logBefore := rt.LogLen()

	// A MUTATE — the t_hook wants LINK so it should NOT fire.
	_, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:filter-target",
		Field:       "status",
		NewValue:    "updated",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Log should grow by only 1 (no t_hook fire).
	if got := rt.LogLen(); got != logBefore+1 {
		t.Errorf("expected log +1 (trigger only, t_hook filtered), got %d → %d", logBefore, got)
	}

	ki, ok := rt.Node("urn:moos:ki:filter-target")
	if !ok {
		t.Fatal("KI not found")
	}
	status, _ := ki.Properties["status"].Value.(string)
	if status == "should-not-be-set" {
		t.Error("t_hook fired when it should have been filtered by event_shape")
	}
	if status != "updated" {
		t.Errorf("expected status=updated, got %q", status)
	}
}

// TestRuntime_GateBlocksRewrite verifies that a gate node linked to a target via
// "guards"/"guarded-by" blocks a MUTATE when its predicate fails (M8 fail-closed).
func TestRuntime_GateBlocksRewrite(t *testing.T) {
	rt := &Runtime{
		state:       graph.NewGraphState(),
		store:       NewMemStore(),
		registry:    nil,
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}

	now := time.Now().UTC()

	seeds := []graph.Envelope{
		// Node to be gated.
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:ki:gated-item",
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"title":  {Value: "Gated KI", Mutability: "immutable"},
				"status": {Value: "raw", Mutability: "mutable"},
				// "approval" field intentionally absent — gate will check for it.
			},
		},
		// Gate: blocks rewrites on gated-item until a separate clearance node exists.
		// The predicate checks a DIFFERENT node — this avoids circular self-reference.
		// M8 semantics: "this data is incomplete until external approval is recorded."
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:gate:require-approval",
			TypeID:      "gate",
			Properties: map[string]graph.Property{
				"name":           {Value: "require-approval", Mutability: "immutable"},
				"created_at":     {Value: now.Format(time.RFC3339), Mutability: "immutable"},
				"predicate_type": {Value: "node_exists", Mutability: "mutable"},
				"target_urn":     {Value: "urn:moos:claim:gated-item.approval", Mutability: "mutable"},
				"negate":         {Value: false, Mutability: "mutable"},
			},
		},
		// LINK: gate guards the gated node.
		{
			RewriteType:     graph.LINK,
			Actor:           "urn:moos:kernel",
			RelationURN:     "urn:moos:rel:gate.guards.ki",
			RewriteCategory: "WF17",
			SrcURN:          "urn:moos:gate:require-approval",
			SrcPort:         "guards",
			TgtURN:          "urn:moos:ki:gated-item",
			TgtPort:         "guarded-by",
		},
	}
	for _, env := range seeds {
		if err := rt.SeedIfAbsent(env); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Attempt MUTATE while gate predicate fails (approval field absent) — must be blocked.
	_, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:gated-item",
		Field:       "status",
		NewValue:    "should-not-land",
	})
	if err == nil {
		t.Fatal("expected gate to block the MUTATE, but Apply succeeded")
	}
	if !strings.Contains(err.Error(), "gate(M8)") {
		t.Errorf("expected gate error, got: %v", err)
	}

	// Satisfy the gate: ADD the external clearance node that the predicate checks.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:user:sam",
		NodeURN:     "urn:moos:claim:gated-item.approval",
		TypeID:      "claim",
		Properties: map[string]graph.Property{
			"text":       {Value: "approved by sam", Mutability: "immutable"},
			"created_at": {Value: now.Format(time.RFC3339), Mutability: "immutable"},
		},
	}); err != nil {
		t.Fatalf("ADD clearance node: %v", err)
	}

	// Now the gate predicate passes — MUTATE on status should succeed.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:user:sam",
		TargetURN:   "urn:moos:ki:gated-item",
		Field:       "status",
		NewValue:    "approved",
	}); err != nil {
		t.Fatalf("MUTATE after gate satisfied: %v", err)
	}

	ki, ok := rt.Node("urn:moos:ki:gated-item")
	if !ok {
		t.Fatal("KI not found")
	}
	status, _ := ki.Properties["status"].Value.(string)
	if status != "approved" {
		t.Errorf("expected status=approved after gate satisfied, got %q", status)
	}
}

func TestRuntime_HDCDriftEmitsClaimAndAnnotation(t *testing.T) {
	rt := &Runtime{
		state:       graph.NewGraphState(),
		store:       NewMemStore(),
		registry:    nil,
		hdcIndex:    hdc.NewLiveIndex(1.1),
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}

	adds := []graph.Envelope{
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel:test",
			NodeURN:     "urn:moos:ki:a",
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"title":  {Value: "Alpha", Mutability: "immutable"},
				"status": {Value: "raw", Mutability: "mutable"},
			},
		},
		{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel:test",
			NodeURN:     "urn:moos:ki:b",
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"title":  {Value: "Beta", Mutability: "immutable"},
				"status": {Value: "raw", Mutability: "mutable"},
			},
		},
	}

	for _, env := range adds {
		if _, err := rt.Apply(env); err != nil {
			t.Fatalf("apply add failed: %v", err)
		}
	}

	expressions := rt.HDCTypeExpressions()
	var maxDrift float64
	for _, row := range expressions {
		if row.DeclaredType == "claim" {
			continue
		}
		if row.Drift > maxDrift {
			maxDrift = row.Drift
		}
	}
	if maxDrift <= 0 {
		t.Fatalf("expected positive drift score from type-expression index, got %f", maxDrift)
	}

	rt.mu.Lock()
	rt.hdcIndex = hdc.NewLiveIndex(maxDrift * 0.5)
	rt.hdcIndex.Recompute(rt.state, nil)
	rt.mu.Unlock()

	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.MUTATE,
		Actor:       "urn:moos:kernel:test",
		TargetURN:   "urn:moos:ki:a",
		Field:       "status",
		NewValue:    "claim-pending",
	}); err != nil {
		t.Fatalf("apply mutate trigger failed: %v", err)
	}

	var claimCount int
	for _, n := range rt.state.Nodes {
		if n.TypeID == "claim" {
			claimCount++
			if _, ok := n.Properties["subject_urn"]; !ok {
				t.Fatal("drift claim missing subject_urn")
			}
		}
	}
	if claimCount == 0 {
		t.Fatal("expected at least one drift claim node")
	}

	var annotationCount int
	for _, rel := range rt.state.Relations {
		if rel.RewriteCategory == graph.WF15 && rel.ContractURN == driftAnnotationContractURN {
			annotationCount++
		}
	}
	if annotationCount == 0 {
		t.Fatal("expected at least one WF15 semantic drift annotation relation")
	}

	expressions = rt.HDCTypeExpressions()
	if len(expressions) == 0 {
		t.Fatal("expected non-empty HDC type-expression index")
	}
}
