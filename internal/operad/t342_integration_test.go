package operad

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
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
//
// The frozen 98f2ccc ValidateLINK is the oracle only where it means
// something (t342, Copilot #80 (5)): a legacy-merge ontology, whose colors
// still partly come from the frozen table, and the pinned versions the t342
// neutrality proof ran on. A self-colored ontology outside that set takes
// every color from its own port_color_map, so 98f2ccc's table and matrix
// cells say nothing about it: it gets the production-only check instead —
// every declared pair admitted by ValidateLINK, every color its own.
// ------------------------------------------------------------------

// t342OraclePinned are the versions the t342 neutrality proof compared
// against the frozen 98f2ccc gate (PR #80): the fleet's 4.0.7, 4.0.8, and
// the self-colored mtdc-2.0.0 and mtdc-2.1.0. The set never grows; a later
// ontology is compared with 98f2ccc only while it is a legacy merge.
var t342OraclePinned = map[string]bool{"4.0.7": true, "4.0.8": true, "mtdc-2.0.0": true, "mtdc-2.1.0": true}

// frozenOracleApplies reports whether a loaded ontology is compared with
// the frozen 98f2ccc ValidateLINK: a legacy-merge registry or a pinned
// version (t342, Copilot #80 (5)).
func frozenOracleApplies(reg *Registry) bool {
	return reg.PortColorSource == PortColorSourceLegacyMerge || t342OraclePinned[reg.Version]
}

// oracleMode names the check frozenOracleApplies picks, for test logs.
func oracleMode(reg *Registry) string {
	if frozenOracleApplies(reg) {
		return "98f2ccc oracle"
	}
	return "production-only"
}

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

// declaredPairProblems runs every declared pair of a loaded real ontology
// through the check frozenOracleApplies picks and returns the pair count and
// one line per problem, sorted. Under the oracle the frozen 98f2ccc
// ValidateLINK and the production one must both admit. Otherwise
// (production-only, t342, Copilot #80 (5)) the production ValidateLINK must
// admit, the color source must be the ontology, and the served colors must
// be exactly the ontology's own map — no frozen-table color anywhere.
func declaredPairProblems(t *testing.T, reg *Registry, own map[string]string) (int, []string) {
	t.Helper()
	var old map[string]string
	if frozenOracleApplies(reg) {
		old = oldMergedColors(t, own)
	}
	var problems []string
	n := 0
	for wf, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			n++
			env := linkEnvelope(declaredPair{wf, pr[0], pr[1]})
			got := reg.ValidateLINK(env)
			if old != nil {
				if oracle := legacyValidateLINK98f2ccc(reg, old, env); oracle != nil || got != nil {
					problems = append(problems, fmt.Sprintf("%s (%s, %s): 98f2ccc %v, t342 %v — want both to admit", wf, pr[0], pr[1], oracle, got))
				}
				continue
			}
			if got != nil {
				problems = append(problems, fmt.Sprintf("%s (%s, %s): t342 %v — want admitted", wf, pr[0], pr[1], got))
			}
		}
	}
	if old == nil {
		if reg.PortColorSource != PortColorSourceOntology {
			problems = append(problems, fmt.Sprintf("color source %q, want %q", reg.PortColorSource, PortColorSourceOntology))
		}
		if len(reg.PortColors) != len(own) {
			problems = append(problems, fmt.Sprintf("%d served colors, the ontology's own map has %d", len(reg.PortColors), len(own)))
		}
		for port, c := range reg.PortColors {
			if want, ok := own[port]; !ok || string(c) != want {
				problems = append(problems, fmt.Sprintf("port %q served %q, the ontology's own map %q", port, c, want))
			}
		}
	}
	sort.Strings(problems)
	return n, problems
}

