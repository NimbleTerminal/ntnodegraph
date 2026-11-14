// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Validation error sentinels so callers can match failure classes with errors.Is.
var (
	ErrUnknownNodeType      = errors.New("ntnodegraph: unknown node type")
	ErrUnresolvedSubgraph   = errors.New("ntnodegraph: unresolved graph/ subgraph node")
	ErrUnsupportedBackend   = errors.New("ntnodegraph: unsupported by backend")
	ErrUnknownNode          = errors.New("ntnodegraph: unknown node")
	ErrUnknownPort          = errors.New("ntnodegraph: unknown port")
	ErrMissingRequiredInlet = errors.New("ntnodegraph: missing required inlet connection")
	ErrInvalidParam         = errors.New("ntnodegraph: invalid parameter")
	ErrUndeclaredCapability = errors.New("ntnodegraph: undeclared capability")
	ErrDuplicateEdge        = errors.New("ntnodegraph: duplicate edge")
	ErrCycleDetected        = errors.New("ntnodegraph: cycle detected")
)

// IssueCode identifies the machine-readable category of a validation Issue.
type IssueCode string

const (
	IssueInvalidSpec          IssueCode = "invalid_spec"
	IssueUnknownNodeType      IssueCode = "unknown_node_type"
	IssueUnresolvedSubgraph   IssueCode = "unresolved_subgraph"
	IssueUnsupportedBackend   IssueCode = "unsupported_backend"
	IssueUnknownNode          IssueCode = "unknown_node"
	IssueUnknownPort          IssueCode = "unknown_port"
	IssueTypeMismatch         IssueCode = "type_mismatch"
	IssueMissingRequiredInlet IssueCode = "missing_required_inlet"
	IssueInvalidParam         IssueCode = "invalid_param"
	IssueUndeclaredCapability IssueCode = "undeclared_capability"
	IssueDuplicateEdge        IssueCode = "duplicate_edge"
	IssueCycleDetected        IssueCode = "cycle_detected"
)

// Issue describes one validation failure in a Spec.
type Issue struct {
	Code    IssueCode `json:"code"`
	Path    string    `json:"path"`
	Message string    `json:"message"`
	err     error
}

// Error implements error.
func (i Issue) Error() string {
	if i.Path == "" {
		return fmt.Sprintf("%s: %s", i.Code, i.Message)
	}
	return fmt.Sprintf("%s (%s): %s", i.Path, i.Code, i.Message)
}

// Unwrap allows errors.Is against validation sentinels.
func (i Issue) Unwrap() error {
	return i.err
}

