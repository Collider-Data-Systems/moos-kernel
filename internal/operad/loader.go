package operad

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"moos/kernel/internal/graph"
)

// LoadRegistry parses ontology.json and builds a Registry. The ontology
// path should point to ffs0/kb/superset/ontology.json or a copy of it.
// The parsed ontology version is captured on the returned Registry's
// Version field; callers that care about schema pinning should read that
// rather than assume a version here.
//
// If path is empty, returns an EmptyRegistry with a warning — the kernel
// will run but without type validation.
func LoadRegistry(path string) (*Registry, error) {
	if path == "" {
		return EmptyRegistry(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("operad: read ontology %q: %w", path, err)
	}

	var raw ontologyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("operad: parse ontology %q: %w", path, err)
	}

	reg := EmptyRegistry()
	reg.Version = raw.Version

	// Load node types: s2_infrastructure, s1_grammar, interaction_nodes.
	// typeOrder keeps declaration order for port generation (t342 ruling 4).
	var typeOrder []graph.TypeID
	for _, group := range [][]rawNodeType{raw.Types.S2Infrastructure, raw.Types.S1Grammar, raw.Types.InteractionNodes} {
		for _, nt := range group {
			reg.NodeTypes[graph.TypeID(nt.ID)] = parseNodeTypeSpec(nt)
			typeOrder = append(typeOrder, graph.TypeID(nt.ID))
		}
	}

	// Load rewrite categories (WF01-WF19)
	for _, wf := range raw.RewriteCategories {
		spec := RewriteCategorySpec{
			ID:        graph.RewriteCategory(wf.ID),
			Name:      wf.Name,
			SrcPort:   wf.SrcPort,
			TgtPort:   wf.TgtPort,
			Authority: wf.Authority,
			SyncMode:  wf.SyncMode,
		}
		for _, rt := range wf.AllowedRewrites {
			spec.AllowedRewrites = append(spec.AllowedRewrites, graph.RewriteType(rt))
		}
		if wf.SrcPort != "" || wf.TgtPort != "" {
			pairLabel := fmt.Sprintf("primary pair (%s, %s)", wf.SrcPort, wf.TgtPort)
			if err := validateTypeList(wf.ID, pairLabel, "src_types", wf.SrcTypes); err != nil {
				return nil, err
			}
			if err := validateTypeList(wf.ID, pairLabel, "tgt_types", wf.TgtTypes); err != nil {
				return nil, err
			}
		}
		for _, t := range wf.SrcTypes {
			spec.SrcTypes = append(spec.SrcTypes, graph.TypeID(t))
		}
		for _, t := range wf.TgtTypes {
			spec.TgtTypes = append(spec.TgtTypes, graph.TypeID(t))
		}
		if ms, ok := wf.MutateScope.([]any); ok {
			for _, s := range ms {
				if sv, ok := s.(string); ok {
					spec.MutateScope = append(spec.MutateScope, sv)
				}
			}
		}
		// Load additional_port_pairs (v3.10+ WF extension mechanism).
		// Each declared pair is a legal (src_port, tgt_port) pairing for this WF
		// in addition to the primary SrcPort/TgtPort. Pre-v3.10 loaders ignored
		// the field; strict port-pair validation lands with this loader change.
		for _, app := range wf.AdditionalPortPairs {
			pair := AdditionalPortPair{
				SrcPort:          app.SrcPort,
				TgtPort:          app.TgtPort,
				SrcTypes:         make([]graph.TypeID, len(app.SrcTypes)),
				TgtTypes:         make([]graph.TypeID, len(app.TgtTypes)),
				AddedInVersion:   app.AddedInVersion,
				PromotesFragment: app.PromotesFragment,
				Description:      app.Description,
			}
			pairLabel := fmt.Sprintf("additional pair (%s, %s)", app.SrcPort, app.TgtPort)
			if err := validateTypeList(wf.ID, pairLabel, "src_types", app.SrcTypes); err != nil {
				return nil, err
			}
			if err := validateTypeList(wf.ID, pairLabel, "tgt_types", app.TgtTypes); err != nil {
				return nil, err
			}
			for i, t := range app.SrcTypes {
				pair.SrcTypes[i] = graph.TypeID(t)
			}
			for i, t := range app.TgtTypes {
				pair.TgtTypes[i] = graph.TypeID(t)
			}
			spec.AdditionalPortPairs = append(spec.AdditionalPortPairs, pair)
		}
		reg.RewriteCategories[spec.ID] = spec
	}

	// Close the color set (t342): the ontology's port_colors vocabulary is
	// the only color a matrix key, a matrix column or a port_color_map value
	// may name, and a matrix cell must be one of the four documented cell
	// values. Before t342 an unknown cell silently became false and the
	// vocabulary was not loaded at all.
	declared, err := declaredPortColors(raw.PortColorCompatibility.PortColors)
	if err != nil {
		return nil, err
	}

	// Load the port color compatibility matrix. Since t342 ruling 1 the
	// gate admits equal colors only; the matrix is kept for display and for
	// the agreement report below.
	if raw.PortColorCompatibility.Matrix != nil {
		m, err := parseColorMatrix(raw.PortColorCompatibility.Matrix, declared)
		if err != nil {
			return nil, err
		}
		reg.PortColorMatrix = m
	}

	// Port-name → color map (§12.1, moos-kernel#50). The ontology's own
	// port_color_map is the single source of port colors when it colors
	// every declared port (t342 ruling 6); see resolvePortColorSource. JSON
	// null, the literal "" and any color outside port_colors are load errors:
	// "" was the WF08 bound-to exemption, retired by t342 ruling 2, and null
	// or a typo would otherwise silently weaken the fail-closed gate.
	own := make(map[string]graph.PortColor, len(raw.PortColorCompatibility.PortColorMap))
	for port, colorName := range raw.PortColorCompatibility.PortColorMap {
		if colorName == nil {
			return nil, fmt.Errorf("operad: port_color_map: null color for port %q (valid: %s)", port, colorList(declared))
		}
		if *colorName == "" {
			return nil, fmt.Errorf("operad: port_color_map: empty color for port %q — the \"\" exemption is gone (t342 ruling 2); give the port its pair partner's color (valid: %s)", port, colorList(declared))
		}
		color := graph.PortColor(*colorName)
		if !declared[color] {
			return nil, fmt.Errorf("operad: port_color_map: unknown color %q for port %q (valid: %s)", *colorName, port, colorList(declared))
		}
		own[port] = color
	}
	if err := resolvePortColorSource(reg, own, declared); err != nil {
		return nil, err
	}

	// Load-time coverage check (soft, moos-kernel#50 review): every declared
	// pair's ports should have a color entry — an uncovered port means every
	// LINK on that pair will be rejected fail-closed at validation time.
	// Warn loudly but boot anyway: refusing to start on a future ontology's
	// new pair would trade a scoped LINK failure for a bricked fleet. Only a
	// legacy-merge registry can reach this: a self-colored ontology colors
	// every declared port by definition.
	var uncovered []string
	for wf, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			for _, port := range pr {
				if _, ok := reg.PortColors[port]; !ok {
					uncovered = append(uncovered, fmt.Sprintf("%s:%s", wf, port))
				}
			}
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		log.Printf("operad: WARNING — %d declared port(s) have no color; LINKs on their pairs will be REJECTED fail-closed (§12.2): %s (add them to port_color_compatibility.port_color_map)",
			len(uncovered), strings.Join(uncovered, ", "))
	}
	reportColorGate(reg)

	// Node-type ports from the declared pairs (t342 ruling 4, moos-kernel#72).
	resolveTypePorts(reg, typeOrder, raw.RewriteCategories)

	return reg, nil
}

