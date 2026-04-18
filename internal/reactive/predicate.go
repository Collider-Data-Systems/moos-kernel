package reactive

import (
	"encoding/json"
	"fmt"

	"moos/kernel/internal/graph"
)

// ThookPredicate is the evaluable shape stored in t_hook.predicate (and in
// future gate / view_filter nodes). Discriminated by Kind.
//
// Supported kinds (§M14 catalog subset — the predicates the T=168 round-8
// delivery-clock t_hooks actually use):
//
//	fires_at  : {kind: "fires_at",  t: N}        — true when currentT >= N
//	closes_at : {kind: "closes_at", t: N}        — identical to fires_at; paired label for delivery-window close
//	after_urn : {kind: "after_urn", urn: U, prop: F, value: V}
//	                                             — true when node U has property F set to V
//	before_urn: {kind: "before_urn", urn: U, prop: F, value: V}
//	                                             — inverse of after_urn (also true if node/prop absent)
//	all_of    : {kind: "all_of", predicates: [...]} — boolean AND
//	any_of    : {kind: "any_of", predicates: [...]} — boolean OR
//
// Other §M14 kinds (window, recurs_every, during_urn, on_event, on_prop_set,
// on_role_change, when_capability, nth, first_of_prop, duration, expires_at,
// reopens_at) are accepted by the parser but return false from the evaluator —
// add cases as they become needed.
//
// Unknown kinds return false (fail-closed — §M8 safety stance).
type ThookPredicate struct {
	Kind string `json:"kind"`

	// fires_at / closes_at / time-based kinds
	T int `json:"t,omitempty"`

	// after_urn / before_urn / on_prop_set — target node + property + expected value
	URN   string `json:"urn,omitempty"`
	Prop  string `json:"prop,omitempty"`
	Value any    `json:"value,omitempty"`

	// all_of / any_of — boolean composition
	Predicates []ThookPredicate `json:"predicates,omitempty"`
}

// EvaluateThookPredicate evaluates a t_hook predicate against current state
// and a caller-supplied calendar T.
//
// The predicate argument is any — callers typically pass the raw value read
// from t_hook.Properties["predicate"].Value (which is map[string]any after
// JSON round-trip through the log). We normalise via a JSON round-trip into
// a ThookPredicate struct and then evaluate recursively.
//
// Unknown kinds, malformed predicates, and missing nodes all return false
// (fail-closed). before_urn is the one exception: a missing node means
// "hasn't happened yet" and returns true.
//
// currentT is the caller-supplied calendar-T value. The kernel itself does
// not track T — it is passed in by the t-cone renderer, a future time-driven
// sweep loop, or a manual introspection call.
func EvaluateThookPredicate(pred any, state *graph.GraphState, currentT int) bool {
	raw, err := json.Marshal(pred)
	if err != nil {
		return false
	}
	var p ThookPredicate
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return evaluateThookPredicateParsed(p, state, currentT)
}

// evaluateThookPredicateParsed is the recursive worker — called after the
// initial JSON normalisation so sub-predicates (inside all_of / any_of) don't
// repeatedly round-trip.
func evaluateThookPredicateParsed(p ThookPredicate, state *graph.GraphState, currentT int) bool {
	switch p.Kind {

	case "fires_at", "closes_at":
		// Time gate — fires once currentT has caught up to the target T.
		return currentT >= p.T

	case "after_urn":
		// State gate — target node must have property set to expected value.
		node, ok := state.Nodes[graph.URN(p.URN)]
		if !ok {
			return false
		}
		prop, ok := node.Properties[p.Prop]
		if !ok {
			return false
		}
		return propValueEquals(prop.Value, p.Value)

	case "before_urn":
		// Inverse of after_urn. "Before" means NOT yet at the expected state,
		// so a missing node or missing property also counts as "before".
		node, ok := state.Nodes[graph.URN(p.URN)]
		if !ok {
			return true
		}
		prop, ok := node.Properties[p.Prop]
		if !ok {
			return true
		}
		return !propValueEquals(prop.Value, p.Value)

	case "all_of":
		// Boolean AND — empty list trivially true (vacuous AND).
		for _, sub := range p.Predicates {
			if !evaluateThookPredicateParsed(sub, state, currentT) {
				return false
			}
		}
		return true

	case "any_of":
		// Boolean OR — empty list trivially false (vacuous OR).
		for _, sub := range p.Predicates {
			if evaluateThookPredicateParsed(sub, state, currentT) {
				return true
			}
		}
		return false
	}

	// Unknown kind — fail-closed.
	return false
}

// propValueEquals compares two property values after normalising via
// fmt.Sprintf("%v", ...). Handles the common case that JSON round-trip
// turns integers into float64 — "%v" on float64(220) is "220", same as
// "%v" on int(220), so equality survives.
//
// Strict-type comparison would require reflect + type coercion rules; we
// deliberately use string-form equality because predicate values are
// almost always enum strings or small integers, and string-form is the
// format both the log and the state renderer agree on.
func propValueEquals(actual, expected any) bool {
	return fmt.Sprintf("%v", actual) == fmt.Sprintf("%v", expected)
}
