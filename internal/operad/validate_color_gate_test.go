package operad

import (
	"os"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// §12.2 color gate — fail-closed behavior (moos-kernel#50)
// ------------------------------------------------------------------

// buildColorGateRegistry declares one WF whose pair uses ports that are
// deliberately NOT in DefaultPortColors, one WF on the explicit exemption,
// and one WF with a declared pair whose colors clash under the fixture
// matrix. The matrix mirrors the loaded ontology's workflow→workflow entry
// only, so anything else color-checked is rejected by Allowed().
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

	// WF08 shape: bound-to is the documented ambiguous exemption.
	reg.RewriteCategories["WF08"] = RewriteCategorySpec{
		ID:              "WF08",
		Name:            "Binding",
		AllowedRewrites: []graph.RewriteType{graph.LINK},
		SrcPort:         "bound-to",
		TgtPort:         "binds",
	}

	// Declared pair with resolvable colors (auth→topology) that the fixture
	// matrix does not allow.
	reg.RewriteCategories["WF96"] = RewriteCategorySpec{
		ID:              "WF96",
		Name:            "Colored but incompatible",
		AllowedRewrites: []graph.RewriteType{graph.LINK},
		SrcPort:         "governs",
		TgtPort:         "hosted-on",
	}

	reg.PortColorMatrix[graph.ColorWorkflow] = map[graph.PortColor]colorCompat{
		graph.ColorWorkflow: compatAllowed,
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

// TestValidateLINK_ExemptPortSkipsMatrix: bound-to is explicitly mapped to
// the empty color (ambiguous compute-or-storage by doctrine). The matrix
// check must be skipped — even though the fixture matrix has no entry that
// would allow it.
func TestValidateLINK_ExemptPortSkipsMatrix(t *testing.T) {
	reg := buildColorGateRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF08",
		SrcPort:         "bound-to",
		TgtPort:         "binds",
	}
	if err := reg.ValidateLINK(env); err != nil {
		t.Fatalf("explicitly exempt port should skip the matrix check; got: %v", err)
	}
}

// TestValidateLINK_ColorIncompatibilityRejected: both ports resolve
// (auth→topology) but the fixture matrix has no auth row — Allowed() is
// fail-closed, so the LINK is rejected with the §12.2 message.
func TestValidateLINK_ColorIncompatibilityRejected(t *testing.T) {
	reg := buildColorGateRegistry()
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: "WF96",
		SrcPort:         "governs",
		TgtPort:         "hosted-on",
	}
	err := reg.ValidateLINK(env)
	if err == nil {
		t.Fatalf("expected color-incompatibility rejection (auth→topology absent from fixture matrix); got nil")
	}
	if !strings.Contains(err.Error(), "§12.2") {
		t.Errorf("error should cite §12.2; got %q", err.Error())
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
// (no loader involved) still resolves the canonical vocabulary.
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

// ------------------------------------------------------------------
// Canonical coverage: every pair declared in ontology v4.0.1 resolves and
// passes the real matrix shape. Hermetic mirror of the ontology's loaded
// pairs + true-cells; the MOOS_INTEGRATION test below checks the real file.
// ------------------------------------------------------------------

// realShapedMatrix mirrors ontology.json v4.0.1
// port_color_compatibility.matrix true-cells: 7 diagonals (projection→
// projection is false) plus auth→topology, compute→topology,
// storage→topology.
func realShapedMatrix() PortColorMatrix {
	m := make(PortColorMatrix)
	for _, c := range []graph.PortColor{
		graph.ColorAuth, graph.ColorTopology, graph.ColorTransport, graph.ColorCompute,
		graph.ColorStorage, graph.ColorWorkflow, graph.ColorSemantic,
	} {
		m[c] = map[graph.PortColor]colorCompat{c: compatAllowed}
	}
	m[graph.ColorAuth][graph.ColorTopology] = compatAllowed
	m[graph.ColorCompute][graph.ColorTopology] = compatAllowed
	m[graph.ColorStorage][graph.ColorTopology] = compatAllowed
	return m
}

// canonicalPairs is the complete (wf, src_port, tgt_port) inventory of
// ontology.json v4.0.1 rewrite_categories — every primary pair and every
// additional_port_pairs entry. If an ontology round adds a pair, the
// MOOS_INTEGRATION test catches the gap against the real file; this table
// keeps the invariant hermetic for plain `go test ./...`.
var canonicalPairs = []struct {
	wf       graph.RewriteCategory
	src, tgt string
}{
	{"WF01", "owns", "child"},
	{"WF01", "owns", "owned-by"},
	{"WF02", "governs", "governed-by"},
	{"WF02", "delegates-to", "delegated-by"},
	{"WF03", "hosts", "hosted-on"},
	{"WF04", "contains", "contained-in"},
	{"WF05", "exposes", "exposed-by"},
	{"WF06", "connects-to", "connected-to"},
	{"WF07", "participates", "participated-by"},
	{"WF07", "anchors", "anchor"},
	{"WF08", "bound-to", "binds"},
	{"WF09", "computes-on", "computed-by"},
	{"WF10", "persisted-in", "persists"},
	{"WF11", "synced-via", "sync-target"},
	{"WF12", "provides-kb", "kb-source"},
	{"WF13", "promotes-to", "promotion-target"},
	{"WF14", "implements", "implemented-by"},
	{"WF15", "{semantic}", "{semantic}"},
	{"WF16", "routes-to", "routed-from"},
	{"WF17", "triggers", "triggered-by"},
	{"WF18", "composes", "composed-by"},
	{"WF18", "spans", "spanned-by"},
	{"WF19", "opens-on", "occupied-by"},
	{"WF19", "has-occupant", "is-occupant-of"},
	{"WF19", "pins-urn", "pinned-by-session"},
	{"WF19", "filtered-by", "filters-session"},
	{"WF19", "mounts-tool", "tool-mounted-in-session"},
	{"WF19", "has-purpose", "purpose-of-session"},
	{"WF19", "presents-as", "presented-by"},
	{"WF20", "promotes", "promoted-from"},
	{"WF21", "causes", "caused-by"},
}

// TestDefaultPortColors_CoverCanonicalOntologyPairs is the flip-safety
// invariant: with the gate fail-closed, every canonical pair must either
// resolve to matrix-compatible colors or be an explicit exemption. This is
// what guarantees #50's step 3 ("only then flip") does not reject the live
// identity spine.
func TestDefaultPortColors_CoverCanonicalOntologyPairs(t *testing.T) {
	reg := EmptyRegistry()
	reg.PortColorMatrix = realShapedMatrix()
	for _, p := range canonicalPairs {
		src, tgt, err := reg.resolvePortColors(p.src, p.tgt)
		if err != nil {
			t.Errorf("%s (%s, %s): no color — DefaultPortColors incomplete: %v", p.wf, p.src, p.tgt, err)
			continue
		}
		if src == "" || tgt == "" {
			continue // explicit exemption (bound-to) — skip matrix by design
		}
		if !reg.PortColorMatrix.Allowed(src, tgt, p.wf) {
			t.Errorf("%s (%s, %s): colors %s→%s not allowed by the real-shaped matrix — flipping fail-closed would reject this live pair",
				p.wf, p.src, p.tgt, src, tgt)
		}
	}
}

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
		pairs := make([][2]string, 0, 1+len(spec.AdditionalPortPairs))
		if spec.SrcPort != "" || spec.TgtPort != "" {
			pairs = append(pairs, [2]string{spec.SrcPort, spec.TgtPort})
		}
		for _, ap := range spec.AdditionalPortPairs {
			pairs = append(pairs, [2]string{ap.SrcPort, ap.TgtPort})
		}
		for _, pr := range pairs {
			src, tgt, err := reg.resolvePortColors(pr[0], pr[1])
			if err != nil {
				t.Errorf("%s (%s, %s): unresolved color in loaded registry — fail-closed gate would reject a declared pair: %v",
					wf, pr[0], pr[1], err)
				continue
			}
			if src == "" || tgt == "" {
				continue // explicit exemption
			}
			if !reg.PortColorMatrix.Allowed(src, tgt, wf) {
				t.Errorf("%s (%s, %s): %s→%s rejected by the loaded matrix — declared pair would fail the live gate",
					wf, pr[0], pr[1], src, tgt)
			}
		}
	}
}
