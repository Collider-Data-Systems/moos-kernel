package fold

import (
	"fmt"
	"time"

	"moos/kernel/internal/graph"
)

// EvaluateProgram applies a slice of Envelopes atomically.
// If any Envelope fails, the entire program is rolled back —
// the original state is returned unchanged.
// On success, returns the new state and per-envelope results.
func EvaluateProgram(state graph.GraphState, envelopes []graph.Envelope) (graph.GraphState, []graph.EvalResult, error) {
	return EvaluateProgramAt(state, envelopes, nil)
}

// EvaluateProgramAt is EvaluateProgram with optional caller-supplied timestamps.
// If appliedAt[i] is present, it is used as the fold-time timestamp for
// envelope i (node/relation CreatedAt on ADD/LINK). Otherwise wall clock is
// used for backwards compatibility.
func EvaluateProgramAt(state graph.GraphState, envelopes []graph.Envelope, appliedAt []time.Time) (graph.GraphState, []graph.EvalResult, error) {
	if len(appliedAt) != 0 && len(appliedAt) != len(envelopes) {
		return state, nil, fmt.Errorf("program appliedAt length mismatch: got %d, want 0 or %d", len(appliedAt), len(envelopes))
	}
	useAppliedAt := len(appliedAt) == len(envelopes)

	working := state.Clone()
	results := make([]graph.EvalResult, 0, len(envelopes))

	for i, env := range envelopes {
		ts := time.Now().UTC()
		if useAppliedAt {
			ts = appliedAt[i]
		}
		result, err := evaluateInPlace(&working, env, ts)
		if err != nil {
			return state, nil, fmt.Errorf("program step %d (%s): %w", i, env.RewriteType, err)
		}
		results = append(results, result)
	}
	return working, results, nil
}

// ProgramResult is returned by kernel.Runtime.ApplyProgram.
type ProgramResult struct {
	Results   []graph.EvalResult       `json:"results"`
	Persisted []graph.PersistedRewrite `json:"persisted"`
	AppliedAt time.Time                `json:"applied_at"`
}
