package kernel

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moos/kernel/internal/fold"
	"moos/kernel/internal/graph"
	"moos/kernel/internal/hdc"
	"moos/kernel/internal/operad"
	"moos/kernel/internal/reactive"
	"moos/kernel/internal/tday"
)

const (
	subscriberBufferSize        = 64
	driftAnnotationSemanticPort = "{semantic}"
	driftAnnotationContractURN  = graph.URN("urn:moos:contract:hdc-drift-annotation.v1")
)

// Runtime is the kernel — the effect layer wrapping the pure catamorphism.
// It holds the current graph state (derived), the append-only log (truth),
// the operad registry (grammar), and the subscriber map (pub/sub).
//
// state(t) = fold(log[0..t])  (CI-4)
//
// All writes go through Apply or ApplyProgram.
// Reads are lock-free via State() snapshot.
type Runtime struct {
	mu         sync.RWMutex
	state      graph.GraphState
	log        []graph.PersistedRewrite
	store      Store
	registry   *operad.Registry
	hdcIndex   *hdc.LiveIndex
	hdcEncoder *hdc.Encoder

	subscriberMu sync.Mutex
	subscribers  map[string]chan graph.PersistedRewrite

	logSeq atomic.Int64

	// preSeqEntries counts replayed entries that predate the log_seq field
	// (deserialize to LogSeq 0; real seqs start at 1). Set once at replay,
	// immutable after — new applies always stamp a real seq. Consumers
	// subtract it from log_len before drift classification (moos-kernel#45).
	preSeqEntries int

	// sweepActor is the actor URN the time-driven sweep uses when emitting
	// governance_proposal envelopes. Empty value → DefaultSweepActor. See
	// SetSweepActor / RunTimedSweep in sweep.go.
	sweepActor graph.URN
}

// NewRuntime creates a Runtime by replaying the full store into memory.
// All future Apply calls will be atomic: validate → fold → log → broadcast.
func NewRuntime(store Store, registry *operad.Registry) (*Runtime, error) {
	entries, err := store.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("kernel: read store: %w", err)
	}

	state, err := fold.Replay(entries)
	if err != nil {
		return nil, fmt.Errorf("kernel: replay: %w", err)
	}

	rt := &Runtime{
		state:       state,
		log:         entries,
		store:       store,
		registry:    registry,
		hdcIndex:    hdc.NewLiveIndex(0.3),
		hdcEncoder:  hdc.NewEncoder(),
		subscribers: make(map[string]chan graph.PersistedRewrite),
	}
	// Seed the sequence counter from the MAX persisted log_seq, not the last
	// entry's: after a multi-writer incident the tail can carry a re-appended
	// block whose seq values are behind the true maximum, and last-entry
	// seeding would mint duplicates forever after (moos-kernel#40). Duplicate
	// seq values found during the scan are a log-integrity signal — warn, but
	// keep replaying: fold.Replay already absorbs idempotent re-applies.
	//
	// Entries with LogSeq == 0 predate the log_seq field entirely (early-2026
	// seed lines carry only {"envelope":...}; real seqs start at 1). They are
	// pre-seq-era legacy, NOT duplicates — counting them as duplicates was
	// the moos-kernel#45 false positive. Track them separately so /healthz
	// consumers can subtract them from log_len before drift classification.
	var maxSeq int64
	var preSeq int   // entries persisted before the log_seq field existed
	var zeroTail int // zero-seq entries AFTER the first real seq — anomalous, not legacy
	sawReal := false // legacy seed lines only ever exist as a leading prefix
	seen := make(map[int64]int, len(entries))
	var dupSeqs []int64 // distinct real seq values that appear more than once
	for _, e := range entries {
		if e.LogSeq == 0 {
			preSeq++
			if sawReal {
				zeroTail++
			}
			continue
		}
		sawReal = true
		if e.LogSeq > maxSeq {
			maxSeq = e.LogSeq
		}
		seen[e.LogSeq]++
		if seen[e.LogSeq] == 2 {
			dupSeqs = append(dupSeqs, e.LogSeq)
		}
	}
	rt.logSeq.Store(maxSeq)
	rt.preSeqEntries = preSeq
	if len(dupSeqs) > 0 {
		excess := len(entries) - preSeq - len(seen)
		sample := dupSeqs
		if len(sample) > 8 {
			sample = sample[:8]
		}
		log.Printf("kernel: log integrity warning: %d log_seq values appear more than once (%d excess entries, e.g. %v) — multi-writer artifact, see moos-kernel#40; log_len=%d max_log_seq=%d",
			len(dupSeqs), excess, sample, len(entries), maxSeq)
	}
	if zeroTail > 0 {
		// A missing seq after real seqs began is corruption / a partial write /
		// a manual edit — never a legacy seed line. Still counted in
		// preSeqEntries so the healthz arithmetic stays honest, but loudly
		// flagged instead of silently classified benign (Copilot catch on #46).
		log.Printf("kernel: log integrity warning: %d entry(ies) missing log_seq AFTER real seqs began — not legacy seed lines; inspect the jsonl (moos-kernel#45); log_len=%d max_log_seq=%d",
			zeroTail, len(entries), maxSeq)
	}
	if preSeq-zeroTail > 0 {
		log.Printf("kernel: replayed %d pre-log_seq-era entries (no seq field; legacy seed lines) — benign, see moos-kernel#45; log_len=%d max_log_seq=%d",
			preSeq-zeroTail, len(entries), maxSeq)
	}
	rt.hdcIndex.Recompute(state, nil)
	return rt, nil
}

