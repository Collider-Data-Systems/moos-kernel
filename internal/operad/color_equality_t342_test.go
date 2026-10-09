package operad

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342 ruling 1: strict color equality replaces the 8×8 matrix.
//
// The proof that the switch is admission-neutral. ValidateLINK checks, in
// order: WF known, WF allows LINK, WF15 contract, pair declared, color. On
// 4.0.7, 4.0.8 and mtdc-2.0.0 every WF declares at least one pair, so every
// LINK that reaches the color stage carries a DECLARED pair. Hence: if the
// 98f2ccc gate and equality agree on every declared pair, they agree on
// every LINK those ontologies can ever be asked to admit.
//
// The tables below are the complete declared-pair inventories of the three
// ontologies (hermetic; the MOOS_T342_* tests in t342_integration_test.go
// check the real files). The oracle is the 98f2ccc color stage, frozen: the
// "" exemption plus PortColorMatrix.Allowed over the 98f2ccc kernel table
// (testdata/operad/kernel-default-port-colors-98f2ccc.json) merged with the
// ontology's own port_color_map.
// ------------------------------------------------------------------

type declaredPair struct {
	wf       graph.RewriteCategory
	src, tgt string
}

// pairs407 is every declared pair of ontology 4.0.7 (the fleet operad,
// ffs0/kb/superset/ontology.json sha256 fd06a4dc…): 35 pairs over 21 WFs.
var pairs407 = []declaredPair{
	{"WF01", "owns", "child"}, {"WF01", "owns", "owned-by"}, {"WF01", "parent-of", "child-of"}, {"WF01", "spouse-of", "spouse-of"}, {"WF01", "sibling-of", "sibling-of"},
	{"WF02", "governs", "governed-by"}, {"WF02", "delegates-to", "delegated-by"}, {"WF02", "member-of", "has-member"},
	{"WF03", "hosts", "hosted-on"},
	{"WF04", "contains", "contained-in"},
	{"WF05", "exposes", "exposed-by"},
	{"WF06", "connects-to", "connected-to"},
	{"WF07", "participates", "participated-by"}, {"WF07", "anchors", "anchor"},
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
	{"WF18", "composes", "composed-by"}, {"WF18", "spans", "spanned-by"},
	{"WF19", "opens-on", "occupied-by"}, {"WF19", "has-occupant", "is-occupant-of"}, {"WF19", "pins-urn", "pinned-by-session"}, {"WF19", "filtered-by", "filters-session"}, {"WF19", "mounts-tool", "tool-mounted-in-session"}, {"WF19", "has-purpose", "purpose-of-session"}, {"WF19", "presents-as", "presented-by"},
	{"WF20", "promotes", "promoted-from"},
	{"WF21", "causes", "caused-by"},
}

// pairs408WF12 are the 8 WF12 knowledge-base pairs 4.0.8 adds to 4.0.7.
var pairs408WF12 = []declaredPair{
	{"WF12", "part-of", "has-part"}, {"WF12", "uses", "used-by"}, {"WF12", "refines", "refined-by"}, {"WF12", "derived-from", "source-of"},
	{"WF12", "decides", "decided-by"}, {"WF12", "replaced-by", "replaces"}, {"WF12", "contradicts", "contradicts"}, {"WF12", "related-to", "related-to"},
}

// pairsMTDC200New are the four pairs mtdc-2.0.0 declares for relations
// already in the fold (patch ruling 5).
var pairsMTDC200New = []declaredPair{
	{"WF12", "classifies", "classified-by"},
	{"WF17", "guards", "guarded-by"},
	{"WF18", "depends-on", "depended-by"}, {"WF18", "scheduled-after", "scheduled-before"},
}

// pairs408 = pairs407 + the WF12 knowledge-base pairs: 43 pairs.
func pairs408() []declaredPair {
	return append(append([]declaredPair{}, pairs407...), pairs408WF12...)
}

