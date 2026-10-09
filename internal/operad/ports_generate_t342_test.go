package operad

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// ------------------------------------------------------------------
// t342 ruling 4 (moos-kernel#72: enforce / delete / generate → generate):
// node-type ports.out / ports.in are generated from the declared pairs. Keyed
// to the ontology's own content: generated when every authored out/in list
// agrees with the pairs (as a set) or is omitted; otherwise the authored
// blocks are served unchanged and the drift is logged. Ports never gate a
// LINK in either mode.
// ------------------------------------------------------------------

// portsWFs exercises every generation rule, in declaration order:
//
//	P1 WF03 hosts→hosted-on            src [a]    tgt [b, c]
//	P2 WF07 participates→participated-by src [d]  tgt [] (every type)
//	P3 WF07 anchors→anchor (additional) src [c]   tgt ["*"]
//	—  WF15 {semantic}→{semantic}       placeholder, skipped
//	P5 WF19 pins-urn→pinned-by-session (additional only) src [a] tgt ["*"]
//	P6 WF04 contains→contained-in       src [a, b] tgt [b]
//	P7 WF98 hosts→hosted-on             src [c]    tgt [a]  (repeated names)
const portsWFs = `[
	{"id": "WF03", "allowed_rewrites": ["LINK"], "src_types": ["a"], "tgt_types": ["b", "c"], "src_port": "hosts", "tgt_port": "hosted-on"},
	{"id": "WF07", "allowed_rewrites": ["LINK"], "src_types": ["d"], "src_port": "participates", "tgt_port": "participated-by",
	 "additional_port_pairs": [{"src_port": "anchors", "tgt_port": "anchor", "src_types": ["c"], "tgt_types": ["*"]}]},
	{"id": "WF15", "allowed_rewrites": ["LINK"], "src_types": ["*"], "tgt_types": ["*"], "src_port": "{semantic}", "tgt_port": "{semantic}"},
	{"id": "WF19", "allowed_rewrites": ["LINK"],
	 "additional_port_pairs": [{"src_port": "pins-urn", "tgt_port": "pinned-by-session", "src_types": ["a"], "tgt_types": ["*"]}]},
	{"id": "WF04", "allowed_rewrites": ["LINK"], "src_types": ["a", "b"], "tgt_types": ["b"], "src_port": "contains", "tgt_port": "contained-in"},
	{"id": "WF98", "allowed_rewrites": ["LINK"], "src_types": ["c"], "tgt_types": ["a"], "src_port": "hosts", "tgt_port": "hosted-on"}
]`

// generatedPorts is what the rule yields for portsWFs.
var generatedPorts = map[graph.TypeID]PortSpec{
	"a": {Out: []string{"hosts", "pins-urn", "contains"}, In: []string{"participated-by", "anchor", "pinned-by-session", "hosted-on"}},
	"b": {Out: []string{"contains"}, In: []string{"hosted-on", "participated-by", "anchor", "pinned-by-session", "contained-in"}},
	"c": {Out: []string{"anchors", "hosts"}, In: []string{"hosted-on", "participated-by", "anchor", "pinned-by-session"}},
	"d": {Out: []string{"participates"}, In: []string{"participated-by", "anchor", "pinned-by-session"}},
	"e": {Out: nil, In: []string{"participated-by", "anchor", "pinned-by-session"}},
}

// portsOntology renders the portsWFs ontology with the given ports blocks
// (JSON object or "" for no block) for types a..e.
func portsOntology(blocks map[string]string) string {
	typ := func(id, stratum string) string {
		s := `{"id": "` + id + `", "stratum": "` + stratum + `"`
		if b := blocks[id]; b != "" {
			s += `, "ports": ` + b
		}
		return s + `}`
	}
	return `{
		"version": "t342-ports",
		"types": {
			"s2_infrastructure": [` + typ("a", "S2") + `, ` + typ("b", "S2") + `],
			"s1_grammar": [` + typ("c", "S1") + `],
			"interaction_nodes": [` + typ("d", "S2") + `, ` + typ("e", "S2") + `]
		},
		"rewrite_categories": ` + portsWFs + `,
		"port_color_compatibility": {"matrix": {}}
	}`
}

func TestGenerateTypePorts_Rule_t342(t *testing.T) {
	var raw []rawRewriteCategory
	if err := json.Unmarshal([]byte(portsWFs), &raw); err != nil {
		t.Fatal(err)
	}
	got := generateTypePorts([]graph.TypeID{"a", "b", "c", "d", "e"}, raw)
	if !reflect.DeepEqual(got, generatedPorts) {
		t.Errorf("generated ports:\n got  %v\n want %v", got, generatedPorts)
	}
	if got["e"].Out != nil {
		t.Errorf("an empty generated list must be nil (serializes like an absent key); got %#v", got["e"].Out)
	}
}