// Apply validates and applies one Envelope atomically.
// Order: §M11 liveness gate → operad validate → fold.Evaluate → lock → persist → broadcast.
func (rt *Runtime) Apply(env graph.Envelope) (graph.EvalResult, error) {
	return rt.applyWithOptions(env, applyOptions{})
}

// applyOptions holds the fine-grained switches for rt.applyWithOptions. The
// public Apply API hard-codes defaults; internal bootstrap paths (SeedIfAbsent,
// reactive proposals) can tune behavior without widening the exported
// surface.
type applyOptions struct {
	// skipLiveness bypasses the §M11 session-liveness check. Used exclusively
	// by SeedIfAbsent during bootstrap: the first ADDs for user, workstation,
	// and kernel nodes run before any session exists, so requiring occupancy
	// would deadlock the bootstrap. See §M11 doctrine and
	// kb/research/kernel/20260421-t171-m11-m12-implementation-plan.md §2.
	skipLiveness bool
}

func (rt *Runtime) applyWithOptions(env graph.Envelope, opts applyOptions) (graph.EvalResult, error) {
	// §M11 liveness gate. Before operad validation so a rewrite with no
	// session context fails fast without paying the structural-validation
	// cost. The check is registry-aware: registry-less mode passes through.
	//
	// checkLiveness reads rt.state maps, so we hold the read-lock for its
	// duration. Release before the write-lock below to avoid lock upgrade
	// (Go's sync.RWMutex does not support upgrade). There is a tiny window
	// between RUnlock and Lock where another goroutine could advance
	// state — that's fine: checkLiveness' output is an assertion about
	// state at the time of observation, not a guarantee about the moment
	// the rewrite actually lands. The fold.Evaluate under the write-lock
	// is the authoritative apply-time check for structural invariants.
	if !opts.skipLiveness {
		rt.mu.RLock()
		err := rt.checkLiveness(env)
		rt.mu.RUnlock()
		if err != nil {
			return graph.EvalResult{}, err
		}
	}

	// Operad validation (type system — read-lock not needed, registry is read-only)
	if err := rt.validate(env); err != nil {
		return graph.EvalResult{}, err
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	// For MUTATE, pass the current node to operad for authority check
	if env.RewriteType == graph.MUTATE && rt.registry != nil {
		node, ok := rt.state.Nodes[env.TargetURN]
		if ok {
			if err := rt.registry.ValidateMUTATE(env, node); err != nil {
				return graph.EvalResult{}, err
			}
			// Additive MUTATE: inject PropertySpec for fields declared in ontology but not yet on node.
			// This allows new optional properties (added in later ontology versions) to be set on existing nodes.
			env = rt.injectPropertySpec(env, node)
		}
	}

	// Strata enforcement (M5): validate LINK direction against filtration rules.
	if env.RewriteType == graph.LINK && rt.registry != nil {
		if err := rt.registry.ValidateStrataLink(env, rt.state); err != nil {
			return graph.EvalResult{}, err
		}
		// WF21 (round-15 v314-3-wf21-causes) acyclicity check. Other LINK
		// rewrite-categories pass through; only WF21 enforces DAG semantics.
		if err := rt.registry.ValidateCausalAcyclic(env, rt.state); err != nil {
			return graph.EvalResult{}, err
		}
	}

	// Gate check (M8): fail-closed if any gate node guards the affected node and its predicate fails.
	if err := rt.checkGatesLocked(env); err != nil {
		return graph.EvalResult{}, err
	}

	now := time.Now().UTC()
	next, result, err := fold.EvaluateAt(rt.state, env, now)
	if err != nil {
		return graph.EvalResult{}, err
	}

	seq := rt.logSeq.Add(1)
	persisted := graph.PersistedRewrite{
		Envelope:  env,
		AppliedAt: now,
		Timestamp: now,
		LogSeq:    seq,
	}
	if err := rt.store.Append([]graph.PersistedRewrite{persisted}); err != nil {
		return graph.EvalResult{}, fmt.Errorf("kernel: persist: %w", err)
	}

	// §M13: resolve session against PRE-apply state (rt.state still
	// holds the pre-fold view here). This handles envelopes that rotate
	// has-occupant — e.g. UNLINK has-occupant + LINK has-occupant on the
	// same actor — where post-apply resolution would diverge from the
	// session §M11 validated against. Captured here, applied after commit.
	bumpSessionURN, bumpOK := rt.resolveSessionForBump(env)

	rt.state = next
	rt.log = append(rt.log, persisted)
	if bumpOK {
		rt.bumpSessionLocalT(bumpSessionURN)
	}
	rt.broadcast(persisted)
	rt.runReactive(persisted)
	rt.runHDCIndexAndDriftLocked(env)

	return result, nil
}

// ApplyProgram applies a slice of Envelopes atomically.
// All or nothing: if any step fails, no state change and nothing is persisted.
// §M11 liveness is checked per-envelope before structural validation so a
// single session-less envelope fails the whole batch fast.
func (rt *Runtime) ApplyProgram(envelopes []graph.Envelope) ([]graph.EvalResult, error) {
	// Preflight under read-lock: §M11 (emitter-context) checks read rt.state
	// maps, so we hold the read-lock across the entire batch preflight. §M11
	// is evaluated against the batch-initial state — emitter references
	// cannot depend on prior envelopes in the batch (see impl-plan §2.4).
	// §M12 is NOT checked here; it runs per-envelope inside the write-locked
	// working-state loop below so target-classification sees mid-batch ADDs.
	rt.mu.RLock()
	for _, env := range envelopes {
		if err := rt.checkLivenessM11(env, rt.state); err != nil {
			rt.mu.RUnlock()
			return nil, err
		}
		if err := rt.validate(env); err != nil {
			rt.mu.RUnlock()
			return nil, err
		}
	}
	rt.mu.RUnlock()

	rt.mu.Lock()
	defer rt.mu.Unlock()

	// Walk the batch, maintaining a working state as we go. Each envelope's
	// PropertySpec injection, strata enforcement, and gate check runs against
	// the state AS IT WOULD BE after the prior envelopes in this batch have
	// been applied. This lets valid sequential patterns land (e.g. ADD an
	// approval node, then MUTATE the gated node in the same program) where
	// the old pre-batch-only validation incorrectly rejected them (PR #8
	// review, Copilot).
	//
	// TODO(perf): the gate check inside this loop is O(envelopes × R) where
	// R is the relation count. For large batches on large graphs, maintain
	// an index of guarded nodes so we can skip the inner scan when no gates
	// touch the target set (PR #8 review, Gemini).
	injected := make([]graph.Envelope, len(envelopes))
	baseNow := time.Now().UTC()
	appliedAt := make([]time.Time, len(envelopes))
	for i := range envelopes {
		appliedAt[i] = baseNow.Add(time.Duration(i) * time.Nanosecond)
	}
	workingState := rt.state.Clone()
	for i, env := range envelopes {
		if env.RewriteType == graph.MUTATE && rt.registry != nil {
			if node, ok := workingState.Nodes[env.TargetURN]; ok {
				env = rt.injectPropertySpec(env, node)
			}
		}
		injected[i] = env

		// §M12 admin-scope gate against working state. Per-envelope so a
		// MUTATE on a node ADDed in an earlier envelope of this batch sees
		// the node's type when classifying. Closes the PR 31 Gemini
		// security-HIGH where initial-state preflight let a batch ADD a
		// target and then MUTATE its kernel-authority property without
		// triggering the admin check. §M11 preflight already ran above;
		// if we reach here the emitter-context check has passed.
		if err := rt.checkLivenessM12(env, workingState); err != nil {
			return nil, err
		}

		// Strata enforcement (M5) + Gate check (M8) against the working state.
		if env.RewriteType == graph.LINK && rt.registry != nil {
			if err := rt.registry.ValidateStrataLink(env, workingState); err != nil {
				return nil, err
			}
			// WF21 acyclicity (round-15) checked against working state so
			// intra-batch causes-LINKs can't close a cycle even when
			// individual envelopes pass against pre-batch state.
			if err := rt.registry.ValidateCausalAcyclic(env, workingState); err != nil {
				return nil, err
			}
		}
		if err := rt.checkGatesAgainstState(env, &workingState); err != nil {
			return nil, err
		}

		// Advance the working state by applying this envelope before
		// validating the next one. On failure bail early — ApplyProgram
		// is all-or-nothing so a later error still rolls back cleanly.
		next, _, err := fold.EvaluateAt(workingState, env, appliedAt[i])
		if err != nil {
			return nil, err
		}
		workingState = next
	}
	envelopes = injected

	nextState, results, err := fold.EvaluateProgramAt(rt.state, envelopes, appliedAt)
	if err != nil {
		return nil, err
	}

	persisted := make([]graph.PersistedRewrite, len(envelopes))
	for i, env := range envelopes {
		seq := rt.logSeq.Add(1)
		ts := appliedAt[i]
		persisted[i] = graph.PersistedRewrite{
			Envelope:  env,
			AppliedAt: ts,
			Timestamp: ts,
			LogSeq:    seq,
		}
	}

	if err := rt.store.Append(persisted); err != nil {
		return nil, fmt.Errorf("kernel: persist program: %w", err)
	}

	// §M13: capture sessions to bump against the BATCH-INITIAL state
	// (rt.state before commit). Mirrors §M11 preflight semantics
	// (checkLivenessM11 also runs against pre-batch state) so a batch
	// that itself rotates has-occupant still credits the originating
	// session, not whatever has-occupant points at after the batch.
	sessionsToBump := make(map[graph.URN]bool)
	for _, env := range envelopes {
		res := operad.ResolveSessionForEnvelope(rt.state, env)
		switch res.Kind {
		case operad.ResolveSessionExplicit,
			operad.ResolveSessionInferred,
			operad.ResolveSessionActorIsSession:
			sessionsToBump[res.SessionURN] = true
		}
	}

	rt.state = nextState
	rt.log = append(rt.log, persisted...)
	// §M13: bump local_t once per unique resolved session in the batch.
	// Dedupe by session URN (not by actor) so a single batch from one
	// agent to one session counts as one tick, while multi-session
	// batches tick each session once. Closes the session-actor-agent-
	// lookup sub-program from T=168.
	for sessionURN := range sessionsToBump {
		rt.bumpSessionLocalT(sessionURN)
	}
	for _, p := range persisted {
		rt.broadcast(p)
	}
	if len(envelopes) > 0 {
		rt.runHDCIndexAndDriftLocked(envelopes[len(envelopes)-1])
	}
	return results, nil
}

// SeedIfAbsent is Apply that silently absorbs ErrNodeExists and ErrRelationExists.
// Used for idempotent bootstrap seeding of infrastructure nodes.
//
// §M11 bypass: seed envelopes skip the liveness gate. Reason: bootstrap runs
// before any session exists, so requiring has-occupant would deadlock the
// first run. This is the only exception; operad.SystemInternalEnvelope
// catches the post-bootstrap kernel-actor emissions that should also pass
// the gate without bypass.
func (rt *Runtime) SeedIfAbsent(env graph.Envelope) error {
	_, err := rt.applyWithOptions(env, applyOptions{skipLiveness: true})
	if err == nil {
		return nil
	}
	if errors.Is(err, fold.ErrNodeExists) || errors.Is(err, fold.ErrRelationExists) {
		return nil
	}
	return err
}

// --- InspectKernel ---

func (rt *Runtime) State() graph.GraphState {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.state.Clone()
}

func (rt *Runtime) Node(urn graph.URN) (graph.Node, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	n, ok := rt.state.Nodes[urn]
	return n, ok
}

func (rt *Runtime) Nodes() []graph.Node {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	nodes := make([]graph.Node, 0, len(rt.state.Nodes))
	for _, n := range rt.state.Nodes {
		nodes = append(nodes, n)
	}
	return nodes
}

func (rt *Runtime) Relation(urn graph.URN) (graph.Relation, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	r, ok := rt.state.Relations[urn]
	return r, ok
}

func (rt *Runtime) Relations() []graph.Relation {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	rels := make([]graph.Relation, 0, len(rt.state.Relations))
	for _, r := range rt.state.Relations {
		rels = append(rels, r)
	}
	return rels
}

func (rt *Runtime) RelationsSrc(urn graph.URN) []graph.Relation {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var out []graph.Relation
	for _, r := range rt.state.Relations {
		if r.SrcURN == urn {
			out = append(out, r)
		}
	}
	return out
}

func (rt *Runtime) RelationsTgt(urn graph.URN) []graph.Relation {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var out []graph.Relation
	for _, r := range rt.state.Relations {
		if r.TgtURN == urn {
			out = append(out, r)
		}
	}
	return out
}

func (rt *Runtime) Log() []graph.PersistedRewrite {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	cp := make([]graph.PersistedRewrite, len(rt.log))
	copy(cp, rt.log)
	return cp
}

func (rt *Runtime) LogLen() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.log)
}

