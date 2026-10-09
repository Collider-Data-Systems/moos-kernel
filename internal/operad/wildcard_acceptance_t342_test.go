package operad

import (
	"os"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

func wildcardTestRegistry() (*Registry, graph.GraphState) {
	reg := EmptyRegistry()
	st := graph.NewGraphState()
	for _, ty := range []graph.TypeID{"user", "agent", "kernel", "program"} {
		reg.NodeTypes[ty] = NodeTypeSpec{ID: ty, Stratum: "S2"}
		u := graph.URN("urn:moos:" + string(ty) + ":t")
		st.Nodes[u] = graph.Node{URN: u, TypeID: ty}
	}
	star := []graph.TypeID{"*"}
	reg.RewriteCategories["WFA"] = RewriteCategorySpec{ID: "WFA", SrcPort: "a", TgtPort: "b",
		SrcTypes: star, TgtTypes: star}
	reg.RewriteCategories["WFB"] = RewriteCategorySpec{ID: "WFB", SrcPort: "a", TgtPort: "b",
		SrcTypes: star, TgtTypes: []graph.TypeID{"program"},
		AdditionalPortPairs: []AdditionalPortPair{
			{SrcPort: "c", TgtPort: "d", SrcTypes: star, TgtTypes: []graph.TypeID{"program"}},
		}}
	return reg, st
}

func TestValidateStrataLink_WildcardEveryPosition(t *testing.T) {
	reg, st := wildcardTestRegistry()
	cases := []struct {
		wf, sp, tp, src, tgt string
		ok                   bool
	}{
		{"WFA", "a", "b", "agent", "program", true},
		{"WFA", "a", "b", "user", "kernel", true},
		{"WFB", "a", "b", "agent", "program", true},
		{"WFB", "a", "b", "agent", "user", false},
		{"WFB", "c", "d", "kernel", "program", true},
		{"WFB", "c", "d", "kernel", "user", false},
	}
	for _, c := range cases {
		env := graph.Envelope{RewriteType: graph.LINK, RewriteCategory: graph.RewriteCategory(c.wf),
			SrcURN: graph.URN("urn:moos:" + c.src + ":t"), SrcPort: c.sp,
			TgtURN: graph.URN("urn:moos:" + c.tgt + ":t"), TgtPort: c.tp}
		if err := reg.ValidateStrataLink(env, st); (err == nil) != c.ok {
			t.Errorf("%s (%s,%s) %s->%s: want ok=%v, got %v", c.wf, c.sp, c.tp, c.src, c.tgt, c.ok, err)
		}
	}
}

func TestLoadRegistry_RejectsEmptyOrMixedTypeLists(t *testing.T) {
	const head = `{"version":"t","types":{"s2_infrastructure":[{"id":"user","stratum":"S2"}]},"rewrite_categories":[`
	const tail = `],"port_color_compatibility":{"matrix":{}}}`
	bad := map[string]string{
		"wf src []":     `{"id":"WFZ","allowed_rewrites":["LINK"],"src_port":"a","tgt_port":"b","src_types":[],"tgt_types":["user"]}`,
		"wf tgt null":   `{"id":"WFZ","allowed_rewrites":["LINK"],"src_port":"a","tgt_port":"b","src_types":["user"],"tgt_types":null}`,
		"wf tgt absent": `{"id":"WFZ","allowed_rewrites":["LINK"],"src_port":"a","tgt_port":"b","src_types":["user"]}`,
		"pair tgt []": `{"id":"WFZ","allowed_rewrites":["LINK"],"src_port":"a","tgt_port":"b","src_types":["user"],"tgt_types":["user"],
			"additional_port_pairs":[{"src_port":"c","tgt_port":"d","src_types":["user"],"tgt_types":[]}]}`,
		"mixed *": `{"id":"WFZ","allowed_rewrites":["LINK"],"src_port":"a","tgt_port":"b","src_types":["*","user"],"tgt_types":["user"]}`,
	}
	for name, wf := range bad {
		_, err := LoadRegistry(writeOntology(t, head+wf+tail))
		if err == nil {
			t.Errorf("%s: expected load error", name)
			continue
		}
		if !strings.Contains(err.Error(), "WFZ") || !strings.Contains(err.Error(), "_types") {
			t.Errorf("%s: error should name the WF and the key; got %q", name, err)
		}
	}
	good := `{"id":"WFZ","allowed_rewrites":["LINK"],"src_port":"a","tgt_port":"b","src_types":["*"],"tgt_types":["user"]}`
	if _, err := LoadRegistry(writeOntology(t, head+good+tail)); err != nil {
		t.Errorf(`["*"] must load: %v`, err)
	}
}

func TestIntegration_WF15WildcardAdmitsParseableTypePairs(t *testing.T) {
	if os.Getenv("MOOS_INTEGRATION") != "1" {
		t.Skip("MOOS_INTEGRATION != 1")
	}
	reg, err := LoadRegistry(integrationOntologyPath(t))
	if err != nil {
		t.Fatal(err)
	}
	st := graph.NewGraphState()
	var ids []graph.TypeID
	for id, spec := range reg.NodeTypes {
		if _, err := graph.ParseStratum(spec.Stratum); err != nil {
			continue // stratum early return: separate issue
		}
		ids = append(ids, id)
		u := graph.URN("urn:moos:t:" + string(id))
		st.Nodes[u] = graph.Node{URN: u, TypeID: id}
	}
	rejected := 0
	for _, s := range ids {
		for _, g := range ids {
			env := graph.Envelope{RewriteType: graph.LINK, RewriteCategory: graph.WF15,
				SrcURN: graph.URN("urn:moos:t:" + string(s)), SrcPort: "{semantic}",
				TgtURN: graph.URN("urn:moos:t:" + string(g)), TgtPort: "{semantic}",
				ContractURN: "urn:moos:contract:test.v1"}
			if err := reg.ValidateLINK(env); err != nil {
				t.Fatalf("ValidateLINK %s->%s: %v", s, g, err)
			}
			if err := reg.ValidateStrataLink(env, st); err != nil {
				rejected++
			}
		}
	}
	if rejected > 0 {
		t.Errorf("WF15 rejected %d of %d parseable type pairs", rejected, len(ids)*len(ids))
	}
}
