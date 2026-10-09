package operad

import (
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342, Copilot #80 (4), kernel#76: a repeated key in the loaded part of
// the color section is a load error. json.Unmarshal keeps the last of two
// equal keys in a map, so before this a repeated port_color_map port or
// matrix row or column loaded silently with whichever value came last, and a
// repeated section key merged the two values or kept only one. Only the
// loaded keys are checked: a repeated key anywhere else — above all inside
// the authored-not-loaded declared_pairs_by_wf — still loads.
// ------------------------------------------------------------------

func TestLoadRegistry_ColorSectionDuplicateKeys_Rejected_t342(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "port_color_map port repeated with another color",
			body: colorSectionOntology(`{"matrix": {}, "port_color_map": {"hosts": "topology", "hosted-on": "topology", "hosts": "auth"}}`, ""),
			want: []string{`port_color_compatibility.port_color_map: duplicate port "hosts"`},
		},
		{
			name: "port_color_map port repeated with the same color",
			body: colorSectionOntology(`{"matrix": {}, "port_color_map": {"binds": "topology", "binds": "topology"}}`, ""),
			want: []string{`port_color_compatibility.port_color_map: duplicate port "binds"`},
		},
		{
			name: "port_color_map port repeated in an escaped spelling",
			body: colorSectionOntology(`{"matrix": {}, "port_color_map": {"hosts": "topology", "\u0068osts": "auth"}}`, ""),
			want: []string{`port_color_compatibility.port_color_map: duplicate port "hosts"`},
		},
		{
			name: "matrix row repeated",
			body: colorSectionOntology(`{"matrix": {"auth": {"auth": true}, "topology": {"topology": true}, "auth": {"auth": false}}}`, ""),
			want: []string{`port_color_compatibility.matrix: duplicate row "auth"`},
		},
		{
			name: "matrix column repeated in one row",
			body: colorSectionOntology(`{"matrix": {"auth": {"auth": true}, "storage": {"storage": true, "topology": true, "storage": false}}}`, ""),
			want: []string{`port_color_compatibility.matrix["storage"]: duplicate column "storage"`},
		},
		{
			name: "port_color_map key repeated",
			body: colorSectionOntology(`{"matrix": {}, "port_color_map": {"hosts": "topology"}, "port_color_map": {"hosts": "auth"}}`, ""),
			want: []string{`port_color_compatibility: duplicate key "port_color_map"`},
		},
		{
			name: "matrix key repeated in another case",
			body: colorSectionOntology(`{"matrix": {"auth": {"auth": true}}, "Matrix": {"topology": {"topology": true}}}`, ""),
			want: []string{`port_color_compatibility: duplicate key "Matrix"`, `the same key as the earlier "matrix"`},
		},
		{
			name: "matrix key repeated as null",
			body: colorSectionOntology(`{"matrix": {"auth": {"auth": true}}, "matrix": null}`, ""),
			want: []string{`port_color_compatibility: duplicate key "matrix"`},
		},
		{
			name: "port_colors key repeated",
			body: colorSectionOntology(`{"port_colors": ["auth"], "port_colors": ["auth", "topology"], "matrix": {}}`, ""),
			want: []string{`port_color_compatibility: duplicate key "port_colors"`},
		},
		{
			// json.Unmarshal folds field names the Unicode way: U+017F (ſ)
			// matches "s", so it reads this key as port_colors too. An
			// ASCII-only case fold would let the repeat through.
			name: "port_colors key repeated in a Unicode case fold (ſ = s)",
			body: colorSectionOntology(`{"port_colors": ["auth"], "port_colorſ": ["auth", "topology"], "matrix": {}}`, ""),
			want: []string{`port_color_compatibility: duplicate key "port_colorſ"`, `the same key as the earlier "port_colors"`},
		},
		{
			name: "port_color_compatibility repeated at the top level",
			body: `{"version": "t342-dupkeys", "types": {}, "rewrite_categories": [],
				"port_color_compatibility": {"matrix": {}, "port_color_map": {"hosts": "topology"}},
				"port_color_compatibility": {"matrix": {}, "port_color_map": {"hosts": "auth"}}}`,
			want: []string{`ontology: duplicate key "port_color_compatibility"`},
		},
		{
			name: "port_color_compatibility repeated at the top level in another case",
			body: `{"version": "t342-dupkeys", "types": {}, "rewrite_categories": [],
				"port_color_compatibility": {"matrix": {}, "port_color_map": {"hosts": "topology"}},
				"PORT_COLOR_COMPATIBILITY": {"port_color_map": {"hosts": "auth"}}}`,
			want: []string{`ontology: duplicate key "PORT_COLOR_COMPATIBILITY"`, `the same key as the earlier "port_color_compatibility"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadRegistry(writeOntology(t, c.body))
			if err == nil {
				t.Fatalf("expected a load error")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error should contain %q; got %q", want, err.Error())
				}
			}
		})
	}
}

// TestLoadRegistry_DuplicateKeysOutsideColorMaps_Load_t342: the same
// repetitions anywhere the loader does not read colors from still load.
// declared_pairs_by_wf is authored, never loaded, and in 4.0.7 lists two
// pairs twice with different colors; here it also carries an object form
// with repeated keys. The loaded maps keep their single values.
func TestLoadRegistry_DuplicateKeysOutsideColorMaps_Load_t342(t *testing.T) {
	body := `{
		"version": "t342-dupkeys", "version_note": "a", "version_note": "b",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": [
			{"id": "session", "stratum": "S2", "stratum": "S2",
			 "properties": {"status": {"type": "string"}, "status": {"type": "string", "mutability": "mutable"}}}
		]},
		"rewrite_categories": [
			{"id": "WF03", "name": "hosting", "name": "hosting (repeated)", "allowed_rewrites": ["LINK"], "src_port": "hosts", "tgt_port": "hosted-on", "src_types": ["*"], "tgt_types": ["*"]},
			{"id": "WF08", "allowed_rewrites": ["LINK"], "src_port": "bound-to", "tgt_port": "binds", "src_types": ["*"], "tgt_types": ["*"]}
		],
		"port_color_compatibility": {
			"description": "first", "description": "second",
			"port_colors": ["auth", "topology", "transport", "compute", "storage", "workflow", "semantic", "projection"],
			"matrix": {"topology": {"topology": true}, "auth": {"topology": true}},
			"port_color_map": {"hosts": "topology", "hosted-on": "topology", "bound-to": "topology", "binds": "topology"},
			"declared_pairs_by_wf": [
				{"wf": "WF06", "src_color": "transport", "tgt_color": "transport", "src_port": "connects-to", "tgt_port": "connected-to"},
				{"wf": "WF06", "src_color": "topology", "tgt_color": "topology", "src_port": "connects-to", "tgt_port": "connected-to"},
				{"wf": "WF08", "src_color": "storage", "tgt_color": "storage", "src_port": "bound-to", "tgt_port": "binds"},
				{"wf": "WF08", "src_color": "compute", "tgt_color": "compute", "src_port": "bound-to", "tgt_port": "binds"},
				{"wf": "WF08", "wf": "WF08", "src_port": "bound-to", "src_port": "binds"}
			],
			"declared_pairs_by_wf": {"WF08": {"bound-to": "storage", "bound-to": "compute"}, "WF08": {}},
			"declared_pairs_by_wf_note": "authored intent, not loaded"
		}
	}`
	reg, err := LoadRegistry(writeOntology(t, body))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.PortColorSource != PortColorSourceOntology || len(reg.PortColors) != 4 {
		t.Errorf("want the own map as single source with 4 entries; source %q, %v", reg.PortColorSource, reg.PortColors)
	}
	if reg.PortColors["bound-to"] != graph.ColorTopology || reg.PortColors["binds"] != graph.ColorTopology {
		t.Errorf("bound-to/binds = %q/%q, want topology/topology", reg.PortColors["bound-to"], reg.PortColors["binds"])
	}
	if !reg.PortColorMatrix.Allowed(graph.ColorAuth, graph.ColorTopology, "WF01") {
		t.Errorf("matrix[auth][topology] did not load")
	}
	for _, p := range []declaredPair{{"WF03", "hosts", "hosted-on"}, {"WF08", "bound-to", "binds"}} {
		if err := reg.ValidateLINK(linkEnvelope(p)); err != nil {
			t.Errorf("%s (%s, %s): %v", p.wf, p.src, p.tgt, err)
		}
	}
}

// TestLoadRegistry_PortNamesAreExact_t342: port names are map keys, and
// json.Unmarshal keeps "hosts" and "Hosts" apart, so a port_color_map with
// both loads. The repeat check compares ports exactly, never by case.
func TestLoadRegistry_PortNamesAreExact_t342(t *testing.T) {
	body := colorSectionOntology(`{"matrix": {}, "port_color_map": {"hosts": "topology", "hosted-on": "topology", "Hosts": "auth"}}`, "")
	var reg *Registry
	var err error
	captureLog(t, func() { reg, err = LoadRegistry(writeOntology(t, body)) })
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.PortColors["hosts"] != graph.ColorTopology || reg.PortColors["Hosts"] != graph.ColorAuth {
		t.Errorf("hosts/Hosts = %q/%q, want topology/auth", reg.PortColors["hosts"], reg.PortColors["Hosts"])
	}
}

// TestCheckColorSectionKeys_Shapes_t342: the shapes shipped ontologies use
// pass — the 4.0.7 matrix (64 cells, the same eight column names in every
// row), the mtdc-2.0.0 own map (114 ports) — and so do no color section, null
// or empty maps, and values that are not objects (json.Unmarshal judges
// those). The real files are checked by the MOOS_T342_* tests, which load
// them.
func TestCheckColorSectionKeys_Shapes_t342(t *testing.T) {
	m407 := tableOntology(t, "4.0.7", pairs407, overrides407, matrix407())
	mtdc := tableOntology(t, "mtdc-2.0.0", pairsMTDC200(), mtdcPortColorMap(), identityMatrix())
	for name, body := range map[string]string{
		"4.0.7 table":        m407,
		"mtdc-2.0.0 table":   mtdc,
		"no color section":   `{"version": "x", "types": {}}`,
		"null color section": `{"version": "x", "port_color_compatibility": null}`,
		"empty maps":         `{"port_color_compatibility": {"port_colors": [], "matrix": {}, "port_color_map": {}}}`,
		"null maps":          `{"port_color_compatibility": {"port_colors": null, "matrix": null, "port_color_map": null}}`,
		"array section":      `{"port_color_compatibility": [{"matrix": 1}, {"matrix": 2}]}`,
		"top-level array":    `[{"port_color_compatibility": {}}, {"port_color_compatibility": {}}]`,
	} {
		if err := checkColorSectionKeys([]byte(body)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