// MaxLogSeq returns the highest log_seq STAMPED so far. Interpreting the
// delta against LogLen: log_len > max_log_seq means the replayed log carries
// multi-writer duplicate entries (moos-kernel#40; the delta is the cumulative
// duplicate count); log_len < max_log_seq means seqs were stamped for
// rewrites whose persist failed (the counter is not rolled back on Append
// error — the resulting seq gap is benign, max-seeding tolerates it).
func (rt *Runtime) MaxLogSeq() int64 {
	return rt.logSeq.Load()
}

// LogStats returns LogLen and MaxLogSeq as one consistent snapshot — both
// read under the same lock, so a poller (e.g. /healthz) never observes a
// mid-batch skew between the two.
func (rt *Runtime) LogStats() (logLen int, maxLogSeq int64) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.log), rt.logSeq.Load()
}

// LogSeqMissing returns the count of replayed entries that predate the
// log_seq field (moos-kernel#45). Drift interpretation:
// log_len - LogSeqMissing() == max_log_seq is a clean log; only the
// remainder beyond that is multi-writer duplication (moos-kernel#40).
// Immutable after replay — no lock needed.
func (rt *Runtime) LogSeqMissing() int {
	return rt.preSeqEntries
}

// HDCTypeExpressions returns a snapshot of the live in-memory type-expression index.
func (rt *Runtime) HDCTypeExpressions() []hdc.TypeExpressionEntry {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if rt.hdcIndex == nil {
		return nil
	}
	return rt.hdcIndex.Expressions()
}