// assertDeclaredPairsAdmitted reports declaredPairProblems as test errors
// and returns the declared pair count.
func assertDeclaredPairsAdmitted(t *testing.T, reg *Registry, own map[string]string) int {
	t.Helper()
	n, problems := declaredPairProblems(t, reg, own)
	for _, p := range problems {
		t.Error(p)
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
	n := assertDeclaredPairsAdmitted(t, reg, raw.PortColorCompatibility.PortColorMap)
	t.Logf("%s (%s): source %s, %d declared pairs admitted (%s), ports generated=%v drift types=%d",
		path, reg.Version, reg.PortColorSource, n, oracleMode(reg), reg.PortsGenerated, len(reg.PortsDrift))

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
// type, a shipped matrix is the identity, and every declared pair is
// admitted — against the frozen 98f2ccc oracle for the pinned mtdc-2.0.0 and
// mtdc-2.1.0, by production alone for any later version (t342, Copilot #80
// (5)). The mtdc-2.0.0 numbers (114 entries, 45 and 46 pairs, 56 types)
// apply only to that file.
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
	// The color gate is equality (t342 ruling 1): a later candidate may drop
	// the display-only matrix; a pinned version, or one that ships a matrix,
	// must carry the identity (t342, Copilot #80 (5)).
	if t342OraclePinned[reg.Version] || len(raw.PortColorCompatibility.Matrix) > 0 {
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

	n := assertDeclaredPairsAdmitted(t, reg, raw.PortColorCompatibility.PortColorMap)
	if v200 && n != 46 {
		t.Errorf("mtdc-2.0.0: %d declared pairs, want 46", n)
	}
	t.Logf("%s (%s): single source %d entries, ports generated for %d types, %d declared pairs admitted (%s)",
		path, reg.Version, own, types, n, oracleMode(reg))
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
	if n := assertDeclaredPairsAdmitted(t, reg, raw.PortColorCompatibility.PortColorMap); n != 43 {
		t.Errorf("%d declared pairs, want 43", n)
	}
}

// TestIntegration_T342_ShadowLog replays every LINK envelope of a real log
// through the frozen 98f2ccc ValidateLINK and the t342 one and requires the
// same verdict and the same message on each — for a legacy-merge or pinned
// ontology; any other is replayed through production alone and only counted
// (frozenOracleApplies, t342, Copilot #80 (5)). Not possible through a lab
// kernel's HTTP surface without writing, so it runs here.
func TestIntegration_T342_ShadowLog(t *testing.T) {
	logPath, ontPath := os.Getenv("MOOS_T342_LOG"), os.Getenv("MOOS_T342_LOG_ONTOLOGY")
	if logPath == "" || ontPath == "" {
		t.Skip("set MOOS_T342_LOG and MOOS_T342_LOG_ONTOLOGY for the shadow replay")
	}
	reg, _ := loadWithLog(t, ontPath)
	raw := readRawColorSection(t, ontPath)
	// Outside frozenOracleApplies the replay is production-only: verdicts
	// are counted, not compared (t342, Copilot #80 (5)).
	var old map[string]string
	if frozenOracleApplies(reg) {
		old = oldMergedColors(t, raw.PortColorCompatibility.PortColorMap)
	}

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
		got := reg.ValidateLINK(pr.Envelope)
		if old != nil {
			if oracle := legacyValidateLINK98f2ccc(reg, old, pr.Envelope); fmt.Sprint(oracle) != fmt.Sprint(got) {
				diffs++
				t.Errorf("log_seq %d %s %s (%s, %s): 98f2ccc %v | t342 %v", pr.LogSeq, pr.Envelope.RelationURN,
					pr.Envelope.RewriteCategory, pr.Envelope.SrcPort, pr.Envelope.TgtPort, oracle, got)
				continue
			}
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
	compared := fmt.Sprintf("%d differences", diffs)
	if old == nil {
		compared = "not compared with 98f2ccc"
	}
	t.Logf("shadow %s × %s (%s, %s): %d entries, %d LINKs, %d admitted, %d rejected at the pair gate, %d rejected elsewhere, %s",
		logPath, ontPath, reg.Version, oracleMode(reg), entries, links, admitted, pairRejected, otherRejected, compared)
	t.Logf("shadow rejection breakdown: %v", reasons)
	if want := os.Getenv("MOOS_T342_LOG_EXPECT"); want != "" {
		if got := fmt.Sprintf("%d/%d/%d", links, admitted, pairRejected); got != want {
			t.Errorf("links/admitted/pair-rejected = %s, want %s", got, want)
		}
	}
}
