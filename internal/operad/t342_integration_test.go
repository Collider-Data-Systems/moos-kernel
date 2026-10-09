package operad

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342 env-gated checks against real ontology files and real logs. Each
// skips unless its variable is set, so plain `go test ./...` stays hermetic.
//
//	MOOS_INTEGRATION=1 [MOOS_ONTOLOGY_PATH=…]  the fleet ontology (CI job)
//	MOOS_T342_MTDC=<moos-mtdc-2.x.json>        a self-colored candidate
//	MOOS_T342_408=<ontology-4.0.8.json>        a legacy-merge ontology
//	MOOS_T342_LOG=<log.jsonl> MOOS_T342_LOG_ONTOLOGY=<ontology.json>
//	  [MOOS_T342_LOG_EXPECT=links/admitted/pair-rejected]  shadow replay
// ------------------------------------------------------------------

// rawColorSection reads an ontology's own port_color_map, matrix and types
// straight from the file, bypassing the loader.
type rawColorSection struct {
	Types struct {
		S2Infrastructure []rawNodeType `json:"s2_infrastructure"`
		S1Grammar        []rawNodeType `json:"s1_grammar"`
		InteractionNodes []rawNodeType `json:"interaction_nodes"`
	} `json:"types"`
	RewriteCategories      []rawRewriteCategory `json:"rewrite_categories"`
	PortColorCompatibility struct {
		PortColorMap map[string]string         `json:"port_color_map"`
		Matrix       map[string]map[string]any `json:"matrix"`
	} `json:"port_color_compatibility"`
}

func readRawColorSection(t *testing.T, path string) rawColorSection {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var raw rawColorSection
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return raw
}

// loadWithLog loads path and returns the registry plus what the loader logged.
func loadWithLog(t *testing.T, path string) (*Registry, string) {
	t.Helper()
	var reg *Registry
	var err error
	logged := captureLog(t, func() { reg, err = LoadRegistry(path) })
	if err != nil {
		t.Fatalf("LoadRegistry(%s): %v", path, err)
	}
	return reg, logged
}

// legacyValidateLINK98f2ccc is ValidateLINK as it stood at 98f2ccc, frozen
// as the oracle: the same WF / LINK / WF15-contract / pair stages (whose
// code t342 does not touch), then the 98f2ccc color stage over the 98f2ccc
// colors (oldColors) and the loaded matrix.
func legacyValidateLINK98f2ccc(reg *Registry, oldColors map[string]string, env graph.Envelope) error {
	wfSpec, ok := reg.RewriteCategories[env.RewriteCategory]
	if !ok {
		return fmt.Errorf("operad: unknown rewrite_category %q", env.RewriteCategory)
	}
	if !containsRewrite(wfSpec.AllowedRewrites, graph.LINK) {
		return fmt.Errorf("operad: rewrite_category %s does not allow LINK", env.RewriteCategory)
	}
	if env.RewriteCategory == graph.WF15 && env.ContractURN == "" {
		return fmt.Errorf("operad: WF15 LINK requires contract_urn")
	}
	pairsDeclared := wfSpec.SrcPort != "" || wfSpec.TgtPort != "" || len(wfSpec.AdditionalPortPairs) > 0
	if pairsDeclared && !linkPairDeclared(wfSpec, env.SrcPort, env.TgtPort) {
		return fmt.Errorf("operad: port pair (%s, %s) not declared for %s; expected %s",
			env.SrcPort, env.TgtPort, env.RewriteCategory, describeDeclaredPairs(wfSpec))
	}
	return legacyColorStage98f2ccc(oldColors, reg.PortColorMatrix, pairsDeclared, env.RewriteCategory, env.SrcPort, env.TgtPort)
}

