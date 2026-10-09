package operad

import (
	"fmt"
	"strings"

	"moos/kernel/internal/graph"
)

// ValidateADD checks structural constraints on an ADD envelope.
// Returns nil if the ADD is valid given the registry.
func (r *Registry) ValidateADD(env graph.Envelope) error {
	spec, ok := r.NodeTypes[env.TypeID]
	if !ok {
		return fmt.Errorf("operad: unknown type_id %q", env.TypeID)
	}

	// Check all required (non-optional) properties are present
	for name, pspec := range spec.Properties {
		if _, provided := env.Properties[name]; !provided {
			// Only enforce for immutable fields — they must be set at ADD time
			if pspec.Mutability == "immutable" {
				return fmt.Errorf("operad: required immutable property %q missing on ADD of %s", name, env.TypeID)
			}
		}
	}
	return nil
}

// ValidateLINK checks port, type, and color constraints on a LINK envelope.
//
// Port-pair validation (T=171 round 11 PR 1): the (src_port, tgt_port) pair
// must match either the WF's primary (SrcPort, TgtPort) or one of its declared
// AdditionalPortPairs. Pairs declared only on the AdditionalPortPairs list of
// ontology.json (e.g. WF19 has-occupant/is-occupant-of, pins-urn/pinned-by-session)
// are now validated — previously they slipped through when the loader didn't
// consume the field. Prospective only: replay of pre-PR-1 logs via fold runs
// without re-validation, so historical non-canonical relations (e.g. a LINK
// recorded at v3.11 with a typo'd tgt_port) persist in state. Correction of
// such relations is via new compensating UNLINK+re-LINK rewrites, not via
// retroactive validator action.
func (r *Registry) ValidateLINK(env graph.Envelope) error {
	wfSpec, ok := r.RewriteCategories[env.RewriteCategory]
	if !ok {
		return fmt.Errorf("operad: unknown rewrite_category %q", env.RewriteCategory)
	}

	// Check allowed_rewrites includes LINK
	if !containsRewrite(wfSpec.AllowedRewrites, graph.LINK) {
		return fmt.Errorf("operad: rewrite_category %s does not allow LINK", env.RewriteCategory)
	}

	// WF15 contract requirement is checked in fold — but double-check here as well
	if env.RewriteCategory == graph.WF15 && env.ContractURN == "" {
		return fmt.Errorf("operad: WF15 LINK requires contract_urn")
	}

	// Port pair must match one of the WF's declared pairs (primary or additional).
	// Only enforced when the WF declares at least one pair — whether a primary
	// pair, additional pairs, or both. Spec-absent WFs (no declared pairs at
	// all) keep the legacy permissive behavior so partial registries and test
	// fixtures that omit port specs still validate.
	pairsDeclared := wfSpec.SrcPort != "" || wfSpec.TgtPort != "" || len(wfSpec.AdditionalPortPairs) > 0
	if pairsDeclared {
		if !linkPairDeclared(wfSpec, env.SrcPort, env.TgtPort) {
			return fmt.Errorf("operad: port pair (%s, %s) not declared for %s; expected %s",
				env.SrcPort, env.TgtPort, env.RewriteCategory,
				describeDeclaredPairs(wfSpec))
		}
	}

	// Port color gate (§12.2): a LINK is color-valid iff both ports carry the
	// SAME color (t342 ruling 1). Strict equality replaced the 8×8 matrix,
	// which on every declared pair of 4.0.7, 4.0.8 and mtdc-2.0.0 admits
	// exactly the same LINKs (color_equality_t342_test.go) but was not
	// transitive under WF15, had a dead projection cell and left semantic as
	// the bottom of the order. An ontology with no matrix at all (v3.0 at
	// ffs0 37d07ce), on which the matrix rejected every colored LINK, is
	// judged by equality too. A future cross-color relation is declared as
	// its own pair with its own colors, never as a matrix cell; the loaded
	// matrix is display-only. FAIL-CLOSED for WFs with declared pairs
	// (moos-kernel#50): a port without a color in Registry.PortColors is
	// rejected, and no color is exempt any more (t342 ruling 2 retired the
	// bound-to "" exemption). Spec-absent WFs keep the legacy permissive
	// skip, matching the pair gate above.
	srcColor, tgtColor, err := r.resolvePortColors(env.SrcPort, env.TgtPort)
	if err != nil {
		if pairsDeclared {
			return fmt.Errorf("operad: %v — color gate is fail-closed for WFs with declared pairs (§12.2, moos-kernel#50)", err)
		}
		return nil
	}
	if srcColor != tgtColor {
		return fmt.Errorf("operad: port color mismatch: %s (%s) → %s (%s) under %s — the color gate admits equal colors only (§12.2, t342 ruling 1)",
			env.SrcPort, srcColor, env.TgtPort, tgtColor, env.RewriteCategory)
	}
	return nil
}

