package operad

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342 ruling 6: the ontology's port_color_map is the single source of port
// colors. Keyed to the ontology's own content: when its map colors every end
// of every declared pair, the kernel's table is not consulted at all; when
// the map is partial (the fleet's 4.0.7) it is laid over the frozen legacy
// table, exactly as at 98f2ccc apart from bound-to.
// ------------------------------------------------------------------

// sourceOntology declares WF03 hosts/hosted-on and WF19 opens-on/occupied-by
// (plus extra, when given) and the given own port_color_map.
func sourceOntology(colorMap, extraWFs string) string {
	wfs := `[
		{"id": "WF03", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "hosts", "tgt_port": "hosted-on"},
		{"id": "WF19", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "opens-on", "tgt_port": "occupied-by"}` + extraWFs + `
	]`
	return colorSectionOntology(`{"matrix": {}, "port_color_map": `+colorMap+`}`, wfs)
}

// captureLog runs f with the standard logger redirected and returns the text.
func captureLog(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	f()
	return buf.String()
}

func TestLoadRegistry_SelfColoredOntologyIsSingleSource_t342(t *testing.T) {
	var reg *Registry
	var err error
	logged := captureLog(t, func() {
		reg, err = LoadRegistry(writeOntology(t, sourceOntology(
			`{"hosts": "topology", "hosted-on": "topology", "opens-on": "workflow", "occupied-by": "workflow", "future-port": "auth"}`, "")))
	})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.PortColorSource != PortColorSourceOntology {
		t.Errorf("PortColorSource = %q, want %q", reg.PortColorSource, PortColorSourceOntology)
	}
	want := map[string]graph.PortColor{
		"hosts": graph.ColorTopology, "hosted-on": graph.ColorTopology,
		"opens-on": graph.ColorWorkflow, "occupied-by": graph.ColorWorkflow,
		"future-port": graph.ColorAuth,
	}
	if len(reg.PortColors) != len(want) {
		t.Errorf("PortColors has %d entries, want exactly the own map's %d: %v", len(reg.PortColors), len(want), reg.PortColors)
	}
	for port, c := range want {
		if reg.PortColors[port] != c {
			t.Errorf("PortColors[%q] = %q, want %q", port, reg.PortColors[port], c)
		}
	}
	if _, ok := reg.PortColors["contains"]; ok {
		t.Errorf("a legacy-only port (contains) leaked into a self-colored registry")
	}
	if !strings.Contains(logged, "single source (5 entries, t342 ruling 6)") {
		t.Errorf("expected the single-source log line; got %q", logged)
	}
	// A legacy-only port is now uncolored, so a pair using it fails closed.
	reg.RewriteCategories["WF04"] = RewriteCategorySpec{ID: "WF04", AllowedRewrites: []graph.RewriteType{graph.LINK}, SrcPort: "contains", TgtPort: "contained-in"}
	if err := reg.ValidateLINK(graph.Envelope{RewriteType: graph.LINK, RewriteCategory: "WF04", SrcPort: "contains", TgtPort: "contained-in"}); err == nil ||
		!strings.Contains(err.Error(), "no declared color") {
		t.Errorf("a port only the kernel table colors must be uncolored for a self-colored ontology; got %v", err)
	}
}

func TestLoadRegistry_PartialOwnMapIsLegacyMerge_t342(t *testing.T) {
	cases := []struct {
		name, colorMap, extra string
		covered, ends         string
	}{
		{"three of four", `{"hosts": "topology", "hosted-on": "topology", "opens-on": "workflow"}`, "", "3", "4"},
		{"complete except {semantic}",
			`{"hosts": "topology", "hosted-on": "topology", "opens-on": "workflow", "occupied-by": "workflow"}`,
			`, {"id": "WF15", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "{semantic}", "tgt_port": "{semantic}"}`,
			"4", "5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reg *Registry
			var err error
			logged := captureLog(t, func() {
				reg, err = LoadRegistry(writeOntology(t, sourceOntology(c.colorMap, c.extra)))
			})
			if err != nil {
				t.Fatalf("LoadRegistry: %v", err)
			}
			if reg.PortColorSource != PortColorSourceLegacyMerge {
				t.Errorf("PortColorSource = %q, want %q", reg.PortColorSource, PortColorSourceLegacyMerge)
			}
			if reg.PortColors["contains"] != graph.ColorTopology || reg.PortColors["{semantic}"] != graph.ColorSemantic {
				t.Errorf("legacy table not merged under the partial own map")
			}
			if want := "colors " + c.covered + " of " + c.ends + " declared ports"; !strings.Contains(logged, want) {
				t.Errorf("expected %q in the log; got %q", want, logged)
			}
		})
	}
}