// pairsMTDC200 = pairs408 with WF01 owns/child dropped (ruling 3 merges it
// into the owns/owned-by primary, which keeps the same port pair) plus the
// four new pairs: 46 pairs.
func pairsMTDC200() []declaredPair {
	var out []declaredPair
	for _, p := range pairs408() {
		if p == (declaredPair{"WF01", "owns", "child"}) {
			continue
		}
		out = append(out, p)
	}
	return append(out, pairsMTDC200New...)
}

// overrides407 is 4.0.7's own port_color_map (6 entries).
var overrides407 = map[string]string{
	"member-of": "auth", "has-member": "auth",
	"parent-of": "topology", "child-of": "topology", "spouse-of": "topology", "sibling-of": "topology",
}

// overrides408 is 4.0.8's own port_color_map: overrides407 plus the 14
// semantic WF12 knowledge-base ports (20 entries).
func overrides408() map[string]string {
	m := make(map[string]string, 20)
	for k, v := range overrides407 {
		m[k] = v
	}
	for _, p := range pairs408WF12 {
		m[p.src], m[p.tgt] = "semantic", "semantic"
	}
	return m
}

// mtdcPortColorMap is mtdc-2.0.0's own port_color_map: the 92 kernel ports
// (bound-to = topology) + overrides408 + classifies/classified-by = 114.
func mtdcPortColorMap() map[string]string {
	m := make(map[string]string, 114)
	for k, v := range legacyPortColors() {
		m[k] = string(v)
	}
	for k, v := range overrides408() {
		m[k] = v
	}
	m["classifies"], m["classified-by"] = "semantic", "semantic"
	return m
}

// matrix407 is 4.0.7's port_color_compatibility.matrix verbatim (4.0.8
// carries the same matrix).
func matrix407() map[string]map[string]any {
	row := func(self string, extra ...string) map[string]any {
		r := map[string]any{}
		for _, c := range kernelPortColors {
			r[string(c)] = false
		}
		r[self] = true
		for _, e := range extra {
			r[e] = true
		}
		r["semantic"] = "wf15_only"
		r["projection"] = "sink_only"
		return r
	}
	m := map[string]map[string]any{
		"auth":       row("auth", "topology"),
		"topology":   row("topology"),
		"transport":  row("transport"),
		"compute":    row("compute", "topology"),
		"storage":    row("storage", "topology"),
		"workflow":   row("workflow"),
		"semantic":   {"auth": true, "topology": true, "transport": true, "compute": true, "storage": true, "workflow": true, "semantic": true, "projection": "sink_only"},
		"projection": {"auth": false, "topology": false, "transport": false, "compute": false, "storage": false, "workflow": false, "semantic": false, "projection": false},
	}
	return m
}

// identityMatrix is mtdc-2.0.0's matrix: only the diagonal is true.
func identityMatrix() map[string]map[string]any {
	m := make(map[string]map[string]any, len(kernelPortColors))
	for _, r := range kernelPortColors {
		m[string(r)] = map[string]any{}
		for _, c := range kernelPortColors {
			m[string(r)][string(c)] = r == c
		}
	}
	return m
}

const frozen98f2cccPath = "../../testdata/operad/kernel-default-port-colors-98f2ccc.json"

// frozen98f2cccSHA256 pins the vendored extract of this repo's own
// DefaultPortColors() at 98f2ccc (92 entries, bound-to = "").
const frozen98f2cccSHA256 = "52437ee958182a15bbca25b3b0fd48e4d478ca8f1142095498ac7ea70eec828f"