// declaredPairs returns every (src, tgt) port pair the WF declares —
// the primary pair (when present) plus all additional_port_pairs.
func declaredPairs(spec RewriteCategorySpec) [][2]string {
	pairs := make([][2]string, 0, 1+len(spec.AdditionalPortPairs))
	if spec.SrcPort != "" || spec.TgtPort != "" {
		pairs = append(pairs, [2]string{spec.SrcPort, spec.TgtPort})
	}
	for _, ap := range spec.AdditionalPortPairs {
		pairs = append(pairs, [2]string{ap.SrcPort, ap.TgtPort})
	}
	return pairs
}

// kernelPortColors is the kernel's §12.1 color vocabulary in canonical order.
var kernelPortColors = []graph.PortColor{
	graph.ColorAuth, graph.ColorTopology, graph.ColorTransport, graph.ColorCompute,
	graph.ColorStorage, graph.ColorWorkflow, graph.ColorSemantic, graph.ColorProjection,
}

// knownPortColor reports whether c is one of the eight §12.1 colors.
func knownPortColor(c graph.PortColor) bool {
	for _, k := range kernelPortColors {
		if c == k {
			return true
		}
	}
	return false
}

// declaredPortColors returns the ontology's color vocabulary
// (port_color_compatibility.port_colors) as a set: the closed color set the
// rest of the color section is checked against (t342 loader closure). Each
// entry must be one of the kernel's eight §12.1 colors and appear once; an
// absent key declares all eight.
func declaredPortColors(raw []string) (map[graph.PortColor]bool, error) {
	declared := make(map[graph.PortColor]bool, len(kernelPortColors))
	if raw == nil {
		for _, c := range kernelPortColors {
			declared[c] = true
		}
		return declared, nil
	}
	for _, name := range raw {
		c := graph.PortColor(name)
		if !knownPortColor(c) {
			return nil, fmt.Errorf("operad: port_color_compatibility.port_colors: unknown color %q (the kernel knows %s)", name, colorList(nil))
		}
		if declared[c] {
			return nil, fmt.Errorf("operad: port_color_compatibility.port_colors: color %q is listed twice", name)
		}
		declared[c] = true
	}
	return declared, nil
}

