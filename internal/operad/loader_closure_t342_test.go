package operad

import (
	"encoding/json"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342 loader closure: the color section of an ontology is closed over its
// own port_colors vocabulary. Matrix row and column keys and port_color_map
// values must be declared colors, a matrix cell must be one of true, false,
// "wf15_only", "sink_only", and "" is no color at all (t342 ruling 2).
// Before t342 a junk cell silently became false and port_colors was unread.
// ------------------------------------------------------------------

// colorSectionOntology wraps a port_color_compatibility JSON object (and an
// optional rewrite_categories array) into a minimal ontology body.
func colorSectionOntology(colorSection, wfs string) string {
	if wfs == "" {
		wfs = "[]"
	}
	return `{
		"version": "t342-closure",
		"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
		"rewrite_categories": ` + wfs + `,
		"port_color_compatibility": ` + colorSection + `
	}`
}

func TestLoadRegistry_ColorClosure_Rejections_t342(t *testing.T) {
	cases := []struct {
		name    string
		section string
		wfs     string
		want    []string
	}{
		{
			name:    "matrix row outside port_colors",
			section: `{"port_colors": ["auth", "topology"], "matrix": {"workflow": {"workflow": true}}}`,
			want:    []string{`row "workflow" is not a declared color`, "auth, topology"},
		},
		{
			name:    "matrix column outside port_colors",
			section: `{"port_colors": ["auth", "topology"], "matrix": {"auth": {"auth": true, "workflow": false}}}`,
			want:    []string{`matrix["auth"]: column "workflow" is not a declared color`},
		},
		{
			name:    "matrix row the kernel does not know",
			section: `{"matrix": {"chartreuse": {"auth": true}}}`,
			want:    []string{`row "chartreuse" is not a declared color`},
		},
		{
			name:    "matrix row null",
			section: `{"matrix": {"auth": null}}`,
			want:    []string{`row "auth" is null`},
		},
		{
			name:    "cell garbage string",
			section: `{"matrix": {"auth": {"auth": "garbage"}}}`,
			want:    []string{`matrix["auth"]["auth"]: invalid cell "garbage"`, `"wf15_only"`},
		},
		{
			name:    "cell number",
			section: `{"matrix": {"auth": {"topology": 1}}}`,
			want:    []string{`matrix["auth"]["topology"]: invalid cell 1`},
		},
		{
			name:    "cell null",
			section: `{"matrix": {"storage": {"storage": null}}}`,
			want:    []string{`matrix["storage"]["storage"]: invalid cell null`},
		},
		{
			name:    "cell object",
			section: `{"matrix": {"workflow": {"workflow": {}}}}`,
			want:    []string{`matrix["workflow"]["workflow"]: invalid cell {}`},
		},
		{
			name:    "port_color_map empty color",
			section: `{"matrix": {}, "port_color_map": {"bound-to": ""}}`,
			want:    []string{`empty color for port "bound-to"`, "t342 ruling 2"},
		},
		{
			name:    "port_color_map color outside a declared subset",
			section: `{"port_colors": ["auth", "topology"], "matrix": {}, "port_color_map": {"opens-on": "workflow"}}`,
			want:    []string{`unknown color "workflow" for port "opens-on"`, "valid: auth, topology"},
		},
		{
			name:    "port_colors names an unknown color",
			section: `{"port_colors": ["auth", "chartreuse"], "matrix": {}}`,
			want:    []string{`port_colors: unknown color "chartreuse"`},
		},
		{
			name:    "port_colors duplicate",
			section: `{"port_colors": ["auth", "topology", "auth"], "matrix": {}}`,
			want:    []string{`port_colors: color "auth" is listed twice`},
		},
		{
			// Legacy merge: the kernel table colors projected-to/rendered-as
			// projection, which this ontology's vocabulary does not declare.
			name:    "legacy color at a declared port outside port_colors",
			section: `{"port_colors": ["auth", "topology", "transport", "compute", "storage", "workflow", "semantic"], "matrix": {}}`,
			wfs:     `[{"id": "WF16", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "projected-to", "tgt_port": "rendered-as"}]`,
			want:    []string{`color "projection" of declared port`, "is not in port_color_compatibility.port_colors"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadRegistry(writeOntology(t, colorSectionOntology(c.section, c.wfs)))
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

// TestLoadRegistry_ColorClosure_Accepts_t342: the 4.0.7 matrix shape (bool,
// "wf15_only" and "sink_only" cells) loads with port_colors present or
// absent, and an absent port_colors declares the kernel's eight colors.
func TestLoadRegistry_ColorClosure_Accepts_t342(t *testing.T) {
	m, err := json.Marshal(matrix407())
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{
		`{"port_colors": ["auth", "topology", "transport", "compute", "storage", "workflow", "semantic", "projection"], "matrix": ` + string(m) + `}`,
		`{"matrix": ` + string(m) + `, "port_color_map": {"future-sink": "projection"}}`,
	} {
		reg, err := LoadRegistry(writeOntology(t, colorSectionOntology(section, "")))
		if err != nil {
			t.Fatalf("LoadRegistry(%s): %v", section, err)
		}
		if !reg.PortColorMatrix.Allowed(graph.ColorSemantic, graph.ColorAuth, graph.WF01) ||
			!reg.PortColorMatrix.Allowed(graph.ColorAuth, graph.ColorSemantic, graph.WF15) ||
			reg.PortColorMatrix.Allowed(graph.ColorAuth, graph.ColorSemantic, graph.WF01) {
			t.Errorf("4.0.7 matrix cells did not load with their 98f2ccc semantics")
		}
	}
}
