package operad

import (
	"os"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// §12.2 color gate — fail-closed behavior (moos-kernel#50), strict color
// equality (t342 ruling 1), no "" exemption (t342 ruling 2)
// ------------------------------------------------------------------

// buildColorGateRegistry declares one WF whose pair uses ports that are
// deliberately NOT in the color map, the WF08 bound-to/binds pair (the
// 98f2ccc "" exemption, topology/topology since t342 ruling 2), and one WF
// with a declared pair whose two ends carry different colors (auth/topology).
// The matrix is left empty: since t342 ruling 1 the gate admits equal colors
// only and never consults it.
func buildColorGateRegistry() *Registry {
	reg := EmptyRegistry()

	// Pair declared, ports unknown to the color map.
	reg.RewriteCategories["WF97"] = RewriteCategorySpec{
		ID:              "WF97",
		Name:            "Declared pair, uncolored ports",
		AllowedRewrites: []graph.RewriteType{graph.LINK},
		SrcPort:         "mystic-port",
		TgtPort:         "mystic-target",
	}

	// WF08 shape: bound-to/binds, topology on both ends.
	reg.RewriteCategories["WF08"] = RewriteCategorySpec{
		ID:              "WF08",
		Name:            "Binding",
		AllowedRewrites: []graph.RewriteType{graph.LINK},
		SrcPort:         "bound-to",
		TgtPort:         "binds",
	}

	// Declared pair with resolvable but different colors (auth→topology).
	reg.RewriteCategories["WF96"] = RewriteCategorySpec{
		ID:              "WF96",
		Name:            "Colored but mismatched",
		AllowedRewrites: []graph.RewriteType{graph.LINK},
		SrcPort:         "governs",
		TgtPort:         "hosted-on",
	}
	return reg
}

// TestValidateLINK_DeclaredPairUnknownColor_FailsClosed is the acceptance
// case for #50: pre-fix, a declared pair whose port had no color silently
// skipped the §12.2 check; post-fix it is rejected.
func TestValidateLINK_DeclaredPairUnknownColor_FailsClosed(t *testing.T) {
	reg := buildColorGateRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF97",
		SrcPort:         "mystic-port",
		TgtPort:         "mystic-target",
	}
	err := reg.ValidateLINK(env)
	if err == nil {
		t.Fatalf("expected fail-closed rejection for declared pair with uncolored ports; got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no declared color") {
		t.Errorf("error should say the port has no declared color; got %q", msg)
	}
	if !strings.Contains(msg, "mystic-port") {
		t.Errorf("error should name the offending port; got %q", msg)
	}
	if !strings.Contains(msg, "fail-closed") {
		t.Errorf("error should state the gate is fail-closed; got %q", msg)
	}
}

// TestValidateLINK_BoundToIsTopology_t342: WF08 bound-to/binds was admitted
// through the "" exemption up to 98f2ccc; since t342 ruling 2 bound-to is
// topology and the pair is admitted as topology = topology.
func TestValidateLINK_BoundToIsTopology_t342(t *testing.T) {
	reg := buildColorGateRegistry()
	if got := reg.PortColors["bound-to"]; got != graph.ColorTopology {
		t.Fatalf("bound-to color = %q, want topology", got)
	}
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF08",
		SrcPort:         "bound-to",
		TgtPort:         "binds",
	}
	if err := reg.ValidateLINK(env); err != nil {
		t.Fatalf("WF08 bound-to/binds (topology = topology) should be admitted; got: %v", err)
	}
}

// TestValidateLINK_EmptyColorFailsClosed_t342: the "" exemption branch is
// gone (t342 ruling 2). A hand-built registry that still maps a port to the
// empty color (the loader refuses "") is rejected fail-closed, not admitted.
func TestValidateLINK_EmptyColorFailsClosed_t342(t *testing.T) {
	reg := buildColorGateRegistry()
	reg.PortColors["bound-to"] = ""
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF08",
		SrcPort:         "bound-to",
		TgtPort:         "binds",
	}
	err := reg.ValidateLINK(env)
	if err == nil {
		t.Fatalf("an empty color must no longer exempt the pair; got nil")
	}
	for _, want := range []string{"no declared color", `src "bound-to"`, "empty color", "t342 ruling 2", "fail-closed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q; got %q", want, err.Error())
		}
	}
}

// TestValidateLINK_ColorMismatchRejected: both ports resolve to different
// colors (auth→topology), so equality rejects with the §12.2 / ruling-1 text.
func TestValidateLINK_ColorMismatchRejected(t *testing.T) {
	reg := buildColorGateRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF96",
		SrcPort:         "governs",
		TgtPort:         "hosted-on",
	}
	err := reg.ValidateLINK(env)
	if err == nil {
		t.Fatalf("expected color mismatch rejection (auth vs topology); got nil")
	}
	for _, want := range []string{"§12.2", "t342 ruling 1", "governs (auth)", "hosted-on (topology)", "WF96"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q; got %q", want, err.Error())
		}
	}
}