// linkPairDeclared reports whether (src, tgt) matches the WF's primary pair or
// any of its additional_port_pairs.
func linkPairDeclared(wfSpec RewriteCategorySpec, src, tgt string) bool {
	if wfSpec.SrcPort == src && wfSpec.TgtPort == tgt {
		return true
	}
	for _, p := range wfSpec.AdditionalPortPairs {
		if p.SrcPort == src && p.TgtPort == tgt {
			return true
		}
	}
	return false
}

// describeDeclaredPairs renders the WF's accepted port pairs for error messages.
// Primary pair first, additional pairs in declaration order.
func describeDeclaredPairs(wfSpec RewriteCategorySpec) string {
	pairs := make([]string, 0, 1+len(wfSpec.AdditionalPortPairs))
	if wfSpec.SrcPort != "" || wfSpec.TgtPort != "" {
		pairs = append(pairs, fmt.Sprintf("(%s, %s)", wfSpec.SrcPort, wfSpec.TgtPort))
	}
	for _, p := range wfSpec.AdditionalPortPairs {
		pairs = append(pairs, fmt.Sprintf("(%s, %s)", p.SrcPort, p.TgtPort))
	}
	if len(pairs) == 0 {
		return "(none declared)"
	}
	return strings.Join(pairs, " | ")
}

// matchingAdditionalPair returns the AdditionalPortPair on wfSpec whose
// (SrcPort, TgtPort) matches the given ports, plus a hit flag. The primary
// pair is NOT searched here — callers handle it separately because its
// type restrictions already come from wfSpec.SrcTypes/TgtTypes.
func matchingAdditionalPair(wfSpec RewriteCategorySpec, src, tgt string) (AdditionalPortPair, bool) {
	for _, p := range wfSpec.AdditionalPortPairs {
		if p.SrcPort == src && p.TgtPort == tgt {
			return p, true
		}
	}
	return AdditionalPortPair{}, false
}

