package kernel

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"moos/kernel/internal/graph"
	"moos/kernel/internal/reactive"
	"moos/kernel/internal/tday"
)

// ----------------------------------------------------------------------------
// Time-driven sweep (§M14 / §M15 — hook-predicates sub-program)
// ----------------------------------------------------------------------------
//
// The sweep walks all pending t_hook nodes on every tick and emits a
// governance_proposal per hook whose predicate evaluates true. Proposals
// sit at status=pending awaiting human or admin-session ratification —
// the sweep NEVER auto-applies a hook's react_template.
//
// Firing semantics (per T=168 round-9 plan, sam's direction):
//   Propose via WF13 governance. Sweep ADDs a governance_proposal carrying
//   the source hook URN, the calendar-T it fired at, and a snapshot of
//   the hook's react_template. An admin session reviews, MUTATEs
//   governance_proposal.status from pending to approved or rejected, and
//   a separate reactor (not in this PR) applies the proposed envelope
//   when status becomes approved.
//
// Idempotency:
//   The sweep uses the existing-proposal set as the idempotency key. On
//   every tick, it builds a lookup of hooks that already have a proposal
//   (matching by governance_proposal.source_t_hook_urn) and skips those
//   hooks in the main loop. This avoids needing a firing_state property
//   on t_hook (which is not in the v3.9 type spec — additive MUTATE would
//   fail). When firing_state lands in a later ontology bump, the sweep
//   can switch to that mechanism.
//
// Kernel does not track calendar-T itself (§M13); the caller supplies it.
// RunTimedSweep derives T from wall-clock via CurrentSweepTDay.

// CurrentSweepTDay returns the current calendar T-day. Thin wrapper over
// tday.Now so the sweep's wall-clock source is the same one the transport
// layer uses — the round-9 review flagged the previous local epoch as a
// drift risk, and T=169 consolidated both to internal/tday.
//
// Kept exported for back-compat with callers that imported it before the
// tday package existed; new code should call tday.Now directly.
func CurrentSweepTDay() int { return tday.Now() }

// DefaultSweepActor is the actor URN used by the sweep when ADDing
// governance_proposal nodes. Override via Runtime.SetSweepActor before
// starting the sweep goroutine if a more specific kernel URN is known
// (e.g. urn:moos:kernel:hp-laptop.primary).
const DefaultSweepActor graph.URN = "urn:moos:kernel:sweep"