// TestValidateLINK_GateIgnoresMatrix_t342: the gate never consults the
// matrix (t342 ruling 1). A matrix that ALLOWS auth→topology (the 4.0.7
// cell) still rejects the mismatched pair, and an EMPTY matrix still admits
// an equal-colored pair.
func TestValidateLINK_GateIgnoresMatrix_t342(t *testing.T) {
	reg := buildColorGateRegistry()
	reg.PortColorMatrix[graph.ColorAuth] = map[graph.PortColor]colorCompat{graph.ColorTopology: compatAllowed}
	if err := reg.ValidateLINK(graph.Envelope{
		RewriteType: graph.LINK, RewriteCategory: "WF96", SrcPort: "governs", TgtPort: "hosted-on",
	}); err == nil {
		t.Errorf("auth→topology must be rejected even when the matrix allows it")
	}

	wf := buildTestRegistry()
	wf.PortColorMatrix = make(PortColorMatrix)
	if err := wf.ValidateLINK(graph.Envelope{
		RewriteType: graph.LINK, RewriteCategory: graph.WF19, SrcPort: "opens-on", TgtPort: "occupied-by",
		SrcURN: "urn:moos:session:test", TgtURN: "urn:moos:kernel:test",
	}); err != nil {
		t.Errorf("workflow→workflow must be admitted with an empty matrix: %v", err)
	}
}

// TestValidateLINK_PhantomPairStillRejectedByDeclarationGate automates the
// live-gate case: the declared_pairs_by_wf phantom rows (claims-session /
// session-claimed-by — never declared on WF19, retained as history per the
// 4.0.1 note) are rejected by the DECLARATION gate, not the color gate. The
// ports are deliberately uncolored AND undeclared; declaration must win.
func TestValidateLINK_PhantomPairStillRejectedByDeclarationGate(t *testing.T) {
	reg := buildTestRegistry() // declares WF19 primary + has-occupant + pins-urn
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: graph.WF19,
		SrcPort:         "claims-session",
		TgtPort:         "session-claimed-by",
		SrcURN:          "urn:moos:session:test",
		TgtURN:          "urn:moos:agent:test",
	}
	err := reg.ValidateLINK(env)
	if err == nil {
		t.Fatalf("phantom WF19 pair should be rejected")
	}
	if !strings.Contains(err.Error(), "port pair") {
		t.Errorf("rejection should come from the declaration gate ('port pair'); got %q", err.Error())
	}
	if strings.Contains(err.Error(), "no declared color") {
		t.Errorf("rejection should not reach the color gate; got %q", err.Error())
	}
}

// TestResolvePortColors_AbsentPortNamesTheSide: the error should say which
// side lacks a color so envelope authors can fix the right port.
func TestResolvePortColors_AbsentPortNamesTheSide(t *testing.T) {
	reg := EmptyRegistry()
	_, _, err := reg.resolvePortColors("governs", "no-such-port")
	if err == nil {
		t.Fatalf("expected error for unknown tgt port")
	}
	if !strings.Contains(err.Error(), `tgt "no-such-port"`) {
		t.Errorf("error should name the tgt side; got %q", err.Error())
	}
	if strings.Contains(err.Error(), `src "governs"`) {
		t.Errorf("error should not blame the known src side; got %q", err.Error())
	}
}

// TestResolvePortColors_NilMapFallsBackToDefaults: a zero-value Registry
// (no loader involved) still resolves the canonical vocabulary from the
// frozen legacy table.
func TestResolvePortColors_NilMapFallsBackToDefaults(t *testing.T) {
	r := &Registry{}
	src, tgt, err := r.resolvePortColors("opens-on", "occupied-by")
	if err != nil {
		t.Fatalf("nil PortColors should fall back to defaults: %v", err)
	}
	if src != graph.ColorWorkflow || tgt != graph.ColorWorkflow {
		t.Errorf("opens-on/occupied-by should both be workflow; got %s/%s", src, tgt)
	}
}

// The hermetic declared-pair inventories (4.0.7, 4.0.8, mtdc-2.0.0) and the
// equivalence proof against the 98f2ccc matrix gate live in
// color_equality_t342_test.go; they replace the v4.0.1 canonicalPairs table.

// ------------------------------------------------------------------
// Integration: the same invariant against the real sibling ontology.json.
// Gated behind MOOS_INTEGRATION=1 like TestLoadRegistry_LoadsRealOntology_*.
// ------------------------------------------------------------------

func TestIntegration_ColorGate_CoversAllDeclaredOntologyPairs(t *testing.T) {
	if os.Getenv("MOOS_INTEGRATION") != "1" {
		t.Skip("set MOOS_INTEGRATION=1 to run against the sibling ffs0 ontology")
	}
	path := integrationOntologyPath(t)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry(%s): %v", path, err)
	}
	for wf, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			src, tgt, err := reg.resolvePortColors(pr[0], pr[1])
			if err != nil {
				t.Errorf("%s (%s, %s): unresolved color in loaded registry — fail-closed gate would reject a declared pair: %v",
					wf, pr[0], pr[1], err)
				continue
			}
			if src == "" || src != tgt {
				t.Errorf("%s (%s, %s): colors %q/%q — equality (t342 ruling 1) would reject a declared pair",
					wf, pr[0], pr[1], src, tgt)
			}
		}
	}
}
