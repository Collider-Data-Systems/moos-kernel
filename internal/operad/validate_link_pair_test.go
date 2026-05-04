package operad

import (
	"strings"
	"testing"

	"moos/kernel/internal/fold"
	"moos/kernel/internal/graph"
)

// buildTestRegistry returns a registry with WF19 plus a second WF that has
// no port spec at all — enough surface to exercise pair validation branches.
// The PortColorMatrix is seeded with workflow→workflow=allowed mirroring
// ontology.json so color-compatibility check does not intercept pair-validation
// assertions.
func buildTestRegistry() *Registry {
	reg := EmptyRegistry()

	// WF19 with its primary pair + the canonical v3.10 has-occupant pair.
	reg.RewriteCategories[graph.WF19] = RewriteCategorySpec{
		ID:              graph.WF19,
		Name:            "Session governance",
		AllowedRewrites: []graph.RewriteType{graph.LINK, graph.UNLINK, graph.MUTATE, graph.ADD},
		SrcPort:         "opens-on",
		TgtPort:         "occupied-by",
		AdditionalPortPairs: []AdditionalPortPair{
			{
				SrcPort:        "has-occupant",
				TgtPort:        "is-occupant-of",
				SrcTypes:       []graph.TypeID{"session"},
				TgtTypes:       []graph.TypeID{"user", "agent"},
				AddedInVersion: "3.10.0",
			},
			{
				SrcPort:        "pins-urn",
				TgtPort:        "pinned-by-session",
				SrcTypes:       []graph.TypeID{"session"},
				TgtTypes:       []graph.TypeID{"*"},
				AddedInVersion: "3.12.0",
			},
		},
		Authority: "kernel",
		SyncMode:  "local-only",
	}

	// WF99 simulating a degenerate / spec-absent WF — no primary, no additional.
	// ValidateLINK should remain permissive in that case for backward compat.
	reg.RewriteCategories["WF99"] = RewriteCategorySpec{
		ID:              "WF99",
		Name:            "Spec-absent category",
		AllowedRewrites: []graph.RewriteType{graph.LINK},
	}

	// Seed the port-color compatibility matrix with the workflow→workflow
	// entry required by the WF19 pairs under test. Matches the
	// `port_color_compatibility.matrix.workflow.workflow = true` line in
	// ffs0/kb/superset/ontology.json.
	reg.PortColorMatrix[graph.ColorWorkflow] = map[graph.PortColor]colorCompat{
		graph.ColorWorkflow: compatAllowed,
	}
	return reg
}

func TestValidateLINK_PrimaryPairAccepted(t *testing.T) {
	reg := buildTestRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcPort:         "opens-on",
		TgtPort:         "occupied-by",
		SrcURN:          "urn:moos:session:test",
		TgtURN:          "urn:moos:kernel:test",
	}
	if err := reg.ValidateLINK(env); err != nil {
		t.Fatalf("primary pair rejected: %v", err)
	}
}

func TestValidateLINK_AdditionalPairAccepted_HasOccupant(t *testing.T) {
	reg := buildTestRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcPort:         "has-occupant",
		TgtPort:         "is-occupant-of",
		SrcURN:          "urn:moos:session:test",
		TgtURN:          "urn:moos:agent:test",
	}
	if err := reg.ValidateLINK(env); err != nil {
		t.Fatalf("canonical has-occupant pair rejected: %v", err)
	}
}

func TestValidateLINK_AdditionalPairAccepted_PinsURN(t *testing.T) {
	reg := buildTestRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcPort:         "pins-urn",
		TgtPort:         "pinned-by-session",
		SrcURN:          "urn:moos:session:test",
		TgtURN:          "urn:moos:program:any",
	}
	if err := reg.ValidateLINK(env); err != nil {
		t.Fatalf("pins-urn pair rejected: %v", err)
	}
}

// TestValidateLINK_UndeclaredPairRejected is the key behavior change landed by
// PR 1. Pre-PR-1 the validator was permissive about any port pair not in the
// declared set (the src_port: "has-occupant" / tgt_port: "occupies" shape was
// silently accepted on Z440 kernel at log_seq=233). Post-PR-1 the validator
// rejects with a message that enumerates the declared alternatives.
func TestValidateLINK_UndeclaredPairRejected(t *testing.T) {
	reg := buildTestRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcPort:         "has-occupant",
		TgtPort:         "occupies", // typo — canonical is is-occupant-of
		SrcURN:          "urn:moos:session:test",
		TgtURN:          "urn:moos:agent:test",
	}
	err := reg.ValidateLINK(env)
	if err == nil {
		t.Fatalf("expected rejection of (has-occupant, occupies) under WF19; got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "port pair") {
		t.Errorf("error does not mention 'port pair': %q", msg)
	}
	if !strings.Contains(msg, "has-occupant") || !strings.Contains(msg, "occupies") {
		t.Errorf("error should echo offending pair; got %q", msg)
	}
	if !strings.Contains(msg, "is-occupant-of") {
		t.Errorf("error should enumerate declared alternatives (expected is-occupant-of in message); got %q", msg)
	}
}