// SweepOnce inspects the given state and returns the list of
// governance_proposal ADD envelopes for every pending t_hook whose
// predicate evaluates true at currentT.
//
// Deterministic (given state + currentT + actor + baseLogSeq + now): does
// not mutate state or perform IO. The caller (Runtime.SweepTick) passes
// a snapshot-time `now` so all proposals in one sweep share a single
// timestamp; this also makes the function testable without a clock stub.
//
// Idempotency (v3.11+): hooks with firing_state ∈ {"", "pending"} are
// eligible to fire; anything else (proposed | approved | rejected |
// applied | closed) is skipped. Empty-string treatment matches the
// v3.11 spec default of pending so pre-v3.11 hooks work without a
// migration write.
//
// baseLogSeq is a STABLE-for-the-tick prefix baked into the generated
// proposal URN for readability. It is NOT guaranteed to match the
// actual log_seq the envelope ends up carrying when ApplyProgram
// appends — reading logSeq outside the write lock is racy. The per-
// tick `emitted` counter is what actually provides uniqueness across
// hooks in a single tick (PR #25 review — Gemini).
func SweepOnce(state graph.GraphState, currentT int, actor graph.URN, baseLogSeq int64, now time.Time) []graph.Envelope {
	var envelopes []graph.Envelope
	emitted := int64(0)
	nowStr := now.UTC().Format(time.RFC3339)

	// v3.11 ontology introduced t_hook.firing_state. The sweep filters on
	// firing_state ∈ {"", "pending"} — missing field is treated as pending
	// per the spec's "default": "pending" clause. Any other value means
	// the hook is already past the pending boundary (proposed, approved,
	// applied, rejected, closed) and the sweep must NOT re-propose it.
	//
	// This replaces the v3.10 O(proposals) scan of governance_proposals
	// with an O(1) per-hook predicate. The idempotency contract is now
	// visible on the hook itself, not reverse-derived from proposal
	// existence — matters for the approver reactor that needs to see
	// a clean state machine.
	for _, hookURN := range state.NodesOfType("t_hook") {
		n, ok := state.Nodes[hookURN]
		if !ok {
			continue
		}
		// Idempotency: only pending (or unset, which defaults to pending) hooks fire.
		if fs := firingStateOf(n); fs != "" && fs != "pending" {
			continue
		}
		// Read the predicate. Missing or nil → skip.
		predProp, hasPred := n.Properties["predicate"]
		if !hasPred || predProp.Value == nil {
			continue
		}
		// Evaluate against the state we received. EvaluateThookPredicate
		// handles the json round-trip when the value arrives from log
		// replay as map[string]any; returns false on malformed kinds.
		if !reactive.EvaluateThookPredicate(predProp.Value, &state, currentT) {
			continue
		}

		// A firing hook produces TWO envelopes in the same ApplyProgram
		// batch so they land atomically:
		//   1. ADD   a governance_proposal node
		//   2. MUTATE the source hook's firing_state pending → proposed
		//
		// ApplyProgram is all-or-nothing, so if either fails (e.g. URN
		// collision on the proposal, or ontology validation rejects the
		// MUTATE) neither lands and the next tick retries.
		//
		// URN disambiguation: the proposal URN embeds hook slug + T-day +
		// a per-tick counter (`emitted`). baseLogSeq is a stable tick-wide
		// prefix read from rt.logSeq at the start of the tick; it's NOT
		// promised to match the actual log_seq the envelope ends up
		// carrying (PR #25 review — Gemini flagged the race). The
		// suffix only needs to be unique across hooks within this tick,
		// and across ticks of this sweep goroutine; `emitted` covers
		// both because ApplyProgram is locked and a single goroutine
		// drives the sweep.
		hookSlug := slugFromURN(n.URN)
		emitted++
		proposalURN := graph.URN(fmt.Sprintf("urn:moos:proposal:kernel.%s-t%d-tick%d-n%d", hookSlug, currentT, baseLogSeq, emitted))

		title := fmt.Sprintf("Fire t_hook %s at T=%d", n.URN, currentT)

		props := map[string]graph.Property{
			"title":             {Value: title, Mutability: "immutable"},
			"status":            {Value: "pending", Mutability: "mutable", AuthorityScope: "principal"},
			"created_at":        {Value: nowStr, Mutability: "immutable"},
			"source_t_hook_urn": {Value: string(n.URN), Mutability: "immutable"},
			"fires_at_t":        {Value: currentT, Mutability: "immutable"},
		}
		if p, ok := n.Properties["react_template"]; ok && p.Value != nil {
			props["proposed_envelope"] = graph.Property{Value: p.Value, Mutability: "immutable"}
		}
		if p, ok := n.Properties["owner_urn"]; ok && p.Value != nil {
			props["owner_urn"] = graph.Property{Value: p.Value, Mutability: "immutable"}
		}

		// RewriteCategory is intentionally omitted on both envelopes:
		//
		//   - The ADD creates a governance_proposal NODE. Node creation
		//     is typed by type_id; no WF category applies. (PR #25
		//     review — Gemini suggested WF13; WF13's allowed_rewrites
		//     are LINK/UNLINK/MUTATE, not ADD, and WF13 models a
		//     governance_proposal PROMOTING to a target, not the
		//     proposal's creation. We don't carry a WF here.)
		//
		//   - The MUTATE on firing_state is ALWAYS additive on first
		//     firing (field absent on the hook → pending → proposed).
		//     Additive MUTATE validation doesn't require a
		//     rewrite_category per operad.ValidateMUTATE. For future
		//     non-additive transitions (approver reactor: proposed →
		//     applied; reopens_at: applied → pending) a WF category
		//     will be needed. TODO(WF): either extend WF17 Reactive
		//     mutate_scope to include firing_state, or introduce a new
		//     WF21 "Reactive firing lifecycle". Pick when the approver
		//     reactor PR lands.
		envelopes = append(envelopes,
			graph.Envelope{
				RewriteType: graph.ADD,
				Actor:       actor,
				NodeURN:     proposalURN,
				TypeID:      "governance_proposal",
				Properties:  props,
			},
			graph.Envelope{
				RewriteType: graph.MUTATE,
				Actor:       actor,
				TargetURN:   n.URN,
				Field:       "firing_state",
				NewValue:    "proposed",
			},
		)
		emitted++
	}

	return envelopes
}