// --- ObservableKernel ---

func (rt *Runtime) Subscribe(id string) <-chan graph.PersistedRewrite {
	rt.subscriberMu.Lock()
	defer rt.subscriberMu.Unlock()
	ch := make(chan graph.PersistedRewrite, subscriberBufferSize)
	rt.subscribers[id] = ch
	return ch
}

func (rt *Runtime) Unsubscribe(id string) {
	rt.subscriberMu.Lock()
	defer rt.subscriberMu.Unlock()
	if ch, ok := rt.subscribers[id]; ok {
		close(ch)
		delete(rt.subscribers, id)
	}
}

func (rt *Runtime) broadcast(pr graph.PersistedRewrite) {
	rt.subscriberMu.Lock()
	defer rt.subscriberMu.Unlock()
	for _, ch := range rt.subscribers {
		select {
		case ch <- pr:
		default:
			// Slow subscriber: drop rather than block the kernel
		}
	}
}

// runReactive evaluates the Watch/React/Guard engine against a just-applied rewrite
// and routes the resulting firings. Called with rt.mu already held.
// Proposals are processed at depth-1 only — reactive results do not trigger further reactions.
//
// Routing (issue #53, Sam ruling t250-Q4 "govern it — parity with TIME"):
//   - t_hook firings (M6 event pathway) are STAGED as governance_proposals,
//     exactly like the time-driven sweep — never applied immediately.
//   - watcher/reactor firings (WF17 Pass 1) keep the immediate-apply path.
//     Same F-a class as the pre-#53 t_hook fast path — flagged as follow-up,
//     out of #53's ruling scope.
func (rt *Runtime) runReactive(trigger graph.PersistedRewrite) {
	// Snapshot state at the time of the trigger for engine evaluation.
	snapshot := rt.state
	eng := reactive.Engine{State: &snapshot}
	firings := eng.EvaluateDetailed(trigger)

	for i, f := range firings {
		switch f.Kind {
		case reactive.FiringTHook:
			rt.stageEventHookLocked(f, trigger, i)
		case reactive.FiringReactor: // WF17 watcher/reactor chain, unchanged.
			rt.applyReactiveLocked(f.Proposal)
		default:
			// Fail-closed: an unknown firing kind must never silently take
			// the immediate-apply path (Copilot catch, PR #56). Drop it —
			// a new kind gets an explicit routing decision here first.
			log.Printf("kernel: runReactive: dropping firing with unknown kind %q (source %s)", f.Kind, f.SourceHook)
		}
	}
}

