package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"moos/kernel/internal/fold"
	"moos/kernel/internal/graph"
)

// Log integrity — the read-only forensic report over historical duplicate
// log_seq values (A6; ffs0#178).
//
// WHY THIS EXISTS. Two historical incident classes left the hp-laptop fold with
// log_len > max_log_seq: a two-writer counter race (distinct rewrites minted the
// same seq) and an operator/harness double-POST (the same envelopes re-applied
// minutes later). Sam's t274 ruling is to GRANDFATHER both — every entry and
// every seq value is preserved, no renumber, no dedup, no persisted entry id.
// What was missing was not a repair but OBSERVABILITY: nothing told a reader
// which entries collided, or which of them actually changed state.
//
// THE PREMISE THIS CORRECTS. The original proposal classified each collision
// group as exact_reapply | distinct_rewrites, from envelope fingerprints alone.
// "exact_reapply" reads as *applied twice*, and that is false: a re-applied ADD
// hits ErrNodeExists and a re-applied CAS MUTATE hits ErrVersionConflict, so the
// second entry is LOGGED and folds to nothing. Fingerprint equality is not fold
// equivalence. Classification is therefore recorded PER ENTRY, three-valued, and
// the group kind is kept only as a coarse summary.
//
// The report is computed once during replay, is immutable afterwards, and never
// touches the log: new writes continue to allocate from max(log_seq)+1.
const (
	// EntryApplied — this entry advanced the fold.
	EntryApplied = "applied"
	// EntryLoggedNoFoldEffect — this entry is in the log but produced no state
	// change: the fold rejected it (ErrNodeExists / ErrRelationExists /
	// ErrRelationNotFound / ErrVersionConflict — fold.isIdempotentSkip's set).
	EntryLoggedNoFoldEffect = "logged_no_fold_effect"
)

const (
	// KindExactReapply — every envelope in the group fingerprints identically.
	KindExactReapply = "exact_reapply"
	// KindDistinctRewrites — the group mixes different envelopes on one seq.
	KindDistinctRewrites = "distinct_rewrites"
)

// CollisionEntry is one log entry participating in a duplicate-seq group.
type CollisionEntry struct {
	// AppendPosition is the 1-based line index in the log. It is a LOCATOR, not
	// an identity: it is stable only while no line is ever inserted, removed or
	// compacted. Cite by fingerprint; use position to find the line.
	AppendPosition int    `json:"append_position"`
	AppliedAt      string `json:"applied_at,omitempty"`
	RewriteType    string `json:"rewrite_type"`
	Actor          string `json:"actor,omitempty"`
	TargetURN      string `json:"target_urn,omitempty"`
	// Fingerprint is a bounded hash over the ENVELOPE only — persistence
	// metadata (applied_at, timestamp, log_seq, append position) is excluded by
	// construction, since it lives on PersistedRewrite and not on Envelope.
	Fingerprint string `json:"fingerprint"`
	FoldEffect  string `json:"fold_effect"`
}

// CollisionGroup is every entry sharing one duplicated log_seq value.
type CollisionGroup struct {
	LogSeq  int64            `json:"log_seq"`
	Count   int              `json:"entry_count"`
	Kind    string           `json:"kind"`
	Entries []CollisionEntry `json:"entries"`
	// DistinctAppliedAt counts distinct apply timestamps in the group. >1 means
	// the collided range spans more than one write event — on hp-laptop, seqs
	// 1079-1080 were re-written ~3h45m after 1073-1078, which a single group
	// label cannot show.
	DistinctAppliedAt int `json:"distinct_applied_at"`
	// AppliedCount is how many entries in this group actually advanced the fold.
	// For a true duplicate this is 1; 0 or >1 is anomalous and worth a look.
	AppliedCount int `json:"applied_count"`
}

// LogIntegrity is the immutable historical integrity report. Self-identifying:
// it carries its own kernel URN because it is served through a federation
// fan-in that keys nothing (router GET /log concatenates every fold's entries
// with no kernel/host key, so log_seq alone is ambiguous fleet-wide).
type LogIntegrity struct {
	KernelURN string `json:"kernel_urn,omitempty"`

	LogLen        int   `json:"log_len"`
	MaxLogSeq     int64 `json:"max_log_seq"`
	LogSeqMissing int   `json:"log_seq_missing"`

	// DuplicateLogSeqValues is the count of distinct seq values seen more than
	// once; DuplicateLogSeqExcessEntries is the number of entries beyond one per
	// value (== log_len - log_seq_missing - distinct real seqs).
	DuplicateLogSeqValues        int `json:"duplicate_log_seq_values"`
	DuplicateLogSeqExcessEntries int `json:"duplicate_log_seq_excess_entries"`

	// CollisionKinds counts groups by kind, e.g. {"exact_reapply":9,
	// "distinct_rewrites":8}.
	CollisionKinds map[string]int `json:"log_seq_collision_kinds"`

	// LoggedNoFoldEffect is the total number of entries across all groups that
	// are in the log but changed no state.
	LoggedNoFoldEffect int `json:"logged_no_fold_effect_entries"`

	// SingleWriter reports whether this kernel holds the exclusive JSONL lock.
	// False means --allow-shared-log is in force, which is the one live way a
	// NEW collision can still be minted; the scalars above are retrospective and
	// cannot detect that on their own.
	SingleWriter bool `json:"log_single_writer"`

	Groups []CollisionGroup `json:"collision_groups"`
}

