package operad

import (
	"os"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// Regression coverage for moos-kernel#64 (G5 close-out) — the 4.0.4 WF02
// member-of pair and its reliance on the port_color_map override.
// No behavior change: these pin the enforcement ffs0#172 (ontology 4.0.4) now
// depends on so a regression fails loudly.
//
// SCOPE — deliberately NOT re-testing the port_color_map merge itself.
// loader_test.go already covers that surface (TestLoadRegistry_PortColorMapOverride
// asserts override-wins, new-port-added, "" exemption, AND untouched-default;
// *_UnknownColorErrors and *_NullColorErrors cover the load-error paths). #64
// ask 2 called it "untested today", but it is tested — with SYNTHETIC ports.
// What was genuinely missing is the member-of specifics:
//   1. the loader captures WF02's member-of/has-member additional pair, and
//      ValidateLINK accepts member-of AND delegates-to while rejecting an
//      undeclared variant — the pair + color gate enforcement 4.0.4 rides;
//   2. against the REAL ontology, member-of is present and colored auth, and
//      every declared pair's ports carry a color (the loader only WARNS on
//      uncovered ports — this makes it a hard failure for the shipped file).
//
// INTERIM MANUAL CHECK (moos-kernel#64 ask 4), until the integration test below
// is wired into a CI job: at Doctor time, diff `/operad/rewrite-categories` and
// `/operad/port-colors` against ffs0/kb/superset/ontology.json — every declared
// pair must be served and every declared port must carry a color. Run the coded
// readback with `MOOS_INTEGRATION=1 go test ./internal/operad/ -run DeclaredPairsAllColored`.

// wf02Ontology declares WF02 governance with BOTH additional pairs the 4.0.x
// line carries (delegates-to v3.13, member-of v4.0.4) and colors member-of /
// has-member via the port_color_map override (member-of is absent from
// DefaultPortColors, so the override is what makes the pair loadable).
const wf02Ontology = `{
	"version": "4.0.4",
	"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
	"rewrite_categories": [
		{
			"id": "WF02",
			"name": "Governance",
			"allowed_rewrites": ["LINK", "UNLINK", "MUTATE"],
			"src_types": ["user", "agent", "group", "role"],
			"tgt_types": ["group", "role", "agent"],
			"src_port": "governs",
			"tgt_port": "governed-by",
			"additional_port_pairs": [
				{
					"src_port": "delegates-to",
					"tgt_port": "delegated-by",
					"src_types": ["role"],
					"tgt_types": ["role"],
					"added_in_version": "3.13.0"
				},
				{
					"src_port": "member-of",
					"tgt_port": "has-member",
					"src_types": ["user", "agent", "group"],
					"tgt_types": ["group"],
					"added_in_version": "4.0.4"
				}
			],
			"authority": "kernel",
			"sync_mode": "local-only"
		}
	],
	"port_color_compatibility": {
		"matrix": {"auth": {"auth": true}},
		"port_color_map": {"member-of": "auth", "has-member": "auth"}
	}
}`

// TestLoadRegistry_WF02MemberOfPair — the loader captures WF02's member-of pair
// (ffs0#172 / ontology 4.0.4). A dropped loop or a typo'd tag would fail here.
func TestLoadRegistry_WF02MemberOfPair(t *testing.T) {
	reg, err := LoadRegistry(writeOntology(t, wf02Ontology))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	spec, ok := reg.RewriteCategories[graph.WF02]
	if !ok {
		t.Fatal("WF02 missing from registry")
	}
	if got, want := len(spec.AdditionalPortPairs), 2; got != want {
		t.Fatalf("WF02 additional_port_pairs = %d, want %d", got, want)
	}
	var memberOf *AdditionalPortPair
	for i := range spec.AdditionalPortPairs {
		if spec.AdditionalPortPairs[i].SrcPort == "member-of" {
			memberOf = &spec.AdditionalPortPairs[i]
		}
	}
	if memberOf == nil {
		t.Fatal("member-of pair missing from WF02")
	}
	if memberOf.TgtPort != "has-member" {
		t.Errorf("member-of tgt_port = %q, want has-member", memberOf.TgtPort)
	}
	if memberOf.AddedInVersion != "4.0.4" {
		t.Errorf("member-of AddedInVersion = %q, want 4.0.4", memberOf.AddedInVersion)
	}
	if len(memberOf.TgtTypes) != 1 || memberOf.TgtTypes[0] != "group" {
		t.Errorf("member-of tgt_types = %v, want [group]", memberOf.TgtTypes)
	}
}

// TestValidateLINK_WF02Pairs — both 4.0.x WF02 additional pairs pass the pair +
// color gate, and an undeclared variant is rejected with the declared set named.
// member-of/has-member are auth-colored ONLY via the port_color_map override, so
// this also exercises that the override reaches the color gate (fail-closed).
func TestValidateLINK_WF02Pairs(t *testing.T) {
	reg, err := LoadRegistry(writeOntology(t, wf02Ontology))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	accepted := []struct {
		name, srcPort, tgtPort, srcURN, tgtURN string
	}{
		{"member-of", "member-of", "has-member", "urn:moos:user:sam", "urn:moos:group:moos"},
		{"delegates-to", "delegates-to", "delegated-by", "urn:moos:role:a", "urn:moos:role:b"},
		{"primary governs", "governs", "governed-by", "urn:moos:user:sam", "urn:moos:agent:x"},
	}
	for _, c := range accepted {
		env := graph.Envelope{
			RewriteType:     graph.LINK,
			RewriteCategory: graph.WF02,
			SrcPort:         c.srcPort,
			TgtPort:         c.tgtPort,
			SrcURN:          graph.URN(c.srcURN),
			TgtURN:          graph.URN(c.tgtURN),
		}
		if err := reg.ValidateLINK(env); err != nil {
			t.Errorf("%s pair rejected: %v", c.name, err)
		}
	}

	// Undeclared variant — right src port, wrong tgt port — must be rejected,
	// and the message must enumerate the declared pairs.
	bad := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF02,
		SrcPort:         "member-of",
		TgtPort:         "belongs-to", // canonical is has-member
		SrcURN:          "urn:moos:user:sam",
		TgtURN:          "urn:moos:group:moos",
	}
	err = reg.ValidateLINK(bad)
	if err == nil {
		t.Fatal("expected rejection of (member-of, belongs-to) under WF02; got nil")
	}
	if !strings.Contains(err.Error(), "has-member") {
		t.Errorf("rejection should enumerate the declared has-member pair; got %q", err.Error())
	}
}

