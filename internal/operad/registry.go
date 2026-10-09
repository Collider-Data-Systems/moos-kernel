package operad

import "moos/kernel/internal/graph"

// Registry is the operad — the grammar and type system of the kernel.
// It is loaded from ontology.json and is read-only at runtime.
// It governs what rewrites are valid: which node types exist,
// which rewrite categories are allowed, what port colors are compatible.
type Registry struct {
	// Version is the ontology version string (e.g. "3.12.0") parsed from the
	// top-level "version" field of ontology.json. Empty when the registry is
	// built via EmptyRegistry (no ontology loaded). Exposed read-only via
	// /healthz so that state-readback tooling can detect runtime vs. on-disk
	// ontology drift without grepping feature flags.
	Version           string
	NodeTypes         map[graph.TypeID]NodeTypeSpec
	RewriteCategories map[graph.RewriteCategory]RewriteCategorySpec
	// PortColorMatrix is the ontology's §12.2 color×color matrix. It is
	// loaded, closure-checked and served on /operad/port-colors, but the
	// color gate does NOT consult it (t342 ruling 1: ValidateLINK admits
	// equal colors only). The loader compares it against equality on every
	// declared pair at boot (reportColorGate) so a disagreement is visible.
	PortColorMatrix PortColorMatrix
	// PortColors is the §12.1 port-name → color map consulted by the §12.2
	// color gate. Loader-populated from port_color_compatibility.port_color_map
	// (t342 ruling 6): when that map colors every end of every declared pair
	// it is the single source (PortColorSource = PortColorSourceOntology);
	// otherwise it is laid over the frozen legacyPortColors() table
	// (PortColorSourceLegacyMerge — the fleet's 4.0.7). Every entry carries
	// a color; the "" exemption is gone (t342 ruling 2). An absent entry is
	// unknown and fail-closed on WFs with declared pairs. nil falls back to
	// legacyPortColors() in resolvePortColors.
	PortColors map[string]graph.PortColor
	// PortColorSource names where PortColors came from (t342 ruling 6):
	// PortColorSourceOntology or PortColorSourceLegacyMerge. Served on
	// /operad/port-colors as color_source.
	PortColorSource string
	// PortsGenerated reports that every NodeTypeSpec.Ports.Out/In was
	// generated from the declared port pairs (t342 ruling 4, moos-kernel#72).
	// False when an authored ports block disagrees with the pairs: the
	// authored blocks are then served unchanged and PortsDrift records how
	// they differ. The ports blocks never gate a LINK either way.
	PortsGenerated bool
	PortsDrift     map[graph.TypeID]PortsDrift
}

// ColorRule is the §12.2 color gate's rule, served on /operad/port-colors
// as color_rule (t342 ruling 1).
const ColorRule = "equality"

// Registry.PortColorSource values (t342 ruling 6).
const (
	// PortColorSourceOntology: the ontology's own port_color_map colors
	// every end of every declared pair and is the only color source.
	PortColorSourceOntology = "ontology"
	// PortColorSourceLegacyMerge: the ontology's own map is partial and is
	// laid over the kernel's frozen legacyPortColors() table.
	PortColorSourceLegacyMerge = "ontology+kernel-legacy"
)

// PortsDrift records how a type's authored ports.out / ports.in differ from
// the ports generated from the declared pairs (t342 ruling 4, kernel#72).
// Missing* = generated but not authored; Unpaired* = authored but on no
// declared pair. Only keys the ontology authors are compared.
type PortsDrift struct {
	MissingOut  []string
	MissingIn   []string
	UnpairedOut []string
	UnpairedIn  []string
}

// NodeTypeSpec describes the valid structure of one node type.
type NodeTypeSpec struct {
	ID         graph.TypeID
	Stratum    string // "S1", "S2", etc.
	URNPattern string
	Ports      PortSpec
	Properties map[string]PropertySpec
}

// PortSpec lists the port names for a node type.
type PortSpec struct {
	Out  []string
	In   []string
	Self []string
}

