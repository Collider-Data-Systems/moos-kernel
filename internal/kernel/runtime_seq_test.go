package kernel

import (
	"testing"
	"time"

	"moos/kernel/internal/graph"
)

func addEntry(seq int64, urn string, at time.Time) graph.PersistedRewrite {
	return graph.PersistedRewrite{
		Envelope: graph.Envelope{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     graph.URN(urn),
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"title": {Value: urn, Mutability: "immutable"},
			},
		},
		AppliedAt: at,
		Timestamp: at,
		LogSeq:    seq,
	}
}

// TestNewRuntimeSeedsCounterFromMaxSeq replays a log carrying a multi-writer
// duplicate tail (moos-kernel#40): entries with seqs 1,2,3 followed by
// re-appended copies of 2,3. Last-entry seeding would restart the counter at
// 3 via the duplicate block's tail regardless — the sharper case is a tail
// whose re-appended seqs sit BELOW earlier ones, so the log here ends on a
// duplicate of seq 2. The counter must seed from max (3), not the tail (2),
// and the next apply must stamp 4.
func TestNewRuntimeSeedsCounterFromMaxSeq(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemStore()
	entries := []graph.PersistedRewrite{
		addEntry(1, "urn:moos:ki:seq-a", now),
		addEntry(2, "urn:moos:ki:seq-b", now),
		addEntry(3, "urn:moos:ki:seq-c", now),
		// multi-writer re-append: same envelopes, same seqs, later timestamps,
		// ending below the true max; seq 3 appears three times in total
		addEntry(3, "urn:moos:ki:seq-c", now.Add(time.Minute)),
		addEntry(3, "urn:moos:ki:seq-c", now.Add(2*time.Minute)),
		addEntry(2, "urn:moos:ki:seq-b", now.Add(2*time.Minute)),
	}
	if err := store.Append(entries); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	rt, err := NewRuntime(store, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	if got := rt.LogLen(); got != 6 {
		t.Fatalf("LogLen = %d, want 6 (duplicates are replayed, not dropped)", got)
	}
	if gotLen, gotMax := rt.LogStats(); gotLen != 6 || gotMax != 3 {
		t.Fatalf("LogStats = (%d, %d), want (6, 3)", gotLen, gotMax)
	}
	if got := rt.MaxLogSeq(); got != 3 {
		t.Fatalf("MaxLogSeq = %d, want 3 (max, not last entry's 2)", got)
	}

	// Next apply continues from max: seq 4, never a re-minted 3.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:kernel",
		NodeURN:     "urn:moos:ki:seq-d",
		TypeID:      "knowledge_item",
		Properties: map[string]graph.Property{
			"title": {Value: "d", Mutability: "immutable"},
		},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	log := rt.Log()
	if got := log[len(log)-1].LogSeq; got != 4 {
		t.Fatalf("applied LogSeq = %d, want 4", got)
	}
	if got := rt.MaxLogSeq(); got != 4 {
		t.Fatalf("MaxLogSeq after apply = %d, want 4", got)
	}

	persisted, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := persisted[len(persisted)-1].LogSeq; got != 4 {
		t.Fatalf("persisted tail LogSeq = %d, want 4", got)
	}
}

// preSeqEntry builds an entry as persisted before the log_seq field existed
// (early-2026 seed lines): LogSeq deserializes to the zero value.
func preSeqEntry(urn string, at time.Time) graph.PersistedRewrite {
	e := addEntry(0, urn, at)
	return e
}

// TestNewRuntimePreSeqEraEntriesAreNotDuplicates — moos-kernel#45: the Z440
// twin logs open with seed lines that predate the log_seq field entirely.
// They deserialize to LogSeq 0 and must be counted as pre-seq legacy, not as
// duplicate seq values; the counter must still seed from the real max, and
// the drift arithmetic log_len - LogSeqMissing() == max_log_seq must hold.
func TestNewRuntimePreSeqEraEntriesAreNotDuplicates(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemStore()
	// The twin shape: 3 pre-seq seed lines, then a clean seq run 1..10.
	entries := []graph.PersistedRewrite{
		preSeqEntry("urn:moos:ki:legacy-a", now),
		preSeqEntry("urn:moos:ki:legacy-b", now),
		preSeqEntry("urn:moos:ki:legacy-c", now),
	}
	for i := int64(1); i <= 10; i++ {
		entries = append(entries, addEntry(i, "urn:moos:ki:real-"+string(rune('a'+i-1)), now.Add(time.Duration(i)*time.Second)))
	}
	if err := store.Append(entries); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	rt, err := NewRuntime(store, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	if got := rt.LogSeqMissing(); got != 3 {
		t.Fatalf("LogSeqMissing = %d, want 3", got)
	}
	logLen, maxSeq := rt.LogStats()
	if logLen != 13 || maxSeq != 10 {
		t.Fatalf("LogStats = (%d, %d), want (13, 10)", logLen, maxSeq)
	}
	// The clean-log invariant with legacy entries subtracted.
	if int64(logLen-rt.LogSeqMissing()) != maxSeq {
		t.Fatalf("log_len - log_seq_missing = %d, want max_log_seq %d", logLen-rt.LogSeqMissing(), maxSeq)
	}

	// Next apply stamps 11 — pre-seq entries never influence the counter.
	if _, err := rt.Apply(graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       "urn:moos:kernel",
		NodeURN:     "urn:moos:ki:real-next",
		TypeID:      "knowledge_item",
		Properties: map[string]graph.Property{
			"title": {Value: "next", Mutability: "immutable"},
		},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := rt.MaxLogSeq(); got != 11 {
		t.Fatalf("MaxLogSeq after apply = %d, want 11", got)
	}
}