// envelopeFingerprint hashes the envelope's canonical JSON, bounded to 16 hex
// chars. Envelope carries no persistence metadata, so equal fingerprints mean
// "the same rewrite was submitted twice" — NOT "it was applied twice".
func envelopeFingerprint(env graph.Envelope) string {
	b, err := json.Marshal(env)
	if err != nil {
		// A non-marshalable envelope cannot have been persisted; degrade to a
		// stable sentinel rather than failing the whole report.
		return "unfingerprintable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// envelopeTarget names what the rewrite addressed, for human triage only.
func envelopeTarget(env graph.Envelope) string {
	switch {
	case env.NodeURN != "":
		return string(env.NodeURN)
	case env.TargetURN != "":
		return string(env.TargetURN)
	case env.SrcURN != "" || env.TgtURN != "":
		return string(env.SrcURN) + " -> " + string(env.TgtURN)
	}
	return ""
}

func formatAppliedAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// computeLogIntegrity builds the report. It is called only when the replay scan
// already found duplicates, so healthy folds pay nothing.
//
// Fold effect is established by walking the log exactly as fold.Replay does —
// EvaluateAt per entry, skip-and-continue on error — and recording, for the
// entries inside collision groups, whether that evaluation advanced the state.
// This mirrors Replay rather than modifying it: fold stays pure and its
// semantics are untouched (an explicit non-goal of A6).
func computeLogIntegrity(
	entries []graph.PersistedRewrite,
	dupSeqs []int64,
	maxSeq int64,
	preSeq int,
	distinctRealSeqs int,
	singleWriter bool,
	kernelURN graph.URN,
) LogIntegrity {
	dup := make(map[int64]bool, len(dupSeqs))
	for _, s := range dupSeqs {
		dup[s] = true
	}

	bySeq := make(map[int64]*CollisionGroup, len(dupSeqs))

	// Mirror of fold.Replay. We cannot reuse it: it returns only the final
	// state, and the per-entry outcome is exactly what this report needs.
	state := graph.NewGraphState()
	for i, pr := range entries {
		at := pr.AppliedAt
		if at.IsZero() {
			at = pr.Timestamp
		}
		next, _, err := fold.EvaluateAt(state, pr.Envelope, at)

		if dup[pr.LogSeq] {
			effect := EntryApplied
			if err != nil {
				effect = EntryLoggedNoFoldEffect
			}
			g := bySeq[pr.LogSeq]
			if g == nil {
				g = &CollisionGroup{LogSeq: pr.LogSeq}
				bySeq[pr.LogSeq] = g
			}
			g.Entries = append(g.Entries, CollisionEntry{
				AppendPosition: i + 1, // 1-based: matches sed -n '<n>p'
				AppliedAt:      formatAppliedAt(at),
				RewriteType:    string(pr.Envelope.RewriteType),
				Actor:          string(pr.Envelope.Actor),
				TargetURN:      envelopeTarget(pr.Envelope),
				Fingerprint:    envelopeFingerprint(pr.Envelope),
				FoldEffect:     effect,
			})
		}

		if err != nil {
			// Same contract as Replay: a rejected entry does not advance state.
			continue
		}
		state = next
	}

	report := LogIntegrity{
		KernelURN:                    string(kernelURN),
		LogLen:                       len(entries),
		MaxLogSeq:                    maxSeq,
		LogSeqMissing:                preSeq,
		DuplicateLogSeqValues:        len(dupSeqs),
		DuplicateLogSeqExcessEntries: len(entries) - preSeq - distinctRealSeqs,
		CollisionKinds:               map[string]int{},
		SingleWriter:                 singleWriter,
	}

	for _, g := range bySeq {
		g.Count = len(g.Entries)

		fingerprints := map[string]bool{}
		stamps := map[string]bool{}
		for _, e := range g.Entries {
			fingerprints[e.Fingerprint] = true
			if e.AppliedAt != "" {
				stamps[e.AppliedAt] = true
			}
			if e.FoldEffect == EntryApplied {
				g.AppliedCount++
			} else {
				report.LoggedNoFoldEffect++
			}
		}
		g.DistinctAppliedAt = len(stamps)
		if len(fingerprints) == 1 {
			g.Kind = KindExactReapply
		} else {
			g.Kind = KindDistinctRewrites
		}
		report.CollisionKinds[g.Kind]++
		report.Groups = append(report.Groups, *g)
	}

	sort.Slice(report.Groups, func(a, b int) bool {
		return report.Groups[a].LogSeq < report.Groups[b].LogSeq
	})
	return report
}