// ValidationError aggregates one or more deterministic validation Issues.
type ValidationError struct {
	Issues []Issue
}

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "ntnodegraph: validation failed"
	}
	if len(e.Issues) == 1 {
		return e.Issues[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ntnodegraph: %d validation errors:\n", len(e.Issues))
	for _, iss := range e.Issues {
		b.WriteString("  - ")
		b.WriteString(iss.Error())
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Unwrap exposes all underlying issue errors for errors.Is / errors.As.
func (e *ValidationError) Unwrap() []error {
	errs := make([]error, len(e.Issues))
	for i := range e.Issues {
		errs[i] = e.Issues[i]
	}
	return errs
}

// EdgeResolution records the static type-check and coercion outcome for one
// validated edge.
type EdgeResolution struct {
	Edge          EdgeSpec      `json:"edge"`
	EffectiveKind string        `json:"effective_kind,omitempty"`
	OutletType    DataType      `json:"outlet_type"`
	InletType     DataType      `json:"inlet_type"`
	Compatibility Compatibility `json:"compatibility"`
	Coercion      CoercionKind  `json:"coercion,omitempty"`
}

// ValidationReport contains the resolved wiring and topological node order for
// a validated Spec.
type ValidationReport struct {
	Edges            []EdgeResolution `json:"edges"`
	TopologicalOrder []string         `json:"topological_order,omitempty"`
}

// ValidateOptions configures static graph validation.
type ValidateOptions struct {
	Backend *BackendProfile
}

// Validate checks spec against registry and opts, returning a deterministic
// ValidationReport or a *ValidationError containing all discovered Issues.
func Validate(spec *Spec, registry *Registry, opts ValidateOptions) (*ValidationReport, error) {
	var issues []Issue
	addIssue := func(code IssueCode, path string, sentinel error, format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		issues = append(issues, Issue{
			Code:    code,
			Path:    path,
			Message: msg,
			err:     fmt.Errorf("%w: %s", sentinel, msg),
		})
	}

	if spec == nil {
		addIssue(IssueInvalidSpec, "", ErrInvalidSpec, "nil spec")
		return nil, &ValidationError{Issues: issues}
	}
	if registry == nil {
		addIssue(IssueInvalidSpec, "", ErrInvalidSpec, "nil registry")
		return nil, &ValidationError{Issues: issues}
	}
	if spec.Version != SpecVersion {
		addIssue(IssueInvalidSpec, "version", ErrUnsupportedVersion, "got version %d, supported is %d", spec.Version, SpecVersion)
	}
	if !IsValidID(spec.Name) {
		addIssue(IssueInvalidSpec, "name", ErrInvalidID, "invalid spec name %q", spec.Name)
	}
	if spec.ComponentVersion < 0 {
		addIssue(IssueInvalidSpec, "component_version", ErrInvalidSpec, "component_version must be >= 0, got %d", spec.ComponentVersion)
	}

	// Track internal "<node>.<param>" targets satisfied by top-level exposed params
	// (either via an explicit default or because callers must supply Required=true).
	boundaryParamSatisfied := make(map[PortRef]ExposedParamSpec)
	for _, ep := range spec.Params {
		if !ep.Required && ep.Default == nil {
			continue
		}
		for _, ref := range ep.ResolvedTargets() {
			if _, exists := boundaryParamSatisfied[ref]; !exists {
				boundaryParamSatisfied[ref] = ep
			}
		}
	}

	nodeIDs := spec.SortedNodeIDs()
	resolvedDefs := make(map[string]TypeDef, len(nodeIDs))

	// 1. Validate groups (pure UI metadata, but referenced nodes must exist).
	for _, gid := range spec.SortedGroupIDs() {
		if !IsValidID(gid) {
			addIssue(IssueInvalidSpec, "groups."+gid, ErrInvalidID, "invalid group id %q", gid)
		}
		g := spec.Groups[gid]
		for _, member := range g.Nodes {
			if _, ok := spec.Nodes[member]; !ok {
				addIssue(IssueUnknownNode, "groups."+gid+".nodes", ErrUnknownNode, "group %q references nonexistent node %q", gid, member)
			}
		}
	}

	// 2. Validate nodes: type resolution, backend support, parameters, capabilities.
	for _, nid := range nodeIDs {
		nodePath := "nodes." + nid
		if !IsValidID(nid) {
			addIssue(IssueInvalidSpec, nodePath, ErrInvalidID, "invalid node id %q", nid)
		}
		node := spec.Nodes[nid]
		if !node.Type.Valid() {
			addIssue(IssueInvalidSpec, nodePath+".type", ErrInvalidTypeID, "invalid node type %q", node.Type)
			continue
		}

		def, ok := registry.Lookup(node.Type)
		if !ok {
			if node.Type.Namespace == NamespaceGraph {
				addIssue(
					IssueUnresolvedSubgraph,
					nodePath+".type",
					ErrUnresolvedSubgraph,
					"subgraph type %q must be flattened before validation or registered in the registry",
					node.Type,
				)
			} else {
				addIssue(IssueUnknownNodeType, nodePath+".type", ErrUnknownNodeType, "unregistered node type %q", node.Type)
			}
			continue
		}
		resolvedDefs[nid] = def

		if opts.Backend != nil {
			b := opts.Backend
			if len(b.Namespaces) > 0 && !slices.Contains(b.Namespaces, node.Type.Namespace) {
				addIssue(IssueUnsupportedBackend, nodePath+".type", ErrUnsupportedBackend, "namespace %q is not supported by backend %q", node.Type.Namespace, b.Name)
			}
			if !def.SupportsBackend(b.Name) {
				addIssue(IssueUnsupportedBackend, nodePath+".type", ErrUnsupportedBackend, "node type %q is not supported by backend %q", node.Type, b.Name)
			}
		}

		validateNodeParams(nid, node, def, boundaryParamSatisfied, addIssue)
		validateNodeCapabilities(nid, node, def, opts.Backend, boundaryParamSatisfied, addIssue)
	}

	// 3. Validate exposed subgraph inlets, outlets, and parameters.
	inletConnected := make(map[PortRef]bool)
	seenExIn := make(map[string]struct{}, len(spec.Inlets))
	for idx, ex := range spec.Inlets {
		path := fmt.Sprintf("inlets[%d]", idx)
		if !IsValidID(ex.Name) {
			addIssue(IssueInvalidSpec, path+".name", ErrInvalidID, "invalid exposed inlet name %q", ex.Name)
		} else if _, dup := seenExIn[ex.Name]; dup {
			addIssue(IssueInvalidSpec, path+".name", ErrInvalidSpec, "duplicate exposed inlet %q", ex.Name)
		} else {
			seenExIn[ex.Name] = struct{}{}
		}
		if !ex.Type.Valid() {
			addIssue(IssueInvalidSpec, path+".type", ErrInvalidDataType, "invalid exposed inlet type %q", ex.Type)
			continue
		}
		targets := ex.ResolvedTargets()
		if len(targets) == 0 {
			addIssue(IssueInvalidSpec, path+".target", ErrInvalidPortRef, "exposed inlet %q must specify at least one target", ex.Name)
			continue
		}
		seenTarget := make(map[PortRef]struct{}, len(targets))
		for tIdx, target := range targets {
			tPath := path + ".target"
			if len(ex.Targets) > 0 {
				tPath = fmt.Sprintf("%s.targets[%d]", path, tIdx)
			}
			if !target.Valid() {
				addIssue(IssueInvalidSpec, tPath, ErrInvalidPortRef, "exposed inlet %q has invalid target %q", ex.Name, target)
				continue
			}
			if _, dup := seenTarget[target]; dup {
				addIssue(IssueInvalidSpec, tPath, ErrInvalidSpec, "exposed inlet %q has duplicate target %q", ex.Name, target)
				continue
			}
			seenTarget[target] = struct{}{}
			tdef, ok := resolvedDefs[target.Node]
			if !ok {
				if _, exists := spec.Nodes[target.Node]; !exists {
					addIssue(IssueUnknownNode, tPath, ErrUnknownNode, "exposed inlet %q targets unknown node %q", ex.Name, target.Node)
				}
				continue
			}
			pdef, ok := tdef.Inlet(target.Port)
			if !ok {
				addIssue(IssueUnknownPort, tPath, ErrUnknownPort, "node %q (%s) has no inlet %q", target.Node, tdef.ID, target.Port)
				continue
			}
			if _, _, err := CheckCompatibility(ex.Type, pdef.Type); err != nil {
				addIssue(IssueTypeMismatch, path+".type", ErrTypeMismatch, "exposed inlet %q (%s) cannot feed %s (%s)", ex.Name, ex.Type, target, pdef.Type)
				continue
			}
			inletConnected[target] = true
		}
	}

	seenExOut := make(map[string]struct{}, len(spec.Outlets))
	for idx, ex := range spec.Outlets {
		path := fmt.Sprintf("outlets[%d]", idx)
		if !IsValidID(ex.Name) {
			addIssue(IssueInvalidSpec, path+".name", ErrInvalidID, "invalid exposed outlet name %q", ex.Name)
		} else if _, dup := seenExOut[ex.Name]; dup {
			addIssue(IssueInvalidSpec, path+".name", ErrInvalidSpec, "duplicate exposed outlet %q", ex.Name)
		} else {
			seenExOut[ex.Name] = struct{}{}
		}
		if !ex.Type.Valid() {
			addIssue(IssueInvalidSpec, path+".type", ErrInvalidDataType, "invalid exposed outlet type %q", ex.Type)
			continue
		}
		if len(ex.Targets) > 0 {
			addIssue(IssueInvalidSpec, path+".targets", ErrInvalidSpec, "exposed outlet %q cannot specify multiple targets", ex.Name)
			continue
		}
		if !ex.Target.Valid() {
			addIssue(IssueInvalidSpec, path+".target", ErrInvalidPortRef, "exposed outlet %q has invalid target %q", ex.Name, ex.Target)
			continue
		}
		tdef, ok := resolvedDefs[ex.Target.Node]
		if !ok {
			if _, exists := spec.Nodes[ex.Target.Node]; !exists {
				addIssue(IssueUnknownNode, path+".target", ErrUnknownNode, "exposed outlet %q targets unknown node %q", ex.Name, ex.Target.Node)
			}
			continue
		}
		pdef, ok := tdef.Outlet(ex.Target.Port)
		if !ok {
			addIssue(IssueUnknownPort, path+".target", ErrUnknownPort, "node %q (%s) has no outlet %q", ex.Target.Node, tdef.ID, ex.Target.Port)
			continue
		}
		if _, _, err := CheckCompatibility(pdef.Type, ex.Type); err != nil {
			addIssue(IssueTypeMismatch, path+".type", ErrTypeMismatch, "%s (%s) cannot feed exposed outlet %q (%s)", ex.Target, pdef.Type, ex.Name, ex.Type)
		}
	}

	seenExParam := make(map[string]struct{}, len(spec.Params))
	claimedParamTargets := make(map[PortRef]string, len(spec.Params))
	for idx, ex := range spec.Params {
		path := fmt.Sprintf("params[%d]", idx)
		if !IsValidID(ex.Name) {
			addIssue(IssueInvalidSpec, path+".name", ErrInvalidID, "invalid exposed param name %q", ex.Name)
		} else if _, dup := seenExParam[ex.Name]; dup {
			addIssue(IssueInvalidSpec, path+".name", ErrInvalidSpec, "duplicate exposed param %q", ex.Name)
		} else {
			seenExParam[ex.Name] = struct{}{}
		}
		if !ex.Type.Valid() {
			addIssue(IssueInvalidParam, path+".type", ErrInvalidParam, "invalid exposed param type %q", ex.Type)
			continue
		}
		if ex.Min != nil && (math.IsNaN(*ex.Min) || math.IsInf(*ex.Min, 0)) {
			addIssue(IssueInvalidParam, path+".min", ErrInvalidParam, "exposed param %q has non-finite min", ex.Name)
			continue
		}
		if ex.Max != nil && (math.IsNaN(*ex.Max) || math.IsInf(*ex.Max, 0)) {
			addIssue(IssueInvalidParam, path+".max", ErrInvalidParam, "exposed param %q has non-finite max", ex.Name)
			continue
		}
		if ex.Min != nil && ex.Max != nil && *ex.Min > *ex.Max {
			addIssue(IssueInvalidParam, path+".min", ErrInvalidParam, "exposed param %q min %v > max %v", ex.Name, *ex.Min, *ex.Max)
			continue
		}
		exParamDef := ParamDef{
			Name:     ex.Name,
			Type:     ex.Type,
			Required: ex.Required,
			Default:  ex.Default,
			Min:      ex.Min,
			Max:      ex.Max,
			Enum:     ex.Enum,
		}
		if ex.Default != nil {
			if err := validateParamValue(exParamDef, ex.Default); err != nil {
				addIssue(IssueInvalidParam, path+".default", ErrInvalidParam, "exposed param %q default: %v", ex.Name, err)
			}
		}
		targets := ex.ResolvedTargets()
		if len(targets) == 0 {
			addIssue(IssueInvalidSpec, path+".target", ErrInvalidPortRef, "exposed param %q must specify at least one target", ex.Name)
			continue
		}
		seenTarget := make(map[PortRef]struct{}, len(targets))
		for tIdx, target := range targets {
			tPath := path + ".target"
			if len(ex.Targets) > 0 {
				tPath = fmt.Sprintf("%s.targets[%d]", path, tIdx)
			}
			if !target.Valid() {
				addIssue(IssueInvalidSpec, tPath, ErrInvalidPortRef, "exposed param %q has invalid target %q", ex.Name, target)
				continue
			}
			if _, dup := seenTarget[target]; dup {
				addIssue(IssueInvalidSpec, tPath, ErrInvalidSpec, "exposed param %q has duplicate target %q", ex.Name, target)
				continue
			}
			seenTarget[target] = struct{}{}
			if prevOwner, taken := claimedParamTargets[target]; taken {
				addIssue(IssueInvalidParam, tPath, ErrInvalidParam, "exposed param %q targets %s which is already targeted by exposed param %q", ex.Name, target, prevOwner)
				continue
			}
			claimedParamTargets[target] = ex.Name
			tdef, ok := resolvedDefs[target.Node]
			if !ok {
				if _, exists := spec.Nodes[target.Node]; !exists {
					addIssue(IssueUnknownNode, tPath, ErrUnknownNode, "exposed param %q targets unknown node %q", ex.Name, target.Node)
				}
				continue
			}
			pdef, ok := tdef.Param(target.Port)
			if !ok {
				addIssue(IssueInvalidParam, tPath, ErrInvalidParam, "node %q (%s) has no parameter %q", target.Node, tdef.ID, target.Port)
				continue
			}
			if !exposedParamTypeCompatible(ex.Type, pdef.Type) {
				addIssue(
					IssueInvalidParam,
					path+".type",
					ErrInvalidParam,
					"exposed param %q (%s) is incompatible with %s (%s)",
					ex.Name, ex.Type, target, pdef.Type,
				)
				continue
			}
			if ex.Default != nil {
				if err := validateParamValue(pdef, ex.Default); err != nil {
					addIssue(IssueInvalidParam, path+".default", ErrInvalidParam, "exposed param %q default invalid for %s: %v", ex.Name, target, err)
				}
			}
		}
	}

	// 4. Validate edges: endpoints, ports, edge kinds, duplicate wiring, port compatibility.
	resolutions := make([]EdgeResolution, 0, len(spec.Edges))
	seenEdges := make(map[EdgeSpec]int, len(spec.Edges))
	for idx, e := range spec.Edges {
		edgePath := fmt.Sprintf("edges[%d]", idx)
		if !e.From.Valid() {
			addIssue(IssueInvalidSpec, edgePath+".from", ErrInvalidPortRef, "invalid source port reference %q", e.From)
			continue
		}
		if !e.To.Valid() {
			addIssue(IssueInvalidSpec, edgePath+".to", ErrInvalidPortRef, "invalid target port reference %q", e.To)
			continue
		}

		effectiveKind := e.Kind
		if opts.Backend != nil {
			if effectiveKind == "" {
				effectiveKind = opts.Backend.DefaultEdgeKind
			}
			if e.Kind != "" && !slices.Contains(opts.Backend.EdgeKinds, e.Kind) {
				addIssue(IssueUnsupportedBackend, edgePath+".kind", ErrUnsupportedBackend, "edge kind %q is not supported by backend %q", e.Kind, opts.Backend.Name)
			}
		}

		normEdge := EdgeSpec{From: e.From, To: e.To, Kind: effectiveKind}
		if prevIdx, dup := seenEdges[normEdge]; dup {
			addIssue(IssueDuplicateEdge, edgePath, ErrDuplicateEdge, "duplicate edge %s -> %s (matches edges[%d])", e.From, e.To, prevIdx)
			continue
		}
		seenEdges[normEdge] = idx

		fromNodeExists := false
		if _, ok := spec.Nodes[e.From.Node]; ok {
			fromNodeExists = true
		} else {
			addIssue(IssueUnknownNode, edgePath+".from", ErrUnknownNode, "unknown source node %q", e.From.Node)
		}
		toNodeExists := false
		if _, ok := spec.Nodes[e.To.Node]; ok {
			toNodeExists = true
		} else {
			addIssue(IssueUnknownNode, edgePath+".to", ErrUnknownNode, "unknown target node %q", e.To.Node)
		}
		if !fromNodeExists || !toNodeExists {
			continue
		}

		fromDef, fromResolved := resolvedDefs[e.From.Node]
		toDef, toResolved := resolvedDefs[e.To.Node]
		if !fromResolved || !toResolved {
			// Node type error already recorded above.
			continue
		}

		outPort, ok := fromDef.Outlet(e.From.Port)
		if !ok {
			addIssue(IssueUnknownPort, edgePath+".from", ErrUnknownPort, "node %q (%s) has no outlet %q", e.From.Node, fromDef.ID, e.From.Port)
			continue
		}
		inPort, ok := toDef.Inlet(e.To.Port)
		if !ok {
			addIssue(IssueUnknownPort, edgePath+".to", ErrUnknownPort, "node %q (%s) has no inlet %q", e.To.Node, toDef.ID, e.To.Port)
			continue
		}

		inletConnected[e.To] = true

		compat, coercion, err := CheckCompatibility(outPort.Type, inPort.Type)
		if err != nil {
			addIssue(
				IssueTypeMismatch,
				edgePath,
				ErrTypeMismatch,
				"cannot wire %s (%s) to %s (%s): %s -> %s is not allowed",
				e.From, outPort.Type, e.To, inPort.Type, outPort.Type, inPort.Type,
			)
			continue
		}

		resolutions = append(resolutions, EdgeResolution{
			Edge:          e,
			EffectiveKind: effectiveKind,
			OutletType:    outPort.Type,
			InletType:     inPort.Type,
			Compatibility: compat,
			Coercion:      coercion,
		})
	}

	// 5. Check required inlets on all resolved nodes.
	for _, nid := range nodeIDs {
		def, ok := resolvedDefs[nid]
		if !ok {
			continue
		}
		for _, inPort := range def.Inlets {
			if !inPort.Required {
				continue
			}
			ref := PortRef{Node: nid, Port: inPort.Name}
			if !inletConnected[ref] {
				addIssue(
					IssueMissingRequiredInlet,
					"nodes."+nid+".inlets."+inPort.Name,
					ErrMissingRequiredInlet,
					"required inlet %s (%s) has no incoming edge",
					ref, inPort.Type,
				)
			}
		}
	}

	// 6. Topological order and cycle checking.
	allowCycles := opts.Backend != nil && opts.Backend.AllowCycles
	var cycleBreakKinds []string
	if opts.Backend != nil {
		cycleBreakKinds = opts.Backend.CycleBreakingEdgeKinds
	}
	topoOrder, cyclePath := analyzeTopology(nodeIDs, spec.Edges, resolvedDefs, opts.Backend, cycleBreakKinds)
	if len(cyclePath) > 0 && !allowCycles {
		addIssue(
			IssueCycleDetected,
			"edges",
			ErrCycleDetected,
			"cycle forbidden by backend: %s",
			strings.Join(cyclePath, " -> "),
		)
	}

	if len(issues) > 0 {
		return nil, &ValidationError{Issues: issues}
	}

	slices.SortFunc(resolutions, func(a, b EdgeResolution) int {
		return CompareEdgeSpec(a.Edge, b.Edge)
	})
	return &ValidationReport{
		Edges:            resolutions,
		TopologicalOrder: topoOrder,
	}, nil
}

func exposedParamTypeCompatible(outer, inner ParamType) bool {
	if outer == inner || inner == ParamJSON {
		return true
	}
	return outer == ParamInt && inner == ParamNumber
}

func validateNodeParams(
	nid string,
	node NodeSpec,
	def TypeDef,
	boundaryParamSatisfied map[PortRef]ExposedParamSpec,
	addIssue func(code IssueCode, path string, sentinel error, format string, args ...any),
) {
	paramKeys := make([]string, 0, len(node.Params))
	for k := range node.Params {
		paramKeys = append(paramKeys, k)
	}
	slices.Sort(paramKeys)

	for _, k := range paramKeys {
		pPath := "nodes." + nid + ".params." + k
		pdef, ok := def.Param(k)
		if !ok {
			addIssue(IssueInvalidParam, pPath, ErrInvalidParam, "unknown parameter %q for node type %s", k, def.ID)
			continue
		}
		if err := validateParamValue(pdef, node.Params[k]); err != nil {
			addIssue(IssueInvalidParam, pPath, ErrInvalidParam, "%v", err)
		}
	}

	for _, pdef := range def.Params {
		if !pdef.Required || pdef.Default != nil {
			continue
		}
		if val, ok := node.Params[pdef.Name]; !ok || val == nil {
			if _, exposed := boundaryParamSatisfied[PortRef{Node: nid, Port: pdef.Name}]; exposed {
				continue
			}
			addIssue(
				IssueInvalidParam,
				"nodes."+nid+".params."+pdef.Name,
				ErrInvalidParam,
				"missing required parameter %q (%s) for %s",
				pdef.Name, pdef.Type, def.ID,
			)
		}
	}
}

func validateParamValue(pdef ParamDef, raw any) error {
	norm, err := normalizeParamValue(raw, 0, DefaultMaxDepth)
	if err != nil {
		return err
	}
	switch pdef.Type {
	case ParamString:
		s, ok := norm.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", raw)
		}
		if len(pdef.Enum) > 0 && !slices.Contains(pdef.Enum, s) {
			return fmt.Errorf("value %q must be one of %v", s, pdef.Enum)
		}
	case ParamDuration:
		s, ok := norm.(string)
		if !ok {
			return fmt.Errorf("expected duration string (e.g. \"60s\"), got %T", raw)
		}
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return fmt.Errorf("invalid non-negative duration %q", s)
		}
		if err := checkNumericBounds(pdef, d.Seconds()); err != nil {
			return err
		}
	case ParamInt:
		i, ok := norm.(int64)
		if !ok {
			return fmt.Errorf("expected integer, got %T", raw)
		}
		if err := checkNumericBounds(pdef, float64(i)); err != nil {
			return err
		}
	case ParamNumber:
		var f float64
		switch v := norm.(type) {
		case int64:
			f = float64(v)
		case float64:
			f = v
		default:
			return fmt.Errorf("expected number, got %T", raw)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("number must be finite")
		}
		if err := checkNumericBounds(pdef, f); err != nil {
			return err
		}
	case ParamBool:
		if _, ok := norm.(bool); !ok {
			return fmt.Errorf("expected bool, got %T", raw)
		}
	case ParamStrings:
		arr, ok := norm.([]any)
		if !ok {
			return fmt.Errorf("expected array of strings, got %T", raw)
		}
		for idx, elem := range arr {
			s, ok := elem.(string)
			if !ok {
				return fmt.Errorf("element[%d]: expected string, got %T", idx, elem)
			}
			if len(pdef.Enum) > 0 && !slices.Contains(pdef.Enum, s) {
				return fmt.Errorf("element[%d] %q must be one of %v", idx, s, pdef.Enum)
			}
		}
	case ParamJSON:
		// Any normalized JSON/YAML value (scalar, array, object) is valid.
	default:
		return fmt.Errorf("unsupported parameter schema type %q", pdef.Type)
	}
	return nil
}

func checkNumericBounds(pdef ParamDef, v float64) error {
	if pdef.Min != nil && v < *pdef.Min {
		return fmt.Errorf("value %v is less than minimum %v", v, *pdef.Min)
	}
	if pdef.Max != nil && v > *pdef.Max {
		return fmt.Errorf("value %v exceeds maximum %v", v, *pdef.Max)
	}
	return nil
}

func validateNodeCapabilities(
	nid string,
	node NodeSpec,
	def TypeDef,
	backend *BackendProfile,
	boundaryParamSatisfied map[PortRef]ExposedParamSpec,
	addIssue func(code IssueCode, path string, sentinel error, format string, args ...any),
) {
	capPath := "nodes." + nid + ".capabilities"
	for i, c := range node.Capabilities {
		if !c.Valid() {
			addIssue(IssueInvalidSpec, fmt.Sprintf("%s[%d]", capPath, i), ErrInvalidCapability, "invalid capability %q", c)
			continue
		}
		if backend != nil && len(backend.Capabilities) > 0 && !slices.Contains(backend.Capabilities, c.Kind) {
			addIssue(
				IssueUnsupportedBackend,
				fmt.Sprintf("%s[%d]", capPath, i),
				ErrUnsupportedBackend,
				"capability kind %q is not supported by backend %q",
				c.Kind, backend.Name,
			)
		}
	}

	for _, rule := range def.Capabilities {
		required := Capability{Kind: rule.Kind, Scope: rule.Scope}
		if rule.HostFromURLParam != "" {
			pdef, hasPDef := def.Param(rule.HostFromURLParam)
			rawURL, hasVal := node.Params[rule.HostFromURLParam]
			boundaryEx, hasBoundary := boundaryParamSatisfied[PortRef{Node: nid, Port: rule.HostFromURLParam}]
			if (!hasVal || rawURL == nil) && hasBoundary && boundaryEx.Default != nil {
				rawURL = boundaryEx.Default
				hasVal = true
			}
			if (!hasVal || rawURL == nil) && hasPDef && pdef.Default != nil {
				rawURL = pdef.Default
				hasVal = true
			}
			if !hasVal || rawURL == nil {
				if !hasBoundary && (!hasPDef || !pdef.Required || pdef.Default != nil) {
					addIssue(
						IssueInvalidParam,
						"nodes."+nid+".params."+rule.HostFromURLParam,
						ErrInvalidParam,
						"missing URL parameter %q required for %q capability host check",
						rule.HostFromURLParam, rule.Kind,
					)
				}
				// When exposed as a required boundary URL param with no static URL on the
				// internal node, outer graph/* callers declare the concrete net:<host>
				// capability and Flatten enforces coverage; only require an internal
				// placeholder capability when not boundary-forwarded.
				if hasBoundary && boundaryEx.Required && boundaryEx.Default == nil {
					continue
				}
				hasKindDecl := false
				for _, decl := range node.Capabilities {
					if decl.Kind == rule.Kind && (!rule.RequireScope || decl.Scope != "") {
						hasKindDecl = true
						break
					}
				}
				if !hasKindDecl {
					addIssue(
						IssueUndeclaredCapability,
						capPath,
						ErrUndeclaredCapability,
						"node %q (%s) requires a scoped %q capability (e.g. \"%s:<host>\")",
						nid, def.ID, rule.Kind, rule.Kind,
					)
				}
				continue
			}

			urlStr, isStr := rawURL.(string)
			if !isStr {
				if !hasPDef || pdef.Type == ParamJSON {
					addIssue(
						IssueInvalidParam,
						"nodes."+nid+".params."+rule.HostFromURLParam,
						ErrInvalidParam,
						"parameter %q must be an http(s) URL string, got %T",
						rule.HostFromURLParam, rawURL,
					)
				}
				continue
			}
			u, err := url.Parse(urlStr)
			if urlStr == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
				addIssue(
					IssueInvalidParam,
					"nodes."+nid+".params."+rule.HostFromURLParam,
					ErrInvalidParam,
					"parameter %q must be a valid http(s) URL with hostname, got %q",
					rule.HostFromURLParam, urlStr,
				)
				continue
			}
			required.Scope = strings.ToLower(u.Hostname())
		}

		covered := false
		for _, decl := range node.Capabilities {
			if rule.RequireScope && decl.Scope == "" {
				continue
			}
			if decl.Covers(required) {
				covered = true
				break
			}
		}
		if !covered {
			if required.Scope != "" {
				addIssue(
					IssueUndeclaredCapability,
					capPath,
					ErrUndeclaredCapability,
					"node %q (%s) requires capability %q",
					nid, def.ID, required,
				)
			} else if rule.RequireScope {
				addIssue(
					IssueUndeclaredCapability,
					capPath,
					ErrUndeclaredCapability,
					"node %q (%s) requires a scoped %q capability (e.g. \"%s:<scope>\")",
					nid, def.ID, rule.Kind, rule.Kind,
				)
			} else {
				addIssue(
					IssueUndeclaredCapability,
					capPath,
					ErrUndeclaredCapability,
					"node %q (%s) requires capability %q",
					nid, def.ID, rule.Kind,
				)
			}
		}
	}
}