func TestValidateLINK_UndeclaredPrimaryWithoutAdditional(t *testing.T) {
	// Same surface but target pair is entirely off-ontology — still rejected.
	reg := buildTestRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcPort:         "invokes",
		TgtPort:         "invoked-by",
	}
	if err := reg.ValidateLINK(env); err == nil {
		t.Fatalf("expected rejection for undeclared pair (invokes, invoked-by) under WF19")
	}
}

// TestValidateLINK_SpecAbsentWFStaysPermissive confirms the legacy permissive
// behavior for WFs with no declared pairs (neither primary nor additional).
// The guard is important for backward compatibility with registries loaded
// from older ontology versions or test fixtures that omit port specs.
func TestValidateLINK_SpecAbsentWFStaysPermissive(t *testing.T) {
	reg := buildTestRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF99",
		SrcPort:         "whatever",
		TgtPort:         "unknown",
	}
	if err := reg.ValidateLINK(env); err != nil {
		t.Fatalf("spec-absent WF should stay permissive; got: %v", err)
	}
}

// TestReplay_GrandfathersNonCanonicalPortPair is the central doctrinal check
// for PR 1 backward compatibility. A persisted log containing a LINK with a
// non-canonical port pair (produced under pre-PR-1 permissive validation)
// must replay cleanly: fold is pure and does not re-validate. Tightening the
// validator affects new rewrites only. Historical truth in the log is
// preserved; state rebuilds identically.
//
// This mirrors the concrete artifact on Z440 at log_seq=233: AG's
// session:sam.moos-diary --has-occupant/occupies--> agent:antigravity.hp-z440
// LINK. PR 1 must not break replay of such entries.
func TestReplay_GrandfathersNonCanonicalPortPair(t *testing.T) {
	// Seed state with the two nodes the LINK references.
	log := []graph.PersistedRewrite{
		{
			LogSeq: 1,
			Envelope: graph.Envelope{
				RewriteType: graph.ADD,
				Actor:       "urn:moos:user:sam",
				NodeURN:     "urn:moos:session:test",
				TypeID:      "session",
				Properties: map[string]graph.Property{
					"started_at": {Value: "2026-04-21T00:00:00Z", Mutability: "immutable"},
				},
			},
		},
		{
			LogSeq: 2,
			Envelope: graph.Envelope{
				RewriteType: graph.ADD,
				Actor:       "urn:moos:user:sam",
				NodeURN:     "urn:moos:agent:test",
				TypeID:      "agent",
				Properties: map[string]graph.Property{
					"name": {Value: "test-agent", Mutability: "immutable"},
				},
			},
		},
		{
			LogSeq: 3,
			Envelope: graph.Envelope{
				RewriteType:     graph.LINK,
				Actor:           "urn:moos:kernel:test",
				RelationURN:     "urn:moos:rel:noncanonical",
				SrcURN:          "urn:moos:session:test",
				SrcPort:         "has-occupant",
				TgtURN:          "urn:moos:agent:test",
				TgtPort:         "occupies", // pre-PR-1 non-canonical shape
				RewriteCategory: graph.WF19,
			},
		},
	}

	state, err := fold.Replay(log)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	rel, ok := state.Relations["urn:moos:rel:noncanonical"]
	if !ok {
		t.Fatalf("non-canonical relation missing from replayed state")
	}
	if rel.SrcPort != "has-occupant" || rel.TgtPort != "occupies" {
		t.Errorf("replayed relation ports mutated: got (%s, %s), want (has-occupant, occupies)",
			rel.SrcPort, rel.TgtPort)
	}
	// Fold ignores operad — this test would pass even without PR 1 — but the
	// test documents the invariant so any future change that starts folding
	// through a validator surface here must confront the grandfathering rule.
}

// ------------------------------------------------------------------
// Pair-level src_types / tgt_types enforcement in ValidateStrataLink
// (T=171 round 11 PR 1 — review follow-up for Gemini HIGH + Copilot)
// ------------------------------------------------------------------

