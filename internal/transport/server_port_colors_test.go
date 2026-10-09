package transport

import (
	"os"
	"path/filepath"
	"testing"

	"moos/kernel/internal/operad"
)

// TestHandleGetPortColors_t342: /operad/port-colors keeps its two keys and
// gains color_rule (always "equality", t342 ruling 1) and color_source (the
// registry's PortColorSource, t342 ruling 6). bound-to is served as topology
// (t342 ruling 2), never "".
func TestHandleGetPortColors_t342(t *testing.T) {
	cases := []struct {
		name, ontology, wantSource string
	}{
		{"no ontology", "", operad.PortColorSourceLegacyMerge},
		{"self-colored", `{
			"version": "t342-transport",
			"types": {"s2_infrastructure": [], "s1_grammar": [], "interaction_nodes": []},
			"rewrite_categories": [{"id": "WF08", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "bound-to", "tgt_port": "binds"}],
			"port_color_compatibility": {"matrix": {"topology": {"topology": true}}, "port_color_map": {"bound-to": "topology", "binds": "topology"}}
		}`, operad.PortColorSourceOntology},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := ""
			if c.ontology != "" {
				path = filepath.Join(t.TempDir(), "ontology.json")
				if err := os.WriteFile(path, []byte(c.ontology), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			reg, err := operad.LoadRegistry(path)
			if err != nil {
				t.Fatalf("LoadRegistry: %v", err)
			}
			s := &Server{registry: reg}
			body := getJSON(t, s.handleGetPortColors, "/operad/port-colors")
			for _, k := range []string{"matrix", "port_colors", "color_rule", "color_source"} {
				if _, ok := body[k]; !ok {
					t.Errorf("response lacks %q: %v", k, body)
				}
			}
			if len(body) != 4 {
				t.Errorf("response has %d keys, want 4: %v", len(body), body)
			}
			if got := body["color_rule"]; got != "equality" {
				t.Errorf("color_rule = %v, want equality", got)
			}
			if got := body["color_source"]; got != c.wantSource {
				t.Errorf("color_source = %v, want %s", got, c.wantSource)
			}
			colors, _ := body["port_colors"].(map[string]any)
			if got := colors["bound-to"]; got != "topology" {
				t.Errorf("port_colors[bound-to] = %v, want topology", got)
			}
		})
	}
}
