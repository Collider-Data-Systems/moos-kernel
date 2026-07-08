package operad

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// TestLoadRegistry_Version verifies the top-level "version" field of
// ontology.json is captured on the Registry so /healthz (and any future
// consumer) can report runtime ontology version without grepping for
// feature-flag markers. See ffs0#33 comment thread (claude-z440 T=171
// state-readback: "runtime version marker not exposed at /operad/node-types").
func TestLoadRegistry_Version(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ontology.json")
	// Minimal ontology shape — just enough structure to parse.
	body := `{
		"$schema": "https://moos.local/ontology.schema.json",
		"version": "3.12.0",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": [],
		"port_color_compatibility": {"matrix": {}}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed ontology: %v", err)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if got, want := reg.Version, "3.12.0"; got != want {
		t.Errorf("registry.Version = %q, want %q", got, want)
	}
}

// TestEmptyRegistry_VersionBlank confirms an empty registry reports an empty
// version string (not a panic) — the /healthz handler relies on this to
// short-circuit cleanly when the kernel runs without an --ontology flag.
func TestEmptyRegistry_VersionBlank(t *testing.T) {
	reg := EmptyRegistry()
	if reg.Version != "" {
		t.Errorf("EmptyRegistry().Version = %q, want empty string", reg.Version)
	}
}

// TestLoadRegistry_EmptyPath confirms passing an empty path yields an empty
// registry with an empty version string — matching the "no ontology loaded"
// fallback in LoadRegistry.
func TestLoadRegistry_EmptyPath(t *testing.T) {
	reg, err := LoadRegistry("")
	if err != nil {
		t.Fatalf("LoadRegistry(\"\"): %v", err)
	}
	if reg.Version != "" {
		t.Errorf("LoadRegistry(\"\").Version = %q, want empty string", reg.Version)
	}
}

// ------------------------------------------------------------------
// §12.1/§12.2 color loading (moos-kernel#50)
// ------------------------------------------------------------------

func writeOntology(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ontology.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed ontology: %v", err)
	}
	return path
}

// TestParseColorMatrix_ValueKinds covers the four cell encodings the loaded
// ontology uses (bool true/false, "wf15_only", "sink_only") plus the
// junk-string fallback — previously untested.
func TestParseColorMatrix_ValueKinds(t *testing.T) {
	m := parseColorMatrix(map[string]map[string]any{
		"workflow": {
			"workflow":   true,
			"projection": "sink_only",
			"semantic":   "wf15_only",
			"auth":       false,
			"storage":    "garbage",
		},
	})
	if !m.Allowed(graph.ColorWorkflow, graph.ColorWorkflow, graph.WF19) {
		t.Errorf("bool true should parse to allowed")
	}
	if m.Allowed(graph.ColorWorkflow, graph.ColorProjection, graph.WF19) {
		t.Errorf("sink_only should reject non-projection use")
	}
	if m.Allowed(graph.ColorWorkflow, graph.ColorSemantic, graph.WF19) {
		t.Errorf("wf15_only should reject under WF19")
	}
	if !m.Allowed(graph.ColorWorkflow, graph.ColorSemantic, graph.WF15) {
		t.Errorf("wf15_only should allow under WF15")
	}
	if m.Allowed(graph.ColorWorkflow, graph.ColorAuth, graph.WF19) {
		t.Errorf("bool false should parse to rejected")
	}
	if m.Allowed(graph.ColorWorkflow, graph.ColorStorage, graph.WF19) {
		t.Errorf("unrecognized string should parse to rejected")
	}
	if m.Allowed(graph.ColorAuth, graph.ColorAuth, graph.WF19) {
		t.Errorf("missing src row should reject")
	}
}

// TestLoadRegistry_PortColors_DefaultsWhenKeyAbsent: an ontology without
// port_color_map (v4.0.1 shape) still yields the full built-in vocabulary.
func TestLoadRegistry_PortColors_DefaultsWhenKeyAbsent(t *testing.T) {
	path := writeOntology(t, `{
		"version": "4.0.1",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": [],
		"port_color_compatibility": {"matrix": {}}
	}`)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if got := reg.PortColors["opens-on"]; got != graph.ColorWorkflow {
		t.Errorf("defaults not applied: opens-on = %q, want workflow", got)
	}
}

// TestLoadRegistry_PortColorMapOverride: the optional ontology object can
// recolor an existing port, add a new one, and declare an exemption — all
// merged over the defaults without a kernel release.
func TestLoadRegistry_PortColorMapOverride(t *testing.T) {
	path := writeOntology(t, `{
		"version": "4.0.2-test",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": [],
		"port_color_compatibility": {
			"matrix": {},
			"port_color_map": {
				"opens-on": "topology",
				"future-port": "auth",
				"weird-port": ""
			}
		}
	}`)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if got := reg.PortColors["opens-on"]; got != graph.ColorTopology {
		t.Errorf("override lost: opens-on = %q, want topology", got)
	}
	if got := reg.PortColors["future-port"]; got != graph.ColorAuth {
		t.Errorf("new port lost: future-port = %q, want auth", got)
	}
	if got, ok := reg.PortColors["weird-port"]; !ok || got != "" {
		t.Errorf("explicit exemption lost: weird-port = (%q, %v), want (\"\", true)", got, ok)
	}
	if got := reg.PortColors["governs"]; got != graph.ColorAuth {
		t.Errorf("untouched default lost: governs = %q, want auth", got)
	}
}

// TestLoadRegistry_PortColorMap_UnknownColorErrors: a typo'd color name must
// fail the load loudly — silently accepting it would weaken the fail-closed
// gate in ways only visible at LINK time.
func TestLoadRegistry_PortColorMap_UnknownColorErrors(t *testing.T) {
	path := writeOntology(t, `{
		"version": "4.0.2-test",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": [],
		"port_color_compatibility": {
			"matrix": {},
			"port_color_map": {"some-port": "chartreuse"}
		}
	}`)
	_, err := LoadRegistry(path)
	if err == nil {
		t.Fatalf("expected load error for unknown color name")
	}
	if !strings.Contains(err.Error(), "chartreuse") || !strings.Contains(err.Error(), "some-port") {
		t.Errorf("error should name the color and the port; got %q", err.Error())
	}
}

// TestLoadRegistry_PortColorMap_NullColorErrors: JSON null must not alias
// onto the "" exemption (adversarial-review finding on #50). A None-emitting
// script would otherwise silently exempt a port from the matrix check.
func TestLoadRegistry_PortColorMap_NullColorErrors(t *testing.T) {
	path := writeOntology(t, `{
		"version": "4.0.2-test",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": [],
		"port_color_compatibility": {
			"matrix": {},
			"port_color_map": {"opens-on": null}
		}
	}`)
	_, err := LoadRegistry(path)
	if err == nil {
		t.Fatalf("expected load error for null color value")
	}
	if !strings.Contains(err.Error(), "null") || !strings.Contains(err.Error(), "opens-on") {
		t.Errorf("error should name null and the port; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), `""`) {
		t.Errorf("error should point at the \"\" exemption syntax; got %q", err.Error())
	}
}

// TestLoadRegistry_UncoveredDeclaredPort_WarnsButBoots: a declared pair whose
// port has no color entry must not brick the load — it logs a loud warning
// and the fail-closed gate handles it at LINK time (soft-first posture).
func TestLoadRegistry_UncoveredDeclaredPort_WarnsButBoots(t *testing.T) {
	path := writeOntology(t, `{
		"version": "4.0.2-test",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": [
			{"id": "WF98", "name": "Future category", "allowed_rewrites": ["LINK"],
			 "src_port": "future-src", "tgt_port": "future-tgt"}
		],
		"port_color_compatibility": {"matrix": {}}
	}`)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("uncovered declared port must not fail the load: %v", err)
	}
	if _, ok := reg.PortColors["future-src"]; ok {
		t.Errorf("future-src should not be colored by defaults in this fixture")
	}
	warned := buf.String()
	if !strings.Contains(warned, "WARNING") || !strings.Contains(warned, "WF98:future-src") {
		t.Errorf("expected loud coverage warning naming WF98:future-src; got %q", warned)
	}
}