// firingStateOf reads the t_hook's firing_state property. Returns "" when
// the property is absent — callers should treat empty-string as pending
// (the v3.11 default) to keep pre-v3.11 hooks working.
func firingStateOf(n graph.Node) string {
	p, ok := n.Properties["firing_state"]
	if !ok || p.Value == nil {
		return ""
	}
	s, _ := p.Value.(string)
	return s
}

// slugFromURN returns the final colon-delimited segment of a URN. Used to
// compose a readable proposal URN suffix; the full URN is fine for the
// source_t_hook_urn property, but the slug keeps the proposal URN short.
func slugFromURN(urn graph.URN) string {
	s := string(urn)
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s
	}
	return s[i+1:]
}

// ----------------------------------------------------------------------------
// Runtime integration: the goroutine loop and manual-trigger helper
// ----------------------------------------------------------------------------

// sweepActorMu guards the sweepActor field on Runtime. Gemini flagged the
// bare read in SweepTick as a data race against SetSweepActor; in practice
// the race is benign (URN is a string and the runtime is single-writer at
// startup), but Go's race detector would catch it and it costs nothing to
// fix (PR #12 review — Gemini).
var sweepActorMu sync.RWMutex

// SetSweepActor overrides the actor URN used by the sweep goroutine.
// Thread-safe: may be called at any point in the runtime lifecycle.
// If never called, DefaultSweepActor is used at tick time.
func (rt *Runtime) SetSweepActor(actor graph.URN) {
	sweepActorMu.Lock()
	defer sweepActorMu.Unlock()
	rt.sweepActor = actor
}

// sweepActorLocked reads the current sweep actor under the package mutex.
// Returns the default when the runtime's field is empty.
func (rt *Runtime) sweepActorLocked() graph.URN {
	sweepActorMu.RLock()
	defer sweepActorMu.RUnlock()
	if rt.sweepActor == "" {
		return DefaultSweepActor
	}
	return rt.sweepActor
}

// RunTimedSweep runs the sweep at the given interval until ctx is canceled.
// An interval <= 0 disables the sweep (function returns immediately after
// logging); pass 0 via --sweep-interval to opt out without recompiling.
//
// The goroutine ticks on a time.Ticker and calls (*Runtime).SweepTick on
// each tick. Shutdown is clean on ctx.Done — the deferred ticker.Stop
// releases the runtime timer.
func (rt *Runtime) RunTimedSweep(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("sweep: disabled (interval=%v)", interval)
		return
	}
	log.Printf("sweep: starting (interval=%v)", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("sweep: shutdown")
			return
		case <-ticker.C:
			rt.SweepTick()
		}
	}
}

// SweepTick performs one sweep pass against the current runtime state.
// Exported so tests and operators can trigger a manual sweep without the
// goroutine — useful for time-travel debugging ("what would fire at T=X?").
//
// Errors from the underlying ApplyProgram are logged and not propagated:
// the next tick will retry. A batch failure leaves state unchanged
// (ApplyProgram is all-or-nothing).
func (rt *Runtime) SweepTick() {
	actor := rt.sweepActorLocked()
	state := rt.State()
	envelopes := SweepOnce(state, CurrentSweepTDay(), actor, rt.logSeq.Load(), time.Now())
	if len(envelopes) == 0 {
		return
	}
	if _, err := rt.ApplyProgram(envelopes); err != nil {
		log.Printf("sweep: ApplyProgram failed for %d envelopes: %v", len(envelopes), err)
	}
}