// TestLoadRegistry_DeclaredPairsAllColored_Integration is the declared-vs-loaded
// readback (moos-kernel#64 ask 3) as a test: against the REAL sibling ontology,
// every port of every declared pair (primary + additional, across all WFs) must
// carry a color in the merged PortColors — otherwise a LINK on that pair is
// rejected fail-closed at runtime. member-of must be present + auth (the 4.0.4
// first-use of the override). The loader only WARNS on uncovered ports; this
// makes it a hard failure for the shipped ontology.
//
// Guarded behind MOOS_INTEGRATION=1 so `go test ./...` stays hermetic. Fails
// (not skips) when the flag is set but no candidate path resolves.
func TestLoadRegistry_DeclaredPairsAllColored_Integration(t *testing.T) {
	if os.Getenv("MOOS_INTEGRATION") != "1" {
		t.Skip("set MOOS_INTEGRATION=1 to run the declared-vs-loaded ontology readback")
	}
	candidates := []string{
		"../../../ffs0/kb/superset/ontology.json",
		"../../../../ffs0/kb/superset/ontology.json",
	}
	var path string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			path = c
			break
		}
	}
	if path == "" {
		t.Fatalf("MOOS_INTEGRATION=1 but ontology.json not found at %v", candidates)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry(%q): %v", path, err)
	}

	// member-of must be present AND colored — the 4.0.4 first-use of the override.
	if c, ok := reg.PortColors["member-of"]; !ok || c != graph.ColorAuth {
		t.Errorf("member-of color in real ontology = %q (present=%v), want auth", c, ok)
	}

	var uncovered []string
	for wf, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			for _, port := range pr {
				if port == "" {
					continue
				}
				if _, ok := reg.PortColors[port]; !ok {
					uncovered = append(uncovered, string(wf)+":"+port)
				}
			}
		}
	}
	if len(uncovered) > 0 {
		t.Errorf("declared ports without a color (LINKs on their pairs would be rejected fail-closed): %v", uncovered)
	}
}
