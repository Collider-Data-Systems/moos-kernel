package hdc

// Spike A (T=274 engine-language lane, ffs0
// dev/design/manifold-bump-4_0/20260802-t274-engine-language-choice.md).
//
// Benchmarks quantifying the language-independent costs on the HDC write
// path before any rewrite-language decision leans on "performance":
//
//   1. Codebook regeneration: Runtime.runHDCIndexAndDriftLocked passes a nil
//      encoder (runtime.go:971, :1068), so embeddingsFromState mints a fresh
//      NewEncoder() per call (spectral.go:406-409) and every URN's
//      10,000-iteration basis generation is redone — twice per rewrite.
//      Compare TypeExpressions with enc=nil vs a persistent warm encoder.
//   2. Per-node vs batch encoding: EncodeNode scans all Relations per node
//      (O(N·R) total); EncodeNodes builds the adjacency map once
//      (encode.go:90) but is unused by spectral.go.
//   3. HV algebra by value: HV is a 40 KB [10000]float32 passed and returned
//      by value — Bind moves ~120 KB per call.
//
// Benchmarks only — no production change. go test -bench=SpikeA -benchmem ./internal/hdc/
import (
	"fmt"
	"testing"

	"moos/kernel/internal/graph"
)

// benchState builds a deterministic synthetic graph: nNodes across 8 types in
// a ring of WF-tagged relations, relsPerNode incident relations per node on
// average. No RNG — URN identity is positional.
func benchState(nNodes, relsPerNode int) graph.GraphState {
	state := graph.NewGraphState()
	types := []graph.TypeID{
		"claim", "knowledge_item", "session", "agent",
		"purpose", "channel", "t_hook", "derivation",
	}
	for i := 0; i < nNodes; i++ {
		urn := graph.URN(fmt.Sprintf("urn:moos:bench:node-%04d", i))
		n := graph.Node{
			URN:    urn,
			TypeID: types[i%len(types)],
			Properties: map[string]graph.Property{
				"text": {Value: fmt.Sprintf("bench node %d", i), Mutability: "mutable"},
			},
		}
		state.Nodes[urn] = n
		graph.IndexAddNodeByType(state.NodesByType, urn, n.TypeID)
	}
	nRels := nNodes * relsPerNode / 2 // each relation touches two nodes
	for i := 0; i < nRels; i++ {
		src := graph.URN(fmt.Sprintf("urn:moos:bench:node-%04d", i%nNodes))
		tgt := graph.URN(fmt.Sprintf("urn:moos:bench:node-%04d", (i*7+1)%nNodes))
		urn := graph.URN(fmt.Sprintf("urn:moos:bench:rel-%05d", i))
		r := graph.Relation{
			URN:             urn,
			RewriteCategory: graph.RewriteCategory(fmt.Sprintf("WF%02d", 1+i%21)),
			SrcURN:          src,
			SrcPort:         "produces",
			TgtURN:          tgt,
			TgtPort:         "produced-by",
		}
		state.Relations[urn] = r
		graph.IndexAddRelationEndpoints(state.RelationsBySrc, state.RelationsByTgt, urn, src, tgt)
	}
	return state
}

// --- 1. Codebook cost -------------------------------------------------------

// Cold path: what the runtime does today on every rewrite — a fresh encoder,
// so every URN pays the 10,000-iteration RNG basis generation.
func BenchmarkSpikeACodebookCold(b *testing.B) {
	urns := make([]graph.URN, 256)
	for i := range urns {
		urns[i] = graph.URN(fmt.Sprintf("urn:moos:bench:node-%04d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb := NewCodebook()
		for _, u := range urns {
			_ = cb.Encode(u)
		}
	}
}

// Warm path: the same URNs against a persistent codebook (map hit only).
func BenchmarkSpikeACodebookWarm(b *testing.B) {
	urns := make([]graph.URN, 256)
	for i := range urns {
		urns[i] = graph.URN(fmt.Sprintf("urn:moos:bench:node-%04d", i))
	}
	cb := NewCodebook()
	for _, u := range urns {
		_ = cb.Encode(u)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, u := range urns {
			_ = cb.Encode(u)
		}
	}
}

// --- 2. TypeExpressions: nil encoder (runtime path) vs persistent encoder ---

func benchmarkTypeExpressions(b *testing.B, nNodes, relsPerNode int, persistent bool) {
	state := benchState(nNodes, relsPerNode)
	var enc *Encoder
	if persistent {
		enc = NewEncoder()
		_ = TypeExpressions(state, enc) // warm the codebook once
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = TypeExpressions(state, enc) // enc == nil → fresh NewEncoder() inside
	}
}

func BenchmarkSpikeATypeExpressionsNilEnc100(b *testing.B) {
	benchmarkTypeExpressions(b, 100, 4, false)
}

func BenchmarkSpikeATypeExpressionsWarmEnc100(b *testing.B) {
	benchmarkTypeExpressions(b, 100, 4, true)
}

func BenchmarkSpikeATypeExpressionsNilEnc300(b *testing.B) {
	benchmarkTypeExpressions(b, 300, 4, false)
}

func BenchmarkSpikeATypeExpressionsWarmEnc300(b *testing.B) {
	benchmarkTypeExpressions(b, 300, 4, true)
}

// --- 3. Per-node vs batch encoding -----------------------------------------

func BenchmarkSpikeAEncodeNodePerNode(b *testing.B) {
	state := benchState(300, 4)
	enc := NewEncoder()
	urns := make([]graph.URN, 0, len(state.Nodes))
	for urn := range state.Nodes {
		urns = append(urns, urn)
	}
	_ = enc.EncodeNodes(state, urns) // warm codebook so both benches compare pure encode cost
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, urn := range urns {
			_ = enc.EncodeNode(state, urn) // O(R) relation scan per node
		}
	}
}

func BenchmarkSpikeAEncodeNodesBatch(b *testing.B) {
	state := benchState(300, 4)
	enc := NewEncoder()
	urns := make([]graph.URN, 0, len(state.Nodes))
	for urn := range state.Nodes {
		urns = append(urns, urn)
	}
	_ = enc.EncodeNodes(state, urns)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = enc.EncodeNodes(state, urns) // adjacency map built once
	}
}

// --- 4. HV algebra, by value -----------------------------------------------

func BenchmarkSpikeABind(b *testing.B) {
	cb := NewCodebook()
	x := cb.Encode("urn:moos:bench:x")
	y := cb.Encode("urn:moos:bench:y")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Bind(x, y) // 2×40 KB in, 40 KB out, all by value
	}
}

func BenchmarkSpikeABundle12(b *testing.B) {
	cb := NewCodebook()
	vs := make([]HV, 12) // typeWeight(6) + self + ~5 wires: a typical EncodeNode bundle
	for i := range vs {
		vs[i] = cb.Encode(graph.URN(fmt.Sprintf("urn:moos:bench:b-%d", i)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Bundle(vs...)
	}
}

func BenchmarkSpikeACosine(b *testing.B) {
	cb := NewCodebook()
	x := cb.Encode("urn:moos:bench:x")
	y := cb.Encode("urn:moos:bench:y")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Cosine(x, y)
	}
}