// buildStrataTestRegistry extends buildTestRegistry with the NodeTypeSpecs
// ValidateStrataLink needs (stratum + existence) and re-declares WF19 with
// a restricted has-occupant pair plus a wildcard-tgt pins-urn pair that
// matches the v3.12 ontology shape.
func buildStrataTestRegistry() *Registry {
	reg := buildTestRegistry()
	// Override WF19 with explicit top-level type lists (broader than pair-level).
	reg.RewriteCategories[graph.WF19] = RewriteCategorySpec{
		ID:              graph.WF19,
		Name:            "Session governance",
		AllowedRewrites: []graph.RewriteType{graph.LINK, graph.UNLINK, graph.MUTATE, graph.ADD},
		SrcTypes:        []graph.TypeID{"session", "agent", "agent_session"},
		TgtTypes:        []graph.TypeID{"kernel", "session", "agent_session", "user", "agent", "program"},
		SrcPort:         "opens-on",
		TgtPort:         "occupied-by",
		AdditionalPortPairs: []AdditionalPortPair{
			{
				SrcPort:  "has-occupant",
				TgtPort:  "is-occupant-of",
				SrcTypes: []graph.TypeID{"session"},
				TgtTypes: []graph.TypeID{"user", "agent"},
			},
			{
				SrcPort:  "pins-urn",
				TgtPort:  "pinned-by-session",
				SrcTypes: []graph.TypeID{"session"},
				TgtTypes: []graph.TypeID{"*"}, // wildcard — any node type accepted
			},
		},
		Authority: "kernel",
		SyncMode:  "local-only",
	}
	reg.NodeTypes["session"] = NodeTypeSpec{ID: "session", Stratum: "S2"}
	reg.NodeTypes["user"] = NodeTypeSpec{ID: "user", Stratum: "S2"}
	reg.NodeTypes["agent"] = NodeTypeSpec{ID: "agent", Stratum: "S2"}
	reg.NodeTypes["program"] = NodeTypeSpec{ID: "program", Stratum: "S2"}
	reg.NodeTypes["kernel"] = NodeTypeSpec{ID: "kernel", Stratum: "S2"}
	return reg
}

func stateForStrataTest() graph.GraphState {
	return graph.GraphState{
		Nodes: map[graph.URN]graph.Node{
			"urn:moos:session:t": {URN: "urn:moos:session:t", TypeID: "session"},
			"urn:moos:agent:t":   {URN: "urn:moos:agent:t", TypeID: "agent"},
			"urn:moos:user:t":    {URN: "urn:moos:user:t", TypeID: "user"},
			"urn:moos:program:t": {URN: "urn:moos:program:t", TypeID: "program"},
			"urn:moos:kernel:t":  {URN: "urn:moos:kernel:t", TypeID: "kernel"},
		},
		Relations: map[graph.URN]graph.Relation{},
	}
}

// TestValidateStrataLink_AdditionalPair_TgtTypeOutsidePairRestriction is the
// behavior change closing the Gemini HIGH-priority gap on PR #27. WF19's
// top-level tgt_types includes "program", so the pair-matching step in
// ValidateLINK accepts a session --has-occupant--> program LINK. Without
// pair-level type enforcement, ValidateStrataLink also accepts it because
// "program" is in WF19.TgtTypes. With pair-level enforcement, the LINK is
// rejected because the has-occupant pair declares tgt_types=[user, agent]
// and program is not in that narrower list.
func TestValidateStrataLink_AdditionalPair_TgtTypeOutsidePairRestriction(t *testing.T) {
	reg := buildStrataTestRegistry()
	state := stateForStrataTest()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcURN:          "urn:moos:session:t",
		SrcPort:         "has-occupant",
		TgtURN:          "urn:moos:program:t", // allowed at WF level, banned at pair level
		TgtPort:         "is-occupant-of",
	}
	err := reg.ValidateStrataLink(env, state)
	if err == nil {
		t.Fatalf("expected pair-level tgt_type rejection (program not in has-occupant pair tgt_types); got nil")
	}
	if !strings.Contains(err.Error(), "pair (has-occupant, is-occupant-of)") {
		t.Errorf("error should name the pair; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "program") {
		t.Errorf("error should name the offending type; got %q", err.Error())
	}
}

func TestValidateStrataLink_AdditionalPair_SrcTypeOutsidePairRestriction(t *testing.T) {
	reg := buildStrataTestRegistry()
	state := stateForStrataTest()
	// WF19.SrcTypes includes "agent", but the has-occupant pair restricts src
	// to session only. agent-as-src should be rejected at pair level even
	// though it passes the WF-level check.
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcURN:          "urn:moos:agent:t", // WF accepts; pair rejects
		SrcPort:         "has-occupant",
		TgtURN:          "urn:moos:user:t",
		TgtPort:         "is-occupant-of",
	}
	err := reg.ValidateStrataLink(env, state)
	if err == nil {
		t.Fatalf("expected pair-level src_type rejection (agent not in has-occupant pair src_types); got nil")
	}
	if !strings.Contains(err.Error(), "src type") {
		t.Errorf("error should mention src type; got %q", err.Error())
	}
}