// colorList renders the colors of set in canonical order for error
// messages; a nil set renders all eight.
func colorList(set map[graph.PortColor]bool) string {
	names := make([]string, 0, len(kernelPortColors))
	for _, c := range kernelPortColors {
		if set == nil || set[c] {
			names = append(names, string(c))
		}
	}
	return strings.Join(names, ", ")
}

// resolvePortColorSource builds Registry.PortColors from the ontology's own
// port_color_map (t342 ruling 6). The ontology is self-colored when it
// declares at least one pair and its own map colors every end of every
// declared pair: the own map is then the single source and the kernel's
// table is not consulted. Otherwise (the fleet's 4.0.7, which colors 6 of
// its 66 declared ports) the own map is laid over the frozen
// legacyPortColors() table, exactly as before t342 apart from bound-to. In
// both modes every color that reaches a declared port must be a declared
// color.
func resolvePortColorSource(reg *Registry, own map[string]graph.PortColor, declared map[graph.PortColor]bool) error {
	ends := declaredPortEnds(reg)
	covered := 0
	for _, port := range ends {
		if _, ok := own[port]; ok {
			covered++
		}
	}
	if len(ends) > 0 && covered == len(ends) {
		reg.PortColors = make(map[string]graph.PortColor, len(own))
		for port, c := range own {
			reg.PortColors[port] = c
		}
		reg.PortColorSource = PortColorSourceOntology
		log.Printf("operad: port colors: ontology port_color_map is the single source (%d entries, t342 ruling 6)", len(own))
	} else {
		reg.PortColors = legacyPortColors()
		for port, c := range own {
			reg.PortColors[port] = c
		}
		reg.PortColorSource = PortColorSourceLegacyMerge
		if len(ends) > 0 {
			log.Printf("operad: port colors: ontology port_color_map colors %d of %d declared ports; the rest come from the kernel's frozen legacy table (t342 ruling 6: the map becomes the single source once it colors every declared port)",
				covered, len(ends))
		}
	}
	for _, port := range ends {
		if c, ok := reg.PortColors[port]; ok && !declared[c] {
			return fmt.Errorf("operad: color %q of declared port %q is not in port_color_compatibility.port_colors (%s)", c, port, colorList(declared))
		}
	}
	return nil
}