// stageEventHookLocked stages a governance_proposal for an event-fired t_hook
// (M6 pathway) instead of applying its react_template immediately — parity
// with the TIME sweep (issue #53). Called with rt.mu already held.
//
// The staged proposed_envelope is the SUBSTITUTED template (the exact
// envelope an approver would apply — $matched_urn/$actor already resolved),
// unlike the sweep's verbatim snapshot: the substitutions only exist on the
// event pathway. It is stored as a plain map (JSON round-trip of the built
// envelope), NOT as a typed graph.Envelope — replay deserializes property
// values to map[string]any, so storing the struct live would make CI-4 hold
// only up to JSON equivalence instead of Go-value equivalence.
//
// Idempotency parity with SweepOnce: only hooks with firing_state ∈
// {"", "pending"} stage. Event hooks can match many rewrites; without this
// check every matching trigger would re-stage a duplicate proposal.
//
// Gate parity (M8): both staging envelopes are pre-flighted through
// checkGatesLocked BEFORE either is applied — a gate freezing the hook
// blocks the entire staging pair fail-closed, exactly like the sweep's
// all-or-nothing ApplyProgram batch.
//
// The two staging envelopes are applied sequentially under the held write
// lock (the sweep gets ApplyProgram atomicity; this path cannot re-enter
// ApplyProgram without deadlocking). applyStagedLocked mirrors the sweep's
// governed-path checks (operad structural validation + fold; NO
// ValidateMUTATE — ApplyProgram never calls it either, and requiring a
// rewrite_category here would reject staging for hooks that carry an
// explicit firing_state, silently defeating the idempotency filter). If the
// proposal ADD does not land, the MUTATE is skipped and the hook stays
// pending — the next matching trigger retries.
//
// firingIdx discriminates multiple hooks firing on the SAME trigger: two
// hooks on one owner whose URNs share a final slug segment would otherwise
// compute identical proposal URNs and the loser would be dropped with no
// retry clock (the sweep's per-tick -n counter is the same device).
func (rt *Runtime) stageEventHookLocked(f reactive.Firing, trigger graph.PersistedRewrite, firingIdx int) {
	hook, ok := rt.state.Nodes[f.SourceHook]
	if !ok {
		return
	}
	if fs := firingStateOf(hook); fs != "" && fs != "pending" {
		return
	}

	currentT := tday.Now()
	nowStr := time.Now().UTC().Format(time.RFC3339)
	// URN scheme mirrors the sweep's (kernel.<slug>-t<T>-tick<seq>-n<i>):
	// evt<seq> is unique per trigger, -n<i> per firing within the trigger.
	proposalURN := graph.URN(fmt.Sprintf("urn:moos:proposal:kernel.%s-t%d-evt%d-n%d", slugFromURN(hook.URN), currentT, trigger.LogSeq, firingIdx))

	// Normalize the substituted envelope to its JSON map form so the live
	// property value is Go-value-identical to what replay reconstructs.
	var proposedEnvelope any
	if raw, err := json.Marshal(f.Proposal); err == nil {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			proposedEnvelope = m
		}
	}
	if proposedEnvelope == nil {
		return // cannot represent the proposal — stage nothing, hook stays pending
	}

	pair := stageHookProposalPair(hook, currentT, rt.sweepActorLocked(), proposalURN, nowStr, proposedEnvelope)

	// The firing_state MUTATE is additive on first firing (field absent on
	// the hook) — fold requires an injected PropertySpec for that. Registry
	// mode injects the declared t_hook.firing_state spec (same as the
	// sweep's ApplyProgram path); registry-less mode falls back to the
	// v3.11-declared shape (mutable, kernel scope) so staging works in
	// dev/test kernels too. When firing_state is already on the hook the
	// fold takes the standard MUTATE path and needs no spec.
	pair[1] = rt.injectPropertySpec(pair[1], hook)
	if pair[1].PropertySpec == nil {
		if _, hasProp := hook.Properties["firing_state"]; !hasProp {
			pair[1].PropertySpec = &graph.Property{Mutability: "mutable", AuthorityScope: "kernel", StratumOrigin: 2}
		}
	}

	// M8 gate pre-flight on BOTH envelopes before applying either —
	// fail-closed parity with the sweep's all-or-nothing batch.
	if err := rt.checkGatesLocked(pair[0]); err != nil {
		return
	}
	if err := rt.checkGatesLocked(pair[1]); err != nil {
		return
	}

	if !rt.applyStagedLocked(pair[0]) {
		return // proposal ADD did not land — hook stays pending, next trigger retries
	}
	rt.applyStagedLocked(pair[1])
}