// ValidateMUTATE checks mutability, WF scope, and authority constraints on a MUTATE envelope.
//
// Two paths:
//  1. Standard MUTATE: field already on node — full WF validation (rewrite_category required, field must be in mutate_scope).
//  2. Additive MUTATE: field not yet on node — validate against ontology type spec only.
//     WF mutate_scope is skipped because the field is being added for the first time (e.g. a new
//     optional property from a later ontology version). rewrite_category may be empty.
func (r *Registry) ValidateMUTATE(env graph.Envelope, node graph.Node) error {
	_, fieldOnNode := node.Properties[env.Field]

	if !fieldOnNode {
		// Additive MUTATE: field not on node — validate via ontology type spec only.
		typeSpec, hasTypeSpec := r.NodeTypes[node.TypeID]
		if hasTypeSpec {
			pspec, hasPspec := typeSpec.Properties[env.Field]
			if !hasPspec {
				return fmt.Errorf("operad: field %q not declared in type spec for %s (additive MUTATE requires ontology-declared field)", env.Field, node.TypeID)
			}
			if pspec.Mutability != "mutable" {
				return fmt.Errorf("operad: field %q is not mutable in type spec for %s", env.Field, node.TypeID)
			}
			if err := checkAuthority(pspec.AuthorityScope, env.Actor, node); err != nil {
				return err
			}
		}
		// If a rewrite_category is provided, it must be a known one — but it's optional here.
		if env.RewriteCategory != "" {
			if _, ok := r.RewriteCategories[env.RewriteCategory]; !ok {
				return fmt.Errorf("operad: unknown rewrite_category %q", env.RewriteCategory)
			}
		}
		return nil
	}

	// Standard MUTATE: field already on node — full WF validation.
	wfSpec, ok := r.RewriteCategories[env.RewriteCategory]
	if !ok {
		return fmt.Errorf("operad: unknown rewrite_category %q", env.RewriteCategory)
	}

	if !containsRewrite(wfSpec.AllowedRewrites, graph.MUTATE) {
		return fmt.Errorf("operad: rewrite_category %s does not allow MUTATE", env.RewriteCategory)
	}

	// Field must be in the WF's exhaustive mutate_scope
	if len(wfSpec.MutateScope) > 0 && !containsString(wfSpec.MutateScope, env.Field) {
		return fmt.Errorf("operad: field %q not in mutate_scope for %s (§5)", env.Field, env.RewriteCategory)
	}

	// Check the property spec for authority_scope
	typeSpec, hasTypeSpec := r.NodeTypes[node.TypeID]
	if hasTypeSpec {
		if pspec, hasProp := typeSpec.Properties[env.Field]; hasProp {
			if pspec.Mutability == "immutable" {
				return fmt.Errorf("operad: field %q is immutable on type %s", env.Field, node.TypeID)
			}
			// Authority check: actor URN prefix must match authority scope
			if err := checkAuthority(pspec.AuthorityScope, env.Actor, node); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateUNLINK checks the UNLINK envelope against the registry.
func (r *Registry) ValidateUNLINK(env graph.Envelope) error {
	if env.RewriteCategory == "" {
		return nil // UNLINK without category is allowed (category resolved from existing relation)
	}
	wfSpec, ok := r.RewriteCategories[env.RewriteCategory]
	if !ok {
		return fmt.Errorf("operad: unknown rewrite_category %q", env.RewriteCategory)
	}
	if !containsRewrite(wfSpec.AllowedRewrites, graph.UNLINK) {
		return fmt.Errorf("operad: rewrite_category %s does not allow UNLINK", env.RewriteCategory)
	}
	return nil
}

// resolvePortColors looks up both port colors from Registry.PortColors —
// the loader-built map (the ontology's port_color_map, laid over the frozen
// legacyPortColors table only when that map is partial; t342 ruling 6). A
// nil map (zero-value Registry, partial test fixtures) falls back to
// legacyPortColors so bare registries resolve the canonical vocabulary.
//
// Two outcomes per port:
//   - present with a color: returned for the §12.2 equality check
//   - absent, or present with the EMPTY color: error naming the port —
//     ValidateLINK rejects (fail-closed) on WFs with declared pairs
//     (moos-kernel#50). An empty color is no longer an exemption (t342
//     ruling 2); the loader refuses "" so only a hand-built registry can
//     carry one.
func (r *Registry) resolvePortColors(srcPort, tgtPort string) (graph.PortColor, graph.PortColor, error) {
	colors := r.PortColors
	if colors == nil {
		colors = legacyPortColors()
	}
	srcColor, srcOk := colors[srcPort]
	tgtColor, tgtOk := colors[tgtPort]
	if !srcOk || !tgtOk || srcColor == "" || tgtColor == "" {
		missing := make([]string, 0, 2)
		if !srcOk || srcColor == "" {
			missing = append(missing, describeUncolored("src", srcPort, srcOk))
		}
		if !tgtOk || tgtColor == "" {
			missing = append(missing, describeUncolored("tgt", tgtPort, tgtOk))
		}
		return "", "", fmt.Errorf("no declared color for port %s (§12.1)", strings.Join(missing, ", "))
	}
	return srcColor, tgtColor, nil
}

// describeUncolored names one uncolored port side for resolvePortColors.
// A present-but-empty entry says so: it used to be the exemption.
func describeUncolored(side, port string, present bool) string {
	if present {
		return fmt.Sprintf("%s %q (empty color — no longer an exemption, t342 ruling 2)", side, port)
	}
	return fmt.Sprintf("%s %q", side, port)
}

// checkAuthority checks whether the actor satisfies the required authority scope.
// This is a heuristic check — full capability-graph checking is done at runtime.
func checkAuthority(scope string, actor graph.URN, node graph.Node) error {
	switch scope {
	case "kernel":
		// Kernel authority: actor must be a kernel URN
		// (urn:moos:kernel:...) — relaxed here, full check at runtime
		return nil
	case "owner":
		// Owner authority: actor must match owner_urn property of the node
		if ownerProp, ok := node.Properties["owner_urn"]; ok {
			if ownerURN, ok := ownerProp.Value.(string); ok {
				if graph.URN(ownerURN) != actor {
					return fmt.Errorf("operad: field requires owner authority; actor %s != owner %s", actor, ownerURN)
				}
			}
		}
	case "principal", "substrate", "delegate":
		// These require capability graph traversal — deferred to runtime capability check
		return nil
	}
	return nil
}

func containsRewrite(list []graph.RewriteType, rt graph.RewriteType) bool {
	for _, r := range list {
		if r == rt {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ValidateStrataLink enforces M5 strata filtration for LINK rewrites.
// Called with the kernel read-lock held (state is consistent).
//
// Rules:
//  1. S4 (Projected) nodes may not be the src of a LINK to S0/S1/S2 nodes.
//     Projection nodes are read-only toward lower strata (M5). This rule
//     applies only when both ends of the link have a parseable stratum.
//  2. If the WF category declares non-empty src_types or tgt_types, the actual
//     node types must be in those lists. (Previously stubbed — now enforced.)
//  3. If the LINK matches a declared AdditionalPortPair (not the primary pair),
//     the pair's own SrcTypes/TgtTypes replace the WF-level lists. ["*"]
//     admits every type. Empty lists are rejected at load, so only a hand-built
//     registry can carry an empty list.
func (r *Registry) ValidateStrataLink(env graph.Envelope, state graph.GraphState) error {
	srcNode, srcOk := state.Nodes[env.SrcURN]
	tgtNode, tgtOk := state.Nodes[env.TgtURN]
	if !srcOk || !tgtOk {
		return nil // nodes not found — fold.applyLINK will return ErrNodeNotFound
	}

	srcSpec, hasSrc := r.NodeTypes[srcNode.TypeID]
	tgtSpec, hasTgt := r.NodeTypes[tgtNode.TypeID]
	if !hasSrc || !hasTgt {
		return nil // unknown type — already rejected by ValidateADD at creation time
	}

	srcStratum, srcErr := graph.ParseStratum(srcSpec.Stratum)
	tgtStratum, tgtErr := graph.ParseStratum(tgtSpec.Stratum)

	// Rule 1: S4 -> S0/S1/S2 LINK is forbidden (M5 filtration direction).
	if srcErr == nil && tgtErr == nil {
		if srcStratum == graph.S4 && tgtStratum < graph.S3 {
			return fmt.Errorf("strata(M5): S4 node %s (%s) may not LINK to %s node %s (%s); projection nodes are read-only toward lower strata",
				env.SrcURN, srcNode.TypeID, tgtSpec.Stratum, env.TgtURN, tgtNode.TypeID)
		}
	}

	wfSpec, ok := r.RewriteCategories[env.RewriteCategory]
	if !ok {
		return nil // unknown WF — already caught by ValidateLINK
	}

	// Determine whether the envelope matches an additional port pair. If yes,
	// the pair's own SrcTypes/TgtTypes (rule 3) govern type allowance entirely;
	// the WF-level lists describe the primary pair only and may legitimately be
	// narrower than what an additional pair admits (e.g. WF19.pins-urn declares
	// tgt_types=["*"] while WF19's primary pair restricts to kernel/session/etc).
	// If no additional pair matches, rule 2 (WF-level) applies as the constraint
	// on the primary pair.
	pair, isAdditional := matchingAdditionalPair(wfSpec, env.SrcPort, env.TgtPort)

	if !isAdditional {
		// Rule 2: WF-level src_types / tgt_types enforcement (primary-pair LINKs).
		if len(wfSpec.SrcTypes) > 0 && !typeAllowed(wfSpec.SrcTypes, srcNode.TypeID) {
			return fmt.Errorf("operad: src type %q not in allowed list for %s: %v",
				srcNode.TypeID, env.RewriteCategory, wfSpec.SrcTypes)
		}
		if len(wfSpec.TgtTypes) > 0 && !typeAllowed(wfSpec.TgtTypes, tgtNode.TypeID) {
			return fmt.Errorf("operad: tgt type %q not in allowed list for %s: %v",
				tgtNode.TypeID, env.RewriteCategory, wfSpec.TgtTypes)
		}
		return nil
	}

	// Rule 3: pair-level src_types/tgt_types enforcement (additional-pair LINKs).
	// A src_types or tgt_types entry of "*" is a wildcard per the ontology convention (see
	// WF19.pins-urn in ontology.json v3.12): any type passes.
	if len(pair.SrcTypes) > 0 && !typeAllowed(pair.SrcTypes, srcNode.TypeID) {
		return fmt.Errorf("operad: src type %q not allowed on pair (%s, %s) of %s: %v",
			srcNode.TypeID, env.SrcPort, env.TgtPort, env.RewriteCategory, pair.SrcTypes)
	}
	if len(pair.TgtTypes) > 0 && !typeAllowed(pair.TgtTypes, tgtNode.TypeID) {
		return fmt.Errorf("operad: tgt type %q not allowed on pair (%s, %s) of %s: %v",
			tgtNode.TypeID, env.SrcPort, env.TgtPort, env.RewriteCategory, pair.TgtTypes)
	}
	return nil
}

func typeAllowed(types []graph.TypeID, ty graph.TypeID) bool {
	for _, v := range types {
		if v == "*" || v == ty {
			return true
		}
	}
	return false
}

// ValidateCausalAcyclic enforces WF21 (causes/caused-by) acyclicity for LINK
// rewrites. Called with the kernel read-lock held (state is consistent), in
// the same apply path as ValidateStrataLink.
//
// When a new LINK src --causes--> tgt is proposed, walk forward through
// existing causes-edges starting from tgt. If we reach src, the new LINK
// would close a cycle in the causes/caused-by DAG and is rejected.
//
// Rules:
//  1. Non-WF21 LINKs pass through (no acyclicity constraint applies).
//  2. Non-LINK rewrites (UNLINK, ADD, MUTATE) pass through.
//  3. Self-LINK (src == tgt) is a 1-cycle and is rejected immediately.
//  4. BFS forward from tgt through outgoing edges where RewriteCategory=WF21
//     and SrcPort="causes" (i.e., true causes-direction edges, not the
//     reverse caused-by side of the same relation). If env.SrcURN is
//     reached, reject.
//
// Round-15 ceremony (T=176): introduced as v314-3-wf21-causes promotion
// companion. Pairs with v314-2-clock-type and v314-4-substrate-property.
// Doctrine anchor: kb/research/spec/07-time-fabric.md §7.3.5 +
// kb/research/spec/05-external-substrates.md §5.1.5/§5.3.5.
func (r *Registry) ValidateCausalAcyclic(env graph.Envelope, state graph.GraphState) error {
	if env.RewriteType != graph.LINK {
		return nil
	}
	if env.RewriteCategory != graph.RewriteCategory("WF21") {
		return nil
	}
	if env.SrcURN == env.TgtURN {
		return fmt.Errorf("WF21 acyclic: self-LINK %s --causes--> %s would create a 1-cycle",
			env.SrcURN, env.TgtURN)
	}

	// BFS forward from tgt along outgoing WF21 causes-edges. If we reach src,
	// the new edge src --causes--> tgt would close a cycle.
	visited := map[graph.URN]bool{env.TgtURN: true}
	queue := []graph.URN{env.TgtURN}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for relURN := range state.RelationsBySrc[cur] {
			rel, ok := state.Relations[relURN]
			if !ok {
				continue
			}
			if rel.RewriteCategory != graph.RewriteCategory("WF21") {
				continue
			}
			// Only follow edges in the canonical causes-direction. The
			// reverse caused-by side of the same relation is implicit
			// in WF21's port-pair (causes / caused-by) — walking that
			// direction would over-detect.
			if rel.SrcPort != "causes" {
				continue
			}
			if rel.TgtURN == env.SrcURN {
				return fmt.Errorf("WF21 acyclic: adding %s --causes--> %s would close a cycle (path exists: %s --causes...--> %s)",
					env.SrcURN, env.TgtURN, env.TgtURN, env.SrcURN)
			}
			if !visited[rel.TgtURN] {
				visited[rel.TgtURN] = true
				queue = append(queue, rel.TgtURN)
			}
		}
	}
	return nil
}