// analyzeTopology builds a deterministic node ordering over instantaneous edges
// using Kahn's algorithm with a sorted ready queue, and extracts a deterministic
// directed cycle (visited in sorted node and edge order) if one exists. When
// cycles remain, unscheduled nodes are appended by iteratively breaking the
// lexicographically smallest remaining node so AllowCycles backends still
// receive a complete, deterministic node schedule.
func analyzeTopology(
	sortedNodes []string,
	edges []EdgeSpec,
	resolvedDefs map[string]TypeDef,
	backend *BackendProfile,
	cycleBreakKinds []string,
) (topo []string, cycle []string) {
	nodeSet := make(map[string]struct{}, len(sortedNodes))
	for _, n := range sortedNodes {
		nodeSet[n] = struct{}{}
	}
	adj := make(map[string][]string, len(sortedNodes))
	indeg := make(map[string]int, len(sortedNodes))
	seenPair := make(map[[2]string]struct{})

	for _, e := range edges {
		effKind := e.Kind
		if effKind == "" && backend != nil {
			effKind = backend.DefaultEdgeKind
		}
		if len(cycleBreakKinds) > 0 && slices.Contains(cycleBreakKinds, effKind) {
			continue
		}
		if _, ok := nodeSet[e.From.Node]; !ok {
			continue
		}
		if _, ok := nodeSet[e.To.Node]; !ok {
			continue
		}
		if backend != nil && backend.CycleBreakingEdge != nil {
			fromDef, okFrom := resolvedDefs[e.From.Node]
			toDef, okTo := resolvedDefs[e.To.Node]
			if okFrom && okTo && backend.CycleBreakingEdge(EdgeSpec{From: e.From, To: e.To, Kind: effKind}, fromDef, toDef) {
				continue
			}
		}
		pair := [2]string{e.From.Node, e.To.Node}
		if _, dup := seenPair[pair]; dup {
			continue
		}
		seenPair[pair] = struct{}{}
		adj[e.From.Node] = append(adj[e.From.Node], e.To.Node)
		indeg[e.To.Node]++
	}
	for u := range adj {
		slices.Sort(adj[u])
	}

	ready := make([]string, 0, len(sortedNodes))
	for _, n := range sortedNodes {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}

	scheduled := make(map[string]bool, len(sortedNodes))
	drainReady := func() {
		for len(ready) > 0 {
			u := ready[0]
			ready = ready[1:]
			if scheduled[u] {
				continue
			}
			scheduled[u] = true
			topo = append(topo, u)
			for _, v := range adj[u] {
				if scheduled[v] {
					continue
				}
				indeg[v]--
				if indeg[v] <= 0 {
					idx, found := slices.BinarySearch(ready, v)
					if !found {
						ready = slices.Insert(ready, idx, v)
					}
				}
			}
		}
	}

	topo = make([]string, 0, len(sortedNodes))
	drainReady()
	if len(topo) == len(sortedNodes) {
		return topo, nil
	}

	cycle = findDeterministicCycle(sortedNodes, adj)
	for _, n := range sortedNodes {
		if scheduled[n] {
			continue
		}
		indeg[n] = 0
		ready = append(ready, n)
		drainReady()
	}
	return topo, cycle
}

func findDeterministicCycle(sortedNodes []string, adj map[string][]string) []string {
	const (
		unvisited uint8 = 0
		visiting  uint8 = 1
		visited   uint8 = 2
	)
	state := make(map[string]uint8, len(sortedNodes))
	var stack []string
	stackPos := make(map[string]int, len(sortedNodes))

	var dfs func(u string) []string
	dfs = func(u string) []string {
		state[u] = visiting
		stackPos[u] = len(stack)
		stack = append(stack, u)
		for _, v := range adj[u] {
			switch state[v] {
			case visiting:
				start := stackPos[v]
				cycle := make([]string, 0, len(stack)-start+1)
				cycle = append(cycle, stack[start:]...)
				cycle = append(cycle, v)
				return cycle
			case unvisited:
				if cyc := dfs(v); len(cyc) > 0 {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackPos, u)
		state[u] = visited
		return nil
	}

	for _, n := range sortedNodes {
		if state[n] == unvisited {
			if cyc := dfs(n); len(cyc) > 0 {
				return cyc
			}
		}
	}
	return nil
}