// applyStagedLocked applies a kernel-internal STAGING envelope while the
// write lock is already held. Mirrors the sweep's governed-path checks for
// its staging pair: operad structural validation + fold + persist. It does
// NOT call ValidateMUTATE — neither does ApplyProgram (the sweep's path);
// the staging MUTATE (firing_state) deliberately carries no
// rewrite_category (see stageHookProposalPair), and ValidateMUTATE's
// standard path would reject it whenever the hook already carries
// firing_state, silently defeating the idempotency filter. Gate checks are
// the caller's responsibility (pre-flighted on the whole pair).
func (rt *Runtime) applyStagedLocked(env graph.Envelope) bool {
	if err := rt.validate(env); err != nil {
		return false
	}

	now := time.Now().UTC()
	next, _, err := fold.EvaluateAt(rt.state, env, now)
	if err != nil {
		return false
	}

	seq := rt.logSeq.Add(1)
	persisted := graph.PersistedRewrite{
		Envelope:  env,
		AppliedAt: now,
		Timestamp: now,
		LogSeq:    seq,
	}
	if err := rt.store.Append([]graph.PersistedRewrite{persisted}); err != nil {
		return false // persist failure: drop, don't corrupt in-memory state
	}

	rt.state = next
	rt.log = append(rt.log, persisted)
	rt.broadcast(persisted)
	return true
}

// applyReactiveLocked applies a single envelope while the write lock is already held.
// Used exclusively for kernel-internal reactive/staging emissions — does NOT trigger
// further reactive evaluation. Returns true iff the envelope landed (validated,
// folded, persisted).
func (rt *Runtime) applyReactiveLocked(env graph.Envelope) bool {
	// Operad validation (registry is read-only, no extra locking needed).
	if err := rt.validate(env); err != nil {
		return false // drop invalid reactive proposals silently
	}
	if env.RewriteType == graph.MUTATE && rt.registry != nil {
		if node, ok := rt.state.Nodes[env.TargetURN]; ok {
			if err := rt.registry.ValidateMUTATE(env, node); err != nil {
				return false
			}
		}
	}

	now := time.Now().UTC()
	next, _, err := fold.EvaluateAt(rt.state, env, now)
	if err != nil {
		return false // skip (e.g. ErrNodeExists for idempotent proposals)
	}

	seq := rt.logSeq.Add(1)
	persisted := graph.PersistedRewrite{
		Envelope:  env,
		AppliedAt: now,
		Timestamp: now,
		LogSeq:    seq,
	}
	if err := rt.store.Append([]graph.PersistedRewrite{persisted}); err != nil {
		return false // persist failure: drop, don't corrupt in-memory state
	}

	rt.state = next
	rt.log = append(rt.log, persisted)
	rt.broadcast(persisted)
	return true
}

// checkGatesLocked evaluates all gate nodes linked to the rewrite's affected
// nodes via "guards"/"guarded-by" relations. Returns an error if any gate
// predicate fails. Gate nodes live on the APPLY pathway (M8) — they block
// rewrites, distinct from guard nodes which live on the REACTIVE pathway
// and gate reactor proposals. Called with rt.mu already held.
//
// For LINK and UNLINK we check gates on BOTH endpoints (src and tgt), so a
// gate on either side blocks the operation. Previously only the src side
// was checked, which allowed a bypass where a gated node could still be
// linked/unlinked as the target (PR #8 review, Copilot).
//
// TODO(perf): this is an O(R) scan of all relations per rewrite. For large
// graphs with many gate relations, maintain a map[URN]struct{} index of
// nodes with at least one "guarded-by" inbound relation, updated during
// LINK/UNLINK (PR #8 review, Gemini + Copilot).
func (rt *Runtime) checkGatesLocked(env graph.Envelope) error {
	return rt.checkGatesAgainstState(env, &rt.state)
}

