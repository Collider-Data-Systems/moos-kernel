package fold

// Spike C (T=274 engine-language lane, ffs0
// dev/design/manifold-bump-4_0/DECISION-engine-language.md §3).
//
// Language-neutral replay-equivalence acceptance gate: fold the committed
// JSONL log at testdata/replay/t274-fixture.jsonl and compare the canonical
// serialization of the resulting state (Nodes + Relations only — the derived
// indexes are json:"-" and rebuilt by Rebuild) against the committed golden
// JSON and its SHA-256.
//
// Any candidate engine, in any language, reproduces the golden hash from the
// same JSONL or it is not an engine. The fixture exercises all four rewrite
// types, the idempotent-skip path (duplicate ADD at seq 9), additive MUTATE
// via PropertySpec (seq 11), and a WF15 contract-carrying LINK (seq 12).
//
// Regenerate goldens after an intentional fold-semantics change with:
//
//	MOOS_UPDATE_GOLDEN=1 go test -run TestReplayFixtureT274 ./internal/fold/
import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

const (
	fixturePath = "../../testdata/replay/t274-fixture.jsonl"
	goldenPath  = "../../testdata/replay/t274-fixture.golden.json"
	hashPath    = "../../testdata/replay/t274-fixture.sha256"
)

// canonicalState is the wire shape a candidate engine must reproduce:
// the two truth-carrying maps, marshaled with encoding/json (which sorts
// map keys), indented for reviewable diffs.
type canonicalState struct {
	Nodes     map[graph.URN]graph.Node     `json:"nodes"`
	Relations map[graph.URN]graph.Relation `json:"relations"`
}

func loadFixture(t *testing.T) []graph.PersistedRewrite {
	t.Helper()
	f, err := os.Open(filepath.Clean(fixturePath))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var entries []graph.PersistedRewrite
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var pr graph.PersistedRewrite
		if err := json.Unmarshal([]byte(line), &pr); err != nil {
			t.Fatalf("parse fixture line: %v", err)
		}
		entries = append(entries, pr)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return entries
}

func canonicalize(t *testing.T, state graph.GraphState) []byte {
	t.Helper()
	out, err := json.MarshalIndent(canonicalState{
		Nodes:     state.Nodes,
		Relations: state.Relations,
	}, "", "  ")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	return append(out, '\n')
}

func TestReplayFixtureT274(t *testing.T) {
	entries := loadFixture(t)
	if len(entries) != 12 {
		t.Fatalf("fixture length = %d, want 12", len(entries))
	}

	state, err := Replay(entries)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	canon := canonicalize(t, state)
	sum := sha256.Sum256(canon)
	gotHash := hex.EncodeToString(sum[:])

	if os.Getenv("MOOS_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, canon, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		if err := os.WriteFile(hashPath, []byte(gotHash+"\n"), 0o644); err != nil {
			t.Fatalf("write hash: %v", err)
		}
		t.Logf("goldens regenerated: sha256=%s", gotHash)
		return
	}

	wantCanon, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run once with MOOS_UPDATE_GOLDEN=1 to create): %v", err)
	}
	wantHashRaw, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatalf("read hash file: %v", err)
	}
	wantHash := strings.TrimSpace(string(wantHashRaw))

	if gotHash != wantHash {
		t.Errorf("canonical state hash mismatch:\n got  %s\n want %s", gotHash, wantHash)
	}
	if string(canon) != string(wantCanon) {
		t.Errorf("canonical state differs from golden %s (hash check above pinpoints it; diff the files for detail)", goldenPath)
	}

	// Determinism within a process: fold the same entries again and require
	// byte-identical canonical output (CI-4: same log → same state, always).
	state2, err := Replay(entries)
	if err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if string(canonicalize(t, state2)) != string(canon) {
		t.Error("Replay is not deterministic across runs in-process")
	}

	// Spot-check the semantics the golden encodes, so a regenerated golden
	// can't silently bless a broken fold:
	// seq 9's duplicate ADD must have been skipped (text keeps the seq-6 MUTATE),
	// seq 10's UNLINK removed the WF12 relation, seq 11 added "confidence".
	claim, ok := state.Nodes["urn:moos:claim:fixture.first"]
	if !ok {
		t.Fatal("claim node missing after replay")
	}
	if got := claim.Properties["text"].Value; got != "the log is truth; state is derived" {
		t.Errorf("claim text = %v — duplicate ADD at seq 9 was not skipped idempotently", got)
	}
	if _, ok := claim.Properties["confidence"]; !ok {
		t.Error("additive MUTATE (seq 11) did not land the confidence property")
	}
	if claim.Version != 3 {
		t.Errorf("claim version = %d, want 3 (ADD + 2 MUTATEs)", claim.Version)
	}
	if _, ok := state.Relations["urn:moos:rel:fixture.provides-kb"]; ok {
		t.Error("WF12 relation still present — UNLINK (seq 10) did not apply")
	}
	if rel, ok := state.Relations["urn:moos:rel:fixture.semantic"]; !ok {
		t.Error("WF15 relation missing")
	} else if rel.ContractURN != "urn:moos:contract:fixture.c1" {
		t.Errorf("WF15 contract_urn = %q", rel.ContractURN)
	}
	if len(state.Nodes) != 5 || len(state.Relations) != 2 {
		t.Errorf("state size = %d nodes / %d relations, want 5 / 2", len(state.Nodes), len(state.Relations))
	}
}
