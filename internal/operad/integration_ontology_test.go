package operad

import (
	"moos/kernel/internal/graph"
	"os"
	"sort"
	"testing"
)

func integrationOntologyPath(t *testing.T) string {
	t.Helper()

	if path := os.Getenv("MOOS_ONTOLOGY_PATH"); path != "" {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("MOOS_ONTOLOGY_PATH=%q is not readable: %v", path, err)
		}
		return path
	}

	candidates := []string{
		"../../../ffs0/kb/superset/ontology.json",
		"../../../../ffs0/kb/superset/ontology.json",
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Fatalf("MOOS_INTEGRATION=1 but ontology.json not found; set MOOS_ONTOLOGY_PATH or provide one of %v", candidates)
	return ""
}

func TestIntegration_EveryNodeTypeStratumParses(t *testing.T) {
	if os.Getenv("MOOS_INTEGRATION") != "1" {
		t.Skip("set MOOS_INTEGRATION=1 to run against the sibling ffs0 ontology")
	}
	path := integrationOntologyPath(t)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry(%s): %v", path, err)
	}
	var bad []string
	for id, spec := range reg.NodeTypes {
		if _, err := graph.ParseStratum(spec.Stratum); err != nil {
			bad = append(bad, string(id)+"="+spec.Stratum)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d node type(s) have no S0-S4 stratum: %v", len(bad), bad)
	}
}