// TestLoadRegistry_PortsGenerated_t342: authored blocks that agree with the
// pairs (as sets, in any order) or are omitted give generated mode — the
// generated lists replace them in declaration order and self is kept.
func TestLoadRegistry_PortsGenerated_t342(t *testing.T) {
	var reg *Registry
	var err error
	logged := captureLog(t, func() {
		reg, err = LoadRegistry(writeOntology(t, portsOntology(map[string]string{
			"a": `{"self": ["introspects"]}`,
			"b": `{"out": ["contains"]}`,
			"c": `{"in": ["pinned-by-session", "anchor", "participated-by", "hosted-on"]}`,
			"e": `{"out": []}`,
		})))
	})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if !reg.PortsGenerated || len(reg.PortsDrift) != 0 {
		t.Fatalf("PortsGenerated = %v, PortsDrift = %v — want generated mode", reg.PortsGenerated, reg.PortsDrift)
	}
	for id, want := range generatedPorts {
		got := reg.NodeTypes[id].Ports
		if id == "e" {
			want.Out = []string{} // authored "out": [] stays [], not null
		}
		if !reflect.DeepEqual(got.Out, want.Out) || !reflect.DeepEqual(got.In, want.In) {
			t.Errorf("%s ports = %#v/%v, want %#v/%v", id, got.Out, got.In, want.Out, want.In)
		}
	}
	if b, _ := json.Marshal(reg.NodeTypes["e"].Ports); !strings.Contains(string(b), `"Out":[]`) {
		t.Errorf("e: authored empty out must serialize as [] on /operad/node-types; got %s", b)
	}
	if self := reg.NodeTypes["a"].Ports.Self; !reflect.DeepEqual(self, []string{"introspects"}) {
		t.Errorf("self must be kept: got %v", self)
	}
	if !strings.Contains(logged, "ports: generated from 6 declared pairs for 5 types") {
		t.Errorf("expected the generated-ports log line; got %q", logged)
	}
}

// TestLoadRegistry_PortsDriftServesAuthored_t342: one authored list that
// disagrees with the pairs keeps every authored block as loaded (the
// fleet's 4.0.7 case) and records the drift.
func TestLoadRegistry_PortsDriftServesAuthored_t342(t *testing.T) {
	var reg *Registry
	var err error
	logged := captureLog(t, func() {
		reg, err = LoadRegistry(writeOntology(t, portsOntology(map[string]string{
			"a": `{"out": ["hosts", "owns"], "self": ["introspects"]}`,
			"c": `{"in": ["anchor"]}`,
		})))
	})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.PortsGenerated {
		t.Fatalf("drifting authored blocks must not be replaced")
	}
	if got := reg.NodeTypes["a"].Ports; !reflect.DeepEqual(got.Out, []string{"hosts", "owns"}) || got.In != nil {
		t.Errorf("a: authored block not served as loaded: %v/%v", got.Out, got.In)
	}
	if got := reg.NodeTypes["b"].Ports; got.Out != nil || got.In != nil {
		t.Errorf("b: absent block must stay absent in drift mode: %v/%v", got.Out, got.In)
	}
	wantDrift := map[graph.TypeID]PortsDrift{
		"a": {MissingOut: []string{"contains", "pins-urn"}, UnpairedOut: []string{"owns"}},
		"c": {MissingIn: []string{"hosted-on", "participated-by", "pinned-by-session"}},
	}
	if !reflect.DeepEqual(reg.PortsDrift, wantDrift) {
		t.Errorf("PortsDrift = %+v, want %+v", reg.PortsDrift, wantDrift)
	}
	if !strings.Contains(logged, "WARNING — ports: 2 of 5 types' authored out/in differ from the pair-generated ports (5 missing, 1 unpaired); serving authored blocks") {
		t.Errorf("expected the drift warning; got %q", logged)
	}
}

// TestGenerateTypePorts_Property_t342: a port is in a type's generated out
// (in) set iff some non-placeholder declared pair has it as src (tgt) and
// admits the type on that side.
func TestGenerateTypePorts_Property_t342(t *testing.T) {
	var raw []rawRewriteCategory
	if err := json.Unmarshal([]byte(portsWFs), &raw); err != nil {
		t.Fatal(err)
	}
	ids := []graph.TypeID{"a", "b", "c", "d", "e"}
	assertPortsProperty(t, ids, raw, generateTypePorts(ids, raw))
}