func TestValidateStrataLink_AdditionalPair_CanonicalTypesAccepted(t *testing.T) {
	// Sanity check: the canonical session → user via has-occupant stays accepted.
	reg := buildStrataTestRegistry()
	state := stateForStrataTest()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcURN:          "urn:moos:session:t",
		SrcPort:         "has-occupant",
		TgtURN:          "urn:moos:user:t",
		TgtPort:         "is-occupant-of",
	}
	if err := reg.ValidateStrataLink(env, state); err != nil {
		t.Fatalf("canonical has-occupant shape rejected: %v", err)
	}
}

func TestValidateStrataLink_AdditionalPair_WildcardTgtType(t *testing.T) {
	// WF19.pins-urn pair declares tgt_types=["*"] — any tgt type accepted.
	// Pinning a program is legal; pinning a kernel, user, agent all legal.
	reg := buildStrataTestRegistry()
	state := stateForStrataTest()
	cases := []graph.URN{
		"urn:moos:program:t",
		"urn:moos:kernel:t",
		"urn:moos:user:t",
		"urn:moos:agent:t",
	}
	for _, tgt := range cases {
		env := graph.Envelope{
			RewriteType:     graph.LINK,
			RewriteCategory: graph.WF19,
			SrcURN:          "urn:moos:session:t",
			SrcPort:         "pins-urn",
			TgtURN:          tgt,
			TgtPort:         "pinned-by-session",
		}
		if err := reg.ValidateStrataLink(env, state); err != nil {
			t.Errorf("pins-urn wildcard pair rejected tgt %s: %v", tgt, err)
		}
	}
}

// TestValidateStrataLink_AdditionalPair_WildcardOverridesNarrowWF closes the
// real-world bug surfaced on hp-laptop kernel at T=183: WF19's primary pair
// declares tgt_types=[kernel, session, agent_session, user, agent], but the
// pins-urn additional pair declares tgt_types=["*"]. Pre-fix, ValidateStrataLink
// ran the WF-level check (rule 2) before the pair-level check (rule 3), which
// rejected `session --pins-urn--> purpose` because "purpose" is not in the
// WF-level TgtTypes. Post-fix, when the LINK matches an additional pair, rule 2
// is skipped — the pair's own types (rule 3) govern. The wildcard ["*"] then
// admits any tgt type for that specific port-pair.
func TestValidateStrataLink_AdditionalPair_WildcardOverridesNarrowWF(t *testing.T) {
	reg := buildStrataTestRegistry()
	// Tighten WF19.TgtTypes to NOT include "program" — mirrors real ontology
	// shape where pins-urn must still admit program-as-tgt via the wildcard.
	spec := reg.RewriteCategories[graph.WF19]
	spec.TgtTypes = []graph.TypeID{"kernel", "session", "agent_session", "user", "agent"}
	reg.RewriteCategories[graph.WF19] = spec
	state := stateForStrataTest()

	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcURN:          "urn:moos:session:t",
		SrcPort:         "pins-urn",
		TgtURN:          "urn:moos:program:t",
		TgtPort:         "pinned-by-session",
	}
	if err := reg.ValidateStrataLink(env, state); err != nil {
		t.Errorf("pins-urn additional pair with [*] tgt_types should accept program even when WF-level excludes it; got: %v", err)
	}
}

func TestValidateStrataLink_PrimaryPair_UsesWFLevelTypesOnly(t *testing.T) {
	// Primary pair matches bypass the pair-level check by design (rule 3 is
	// for *additional* pairs only). Confirm a primary-pair LINK with a target
	// type that's in WF.TgtTypes but would fail the has-occupant pair rule
	// still validates. session → program via opens-on/occupied-by is valid
	// under WF19.TgtTypes (includes program).
	reg := buildStrataTestRegistry()
	state := stateForStrataTest()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcURN:          "urn:moos:session:t",
		SrcPort:         "opens-on",
		TgtURN:          "urn:moos:program:t",
		TgtPort:         "occupied-by",
	}
	if err := reg.ValidateStrataLink(env, state); err != nil {
		t.Errorf("primary-pair LINK with WF-legal types rejected: %v", err)
	}
}