// checkGatesAgainstState is the state-parameterised form of checkGatesLocked.
// Exported-to-package so ApplyProgram can evaluate gates against an evolving
// working state (one envelope at a time) rather than the pre-batch state —
// prevents the earlier-ADD-then-later-gated-MUTATE valid pattern from being
// incorrectly rejected (PR #8 review, Copilot).
func (rt *Runtime) checkGatesAgainstState(env graph.Envelope, state *graph.GraphState) error {
	// Collect all node URNs affected by this rewrite. For LINK/UNLINK we
	// check gates on BOTH endpoints — a gate on either side blocks the op.
	var targets []graph.URN
	switch env.RewriteType {
	case graph.ADD:
		return nil // ADD creates a new node — no existing gates can guard it yet
	case graph.MUTATE:
		if env.TargetURN != "" {
			targets = append(targets, env.TargetURN)
		}
	case graph.LINK:
		if env.SrcURN != "" {
			targets = append(targets, env.SrcURN)
		}
		if env.TgtURN != "" && env.TgtURN != env.SrcURN {
			targets = append(targets, env.TgtURN)
		}
	case graph.UNLINK:
		if rel, ok := state.Relations[env.RelationURN]; ok {
			if rel.SrcURN != "" {
				targets = append(targets, rel.SrcURN)
			}
			if rel.TgtURN != "" && rel.TgtURN != rel.SrcURN {
				targets = append(targets, rel.TgtURN)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}

	// Build a set for O(1) membership check inside the relations loop.
	targetSet := make(map[graph.URN]struct{}, len(targets))
	for _, u := range targets {
		targetSet[u] = struct{}{}
	}

	eng := reactive.Engine{State: state}
	for _, rel := range state.Relations {
		// gate --guards--> targetNode (same port pair as guard→watcher in WF17)
		if rel.TgtPort != "guarded-by" {
			continue
		}
		if _, guarded := targetSet[rel.TgtURN]; !guarded {
			continue
		}
		gateNode, ok := state.Nodes[rel.SrcURN]
		if !ok {
			return fmt.Errorf("gate(M8): node %s referenced but not found — rewrite blocked (fail-closed)", rel.SrcURN)
		}
		if gateNode.TypeID != "gate" {
			continue // guard nodes (WF17 reactive) are ignored here
		}
		if !eng.EvaluatePredicate(gateNode) {
			predType := ""
			if p, ok := gateNode.Properties["predicate_type"]; ok {
				predType, _ = p.Value.(string)
			}
			return fmt.Errorf("gate(M8): gate %s predicate %q failed for %s — rewrite blocked",
				rel.SrcURN, predType, rel.TgtURN)
		}
	}
	return nil
}

// bumpSessionLocalT increments the local_t property on a session node after each
// kernel-acknowledged rewrite issued by that session (M1).
// Called with rt.mu already held.
// The increment is a kernel-internal MUTATE — it is persisted to the log and broadcast.
func (rt *Runtime) bumpSessionLocalT(sessionURN graph.URN) {
	actorNode, ok := rt.state.Nodes[sessionURN]
	if !ok || actorNode.TypeID != "session" {
		return
	}

	var currentT int64
	if prop, ok := actorNode.Properties["local_t"]; ok {
		switch v := prop.Value.(type) {
		case float64:
			currentT = int64(v)
		case int64:
			currentT = v
		case int:
			currentT = int64(v)
		}
	}

	kernelActor := rt.hdcActorURN(sessionURN)
	mutEnv := graph.Envelope{
		RewriteType:     graph.MUTATE,
		Actor:           kernelActor,
		RewriteCategory: graph.WF19,
		TargetURN:       sessionURN,
		Field:           "local_t",
		NewValue:        currentT + 1,
	}
	// Inject PropertySpec for additive MUTATE (local_t may not be on node yet).
	mutEnv = rt.injectPropertySpec(mutEnv, actorNode)

	now := time.Now().UTC()
	next, _, err := fold.EvaluateAt(rt.state, mutEnv, now)
	if err != nil {
		return // local_t not declared in ontology or other structural issue — skip
	}

	seq := rt.logSeq.Add(1)
	persisted := graph.PersistedRewrite{
		Envelope:  mutEnv,
		AppliedAt: now,
		Timestamp: now,
		LogSeq:    seq,
	}
	if err := rt.store.Append([]graph.PersistedRewrite{persisted}); err != nil {
		return
	}
	rt.state = next
	rt.log = append(rt.log, persisted)
	rt.broadcast(persisted)
}

// resolveSessionForBump returns the session URN whose local_t should
// increment for this envelope, regardless of whether env.Actor is itself
// a session, an agent occupying exactly one session (inferred), or
// accompanied by an explicit env.SessionURN.
//
// Returns ok=false when:
//   - actor occupies multiple sessions and env.SessionURN is empty
//     (ResolveSessionAmbiguous; gate rejects upstream — never reached
//     here in practice, but defensive)
//   - explicit env.SessionURN doesn't match has-occupant
//     (ResolveSessionExplicitMismatch; gate rejects upstream)
//   - actor occupies no session (ResolveSessionAbsent; system-internal
//     allowlist path — kernel-actor sweep emissions, etc. — not
//     session-scoped work, no bump warranted)
//
// Closes the §M13 sub-program `session-actor-agent-lookup` (T=168) by
// covering the agent-actor inferred-session path that the prior
// TypeID=="session" check skipped. See:
// ffs0/kb/research/kernel/20260417-t187-kernel-proper.md §M13.
//
// Called with rt.mu already held (state read).
func (rt *Runtime) resolveSessionForBump(env graph.Envelope) (graph.URN, bool) {
	res := operad.ResolveSessionForEnvelope(rt.state, env)
	switch res.Kind {
	case operad.ResolveSessionExplicit,
		operad.ResolveSessionInferred,
		operad.ResolveSessionActorIsSession:
		return res.SessionURN, true
	default:
		return "", false
	}
}

// runHDCIndexAndDriftLocked updates the in-memory HDC index and emits type-drift claims.
// Called with rt.mu already held.
func (rt *Runtime) runHDCIndexAndDriftLocked(trigger graph.Envelope) {
	if rt.hdcIndex == nil {
		return
	}

	switch trigger.RewriteType {
	case graph.ADD, graph.LINK, graph.UNLINK, graph.MUTATE:
		// continue
	default:
		return
	}

	rt.hdcIndex.Recompute(rt.state, rt.hdcEncoder)
	drifted := rt.hdcIndex.Drifted()
	if len(drifted) == 0 {
		return
	}

	actor := rt.hdcActorURN(trigger.Actor)
	now := time.Now().UTC().Format(time.RFC3339)

	for _, row := range drifted {
		if strings.HasPrefix(row.URN.String(), "urn:moos:claim:type-drift.") {
			continue
		}
		if row.DeclaredType == "claim" {
			continue
		}

		hash := shortHash(row.URN.String())
		claimURN := graph.URN("urn:moos:claim:type-drift." + hash)
		if _, exists := rt.state.Nodes[claimURN]; exists {
			continue
		}

		confidence := row.Drift
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}

		addClaim := graph.Envelope{
			RewriteType: graph.ADD,
			Actor:       actor,
			NodeURN:     claimURN,
			TypeID:      "claim",
			Properties: map[string]graph.Property{
				"text": {
					Value:      fmt.Sprintf("type drift detected for %s", row.URN),
					Mutability: "immutable",
				},
				"created_at": {
					Value:      now,
					Mutability: "immutable",
				},
				"source_ki_urn": {
					Value:      "urn:moos:ki:system.hdc-drift",
					Mutability: "immutable",
				},
				"confidence": {
					Value:          confidence,
					Mutability:     "mutable",
					AuthorityScope: "kernel",
				},
				"subject_urn": {
					Value:      row.URN.String(),
					Mutability: "immutable",
				},
				"declared_type": {
					Value:      string(row.DeclaredType),
					Mutability: "immutable",
				},
				"expressed_type": {
					Value:      string(row.ExpressedType),
					Mutability: "immutable",
				},
				"drift_score": {
					Value:          row.Drift,
					Mutability:     "mutable",
					AuthorityScope: "kernel",
				},
			},
		}
		rt.applyReactiveLocked(addClaim)

		relURN := graph.URN("urn:moos:rel:type-drift." + hash + ".annotation")
		if _, exists := rt.state.Relations[relURN]; exists {
			continue
		}

		linkClaim := graph.Envelope{
			RewriteType: graph.LINK,
			// Re-home drift annotations to WF15 open-semantic links.
			// WF11 only declares synced-via/sync-target; the old tagged/tagged-in
			// pair was rejected by the declaration gate (moos-kernel#52).
			RewriteCategory: graph.WF15,
			Actor:           actor,
			RelationURN:     relURN,
			SrcURN:          claimURN,
			SrcPort:         driftAnnotationSemanticPort,
			TgtURN:          row.URN,
			TgtPort:         driftAnnotationSemanticPort,
			ContractURN:     driftAnnotationContractURN,
		}
		rt.applyReactiveLocked(linkClaim)
	}

	rt.hdcIndex.Recompute(rt.state, rt.hdcEncoder)
}

func (rt *Runtime) hdcActorURN(defaultActor graph.URN) graph.URN {
	if strings.HasPrefix(defaultActor.String(), "urn:moos:kernel:") {
		return defaultActor
	}

	kernels := make([]string, 0)
	for urn, node := range rt.state.Nodes {
		if node.TypeID == "kernel" {
			kernels = append(kernels, urn.String())
		}
	}
	if len(kernels) == 0 {
		return defaultActor
	}
	sort.Strings(kernels)
	for _, urn := range kernels {
		if strings.HasSuffix(urn, ".primary") {
			return graph.URN(urn)
		}
	}
	return graph.URN(kernels[0])
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// validate dispatches to the appropriate operad validator by rewrite type.
func (rt *Runtime) validate(env graph.Envelope) error {
	if rt.registry == nil {
		return nil
	}
	switch env.RewriteType {
	case graph.ADD:
		return rt.registry.ValidateADD(env)
	case graph.LINK:
		return rt.registry.ValidateLINK(env)
	case graph.UNLINK:
		return rt.registry.ValidateUNLINK(env)
	case graph.MUTATE:
		// MUTATE needs the current node — done inline in Apply with lock held
		return nil
	}
	return nil
}

// injectPropertySpec handles additive MUTATE: if the target field is not on the node but IS
// declared as mutable in the ontology type definition, inject the PropertySpec so fold can create it.
// This allows new optional properties (added in later ontology versions) to be set on existing nodes.
//
// Safe to call when rt.registry is nil (no-op return); callers such as
// bumpSessionLocalT invoke this regardless of whether the kernel was started
// with --ontology, so the nil-guard prevents a panic in registry-less mode.
func (rt *Runtime) injectPropertySpec(env graph.Envelope, node graph.Node) graph.Envelope {
	if env.PropertySpec != nil {
		return env // already set (e.g. during replay)
	}
	if rt.registry == nil {
		return env // registry-less mode — nothing to inject from
	}
	if _, hasProp := node.Properties[env.Field]; hasProp {
		return env // field already on node — standard MUTATE path
	}
	typeSpec, hasType := rt.registry.NodeTypes[node.TypeID]
	if !hasType {
		return env
	}
	pspec, hasPspec := typeSpec.Properties[env.Field]
	if !hasPspec || pspec.Mutability != "mutable" {
		return env
	}
	env.PropertySpec = &graph.Property{
		Mutability:     pspec.Mutability,
		AuthorityScope: pspec.AuthorityScope,
		StratumOrigin:  2,
	}
	return env
}
