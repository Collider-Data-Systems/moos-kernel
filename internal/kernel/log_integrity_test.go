package kernel

import (
	"testing"
	"time"

	"moos/kernel/internal/graph"
)

// A6 regression coverage for the three historical duplicate-log_seq classes.
//
// FIXTURES ARE SYNTHETIC BY REQUIREMENT. moos-kernel is a PUBLIC repository and
// the sovereign logs are Sam's private history — no fixture here is copied from,
// derived from, or excerpted out of any real log. The shapes below reproduce the
// three classes structurally; nothing else about them is real.
//
//	class 1  exact re-apply of an ADD      -> second entry hits ErrNodeExists
//	class 2  distinct rewrites, one seq    -> two-writer counter race
//	class 3  exact re-apply of a CAS MUTATE-> second entry hits ErrVersionConflict
//
// The premise under test in classes 1 and 3: identical fingerprints do NOT mean
// "applied twice". Exactly one entry per group advances the fold.

func at(sec int) time.Time {
	return time.Date(2026, 4, 17, 23, 0, sec, 0, time.UTC)
}

// addEnv creates a node carrying a mutable `status` property, so the MUTATE
// fixtures below have a field that actually exists to change.
func addEnv(urn string) graph.Envelope {
	return graph.Envelope{
		RewriteType: graph.ADD,
		Actor:       graph.URN("urn:moos:kernel:test.primary"),
		NodeURN:     graph.URN(urn),
		TypeID:      "workstation",
		Properties: map[string]graph.Property{
			"status": {Value: "pending", Mutability: "mutable", AuthorityScope: "principal"},
		},
	}
}

func mutateEnv(urn string, expected int64) graph.Envelope {
	return graph.Envelope{
		RewriteType:     graph.MUTATE,
		Actor:           graph.URN("urn:moos:kernel:test.primary"),
		TargetURN:       graph.URN(urn),
		Field:           "status",
		NewValue:        "active",
		ExpectedVersion: expected,
	}
}

// buildFixture returns a log carrying all three collision classes plus clean
// entries around them, so position arithmetic is exercised too.
func buildFixture() []graph.PersistedRewrite {
	node := "urn:moos:workstation:fixture-a"
	other := "urn:moos:workstation:fixture-b"

	return []graph.PersistedRewrite{
		// seq 1 — clean ADD, establishes the node.
		{Envelope: addEnv(node), LogSeq: 1, AppliedAt: at(1)},
		// seq 2 — clean ADD of a second node.
		{Envelope: addEnv(other), LogSeq: 2, AppliedAt: at(2)},
		// seq 3 — class 1 first entry: a genuine ADD (applied).
		{Envelope: addEnv("urn:moos:workstation:fixture-c"), LogSeq: 3, AppliedAt: at(3)},
		// seq 3 AGAIN — byte-identical envelope re-POSTed ~55s later. Logged,
		// folds to nothing (ErrNodeExists).
		{Envelope: addEnv("urn:moos:workstation:fixture-c"), LogSeq: 3, AppliedAt: at(58)},
		// seq 4 — class 2 first entry.
		{Envelope: addEnv("urn:moos:workstation:fixture-d"), LogSeq: 4, AppliedAt: at(5)},
		// seq 4 AGAIN — a DIFFERENT rewrite on the same seq (two-writer race).
		{Envelope: mutateEnv(node, 0), LogSeq: 4, AppliedAt: at(6)},
		// seq 5 — class 3 first entry: CAS MUTATE, expects version 1.
		{Envelope: mutateEnv(other, 1), LogSeq: 5, AppliedAt: at(7)},
		// seq 5 AGAIN — same CAS guard. At most one can win.
		{Envelope: mutateEnv(other, 1), LogSeq: 5, AppliedAt: at(46)},
		// seq 6 — class 4, and the one the two-valued model hides completely:
		// an UNGUARDED MUTATE (ExpectedVersion 0 = skip CAS) re-applied. The
		// envelopes fingerprint identically, but nothing rejects the second, so
		// it DOES land and bumps the node's version again. "exact_reapply"
		// therefore does not imply "folded to nothing" — only per-entry fold
		// effect can tell these apart. Historically this is the majority of
		// the exact-fingerprint groups, not a corner case.
		{Envelope: mutateEnv(node, 0), LogSeq: 6, AppliedAt: at(8)},
		{Envelope: mutateEnv(node, 0), LogSeq: 6, AppliedAt: at(63)},
	}
}

