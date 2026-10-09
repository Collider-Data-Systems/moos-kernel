package operad

import (
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342, Copilot #80 (5): the env-gated integration checks compare against
// the frozen 98f2ccc ValidateLINK only for a legacy-merge ontology or a
// pinned version (frozenOracleApplies). A later self-colored ontology gets
// the production-only check, so adding a valid, colored port pair — or
// retiring the display-only matrix — does not fail as a 98f2ccc divergence.
// ------------------------------------------------------------------

// pairCites is a port pair no shipped ontology declares.
var pairCites = declaredPair{"WF12", "cites", "cited-by"}

// selfColoredWithCites is mtdc-2.0.0 plus pairCites, colored src/tgt in its
// own map, under a version outside t342OraclePinned.
func selfColoredWithCites(t *testing.T, src, tgt string, matrix map[string]map[string]any) (*Registry, map[string]string) {
	t.Helper()
	own := mtdcPortColorMap()
	own[pairCites.src], own[pairCites.tgt] = src, tgt
	pairs := append(pairsMTDC200(), pairCites)
	var reg *Registry
	var err error
	captureLog(t, func() {
		reg, err = LoadRegistry(writeOntology(t, tableOntology(t, "mtdc-2.0.0+cites-t342-synthetic", pairs, own, matrix)))
	})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg, own
}

func TestIntegrationOracle_SelfColoredNewPairAdmitted_t342(t *testing.T) {
	for name, matrix := range map[string]map[string]map[string]any{
		"identity matrix": identityMatrix(),
		"no matrix":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			reg, own := selfColoredWithCites(t, "semantic", "semantic", matrix)
			if reg.PortColorSource != PortColorSourceOntology || frozenOracleApplies(reg) || oracleMode(reg) != "production-only" {
				t.Fatalf("want a self-colored, unpinned registry on the production-only check; source %q, mode %q", reg.PortColorSource, oracleMode(reg))
			}
			n, problems := declaredPairProblems(t, reg, own)
			if n != 47 || len(problems) != 0 {
				t.Errorf("%d declared pairs, problems %v; want 47 and none", n, problems)
			}
			// What the restriction buys: without a matrix the frozen 98f2ccc
			// gate rejects every pair the production gate admits.
			if matrix != nil {
				return
			}
			old, rejected := oldMergedColors(t, own), 0
			for wf, spec := range reg.RewriteCategories {
				for _, pr := range declaredPairs(spec) {
					if legacyValidateLINK98f2ccc(reg, old, linkEnvelope(declaredPair{wf, pr[0], pr[1]})) != nil {
						rejected++
					}
				}
			}
			oracle := legacyValidateLINK98f2ccc(reg, old, linkEnvelope(pairCites))
			if rejected != n || oracle == nil || !strings.Contains(oracle.Error(), "semantic → semantic not allowed") {
				t.Errorf("98f2ccc oracle without a matrix: %d of %d pairs rejected, %v on %v; want all rejected by the matrix", rejected, n, oracle, pairCites)
			}
		})
	}
}

// TestIntegrationOracle_ProductionOnlyHasTeeth_t342: the production-only
// check still reports a declared pair production rejects and a served color
// that is not the ontology's own.
func TestIntegrationOracle_ProductionOnlyHasTeeth_t342(t *testing.T) {
	reg, own := selfColoredWithCites(t, "semantic", "storage", identityMatrix())
	_, problems := declaredPairProblems(t, reg, own)
	if len(problems) != 1 || !strings.Contains(problems[0], "WF12 (cites, cited-by): t342 ") {
		t.Errorf("want exactly the semantic/storage pair reported; got %v", problems)
	}

	reg, own = selfColoredWithCites(t, "semantic", "semantic", identityMatrix())
	reg.PortColors["governs"] = graph.ColorWorkflow // auth in the own map
	_, problems = declaredPairProblems(t, reg, own)
	if got := strings.Join(problems, "\n"); !strings.Contains(got, `port "governs" served "workflow", the ontology's own map "auth"`) {
		t.Errorf("want the recolored port reported; got %v", problems)
	}
}

// TestIntegrationOracle_PinnedAndLegacyKeepTheOracle_t342: the frozen
// oracle still judges the pinned versions and every legacy-merge ontology
// — including a later one that adds a pair colored in its own map.
func TestIntegrationOracle_PinnedAndLegacyKeepTheOracle_t342(t *testing.T) {
	own409 := map[string]string{pairCites.src: "semantic", pairCites.tgt: "semantic"}
	for k, v := range overrides407 {
		own409[k] = v
	}
	cases := []struct {
		version    string
		pairs      []declaredPair
		own        map[string]string
		matrix     map[string]map[string]any
		wantSource string
		wantPairs  int
	}{
		{"4.0.7", pairs407, overrides407, matrix407(), PortColorSourceLegacyMerge, 35},
		{"mtdc-2.0.0", pairsMTDC200(), mtdcPortColorMap(), identityMatrix(), PortColorSourceOntology, 46},
		{"4.0.9-t342-synthetic", append(append([]declaredPair{}, pairs407...), pairCites), own409, matrix407(), PortColorSourceLegacyMerge, 36},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			var reg *Registry
			var err error
			captureLog(t, func() {
				reg, err = LoadRegistry(writeOntology(t, tableOntology(t, c.version, c.pairs, c.own, c.matrix)))
			})
			if err != nil {
				t.Fatalf("LoadRegistry: %v", err)
			}
			if reg.PortColorSource != c.wantSource || !frozenOracleApplies(reg) || oracleMode(reg) != "98f2ccc oracle" {
				t.Fatalf("source %q, mode %q; want %q under the 98f2ccc oracle", reg.PortColorSource, oracleMode(reg), c.wantSource)
			}
			if n, problems := declaredPairProblems(t, reg, c.own); n != c.wantPairs || len(problems) != 0 {
				t.Errorf("%d declared pairs, problems %v; want %d and none", n, problems, c.wantPairs)
			}
		})
	}
}