// assertDeclaredPairsNeutral runs every declared pair of a loaded real
// ontology through the oracle and the production gate.
func assertDeclaredPairsNeutral(t *testing.T, reg *Registry, own map[string]string) int {
	t.Helper()
	old := oldMergedColors(t, own)
	n := 0
	for wf, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			n++
			env := linkEnvelope(declaredPair{wf, pr[0], pr[1]})
			oracle, got := legacyValidateLINK98f2ccc(reg, old, env), reg.ValidateLINK(env)
			if oracle != nil || got != nil {
				t.Errorf("%s (%s, %s): 98f2ccc %v, t342 %v — want both to admit", wf, pr[0], pr[1], oracle, got)
			}
		}
	}
	return n
}

func registryTypeOrder(raw rawColorSection) []graph.TypeID {
	var ids []graph.TypeID
	for _, g := range [][]rawNodeType{raw.Types.S2Infrastructure, raw.Types.S1Grammar, raw.Types.InteractionNodes} {
		for _, nt := range g {
			ids = append(ids, graph.TypeID(nt.ID))
		}
	}
	return ids
}

// TestIntegration_T342_FleetOntology: the CI ontology-integration job's view.
// Mode-only assertions for any ontology; the 4.0.7 numbers apply only while
// the fleet runs 4.0.7, so CI stays correct when ffs0 moves on.
func TestIntegration_T342_FleetOntology(t *testing.T) {
	if os.Getenv("MOOS_INTEGRATION") != "1" {
		t.Skip("set MOOS_INTEGRATION=1 to run against the sibling ffs0 ontology")
	}
	path := integrationOntologyPath(t)
	reg, logged := loadWithLog(t, path)
	raw := readRawColorSection(t, path)

	if strings.Contains(logged, "WARNING — color gate") || strings.Contains(logged, "have no color") {
		t.Errorf("the fleet ontology must load without color-gate or coverage warnings; got %q", logged)
	}
	n := assertDeclaredPairsNeutral(t, reg, raw.PortColorCompatibility.PortColorMap)
	t.Logf("%s (%s): source %s, %d declared pairs neutral, ports generated=%v drift types=%d",
		path, reg.Version, reg.PortColorSource, n, reg.PortsGenerated, len(reg.PortsDrift))

	if reg.PortColorSource == PortColorSourceLegacyMerge {
		old := oldMergedColors(t, raw.PortColorCompatibility.PortColorMap)
		for port, c := range old {
			want := c
			if port == "bound-to" {
				want = "topology"
			}
			if string(reg.PortColors[port]) != want {
				t.Errorf("%s: 98f2ccc %q, t342 %q", port, c, reg.PortColors[port])
			}
		}
		if len(old) != len(reg.PortColors) {
			t.Errorf("merged map: %d entries, 98f2ccc %d", len(reg.PortColors), len(old))
		}
	}

	if reg.Version != "4.0.7" {
		return
	}
	if reg.PortColorSource != PortColorSourceLegacyMerge || !strings.Contains(logged, "colors 6 of 66 declared ports") {
		t.Errorf("4.0.7: want legacy merge with 6 of 66 own-colored ports; source %q, log %q", reg.PortColorSource, logged)
	}
	if n != 35 {
		t.Errorf("4.0.7: %d declared pairs, want 35", n)
	}
	missing, unpaired := 0, 0
	for _, d := range reg.PortsDrift {
		missing += len(d.MissingOut) + len(d.MissingIn)
		unpaired += len(d.UnpairedOut) + len(d.UnpairedIn)
	}
	if reg.PortsGenerated || len(reg.PortsDrift) != 39 || missing != 181 || unpaired != 73 {
		t.Errorf("4.0.7 ports: generated=%v drift types=%d missing=%d unpaired=%d, want false/39/181/73",
			reg.PortsGenerated, len(reg.PortsDrift), missing, unpaired)
	}
	// Drift mode serves the authored blocks exactly as loaded.
	for _, nt := range append(append(raw.Types.S2Infrastructure, raw.Types.S1Grammar...), raw.Types.InteractionNodes...) {
		got := reg.NodeTypes[graph.TypeID(nt.ID)].Ports
		var want PortSpec
		if nt.Ports != nil {
			want = PortSpec{Out: nt.Ports.Out, In: nt.Ports.In, Self: nt.Ports.Self}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("4.0.7 %s: served ports %+v differ from the authored block %+v", nt.ID, got, want)
		}
	}
}