func newFixtureRuntime(t *testing.T) *Runtime {
	t.Helper()
	store := NewMemStore()
	if err := store.Append(buildFixture()); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	rt, err := NewRuntime(store, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	rt.SetKernelURN("urn:moos:kernel:test.primary")
	return rt
}

func TestLogIntegrity_CountsAndScalars(t *testing.T) {
	rt := newFixtureRuntime(t)
	got := rt.LogIntegrity()

	if got.LogLen != 10 {
		t.Errorf("log_len = %d, want 10", got.LogLen)
	}
	if got.MaxLogSeq != 6 {
		t.Errorf("max_log_seq = %d, want 6", got.MaxLogSeq)
	}
	if got.DuplicateLogSeqValues != 4 {
		t.Errorf("duplicate_log_seq_values = %d, want 4 (seqs 3, 4, 5, 6)", got.DuplicateLogSeqValues)
	}
	// 10 entries, 0 pre-seq, 6 distinct real seqs -> 4 excess.
	if got.DuplicateLogSeqExcessEntries != 4 {
		t.Errorf("duplicate_log_seq_excess_entries = %d, want 4", got.DuplicateLogSeqExcessEntries)
	}
	if got.KernelURN != "urn:moos:kernel:test.primary" {
		t.Errorf("kernel_urn = %q, want the fixture URN — the report must be self-identifying", got.KernelURN)
	}
	if !got.SingleWriter {
		t.Error("log_single_writer = false for a MemStore; want true (no shared file to race on)")
	}
	if len(got.Groups) != 4 {
		t.Fatalf("collision_groups = %d, want 4", len(got.Groups))
	}
	// Groups must be seq-ordered so a human can scan them against the log.
	for i := 1; i < len(got.Groups); i++ {
		if got.Groups[i-1].LogSeq >= got.Groups[i].LogSeq {
			t.Errorf("groups not sorted by log_seq: %v", got.Groups)
		}
	}
}

// The core premise fix: an exact re-apply is NOT "applied twice".
func TestLogIntegrity_ExactReapplyFoldsToNothing(t *testing.T) {
	rt := newFixtureRuntime(t)
	groups := rt.LogIntegrity().Groups

	g := groups[0] // seq 3
	if g.LogSeq != 3 {
		t.Fatalf("first group is seq %d, want 3", g.LogSeq)
	}
	if g.Kind != KindExactReapply {
		t.Errorf("kind = %q, want %q — both envelopes are byte-identical", g.Kind, KindExactReapply)
	}
	if len(g.Entries) != 2 {
		t.Fatalf("entry_count = %d, want 2", len(g.Entries))
	}
	if g.Entries[0].Fingerprint != g.Entries[1].Fingerprint {
		t.Error("fingerprints differ for an identical envelope pair")
	}
	// The whole point of A6.1.
	if g.Entries[0].FoldEffect != EntryApplied {
		t.Errorf("first entry fold_effect = %q, want %q", g.Entries[0].FoldEffect, EntryApplied)
	}
	if g.Entries[1].FoldEffect != EntryLoggedNoFoldEffect {
		t.Errorf("second entry fold_effect = %q, want %q — a re-applied ADD hits ErrNodeExists and changes no state",
			g.Entries[1].FoldEffect, EntryLoggedNoFoldEffect)
	}
	if g.AppliedCount != 1 {
		t.Errorf("applied_count = %d, want exactly 1", g.AppliedCount)
	}
	// Append positions must uniquely locate both members.
	if g.Entries[0].AppendPosition != 3 || g.Entries[1].AppendPosition != 4 {
		t.Errorf("append positions = %d/%d, want 3/4 (1-based)",
			g.Entries[0].AppendPosition, g.Entries[1].AppendPosition)
	}
	// Two distinct write events, which a single group label cannot express.
	if g.DistinctAppliedAt != 2 {
		t.Errorf("distinct_applied_at = %d, want 2", g.DistinctAppliedAt)
	}
}

// A CAS-guarded MUTATE pair: at most one can win, so the same premise holds for
// a rewrite type that is not ADD.
func TestLogIntegrity_CASReapplyFoldsToNothing(t *testing.T) {
	rt := newFixtureRuntime(t)
	groups := rt.LogIntegrity().Groups

	g := groups[2] // seq 5
	if g.LogSeq != 5 {
		t.Fatalf("third group is seq %d, want 5", g.LogSeq)
	}
	if g.Kind != KindExactReapply {
		t.Errorf("kind = %q, want %q", g.Kind, KindExactReapply)
	}
	if g.AppliedCount != 1 {
		t.Errorf("applied_count = %d, want 1 — two MUTATEs sharing expected_version cannot both land", g.AppliedCount)
	}
	if g.Entries[1].FoldEffect != EntryLoggedNoFoldEffect {
		t.Errorf("second CAS entry fold_effect = %q, want %q", g.Entries[1].FoldEffect, EntryLoggedNoFoldEffect)
	}
}

// The two-writer race: different rewrites, one seq. Both really applied.
func TestLogIntegrity_DistinctRewritesBothApply(t *testing.T) {
	rt := newFixtureRuntime(t)
	groups := rt.LogIntegrity().Groups

	g := groups[1] // seq 4
	if g.LogSeq != 4 {
		t.Fatalf("second group is seq %d, want 4", g.LogSeq)
	}
	if g.Kind != KindDistinctRewrites {
		t.Errorf("kind = %q, want %q", g.Kind, KindDistinctRewrites)
	}
	if g.Entries[0].Fingerprint == g.Entries[1].Fingerprint {
		t.Error("fingerprints match for an ADD/MUTATE pair; they must differ")
	}
	if g.AppliedCount != 2 {
		t.Errorf("applied_count = %d, want 2 — distinct rewrites both change state", g.AppliedCount)
	}
	if g.Entries[0].RewriteType != string(graph.ADD) || g.Entries[1].RewriteType != string(graph.MUTATE) {
		t.Errorf("rewrite types = %q/%q, want ADD/MUTATE — the mix is what makes this class distinct",
			g.Entries[0].RewriteType, g.Entries[1].RewriteType)
	}
}

// Allocation must continue from max(log_seq)+1 even though the tail seq is not
// the maximum — the moos-kernel#40 invariant that grandfathering depends on.
func TestLogIntegrity_NextSeqIsMaxPlusOne(t *testing.T) {
	rt := newFixtureRuntime(t)
	if got := rt.logSeq.Load(); got != 6 {
		t.Fatalf("seeded counter = %d, want 6 (max, not tail)", got)
	}
	report := rt.LogIntegrity()
	if report.MaxLogSeq != 6 {
		t.Errorf("max_log_seq = %d, want 6", report.MaxLogSeq)
	}
	if report.LoggedNoFoldEffect != 2 {
		t.Errorf("logged_no_fold_effect_entries = %d, want 2 (the ADD re-apply and the CAS re-apply)",
			report.LoggedNoFoldEffect)
	}
}

// The class the two-valued model cannot express, and the majority class on the
// affected fold: identical fingerprints, and the second entry DOES land.
// An unguarded MUTATE has nothing to reject it, so it re-applies and bumps the
// node version again — same value, new version. A reader who sees only
// "exact_reapply" would wrongly conclude this folded to nothing.
func TestLogIntegrity_UnguardedMutateReapplyDoesApply(t *testing.T) {
	rt := newFixtureRuntime(t)
	groups := rt.LogIntegrity().Groups

	g := groups[3] // seq 6
	if g.LogSeq != 6 {
		t.Fatalf("fourth group is seq %d, want 6", g.LogSeq)
	}
	if g.Kind != KindExactReapply {
		t.Errorf("kind = %q, want %q — the envelopes are identical", g.Kind, KindExactReapply)
	}
	if g.Entries[0].Fingerprint != g.Entries[1].Fingerprint {
		t.Error("fingerprints differ; the fixture is meant to be an exact pair")
	}
	if g.AppliedCount != 2 {
		t.Fatalf("applied_count = %d, want 2 — an unguarded MUTATE re-applies", g.AppliedCount)
	}
	for i, e := range g.Entries {
		if e.FoldEffect != EntryApplied {
			t.Errorf("entry %d fold_effect = %q, want %q — nothing rejects an unguarded MUTATE",
				i, e.FoldEffect, EntryApplied)
		}
	}
}

// A fold with no duplicates must still answer, with zeroes — absence of a report
// would be indistinguishable from "endpoint broken".
func TestLogIntegrity_CleanFoldReportsZeroes(t *testing.T) {
	store := NewMemStore()
	if err := store.Append([]graph.PersistedRewrite{
		{Envelope: addEnv("urn:moos:workstation:solo"), LogSeq: 1, AppliedAt: at(1)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rt, err := NewRuntime(store, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	got := rt.LogIntegrity()
	if got.DuplicateLogSeqValues != 0 || len(got.Groups) != 0 {
		t.Errorf("clean fold reported duplicates: %+v", got)
	}
	if got.LogLen != 1 {
		t.Errorf("log_len = %d, want 1 — a clean fold still reports live scalars", got.LogLen)
	}
	if got.MaxLogSeq != 1 {
		t.Errorf("max_log_seq = %d, want 1", got.MaxLogSeq)
	}
	if got.CollisionKinds == nil {
		t.Error("log_seq_collision_kinds is nil; want an empty map so consumers need no nil check")
	}
}
