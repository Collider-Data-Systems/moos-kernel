package operad

import "moos/kernel/internal/graph"

// DefaultPortColors is the built-in port-name → color map (§12.1), the
// kernel-side successor of the former hardcoded portColorFromName switch
// (moos-kernel#50). It covers every port declared in ontology.json v4.0.1's
// rewrite_categories (primary pairs + additional_port_pairs), so the §12.2
// color gate can run fail-closed for WFs with declared pairs.
//
// Color choices are grounded in the ontology's authored intent
// (port_color_compatibility.declared_pairs_by_wf src_color/tgt_color rows —
// authored-not-loaded per the 4.0.1 note) where a row exists; ports without
// an authored row take their WF family's color (e.g. WF19 session-governance
// pairs → workflow, WF02 delegation → auth, WF20 promotion → auth).
//
// An entry mapped to the EMPTY color is an explicit, documented exemption:
// the port's color is ambiguous by doctrine and the matrix check is skipped
// for pairs involving it. This is distinct from an ABSENT entry, which the
// gate treats as unknown and rejects (fail-closed) on declared-pair WFs.
//
// ontology.json may override or extend this map via an optional
// port_color_compatibility.port_color_map object (port name → color name;
// empty string = exempt). See loader.go. The merged result lives on
// Registry.PortColors.
func DefaultPortColors() map[string]graph.PortColor {
	return map[string]graph.PortColor{
		// auth — governance, delegation, promotion (WF02, WF13, WF20)
		"governs":          graph.ColorAuth,
		"governed-by":      graph.ColorAuth,
		"granted-by":       graph.ColorAuth,
		"identity":         graph.ColorAuth,
		"promotes-to":      graph.ColorAuth,
		"promotion-target": graph.ColorAuth,
		"delegates-to":     graph.ColorAuth, // WF02 v313-6 role→role capability narrowing
		"delegated-by":     graph.ColorAuth,
		"promotes":         graph.ColorAuth, // WF20 grammar promotion (family: WF13 auth)
		"promoted-from":    graph.ColorAuth,

		// topology — ownership, containment, hosting, spanning (WF01, WF03, WF04, WF18.spans)
		"contains":     graph.ColorTopology,
		"contained-in": graph.ColorTopology,
		"owns":         graph.ColorTopology,
		"owned-by":     graph.ColorTopology, // WF01 v313-9 pair partner of owns
		"child":        graph.ColorTopology,
		"hosts":        graph.ColorTopology,
		"hosted-on":    graph.ColorTopology,
		"binds":        graph.ColorTopology,
		"spans":        graph.ColorTopology, // WF18 g2 (4.0.1) manifold colimit legs
		"spanned-by":   graph.ColorTopology,

		// transport (WF05, WF06, WF14, WF16)
		"exposes":        graph.ColorTransport,
		"exposed-by":     graph.ColorTransport,
		"connects-to":    graph.ColorTransport, // authored rows carry topology|transport; transport kept (pre-#50 behavior)
		"connected-to":   graph.ColorTransport,
		"implements":     graph.ColorTransport,
		"implemented-by": graph.ColorTransport,
		"routes-to":      graph.ColorTransport,
		"routed-from":    graph.ColorTransport,
		"shard-of":       graph.ColorTransport,
		"sharded-by":     graph.ColorTransport,

		// compute (WF09)
		"computes-on": graph.ColorCompute,
		"computed-by": graph.ColorCompute,

		// storage (WF10, WF11, WF12)
		"persisted-in": graph.ColorStorage,
		"persists":     graph.ColorStorage,
		"synced-via":   graph.ColorStorage,
		"sync-target":  graph.ColorStorage,
		"provides-kb":  graph.ColorStorage,
		"kb-source":    graph.ColorStorage,
		"produces":     graph.ColorStorage,
		"produced-by":  graph.ColorStorage,
		"asserts":      graph.ColorStorage,
		"asserted-in":  graph.ColorStorage,
		"tagged":       graph.ColorStorage,
		"tagged-in":    graph.ColorStorage,

		// EXPLICIT EXEMPTION — ambiguous compute-or-storage by doctrine
		// (WF08 bound-to→binds is a declared color crossing: both
		// compute→topology and storage→topology are true in the matrix).
		// Empty color = matrix check skipped, deliberately and visibly.
		"bound-to": "",

		// workflow — session/occupancy/purpose/causality/scheduling
		// (WF07, WF17, WF18, WF19, WF21)
		"participates":            graph.ColorWorkflow,
		"participated-by":         graph.ColorWorkflow,
		"focus":                   graph.ColorWorkflow,
		"on":                      graph.ColorWorkflow,
		"anchors":                 graph.ColorWorkflow,
		"anchor":                  graph.ColorWorkflow,
		"causes":                  graph.ColorWorkflow,
		"caused-by":               graph.ColorWorkflow, // WF21 pair partner of causes
		"summarizes":              graph.ColorWorkflow,
		"daily-summary":           graph.ColorWorkflow,
		"depends-on":              graph.ColorWorkflow,
		"depended-by":             graph.ColorWorkflow,
		"participant":             graph.ColorWorkflow,
		"triggers":                graph.ColorWorkflow,
		"triggered-by":            graph.ColorWorkflow,
		"guards":                  graph.ColorWorkflow,
		"guarded-by":              graph.ColorWorkflow,
		"emits":                   graph.ColorWorkflow,
		"emitted-by":              graph.ColorWorkflow,
		"watches":                 graph.ColorWorkflow,
		"watched-by":              graph.ColorWorkflow,
		"blocks":                  graph.ColorWorkflow,
		"blocked-by":              graph.ColorWorkflow,
		"scheduled-after":         graph.ColorWorkflow,
		"scheduled-before":        graph.ColorWorkflow,
		"steers":                  graph.ColorWorkflow,
		"steered-by":              graph.ColorWorkflow,
		"composes":                graph.ColorWorkflow, // WF18 primary (authored: workflow)
		"composed-by":             graph.ColorWorkflow,
		"opens-on":                graph.ColorWorkflow, // WF19 PRIMARY pair (authored: workflow) — previously uncovered
		"occupied-by":             graph.ColorWorkflow,
		"has-occupant":            graph.ColorWorkflow, // v3.10 WF19 session-occupancy (§M19)
		"is-occupant-of":          graph.ColorWorkflow,
		"pins-urn":                graph.ColorWorkflow, // v3.12 WF19 session pins (§M18, D19.3)
		"pinned-by-session":       graph.ColorWorkflow,
		"filtered-by":             graph.ColorWorkflow, // v3.12 WF19 session filter binding (§M18, D19.4)
		"filters-session":         graph.ColorWorkflow,
		"mounts-tool":             graph.ColorWorkflow, // v3.12 WF19 session tool mount (§M20, D20.1)
		"tool-mounted-in-session": graph.ColorWorkflow,
		"has-purpose":             graph.ColorWorkflow, // WF19 D22.1 (v3.16) session purpose
		"purpose-of-session":      graph.ColorWorkflow,
		"presents-as":             graph.ColorWorkflow, // WF19 d4 (4.0.1) — presentation, NOT authority (deliberately not auth)
		"presented-by":            graph.ColorWorkflow,

		// semantic — WF15 contract-mediated composition; the ontology declares
		// the literal "{semantic}" placeholder as WF15's port pair.
		"{semantic}": graph.ColorSemantic,

		// projection — S4 sinks
		"projected-to": graph.ColorProjection,
		"rendered-as":  graph.ColorProjection,
		"displayed-as": graph.ColorProjection,
	}
}