// TestIntegration_T342_MTDC: a self-colored mtdc candidate. Mode-only
// assertions for any version: its own map is the only color source, its
// ports are generated with zero drift and equal the authored blocks for every
// type, its matrix is the identity, and every declared pair is neutral. The
// mtdc-2.0.0 numbers (114 entries, 45 and 46 pairs, 56 types) apply only to
// that file, so later candidates (mtdc-2.1.0) run the same checks.
func TestIntegration_T342_MTDC(t *testing.T) {
	path := os.Getenv("MOOS_T342_MTDC")
	if path == "" {
		t.Skip("set MOOS_T342_MTDC to a self-colored mtdc ontology path (moos-mtdc-2.0.0.json or later)")
	}
	reg, logged := loadWithLog(t, path)
	raw := readRawColorSection(t, path)
	v200 := reg.Version == "mtdc-2.0.0"

	own := len(raw.PortColorCompatibility.PortColorMap)
	if reg.PortColorSource != PortColorSourceOntology || !strings.Contains(logged, fmt.Sprintf("single source (%d entries", own)) {
		t.Errorf("want the ontology as single color source (%d entries); source %q, log %q", own, reg.PortColorSource, logged)
	}
	if len(reg.PortColors) != own {
		t.Errorf("PortColors has %d entries, the file's map %d", len(reg.PortColors), own)
	}
	for port, c := range raw.PortColorCompatibility.PortColorMap {
		if string(reg.PortColors[port]) != c {
			t.Errorf("PortColors[%q] = %q, file %q", port, reg.PortColors[port], c)
		}
	}
	if v200 && own != 114 {
		t.Errorf("mtdc-2.0.0: own map has %d entries, want 114", own)
	}
	if want := mtdcPortColorMap(); v200 && !reflect.DeepEqual(raw.PortColorCompatibility.PortColorMap, want) {
		t.Errorf("the file's map differs from the hermetic mtdcPortColorMap table")
	}
	for _, r := range kernelPortColors {
		for _, c := range kernelPortColors {
			want := compatFalse
			if r == c {
				want = compatAllowed
			}
			if got := reg.PortColorMatrix[r][c]; got != want {
				t.Errorf("matrix[%s][%s] = %q, want %q (identity)", r, c, got, want)
			}
		}
	}
	if strings.Contains(logged, "WARNING") {
		t.Errorf("%s must load without warnings; got %q", reg.Version, logged)
	}

	if !reg.PortsGenerated || len(reg.PortsDrift) != 0 || !strings.Contains(logged, "ports: generated from ") {
		t.Errorf("want generated ports with zero drift; generated=%v drift=%v log=%q", reg.PortsGenerated, reg.PortsDrift, logged)
	}
	if v200 && !strings.Contains(logged, "ports: generated from 45 declared pairs for 56 types") {
		t.Errorf("mtdc-2.0.0: want ports generated from 45 declared pairs for 56 types; log %q", logged)
	}
	types := 0
	for _, nt := range append(append(raw.Types.S2Infrastructure, raw.Types.S1Grammar...), raw.Types.InteractionNodes...) {
		types++
		got := reg.NodeTypes[graph.TypeID(nt.ID)].Ports
		var want PortSpec
		if nt.Ports != nil {
			want = PortSpec{Out: nt.Ports.Out, In: nt.Ports.In, Self: nt.Ports.Self}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: generated %+v, authored %+v", nt.ID, got, want)
		}
	}
	if v200 && types != 56 {
		t.Errorf("mtdc-2.0.0: %d types, want 56", types)
	}
	ids := registryTypeOrder(raw)
	assertPortsProperty(t, ids, raw.RewriteCategories, generateTypePorts(ids, raw.RewriteCategories))

	n := assertDeclaredPairsNeutral(t, reg, raw.PortColorCompatibility.PortColorMap)
	if v200 && n != 46 {
		t.Errorf("mtdc-2.0.0: %d declared pairs, want 46", n)
	}
	t.Logf("%s (%s): single source %d entries, ports generated for %d types, %d declared pairs neutral",
		path, reg.Version, own, types, n)
}

