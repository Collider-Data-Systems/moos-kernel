package graph

import "time"

// RewriteCategory classifies which ADD/LINK/MUTATE/UNLINK operations are valid,
// on which node types, through which ports, under what authority.
// A relation in the graph labeled WF01 exists because a past LINK rewrite
// in category WF01 created it. The category is NOT a named static relationship.
type RewriteCategory string

const (
	WF01 RewriteCategory = "WF01" // Ownership
	WF02 RewriteCategory = "WF02" // Governance
	WF03 RewriteCategory = "WF03" // Hosting
	WF04 RewriteCategory = "WF04" // Containment
	WF05 RewriteCategory = "WF05" // Exposure
	WF06 RewriteCategory = "WF06" // Connection
	WF07 RewriteCategory = "WF07" // Session
	WF08 RewriteCategory = "WF08" // Substrate binding
	WF09 RewriteCategory = "WF09" // Compute attachment
	WF10 RewriteCategory = "WF10" // Persistence
	WF11 RewriteCategory = "WF11" // Sync
	WF12 RewriteCategory = "WF12" // KB provision
	WF13 RewriteCategory = "WF13" // Promotion
	WF14 RewriteCategory = "WF14" // Implementation
	WF15 RewriteCategory = "WF15" // Semantic (open, contract_urn required)
	WF16 RewriteCategory = "WF16" // Projection (S4 output relations)
	WF17 RewriteCategory = "WF17" // Reactive (Watch/React/Guard)
	WF18 RewriteCategory = "WF18" // Annotation (tagging, claims)
	WF19 RewriteCategory = "WF19" // Session-local T counter (M1 chrono)
)

// PortColor classifies a port's semantic domain.
// §12.2 color gate: a LINK is color-valid iff both ports share one color
// (t342 ruling 1 — strict equality replaced the 8×8 compatibility matrix).
type PortColor string

const (
	ColorAuth       PortColor = "auth"       // Principal, delegate, role flow
	ColorTopology   PortColor = "topology"   // Ownership and hosting shape
	ColorTransport  PortColor = "transport"  // Network and protocol routing
	ColorCompute    PortColor = "compute"    // Execution substrate mapping
	ColorStorage    PortColor = "storage"    // Persistence and sync
	ColorWorkflow   PortColor = "workflow"   // Session and task control
	ColorSemantic   PortColor = "semantic"   // Open domain relations (WF15)
	ColorProjection PortColor = "projection" // Read model / output only — never source of truth
)

// Relation is a typed connection between two nodes.
// It exists because a past LINK rewrite created it.
// It persists until a future UNLINK rewrite removes it.
// It does not carry messages, invoke methods, or trigger behavior.
// It is topology — structure that the kernel reads when validating future rewrites.
//
// A relation carries no port colors (t342 ruling 6). The former SrcColor /
// TgtColor fields were never written (fold.applyLINK left them ""), so every
// relation served src_color="" / tgt_color="", which read like the retired ""
// exemption. An end's color is derived on demand from the operad's
// Registry.PortColors by port name — the ontology is the single source.
type Relation struct {
	URN             URN             `json:"urn"`
	RewriteCategory RewriteCategory `json:"rewrite_category"`
	SrcURN          URN             `json:"src_urn"`
	SrcPort         string          `json:"src_port"`
	TgtURN          URN             `json:"tgt_urn"`
	TgtPort         string          `json:"tgt_port"`
	// ContractURN is required for WF15 (semantic) relations.
	ContractURN URN       `json:"contract_urn,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
