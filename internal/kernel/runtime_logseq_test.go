package kernel

import (
	"fmt"
	"testing"

	"moos/kernel/internal/graph"
)

func seqEntry(seq int64, slug string) graph.PersistedRewrite {
	return graph.PersistedRewrite{
		Envelope: graph.Envelope{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:agent:test",
			NodeURN:     graph.URN(fmt.Sprintf("urn:moos:program:%s", slug)),
			TypeID:      "program",
		},
		LogSeq: seq,
	}
}

// TestNewRuntimeSeedsFromMaxLogSeq — moos-kernel#40 fix (b): the counter
// seeds from max(log_seq), not the last entry. A multi-writer-damaged log can
// end on a stale duplicate below the high-water mark; last-entry seeding
// would mint further collisions on the next apply.
func TestNewRuntimeSeedsFromMaxLogSeq(t *testing.T) {
	store := NewMemStore()
	// seqs 1,2,3 then a stale-counter writer stamps 2 again on a different
	// envelope — the last entry (2) is below the max (3).
	if err := store.Append([]graph.PersistedRewrite{
		seqEntry(1, "a"), seqEntry(2, "b"), seqEntry(3, "c"), seqEntry(2, "d"),
	}); err != nil {
		t.Fatalf("preload: %v", err)
	}

	rt, err := NewRuntime(store, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if got := rt.MaxLogSeq(); got != 3 {
		t.Fatalf("counter must seed from max seq 3, got %d", got)
	}

	// Next apply stamps 4 — no collision with the existing high-water mark.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:agent:test",
		NodeURN:     "urn:moos:program:next",
		TypeID:      "program",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	log := rt.Log()
	if got := log[len(log)-1].LogSeq; got != 4 {
		t.Fatalf("next apply must stamp seq 4, got %d", got)
	}
	if got := rt.MaxLogSeq(); got != 4 {
		t.Fatalf("MaxLogSeq must advance to 4, got %d", got)
	}
	// The drift signature stays observable: log_len 5 > max_log_seq 4.
	if rt.LogLen() != 5 {
		t.Fatalf("log_len must be 5, got %d", rt.LogLen())
	}
}

func TestAuditLogSeqs(t *testing.T) {
	cases := []struct {
		name    string
		entries []graph.PersistedRewrite
		wantMax int64
		wantDup []int64
	}{
		{"empty", nil, 0, nil},
		{"clean", []graph.PersistedRewrite{seqEntry(1, "a"), seqEntry(2, "b")}, 2, nil},
		{"dups", []graph.PersistedRewrite{
			seqEntry(1, "a"), seqEntry(2, "b"), seqEntry(2, "c"), seqEntry(3, "d"), seqEntry(1, "e"),
		}, 3, []int64{1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			maxSeq, dups := auditLogSeqs(tc.entries)
			if maxSeq != tc.wantMax {
				t.Fatalf("max: want %d got %d", tc.wantMax, maxSeq)
			}
			if len(dups) != len(tc.wantDup) {
				t.Fatalf("dups: want %v got %v", tc.wantDup, dups)
			}
			for i := range dups {
				if dups[i] != tc.wantDup[i] {
					t.Fatalf("dups: want %v got %v", tc.wantDup, dups)
				}
			}
		})
	}
}