// frozen98f2cccColors returns the 98f2ccc kernel table from the vendored
// file, after checking its hash.
func frozen98f2cccColors(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(frozen98f2cccPath)
	if err != nil {
		t.Fatalf("read %s: %v", frozen98f2cccPath, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != frozen98f2cccSHA256 {
		t.Fatalf("%s sha256 = %s, want %s (the frozen table must stay byte-exact)", frozen98f2cccPath, got, frozen98f2cccSHA256)
	}
	var doc struct {
		PortColors map[string]string `json:"port_colors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", frozen98f2cccPath, err)
	}
	return doc.PortColors
}

// oldMergedColors is the PortColors a 98f2ccc kernel built for an ontology
// with the given own map: the 98f2ccc table with the own map laid over it.
func oldMergedColors(t *testing.T, own map[string]string) map[string]string {
	t.Helper()
	m := frozen98f2cccColors(t)
	for k, v := range own {
		m[k] = v
	}
	return m
}

// legacyColorStage98f2ccc is the frozen 98f2ccc color stage of ValidateLINK —
// the oracle. Returns nil (admitted) or the 98f2ccc rejection text.
func legacyColorStage98f2ccc(colors map[string]string, matrix PortColorMatrix, pairsDeclared bool, wf graph.RewriteCategory, srcPort, tgtPort string) error {
	srcColor, srcOk := colors[srcPort]
	tgtColor, tgtOk := colors[tgtPort]
	if !srcOk || !tgtOk {
		if !pairsDeclared {
			return nil
		}
		missing := make([]string, 0, 2)
		if !srcOk {
			missing = append(missing, fmt.Sprintf("src %q", srcPort))
		}
		if !tgtOk {
			missing = append(missing, fmt.Sprintf("tgt %q", tgtPort))
		}
		return fmt.Errorf("operad: no declared color for port %s (§12.1) — color gate is fail-closed for WFs with declared pairs (§12.2, moos-kernel#50)", strings.Join(missing, ", "))
	}
	if srcColor == "" || tgtColor == "" {
		return nil // the 98f2ccc exemption (bound-to)
	}
	if !matrix.Allowed(graph.PortColor(srcColor), graph.PortColor(tgtColor), wf) {
		return fmt.Errorf("operad: port color incompatibility: %s → %s not allowed under %s (§12.2)", srcColor, tgtColor, wf)
	}
	return nil
}

// tableOntology renders a synthetic ontology.json from a pair table: one WF
// entry per WF id (its first pair primary, the rest additional), each
// allowing LINK, with the given own port_color_map and matrix.
func tableOntology(t *testing.T, version string, pairs []declaredPair, own map[string]string, matrix map[string]map[string]any) string {
	t.Helper()
	type rawPair struct {
		Src string `json:"src_port"`
		Tgt string `json:"tgt_port"`
	}
	type rawWF struct {
		ID      string    `json:"id"`
		Allowed []string  `json:"allowed_rewrites"`
		Src     string    `json:"src_port"`
		Tgt     string    `json:"tgt_port"`
		Add     []rawPair `json:"additional_port_pairs,omitempty"`
	}
	var wfs []*rawWF
	byID := map[graph.RewriteCategory]*rawWF{}
	for _, p := range pairs {
		w, ok := byID[p.wf]
		if !ok {
			w = &rawWF{ID: string(p.wf), Allowed: []string{"LINK", "UNLINK"}, Src: p.src, Tgt: p.tgt}
			byID[p.wf] = w
			wfs = append(wfs, w)
			continue
		}
		w.Add = append(w.Add, rawPair{p.src, p.tgt})
	}
	body, err := json.Marshal(map[string]any{
		"version":            version,
		"types":              map[string]any{"s2_infrastructure": []any{}, "s1_grammar": []any{}, "interaction_nodes": []any{}},
		"rewrite_categories": wfs,
		"port_color_compatibility": map[string]any{
			"port_colors":    kernelPortColors,
			"matrix":         matrix,
			"port_color_map": own,
		},
	})
	if err != nil {
		t.Fatalf("marshal table ontology: %v", err)
	}
	return string(body)
}

func linkEnvelope(p declaredPair) graph.Envelope {
	env := graph.Envelope{
		RewriteType:     graph.LINK,
		RewriteCategory: p.wf,
		SrcPort:         p.src,
		TgtPort:         p.tgt,
		SrcURN:          "urn:moos:test:src",
		TgtURN:          "urn:moos:test:tgt",
	}
	if p.wf == graph.WF15 {
		env.ContractURN = "urn:moos:contract:t342"
	}
	return env
}

// TestColorEquality_AdmissionNeutral_t342 is the ruling-1 proof over the
// three declared-pair tables: for every declared pair, the frozen 98f2ccc
// color stage and the production ValidateLINK (on a registry the real loader
// builds from the table) give the same verdict — and both admit.
func TestColorEquality_AdmissionNeutral_t342(t *testing.T) {
	cases := []struct {
		name       string
		pairs      []declaredPair
		own        map[string]string
		matrix     map[string]map[string]any
		wantSource string
	}{
		{"4.0.7", pairs407, overrides407, matrix407(), PortColorSourceLegacyMerge},
		{"4.0.8", pairs408(), overrides408(), matrix407(), PortColorSourceLegacyMerge},
		{"mtdc-2.0.0", pairsMTDC200(), mtdcPortColorMap(), identityMatrix(), PortColorSourceOntology},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, err := LoadRegistry(writeOntology(t, tableOntology(t, c.name, c.pairs, c.own, c.matrix)))
			if err != nil {
				t.Fatalf("LoadRegistry: %v", err)
			}
			if reg.PortColorSource != c.wantSource {
				t.Errorf("PortColorSource = %q, want %q", reg.PortColorSource, c.wantSource)
			}
			old := oldMergedColors(t, c.own)
			admitted := 0
			for _, p := range c.pairs {
				oracle := legacyColorStage98f2ccc(old, reg.PortColorMatrix, true, p.wf, p.src, p.tgt)
				got := reg.ValidateLINK(linkEnvelope(p))
				if (oracle == nil) != (got == nil) {
					t.Errorf("%s (%s, %s): verdict differs — 98f2ccc %v, equality %v", p.wf, p.src, p.tgt, oracle, got)
					continue
				}
				if got != nil {
					t.Errorf("%s (%s, %s): declared pair rejected by both gates: %v", p.wf, p.src, p.tgt, got)
					continue
				}
				admitted++
			}
			t.Logf("%s: %d declared pairs, %d admitted by both the 98f2ccc gate and equality, 0 differences", c.name, len(c.pairs), admitted)
		})
	}
}

// TestColorEquality_MatrixDiffersOnlyOffDeclaredPairs enumerates every cell
// where the 4.0.7 matrix and equality disagree and proves no declared pair
// of any of the three ontologies reaches one — with the 98f2ccc colors or
// the t342 colors. The cells exist (the test has teeth): auth, compute and
// storage → topology, semantic → anything, anything → semantic under WF15,
// and the dead projection → projection cell.
func TestColorEquality_MatrixDiffersOnlyOffDeclaredPairs(t *testing.T) {
	declared, _ := declaredPortColors(nil)
	m407, err := parseColorMatrix(matrix407(), declared)
	if err != nil {
		t.Fatalf("parse matrix407: %v", err)
	}
	type cell struct {
		src, tgt graph.PortColor
		wf15     bool
	}
	differs := map[cell]bool{}
	for _, wf := range []graph.RewriteCategory{"WF01", graph.WF15} {
		for _, s := range kernelPortColors {
			for _, tg := range kernelPortColors {
				if m407.Allowed(s, tg, wf) != (s == tg) {
					differs[cell{s, tg, wf == graph.WF15}] = true
				}
			}
		}
	}
	nonWF15, wf15 := 0, 0
	for c := range differs {
		if c.wf15 {
			wf15++
		} else {
			nonWF15++
		}
	}
	// 3 →topology + 6 semantic→other + projection→projection; WF15 adds the 6
	// wf15_only cells other→semantic.
	if nonWF15 != 10 || wf15 != 16 {
		t.Fatalf("matrix407 vs equality differ on %d non-WF15 / %d WF15 cells, want 10 / 16", nonWF15, wf15)
	}
	for _, cc := range []struct {
		name  string
		pairs []declaredPair
		own   map[string]string
	}{
		{"4.0.7", pairs407, overrides407},
		{"4.0.8", pairs408(), overrides408()},
		{"mtdc-2.0.0", pairsMTDC200(), mtdcPortColorMap()},
	} {
		oldColors := oldMergedColors(t, cc.own)
		newColors := legacyPortColors()
		for k, v := range cc.own {
			newColors[k] = graph.PortColor(v)
		}
		for _, p := range cc.pairs {
			for label, pc := range map[string][2]graph.PortColor{
				"98f2ccc": {graph.PortColor(oldColors[p.src]), graph.PortColor(oldColors[p.tgt])},
				"t342":    {newColors[p.src], newColors[p.tgt]},
			} {
				if differs[cell{pc[0], pc[1], p.wf == graph.WF15}] {
					t.Errorf("%s %s (%s, %s) reaches a disagreeing cell %s→%s under %s colors", cc.name, p.wf, p.src, p.tgt, pc[0], pc[1], label)
				}
			}
		}
	}
}

// TestColorEquality_EveryDeclaredPairIsDiagonal: with the t342 colors every
// declared pair of the three ontologies joins one color to itself, so the
// equality gate admits each of them; WF08 bound-to/binds is topology/topology.
func TestColorEquality_EveryDeclaredPairIsDiagonal(t *testing.T) {
	for _, cc := range []struct {
		name  string
		pairs []declaredPair
		own   map[string]string
	}{
		{"4.0.7", pairs407, overrides407},
		{"4.0.8", pairs408(), overrides408()},
		{"mtdc-2.0.0", pairsMTDC200(), mtdcPortColorMap()},
	} {
		colors := legacyPortColors()
		for k, v := range cc.own {
			colors[k] = graph.PortColor(v)
		}
		for _, p := range cc.pairs {
			s, okS := colors[p.src]
			tg, okT := colors[p.tgt]
			if !okS || !okT || s == "" || s != tg {
				t.Errorf("%s %s (%s, %s): colors %q/%q — not one shared color", cc.name, p.wf, p.src, p.tgt, s, tg)
			}
		}
	}
	if got := legacyPortColors()["bound-to"]; got != graph.ColorTopology {
		t.Errorf("bound-to = %q, want topology (t342 ruling 2)", got)
	}
}

// TestLegacyPortColors_FrozenAt98f2ccc: the legacy table is the 98f2ccc
// DefaultPortColors() with exactly one difference — bound-to "" → topology
// (t342 ruling 2). It must never grow: new ports belong in the ontology's
// port_color_map (t342 ruling 6).
func TestLegacyPortColors_FrozenAt98f2ccc(t *testing.T) {
	frozen := frozen98f2cccColors(t)
	got := legacyPortColors()
	if len(frozen) != 92 || len(got) != 92 {
		t.Fatalf("table sizes: 98f2ccc %d, legacy %d — want 92 / 92 (the table is frozen)", len(frozen), len(got))
	}
	var diffs []string
	for port, c := range frozen {
		if string(got[port]) != c {
			diffs = append(diffs, fmt.Sprintf("%s: %q → %q", port, c, got[port]))
		}
	}
	for port := range got {
		if _, ok := frozen[port]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s: added", port))
		}
	}
	if len(diffs) != 1 || diffs[0] != `bound-to: "" → "topology"` {
		t.Errorf("legacy table vs 98f2ccc differs by %v, want exactly [bound-to: \"\" → \"topology\"]", diffs)
	}
	for port, c := range got {
		if c == "" {
			t.Errorf("legacy table still carries an empty color at %q (t342 ruling 2)", port)
		}
	}
}