// assertPortsProperty checks the generation property for every type and
// every named pair end. Shared with the real-ontology integration test.
func assertPortsProperty(t *testing.T, ids []graph.TypeID, raw []rawRewriteCategory, gen map[graph.TypeID]PortSpec) {
	t.Helper()
	admits := func(list []string, id graph.TypeID) bool {
		if len(list) == 0 {
			return true
		}
		for _, x := range list {
			if x == "*" || graph.TypeID(x) == id {
				return true
			}
		}
		return false
	}
	type side struct{ out bool }
	expect := map[graph.TypeID]map[side]map[string]bool{}
	for _, id := range ids {
		expect[id] = map[side]map[string]bool{{true}: {}, {false}: {}}
	}
	for _, wf := range raw {
		pairs := []rawPortPair{}
		if wf.SrcPort != "" || wf.TgtPort != "" {
			pairs = append(pairs, rawPortPair{wf.SrcPort, wf.TgtPort, wf.SrcTypes, wf.TgtTypes})
		}
		for _, ap := range wf.AdditionalPortPairs {
			pairs = append(pairs, rawPortPair{ap.SrcPort, ap.TgtPort, ap.SrcTypes, ap.TgtTypes})
		}
		for _, p := range pairs {
			if strings.HasPrefix(p.src, "{") || strings.HasPrefix(p.tgt, "{") {
				continue
			}
			for _, id := range ids {
				if p.src != "" && admits(p.srcTypes, id) {
					expect[id][side{true}][p.src] = true
				}
				if p.tgt != "" && admits(p.tgtTypes, id) {
					expect[id][side{false}][p.tgt] = true
				}
			}
		}
	}
	for _, id := range ids {
		for s, list := range map[side][]string{{true}: gen[id].Out, {false}: gen[id].In} {
			got := map[string]bool{}
			for _, p := range list {
				if got[p] {
					t.Errorf("%s: port %q generated twice", id, p)
				}
				got[p] = true
			}
			if !reflect.DeepEqual(got, expect[id][s]) {
				t.Errorf("%s out=%v: generated %v, want %v", id, s.out, got, expect[id][s])
			}
		}
	}
}

// TestValidateLINK_PortsBlockIsInert is the test kernel#72 owed: the ports
// blocks never gate a LINK. ValidateLINK and ValidateStrataLink admit a LINK
// on a port the source type's block does not list, and every verdict is
// identical with the blocks zeroed, as authored (drift mode) or generated.
func TestValidateLINK_PortsBlockIsInert(t *testing.T) {
	authored, err := LoadRegistry(writeOntology(t, portsOntology(map[string]string{
		"a": `{"out": ["hosts", "owns"]}`, // no "contains"
	})))
	if err != nil {
		t.Fatalf("LoadRegistry authored: %v", err)
	}
	generated, err := LoadRegistry(writeOntology(t, portsOntology(nil)))
	if err != nil {
		t.Fatalf("LoadRegistry generated: %v", err)
	}
	if authored.PortsGenerated || !generated.PortsGenerated {
		t.Fatalf("fixture modes: authored generated=%v, generated generated=%v", authored.PortsGenerated, generated.PortsGenerated)
	}
	zeroed, _ := LoadRegistry(writeOntology(t, portsOntology(nil)))
	for id, spec := range zeroed.NodeTypes {
		spec.Ports = PortSpec{}
		zeroed.NodeTypes[id] = spec
	}

	state := graph.GraphState{Nodes: map[graph.URN]graph.Node{
		"urn:moos:a:1": {URN: "urn:moos:a:1", TypeID: "a"},
		"urn:moos:b:1": {URN: "urn:moos:b:1", TypeID: "b"},
		"urn:moos:c:1": {URN: "urn:moos:c:1", TypeID: "c"},
	}}
	envs := []graph.Envelope{
		// a --contains--> b: admitted although a's authored block omits "contains".
		{RewriteType: graph.LINK, RewriteCategory: "WF04", SrcURN: "urn:moos:a:1", SrcPort: "contains", TgtURN: "urn:moos:b:1", TgtPort: "contained-in"},
		// c --hosts--> a under WF98: admitted; c's block never lists hosts in drift mode.
		{RewriteType: graph.LINK, RewriteCategory: "WF98", SrcURN: "urn:moos:c:1", SrcPort: "hosts", TgtURN: "urn:moos:a:1", TgtPort: "hosted-on"},
		// b --hosts--> c under WF03: rejected by the WF-level src_types, never by ports.
		{RewriteType: graph.LINK, RewriteCategory: "WF03", SrcURN: "urn:moos:b:1", SrcPort: "hosts", TgtURN: "urn:moos:c:1", TgtPort: "hosted-on"},
	}
	verdict := func(reg *Registry, env graph.Envelope) string {
		if err := reg.ValidateLINK(env); err != nil {
			return "link: " + err.Error()
		}
		if err := reg.ValidateStrataLink(env, state); err != nil {
			return "strata: " + err.Error()
		}
		return "admitted"
	}
	want := []string{"admitted", "admitted", "strata"}
	for i, env := range envs {
		va, vg, vz := verdict(authored, env), verdict(generated, env), verdict(zeroed, env)
		if va != vg || va != vz {
			t.Errorf("%s (%s, %s): verdict depends on the ports blocks — authored %q, generated %q, zeroed %q", env.RewriteCategory, env.SrcPort, env.TgtPort, va, vg, vz)
		}
		if !strings.HasPrefix(va, want[i]) {
			t.Errorf("%s (%s, %s): verdict %q, want %s", env.RewriteCategory, env.SrcPort, env.TgtPort, va, want[i])
		}
	}
}