// TestIntegration_T342_408: 4.0.8 (20-entry own map) is a legacy merge and
// every one of its 43 declared pairs is admitted by both gates.
func TestIntegration_T342_408(t *testing.T) {
	path := os.Getenv("MOOS_T342_408")
	if path == "" {
		t.Skip("set MOOS_T342_408 to the ontology 4.0.8 path")
	}
	reg, logged := loadWithLog(t, path)
	raw := readRawColorSection(t, path)
	if reg.PortColorSource != PortColorSourceLegacyMerge || !strings.Contains(logged, "colors 20 of 80 declared ports") {
		t.Errorf("want legacy merge (20 of 80); source %q, log %q", reg.PortColorSource, logged)
	}
	if !reflect.DeepEqual(raw.PortColorCompatibility.PortColorMap, overrides408()) {
		t.Errorf("the file's map differs from the hermetic overrides408 table")
	}
	if n := assertDeclaredPairsNeutral(t, reg, raw.PortColorCompatibility.PortColorMap); n != 43 {
		t.Errorf("%d declared pairs, want 43", n)
	}
}

// TestIntegration_T342_ShadowLog replays every LINK envelope of a real log
// through the frozen 98f2ccc ValidateLINK and the t342 one and requires the
// same verdict and the same message on each. Not possible through a lab
// kernel's HTTP surface without writing, so it runs here.
func TestIntegration_T342_ShadowLog(t *testing.T) {
	logPath, ontPath := os.Getenv("MOOS_T342_LOG"), os.Getenv("MOOS_T342_LOG_ONTOLOGY")
	if logPath == "" || ontPath == "" {
		t.Skip("set MOOS_T342_LOG and MOOS_T342_LOG_ONTOLOGY for the shadow replay")
	}
	reg, _ := loadWithLog(t, ontPath)
	raw := readRawColorSection(t, ontPath)
	old := oldMergedColors(t, raw.PortColorCompatibility.PortColorMap)

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open %s: %v", logPath, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	entries, links, admitted, pairRejected, otherRejected, diffs := 0, 0, 0, 0, 0, 0
	reasons := map[string]int{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var pr graph.PersistedRewrite
		if err := json.Unmarshal([]byte(line), &pr); err != nil {
			t.Fatalf("parse log line %d: %v", entries+1, err)
		}
		entries++
		if pr.Envelope.RewriteType != graph.LINK {
			continue
		}
		links++
		oracle, got := legacyValidateLINK98f2ccc(reg, old, pr.Envelope), reg.ValidateLINK(pr.Envelope)
		if fmt.Sprint(oracle) != fmt.Sprint(got) {
			diffs++
			t.Errorf("log_seq %d %s %s (%s, %s): 98f2ccc %v | t342 %v", pr.LogSeq, pr.Envelope.RelationURN,
				pr.Envelope.RewriteCategory, pr.Envelope.SrcPort, pr.Envelope.TgtPort, oracle, got)
			continue
		}
		switch {
		case got == nil:
			admitted++
		case strings.Contains(got.Error(), "port pair"):
			pairRejected++
			reasons[fmt.Sprintf("%s (%s, %s)", pr.Envelope.RewriteCategory, pr.Envelope.SrcPort, pr.Envelope.TgtPort)]++
		default:
			otherRejected++
			reasons[got.Error()]++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", logPath, err)
	}
	t.Logf("shadow %s × %s (%s): %d entries, %d LINKs, %d admitted by both, %d rejected by both at the pair gate, %d rejected by both elsewhere, %d differences",
		logPath, ontPath, reg.Version, entries, links, admitted, pairRejected, otherRejected, diffs)
	t.Logf("shadow rejection breakdown: %v", reasons)
	if want := os.Getenv("MOOS_T342_LOG_EXPECT"); want != "" {
		if got := fmt.Sprintf("%d/%d/%d", links, admitted, pairRejected); got != want {
			t.Errorf("links/admitted/pair-rejected = %s, want %s", got, want)
		}
	}
}