// declaredPortEnds returns every port name on a declared pair of any WF,
// sorted and without duplicates.
func declaredPortEnds(reg *Registry) []string {
	seen := make(map[string]bool)
	for _, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			seen[pr[0]] = true
			seen[pr[1]] = true
		}
	}
	ends := make([]string, 0, len(seen))
	for port := range seen {
		ends = append(ends, port)
	}
	sort.Strings(ends)
	return ends
}

// reportColorGate logs, at load time, every declared pair the §12.2 color
// gate treats specially (t342 ruling 1). It never fails the load, matching
// the coverage check's posture: a scoped LINK failure beats a dead kernel,
// and the operad workbench is the hard gate upstream.
//
//   - a declared pair whose two ends carry different colors: equality
//     rejects every LINK on it;
//   - when the ontology ships a non-empty matrix: a declared pair on which
//     the matrix (98f2ccc cell semantics) and equality disagree. None of
//     4.0.4 through mtdc-2.0.0 has one, which is why the switch to equality
//     is admission-neutral on the fleet.
//
// Pairs with an uncolored end are left to the coverage warning.
func reportColorGate(reg *Registry) {
	var mismatched, disagree []string
	for wf, spec := range reg.RewriteCategories {
		for _, pr := range declaredPairs(spec) {
			src, tgt, err := reg.resolvePortColors(pr[0], pr[1])
			if err != nil {
				continue
			}
			equal := src == tgt
			if !equal {
				mismatched = append(mismatched, fmt.Sprintf("%s (%s, %s) %s/%s", wf, pr[0], pr[1], src, tgt))
			}
			if len(reg.PortColorMatrix) > 0 && reg.PortColorMatrix.Allowed(src, tgt, wf) != equal {
				disagree = append(disagree, fmt.Sprintf("%s (%s, %s) %s→%s matrix=%t equality=%t", wf, pr[0], pr[1], src, tgt, !equal, equal))
			}
		}
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		log.Printf("operad: WARNING — color gate: %d declared pair(s) join two different colors; every LINK on them is REJECTED (§12.2 equality, t342 ruling 1): %s",
			len(mismatched), strings.Join(mismatched, ", "))
	}
	if len(disagree) > 0 {
		sort.Strings(disagree)
		log.Printf("operad: WARNING — color gate: port_color_compatibility.matrix disagrees with equality on %d declared pair(s); the kernel follows equality (t342 ruling 1): %s",
			len(disagree), strings.Join(disagree, ", "))
	}
}

func parseNodeTypeSpec(raw rawNodeType) NodeTypeSpec {
	spec := NodeTypeSpec{
		ID:         graph.TypeID(raw.ID),
		Stratum:    raw.Stratum,
		URNPattern: raw.URNPattern,
	}
	if raw.Ports != nil {
		spec.Ports = PortSpec{
			Out:  raw.Ports.Out,
			In:   raw.Ports.In,
			Self: raw.Ports.Self,
		}
	}
	spec.Properties = make(map[string]PropertySpec, len(raw.Properties))
	for name, p := range raw.Properties {
		ps := PropertySpec{
			Mutability:     p.Mutability,
			AuthorityScope: p.AuthorityScope,
			Type:           p.Type,
			Note:           p.Note,
		}
		for _, v := range p.Values {
			ps.Values = append(ps.Values, v)
		}
		spec.Properties[name] = ps
	}
	return spec
}

