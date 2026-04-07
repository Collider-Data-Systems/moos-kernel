package kernel

import (
	"testing"
	"time"

	"moos/kernel/internal/graph"
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