// PropertySpec declares the valid structure of one property on a node type.
type PropertySpec struct {
	Mutability     string // "immutable" | "mutable"
	AuthorityScope string // "kernel" | "owner" | "principal" | "substrate" | "delegate"
	Type           string // "string" | "enum" | "integer" | "datetime" | "urn" | "array" | "object"
	Values         []any  // valid enum values, if type == "enum"
	Note           string
}

// RewriteCategorySpec declares the rules for one WF category.
type RewriteCategorySpec struct {
	ID                  graph.RewriteCategory
	Name                string
	AllowedRewrites     []graph.RewriteType
	SrcTypes            []graph.TypeID
	TgtTypes            []graph.TypeID
	SrcPort             string
	TgtPort             string
	AdditionalPortPairs []AdditionalPortPair // v3.10+ extension mechanism (§M19 has-occupant, §M18 pins-urn, etc.)
	Authority           string
	MutateScope         []string // exhaustive list of fields that may be changed under this WF
	SyncMode            string   // "strict" | "eventual" | "local-only"
}

// AdditionalPortPair declares a secondary (src_port, tgt_port) pairing that a WF
// category also accepts in addition to its primary (SrcPort, TgtPort). Introduced
// by the v3.10 D19.1 grammar_fragment to carry WF19 has-occupant/is-occupant-of
// topology without bumping to a new WF number, then extended by v3.12 (D19.3
// pins-urn, D19.4 filtered-by, D20.1 mounts-tool) for §M18-§M20 workspace shape.
//
// Loader consumes these into the registry so ValidateLINK can accept any
// declared pair for the WF. For LINKs on this pair, SrcTypes/TgtTypes replace the WF-level lists (no fallback). ["*"] admits every type. LoadRegistry rejects empty, null, absent or mixed lists, so only a hand-built registry can carry an empty list, which admits every type.
type AdditionalPortPair struct {
	SrcPort          string
	TgtPort          string
	SrcTypes         []graph.TypeID
	TgtTypes         []graph.TypeID
	AddedInVersion   string // e.g. "3.10.0"
	PromotesFragment string // URN of the grammar_fragment this pair promotes from, if any
	Description      string
}

// PortColorMatrix is the ontology's §12.2 compatibility matrix as loaded.
// Cells: true, false, "wf15_only" (only allowed with WF15 + contract_urn),
// "sink_only" (projection sink, no truth-carrying relation produced). Since
// t342 ruling 1 the color gate admits equal colors only and ignores this
// matrix; it is kept for display and for the loader's agreement report.
type PortColorMatrix map[graph.PortColor]map[graph.PortColor]colorCompat

type colorCompat string

const (
	compatAllowed  colorCompat = "true"
	compatWF15Only colorCompat = "wf15_only"
	compatSinkOnly colorCompat = "sink_only"
	compatFalse    colorCompat = "false"
)

// Allowed reports the matrix verdict for a LINK from a srcColor port to a
// tgtColor port under the given rewrite category, with the cell semantics
// the color gate used up to 98f2ccc. Not consulted by ValidateLINK (t342
// ruling 1); reportColorGate uses it to show where a loaded matrix and
// equality would disagree on a declared pair.
func (m PortColorMatrix) Allowed(src, tgt graph.PortColor, wf graph.RewriteCategory) bool {
	row, ok := m[src]
	if !ok {
		return false
	}
	switch row[tgt] {
	case compatAllowed:
		return true
	case compatWF15Only:
		return wf == graph.WF15
	default:
		return false
	}
}

// Empty registry for testing or startup before ontology is loaded.
func EmptyRegistry() *Registry {
	return &Registry{
		NodeTypes:         make(map[graph.TypeID]NodeTypeSpec),
		RewriteCategories: make(map[graph.RewriteCategory]RewriteCategorySpec),
		PortColorMatrix:   make(PortColorMatrix),
		PortColors:        legacyPortColors(),
		PortColorSource:   PortColorSourceLegacyMerge,
	}
}