// parseColorMatrix loads port_color_compatibility.matrix and closes it over
// the declared colors (t342): every row and column key must be a declared
// color and every cell one of true, false, "wf15_only", "sink_only". Any
// other cell (another string, a number, null, an object) is a load error
// naming the row, the column and the value; before t342 it silently became
// false.
func parseColorMatrix(raw map[string]map[string]any, declared map[graph.PortColor]bool) (PortColorMatrix, error) {
	m := make(PortColorMatrix, len(raw))
	rows := make([]string, 0, len(raw))
	for src := range raw {
		rows = append(rows, src)
	}
	sort.Strings(rows)
	for _, src := range rows {
		srcColor := graph.PortColor(src)
		if !declared[srcColor] {
			return nil, fmt.Errorf("operad: port_color_compatibility.matrix: row %q is not a declared color (%s)", src, colorList(declared))
		}
		row := raw[src]
		if row == nil {
			return nil, fmt.Errorf("operad: port_color_compatibility.matrix: row %q is null", src)
		}
		cols := make([]string, 0, len(row))
		for tgt := range row {
			cols = append(cols, tgt)
		}
		sort.Strings(cols)
		m[srcColor] = make(map[graph.PortColor]colorCompat, len(row))
		for _, tgt := range cols {
			tgtColor := graph.PortColor(tgt)
			if !declared[tgtColor] {
				return nil, fmt.Errorf("operad: port_color_compatibility.matrix[%q]: column %q is not a declared color (%s)", src, tgt, colorList(declared))
			}
			cell, ok := parseColorCell(row[tgt])
			if !ok {
				val, _ := json.Marshal(row[tgt])
				return nil, fmt.Errorf("operad: port_color_compatibility.matrix[%q][%q]: invalid cell %s (valid: true, false, \"wf15_only\", \"sink_only\")", src, tgt, val)
			}
			m[srcColor][tgtColor] = cell
		}
	}
	return m, nil
}

// parseColorCell maps one JSON matrix cell onto its colorCompat value.
func parseColorCell(val any) (colorCompat, bool) {
	switch v := val.(type) {
	case bool:
		if v {
			return compatAllowed, true
		}
		return compatFalse, true
	case string:
		switch v {
		case "wf15_only":
			return compatWF15Only, true
		case "sink_only":
			return compatSinkOnly, true
		}
	}
	return "", false
}

// rawPortPair is one declared pair as authored: a WF's primary pair with the
// WF-level type lists, or an additional pair with its own lists.
type rawPortPair struct {
	src, tgt           string
	srcTypes, tgtTypes []string
}

// rawDeclaredPairs walks the authored rewrite categories in declaration
// order: each WF's primary pair (when present), then its
// additional_port_pairs.
func rawDeclaredPairs(wfs []rawRewriteCategory) []rawPortPair {
	var pairs []rawPortPair
	for _, wf := range wfs {
		if wf.SrcPort != "" || wf.TgtPort != "" {
			pairs = append(pairs, rawPortPair{wf.SrcPort, wf.TgtPort, wf.SrcTypes, wf.TgtTypes})
		}
		for _, ap := range wf.AdditionalPortPairs {
			pairs = append(pairs, rawPortPair{ap.SrcPort, ap.TgtPort, ap.SrcTypes, ap.TgtTypes})
		}
	}
	return pairs
}

// isPlaceholderPort reports a "{…}" pair end such as WF15's "{semantic}": a
// contract slot, not a port name.
func isPlaceholderPort(port string) bool {
	return len(port) >= 2 && port[0] == '{' && port[len(port)-1] == '}'
}

// admitsType reports whether a pair's src or tgt type list admits id: an
// empty list or "*" admits every type.
func admitsType(list []string, id graph.TypeID) bool {
	if len(list) == 0 {
		return true
	}
	for _, t := range list {
		if t == "*" || graph.TypeID(t) == id {
			return true
		}
	}
	return false
}

// generateTypePorts derives every type's ports.out / ports.in from the
// declared pairs (t342 ruling 4, moos-kernel#72) by the operad workbench's
// rule (mtdc-lab operad-lib.js generateTypePorts): a type gets an out-port
// for each pair whose src list admits it and an in-port for each pair whose
// tgt list admits it. Placeholder pairs are skipped, and so is an unnamed
// end. Pairs are taken in declaration order and the first occurrence of a
// port wins. A type with no generated port gets nil, which serializes like
// an absent authored key; resolveTypePorts keeps an authored empty list as
// [] so its /operad/node-types shape does not change.
func generateTypePorts(typeIDs []graph.TypeID, wfs []rawRewriteCategory) map[graph.TypeID]PortSpec {
	gen := make(map[graph.TypeID]PortSpec, len(typeIDs))
	for _, id := range typeIDs {
		gen[id] = PortSpec{}
	}
	for _, pr := range rawDeclaredPairs(wfs) {
		if isPlaceholderPort(pr.src) || isPlaceholderPort(pr.tgt) {
			continue
		}
		for _, id := range typeIDs {
			ps := gen[id]
			if pr.src != "" && admitsType(pr.srcTypes, id) && !containsString(ps.Out, pr.src) {
				ps.Out = append(ps.Out, pr.src)
			}
			if pr.tgt != "" && admitsType(pr.tgtTypes, id) && !containsString(ps.In, pr.tgt) {
				ps.In = append(ps.In, pr.tgt)
			}
			gen[id] = ps
		}
	}
	return gen
}