// TestLoadRegistry_NoDeclaredPairsIsLegacyMerge_t342: an ontology that
// declares no pair cannot be self-colored (nothing to cover), so the
// existing rewrite_categories: [] fixtures keep the full legacy vocabulary.
func TestLoadRegistry_NoDeclaredPairsIsLegacyMerge_t342(t *testing.T) {
	var reg *Registry
	var err error
	logged := captureLog(t, func() {
		reg, err = LoadRegistry(writeOntology(t, colorSectionOntology(`{"matrix": {}, "port_color_map": {"opens-on": "workflow"}}`, "")))
	})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.PortColorSource != PortColorSourceLegacyMerge || reg.PortColors["governs"] != graph.ColorAuth {
		t.Errorf("no declared pairs: source %q, governs %q — want the legacy merge", reg.PortColorSource, reg.PortColors["governs"])
	}
	if strings.Contains(logged, "port colors:") {
		t.Errorf("no color-source line expected without declared pairs; got %q", logged)
	}
}

// TestLoadRegistry_LegacyMerge407Unchanged_t342: for the fleet's 4.0.7 shape
// (pairs407 + its 6-entry own map) the merged PortColors equals what 98f2ccc
// built, except bound-to "" → topology — and nothing about the gate's
// verdicts changes (color_equality_t342_test.go).
func TestLoadRegistry_LegacyMerge407Unchanged_t342(t *testing.T) {
	var reg *Registry
	var err error
	logged := captureLog(t, func() {
		reg, err = LoadRegistry(writeOntology(t, tableOntology(t, "4.0.7", pairs407, overrides407, matrix407())))
	})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.PortColorSource != PortColorSourceLegacyMerge {
		t.Errorf("PortColorSource = %q, want legacy merge", reg.PortColorSource)
	}
	old := oldMergedColors(t, overrides407)
	if len(old) != len(reg.PortColors) {
		t.Errorf("merged map has %d entries, 98f2ccc built %d", len(reg.PortColors), len(old))
	}
	for port, c := range old {
		got := string(reg.PortColors[port])
		if port == "bound-to" {
			if c != "" || got != "topology" {
				t.Errorf("bound-to: 98f2ccc %q, t342 %q — want \"\" → topology", c, got)
			}
			continue
		}
		if got != c {
			t.Errorf("%s: 98f2ccc %q, t342 %q", port, c, got)
		}
	}
	if !strings.Contains(logged, "colors 6 of 66 declared ports") {
		t.Errorf("expected the 4.0.7 legacy-merge line (6 of 66); got %q", logged)
	}
	if strings.Contains(logged, "WARNING — color gate") || strings.Contains(logged, "have no color") {
		t.Errorf("4.0.7 shape must load without color-gate or coverage warnings; got %q", logged)
	}
}

// TestLoadRegistry_ColorGateReport_t342: reportColorGate warns (never fails)
// on a declared pair joining two colors, and on a declared pair where the
// ontology's matrix and equality disagree.
func TestLoadRegistry_ColorGateReport_t342(t *testing.T) {
	var err error
	logged := captureLog(t, func() {
		_, err = LoadRegistry(writeOntology(t, colorSectionOntology(
			`{"matrix": {"auth": {"auth": true, "topology": true}, "workflow": {"workflow": false}}}`,
			`[{"id": "WF96", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "governs", "tgt_port": "hosted-on"},
			  {"id": "WF19", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "opens-on", "tgt_port": "occupied-by"}]`)))
	})
	if err != nil {
		t.Fatalf("the color-gate report must never fail the load: %v", err)
	}
	for _, want := range []string{
		"1 declared pair(s) join two different colors",
		"WF96 (governs, hosted-on) auth/topology",
		"matrix disagrees with equality on 2 declared pair(s)",
		"WF19 (opens-on, occupied-by) workflow→workflow matrix=false equality=true",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("expected %q in the report; got %q", want, logged)
		}
	}
}
