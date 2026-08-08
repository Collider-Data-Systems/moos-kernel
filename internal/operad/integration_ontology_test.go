package operad

import (
	"os"
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
