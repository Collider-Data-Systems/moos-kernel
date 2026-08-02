package fold

// Spike A (T=274 engine-language lane) — fold-side costs.
//
// GraphState.Clone() deep-copies every node's Properties map and all three
// derived indexes on every rewrite (graph/state.go:91, TODO(perf) at :84),
// and EvaluateProgramAt clones once up front plus once per envelope inside
// EvaluateAt — N+1 full deep copies for an N-step program (program.go:28,36).
//
// Benchmarks only — no production change. go test -bench=SpikeA -benchmem ./internal/fold/
import (
	"fmt"
	"testing"
	"time"

	"moos/kernel/internal/graph"
)

func benchFoldState(nNodes, nRels int) graph.GraphState {
	state := graph.NewGraphState()
	for i := 0; i < nNodes; i++ {
		urn := graph.URN(fmt.Sprintf("urn:moos:bench:node-%05d", i))
		n := graph.Node{
			URN:    urn,
			TypeID: "knowledge_item",
			Properties: map[string]graph.Property{
				"text":      {Value: fmt.Sprintf("bench node %d", i), Mutability: "mutable"},
				"owner_urn": {Value: "urn:moos:user:sam", Mutability: "immutable"},
			},
		}
		state.Nodes[urn] = n
		graph.IndexAddNodeByType(state.NodesByType, urn, n.TypeID)
	}
	for i := 0; i < nRels; i++ {
		src := graph.URN(fmt.Sprintf("urn:moos:bench:node-%05d", i%nNodes))
		tgt := graph.URN(fmt.Sprintf("urn:moos:bench:node-%05d", (i*7+1)%nNodes))
		urn := graph.URN(fmt.Sprintf("urn:moos:bench:rel-%06d", i))
		r := graph.Relation{
			URN:             urn,
			RewriteCategory: "WF12",
			SrcURN:          src,
			SrcPort:         "provides-kb",
			TgtURN:          tgt,
			TgtPort:         "kb-provided-by",
		}
		state.Relations[urn] = r
		graph.IndexAddRelationEndpoints(state.RelationsBySrc, state.RelationsByTgt, urn, src, tgt)
	}
	return state
}

// --- Clone cost by graph size ----------------------------------------------

func benchmarkClone(b *testing.B, nNodes, nRels int) {
	state := benchFoldState(nNodes, nRels)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = state.Clone()
	}
}

func BenchmarkSpikeAClone100(b *testing.B) { benchmarkClone(b, 100, 200) }
func BenchmarkSpikeAClone1k(b *testing.B)  { benchmarkClone(b, 1000, 2000) }
func BenchmarkSpikeAClone5k(b *testing.B)  { benchmarkClone(b, 5000, 10000) }

// --- Single rewrite = one full clone ---------------------------------------

func benchmarkEvaluateADD(b *testing.B, nNodes, nRels int) {
	state := benchFoldState(nNodes, nRels)
	ts := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env := graph.Envelope{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:agent:bench",
			NodeURN:     graph.URN(fmt.Sprintf("urn:moos:bench:new-%09d", i)),
			TypeID:      "knowledge_item",
			Properties: map[string]graph.Property{
				"text": {Value: "one added node", Mutability: "mutable"},
			},
		}
		if _, _, err := EvaluateAt(state, env, ts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSpikeAEvaluateADD1k(b *testing.B) { benchmarkEvaluateADD(b, 1000, 2000) }
func BenchmarkSpikeAEvaluateADD5k(b *testing.B) { benchmarkEvaluateADD(b, 5000, 10000) }

// --- N-step program = N+1 full clones --------------------------------------

func benchmarkProgram(b *testing.B, nNodes, nRels, steps int) {
	state := benchFoldState(nNodes, nRels)
	ts := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		envs := make([]graph.Envelope, steps)
		stamps := make([]time.Time, steps)
		for s := 0; s < steps; s++ {
			envs[s] = graph.Envelope{
				RewriteType: graph.ADD,
				Actor:       "urn:moos:agent:bench",
				NodeURN:     graph.URN(fmt.Sprintf("urn:moos:bench:prog-%09d-%03d", i, s)),
				TypeID:      "knowledge_item",
				Properties: map[string]graph.Property{
					"text": {Value: "program step", Mutability: "mutable"},
				},
			}
			stamps[s] = ts
		}
		if _, _, err := EvaluateProgramAt(state, envs, stamps); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSpikeAProgram1kNodes8Steps(b *testing.B)  { benchmarkProgram(b, 1000, 2000, 8) }
func BenchmarkSpikeAProgram1kNodes32Steps(b *testing.B) { benchmarkProgram(b, 1000, 2000, 32) }