// portsDrift compares each type's authored ports.out / ports.in, as sets,
// with the generated ports. Only authored keys are compared: a nil list is
// an absent key, which generation fills rather than contradicts.
func portsDrift(nodeTypes map[graph.TypeID]NodeTypeSpec, gen map[graph.TypeID]PortSpec) map[graph.TypeID]PortsDrift {
	drift := make(map[graph.TypeID]PortsDrift)
	for id, spec := range nodeTypes {
		var d PortsDrift
		if spec.Ports.Out != nil {
			d.MissingOut, d.UnpairedOut = setDiff(gen[id].Out, spec.Ports.Out), setDiff(spec.Ports.Out, gen[id].Out)
		}
		if spec.Ports.In != nil {
			d.MissingIn, d.UnpairedIn = setDiff(gen[id].In, spec.Ports.In), setDiff(spec.Ports.In, gen[id].In)
		}
		if len(d.MissingOut)+len(d.MissingIn)+len(d.UnpairedOut)+len(d.UnpairedIn) > 0 {
			drift[id] = d
		}
	}
	return drift
}

// setDiff returns the sorted members of a that are not in b, or nil.
func setDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !containsString(b, x) && !containsString(out, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// resolveTypePorts decides where NodeTypeSpec.Ports.Out/In come from (t342
// ruling 4, moos-kernel#72: enforce / delete / generate → generate). When
// every authored out/in list equals, as a set, the ports generated from the
// declared pairs, or is omitted, the generated lists replace them (self is
// kept: no pair describes it) and Registry.PortsGenerated is set. When any
// authored list differs, the authored blocks are served unchanged and the
// differences are logged and kept in Registry.PortsDrift. That is the
// fleet's 4.0.7, whose /operad/node-types output therefore does not change.
// An ontology without a nameable pair keeps its blocks as loaded. The ports
// blocks never gate a LINK in either mode.
func resolveTypePorts(reg *Registry, typeOrder []graph.TypeID, wfs []rawRewriteCategory) {
	pairs := 0
	for _, pr := range rawDeclaredPairs(wfs) {
		if !isPlaceholderPort(pr.src) && !isPlaceholderPort(pr.tgt) {
			pairs++
		}
	}
	if pairs == 0 {
		return
	}
	gen := generateTypePorts(typeOrder, wfs)
	drift := portsDrift(reg.NodeTypes, gen)
	if len(drift) == 0 {
		for id, spec := range reg.NodeTypes {
			out, in := gen[id].Out, gen[id].In
			// An authored empty list that generates nothing stays [] and
			// does not become null on /operad/node-types (t342 ruling 4).
			if out == nil && spec.Ports.Out != nil {
				out = []string{}
			}
			if in == nil && spec.Ports.In != nil {
				in = []string{}
			}
			spec.Ports.Out, spec.Ports.In = out, in
			reg.NodeTypes[id] = spec
		}
		reg.PortsGenerated = true
		log.Printf("operad: ports: generated from %d declared pairs for %d types (t342 ruling 4, kernel#72)", pairs, len(reg.NodeTypes))
		return
	}
	reg.PortsDrift = drift
	missing, unpaired := 0, 0
	for _, d := range drift {
		missing += len(d.MissingOut) + len(d.MissingIn)
		unpaired += len(d.UnpairedOut) + len(d.UnpairedIn)
	}
	log.Printf("operad: WARNING — ports: %d of %d types' authored out/in differ from the pair-generated ports (%d missing, %d unpaired); serving authored blocks (kernel#72; t342 ruling 4 generates them once the blocks match the pairs or are omitted; ports never gate a LINK)",
		len(drift), len(reg.NodeTypes), missing, unpaired)
}

// --- raw JSON shapes for ontology.json ---

type ontologyJSON struct {
	Version string `json:"version"`
	Types   struct {
		S2Infrastructure []rawNodeType `json:"s2_infrastructure"`
		S1Grammar        []rawNodeType `json:"s1_grammar"`
		InteractionNodes []rawNodeType `json:"interaction_nodes"`
	} `json:"types"`
	RewriteCategories      []rawRewriteCategory `json:"rewrite_categories"`
	PortColorCompatibility rawPortColorCompat   `json:"port_color_compatibility"`
}

type rawNodeType struct {
	ID         string                     `json:"id"`
	Stratum    string                     `json:"stratum"`
	URNPattern string                     `json:"urn_pattern"`
	Ports      *rawPorts                  `json:"ports"`
	Properties map[string]rawPropertySpec `json:"properties"`
}

// rawPorts is a type's authored ports block. out/in are replaced by the
// ports generated from the declared pairs when the authored lists agree with
// them or are omitted (t342 ruling 4, resolveTypePorts); a nil list is an
// absent key.
type rawPorts struct {
	Out  []string `json:"out"`
	In   []string `json:"in"`
	Self []string `json:"self"`
}

type rawPropertySpec struct {
	Mutability     string `json:"mutability"`
	AuthorityScope string `json:"authority_scope"`
	Type           string `json:"type"`
	Values         []any  `json:"values"`
	Note           string `json:"note"`
}

type rawRewriteCategory struct {
	ID                  string                  `json:"id"`
	Name                string                  `json:"name"`
	AllowedRewrites     []string                `json:"allowed_rewrites"`
	SrcTypes            []string                `json:"src_types"`
	TgtTypes            []string                `json:"tgt_types"`
	SrcPort             string                  `json:"src_port"`
	TgtPort             string                  `json:"tgt_port"`
	AdditionalPortPairs []rawAdditionalPortPair `json:"additional_port_pairs"`
	Authority           string                  `json:"authority"`
	MutateScope         any                     `json:"mutate_scope"` // []string or null or string
	SyncMode            string                  `json:"sync_mode"`
}

type rawAdditionalPortPair struct {
	SrcPort          string   `json:"src_port"`
	TgtPort          string   `json:"tgt_port"`
	SrcTypes         []string `json:"src_types"`
	TgtTypes         []string `json:"tgt_types"`
	AddedInVersion   string   `json:"added_in_version"`
	PromotesFragment string   `json:"promotes_fragment"`
	Description      string   `json:"description"`
}

type rawPortColorCompat struct {
	// PortColors is the ontology's color vocabulary: the closed set every
	// matrix key, matrix column and port_color_map value is checked against
	// (t342). Absent = the kernel's eight §12.1 colors.
	PortColors []string `json:"port_colors"`
	// Matrix is the §12.2 color×color matrix: loaded and closure-checked,
	// not consulted by the color gate since t342 ruling 1.
	Matrix map[string]map[string]any `json:"matrix"`
	// PortColorMap is the port-name → color-name object (moos-kernel#50),
	// the single source of port colors once it colors every declared port
	// (t342 ruling 6, resolvePortColorSource). Absent in ontology v4.0.1.
	// Values are pointers so JSON null is distinguishable from "" — both are
	// load errors (the "" exemption is gone, t342 ruling 2). Sibling keys
	// (description, projection_rule, semantic_rule, declared_pairs_by_wf)
	// remain doc-only and unloaded.
	PortColorMap map[string]*string `json:"port_color_map"`
}

func validateTypeList(wfID string, pairLabel string, key string, types []string) error {
	if len(types) == 0 {
		return fmt.Errorf("operad: %s %s: %s is empty or missing — list the admitted types, or [\"*\"] for all types", wfID, pairLabel, key)
	}
	hasStar := false
	for _, t := range types {
		if t == "*" {
			hasStar = true
			break
		}
	}
	if hasStar && len(types) > 1 {
		return fmt.Errorf("operad: %s %s: %s cannot mix '*' wildcard with other types", wfID, pairLabel, key)
	}
	return nil
}
