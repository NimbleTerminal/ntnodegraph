// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// Sentinel errors returned by headless editing operations.
var (
	ErrNodeExists   = errors.New("ntnodegraph: node already exists")
	ErrGroupExists  = errors.New("ntnodegraph: group already exists")
	ErrUnknownGroup = errors.New("ntnodegraph: unknown group")
	ErrUnknownEdge  = errors.New("ntnodegraph: edge not found")
)

// EditOp is an atomic, pure transformation on an in-memory Spec.
// Both the visual editor and headless LLM/MCP callers emit EditOps rather than
// mutating runtime state directly.
type EditOp interface {
	Apply(spec *Spec) error
}

// EditFunc adapts a function to EditOp.
type EditFunc func(spec *Spec) error

// Apply implements EditOp.
func (f EditFunc) Apply(spec *Spec) error {
	return f(spec)
}

// NewSpec constructs a clean, initialized Spec with Version = SpecVersion.
func NewSpec(name string) (*Spec, error) {
	if !IsValidID(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, name)
	}
	return &Spec{
		Version: SpecVersion,
		Name:    name,
		Nodes:   make(map[string]NodeSpec),
		Edges:   []EdgeSpec{},
	}, nil
}

// ApplyEdits applies ops transactionally to a clone of spec and returns the
// canonicalized result. If any EditOp fails, the original spec is untouched.
func ApplyEdits(spec *Spec, ops ...EditOp) (*Spec, error) {
	if spec == nil {
		return nil, fmt.Errorf("%w: nil spec", ErrInvalidSpec)
	}
	working := spec.Clone()
	if working.Nodes == nil {
		working.Nodes = make(map[string]NodeSpec)
	}
	for i, op := range ops {
		if op == nil {
			continue
		}
		if err := op.Apply(working); err != nil {
			return nil, fmt.Errorf("edit[%d]: %w", i, err)
		}
	}
	return working.Canonicalize(), nil
}

// AddNode inserts a new node into the graph, failing if id already exists.
func AddNode(id string, node NodeSpec) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if !IsValidID(id) {
			return fmt.Errorf("%w: %q", ErrInvalidID, id)
		}
		if !node.Type.Valid() {
			return fmt.Errorf("node %q: %w: %q", id, ErrInvalidTypeID, node.Type)
		}
		if _, exists := spec.Nodes[id]; exists {
			return fmt.Errorf("%w: %q", ErrNodeExists, id)
		}
		for _, c := range node.Capabilities {
			if !c.Valid() {
				return fmt.Errorf("node %q: %w: %q", id, ErrInvalidCapability, c)
			}
		}
		normParams, err := normalizeNodeParams(node.Params, DefaultMaxDepth)
		if err != nil {
			return fmt.Errorf("node %q: %w", id, err)
		}
		if node.UI != nil {
			if err := checkFiniteUI(node.UI.X, node.UI.Y); err != nil {
				return fmt.Errorf("node %q: %w", id, err)
			}
		}
		if spec.Nodes == nil {
			spec.Nodes = make(map[string]NodeSpec)
		}
		cp := cloneNodeSpec(node)
		cp.Params = normParams
		spec.Nodes[id] = cp
		return nil
	})
}

// RemoveNode deletes node id, detaches any incident edges, and removes id from
// any UI groups and exposed ports.
func RemoveNode(id string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if _, exists := spec.Nodes[id]; !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, id)
		}
		delete(spec.Nodes, id)

		keptEdges := spec.Edges[:0]
		for _, e := range spec.Edges {
			if e.From.Node != id && e.To.Node != id {
				keptEdges = append(keptEdges, e)
			}
		}
		spec.Edges = keptEdges

		for gid, g := range spec.Groups {
			g.Nodes = slices.DeleteFunc(g.Nodes, func(member string) bool {
				return member == id
			})
			spec.Groups[gid] = g
		}
		keptInlets := spec.Inlets[:0]
		for _, p := range spec.Inlets {
			rem := slices.DeleteFunc(p.ResolvedTargets(), func(ref PortRef) bool {
				return ref.Node == id
			})
			if len(rem) == 0 {
				continue
			}
			p.Target, p.Targets = canonicalizeTargets(PortRef{}, rem)
			keptInlets = append(keptInlets, p)
		}
		if len(keptInlets) == 0 {
			spec.Inlets = nil
		} else {
			spec.Inlets = keptInlets
		}
		spec.Outlets = slices.DeleteFunc(spec.Outlets, func(p ExposedPortSpec) bool {
			return p.Target.Node == id
		})
		if len(spec.Outlets) == 0 {
			spec.Outlets = nil
		}
		keptParams := spec.Params[:0]
		for _, p := range spec.Params {
			rem := slices.DeleteFunc(p.ResolvedTargets(), func(ref PortRef) bool {
				return ref.Node == id
			})
			if len(rem) == 0 {
				continue
			}
			p.Target, p.Targets = canonicalizeTargets(PortRef{}, rem)
			keptParams = append(keptParams, p)
		}
		if len(keptParams) == 0 {
			spec.Params = nil
		} else {
			spec.Params = keptParams
		}
		return nil
	})
}

// SetParam sets or updates parameter key on node id.
func SetParam(nodeID, key string, value any) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		node, exists := spec.Nodes[nodeID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, nodeID)
		}
		if !IsValidID(key) {
			return fmt.Errorf("%w: param %q", ErrInvalidID, key)
		}
		nv, err := normalizeParamValue(value, 0, DefaultMaxDepth)
		if err != nil {
			return fmt.Errorf("node %q param %q: %w", nodeID, key, err)
		}
		if node.Params == nil {
			node.Params = make(map[string]any)
		}
		node.Params[key] = nv
		spec.Nodes[nodeID] = node
		return nil
	})
}

// DeleteParam removes parameter key from node id.
func DeleteParam(nodeID, key string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		node, exists := spec.Nodes[nodeID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, nodeID)
		}
		delete(node.Params, key)
		if len(node.Params) == 0 {
			node.Params = nil
		}
		spec.Nodes[nodeID] = node
		return nil
	})
}

// SetCapabilities replaces the declared capability list on node id.
func SetCapabilities(nodeID string, caps ...Capability) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		node, exists := spec.Nodes[nodeID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, nodeID)
		}
		for _, c := range caps {
			if !c.Valid() {
				return fmt.Errorf("node %q: %w: %q", nodeID, ErrInvalidCapability, c)
			}
		}
		if len(caps) == 0 {
			node.Capabilities = nil
		} else {
			node.Capabilities = append([]Capability(nil), caps...)
		}
		spec.Nodes[nodeID] = node
		return nil
	})
}

// MoveNode updates the editor (x, y) coordinates on node id, preserving any
// existing UI label or collapsed state.
func MoveNode(nodeID string, x, y float64) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		node, exists := spec.Nodes[nodeID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, nodeID)
		}
		if err := checkFiniteUI(x, y); err != nil {
			return fmt.Errorf("node %q: %w", nodeID, err)
		}
		if node.UI == nil {
			node.UI = &NodeUI{X: x, Y: y}
		} else {
			uiCopy := *node.UI
			uiCopy.X = x
			uiCopy.Y = y
			node.UI = &uiCopy
		}
		spec.Nodes[nodeID] = node
		return nil
	})
}

// SetNodeLabel updates the editor UI display label on node id.
func SetNodeLabel(nodeID, label string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		node, exists := spec.Nodes[nodeID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, nodeID)
		}
		if node.UI == nil {
			if label == "" {
				return nil
			}
			node.UI = &NodeUI{Label: label}
		} else {
			uiCopy := *node.UI
			uiCopy.Label = label
			node.UI = &uiCopy
		}
		spec.Nodes[nodeID] = node
		return nil
	})
}

// SetNodeCollapsed updates the editor UI collapsed state on node id.
func SetNodeCollapsed(nodeID string, collapsed bool) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		node, exists := spec.Nodes[nodeID]
		if !exists {
			return fmt.Errorf("%w: %q", ErrUnknownNode, nodeID)
		}
		if node.UI == nil {
			if !collapsed {
				return nil
			}
			node.UI = &NodeUI{Collapsed: true}
		} else {
			uiCopy := *node.UI
			uiCopy.Collapsed = collapsed
			node.UI = &uiCopy
		}
		spec.Nodes[nodeID] = node
		return nil
	})
}

// Connect adds an edge from outlet to inlet with optional delivery kind.
func Connect(from, to PortRef, kind string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if !from.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidPortRef, from)
		}
		if !to.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidPortRef, to)
		}
		if kind != "" && !isValidNamespace(kind) {
			return fmt.Errorf("%w: invalid edge kind %q", ErrInvalidSpec, kind)
		}
		if _, ok := spec.Nodes[from.Node]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownNode, from.Node)
		}
		if _, ok := spec.Nodes[to.Node]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownNode, to.Node)
		}
		candidate := EdgeSpec{From: from, To: to, Kind: kind}
		for _, existing := range spec.Edges {
			if existing == candidate {
				return fmt.Errorf("%w: %s -> %s", ErrDuplicateEdge, from, to)
			}
		}
		spec.Edges = append(spec.Edges, candidate)
		return nil
	})
}

// Disconnect removes an edge matching (from, to, kind). If kind is empty, it
// removes an edge with an empty kind, or if exactly one edge connects from -> to
// regardless of kind, removes that edge.
func Disconnect(from, to PortRef, kind string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		exactIdx := -1
		var anyMatches []int
		for i, e := range spec.Edges {
			if e.From == from && e.To == to {
				anyMatches = append(anyMatches, i)
				if e.Kind == kind {
					exactIdx = i
					break
				}
			}
		}
		targetIdx := exactIdx
		if targetIdx < 0 && kind == "" && len(anyMatches) == 1 {
			targetIdx = anyMatches[0]
		}
		if targetIdx < 0 {
			return fmt.Errorf("%w: %s -> %s", ErrUnknownEdge, from, to)
		}
		spec.Edges = slices.Delete(spec.Edges, targetIdx, targetIdx+1)
		return nil
	})
}

// PutGroup creates or updates an editor UI group, verifying all member nodes exist.
func PutGroup(groupID string, group GroupSpec) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if !IsValidID(groupID) {
			return fmt.Errorf("%w: %q", ErrInvalidID, groupID)
		}
		for _, member := range group.Nodes {
			if _, exists := spec.Nodes[member]; !exists {
				return fmt.Errorf("group %q: %w: %q", groupID, ErrUnknownNode, member)
			}
		}
		if group.UI != nil {
			if err := checkFiniteUI(group.UI.X, group.UI.Y); err != nil {
				return fmt.Errorf("group %q: %w", groupID, err)
			}
			if err := checkFiniteUI(group.UI.Width, group.UI.Height); err != nil {
				return fmt.Errorf("group %q: %w", groupID, err)
			}
			if group.UI.Width < 0 || group.UI.Height < 0 {
				return fmt.Errorf("%w: group %q negative dimensions", ErrInvalidSpec, groupID)
			}
		}
		if spec.Groups == nil {
			spec.Groups = make(map[string]GroupSpec)
		}
		cp := GroupSpec{
			Label:     group.Label,
			Nodes:     append([]string(nil), group.Nodes...),
			Collapsed: group.Collapsed,
		}
		if group.UI != nil {
			uiCopy := *group.UI
			cp.UI = &uiCopy
		}
		spec.Groups[groupID] = cp
		return nil
	})
}

// RemoveGroup deletes an editor UI group without removing its member nodes.
func RemoveGroup(groupID string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if _, exists := spec.Groups[groupID]; !exists {
			return fmt.Errorf("%w: %q", ErrUnknownGroup, groupID)
		}
		delete(spec.Groups, groupID)
		if len(spec.Groups) == 0 {
			spec.Groups = nil
		}
		return nil
	})
}

// SetComponentVersion sets the optional component_version major number on spec
// (pass 0 to omit and default to 1).
func SetComponentVersion(major int) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if major < 0 {
			return fmt.Errorf("%w: component_version must be >= 0, got %d", ErrInvalidSpec, major)
		}
		spec.ComponentVersion = major
		return nil
	})
}

// ExposeInlet adds or replaces a subgraph boundary inlet by Name.
func ExposeInlet(inlet ExposedPortSpec) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if !IsValidID(inlet.Name) {
			return fmt.Errorf("exposed inlet: %w %q", ErrInvalidID, inlet.Name)
		}
		if !inlet.Type.Valid() {
			return fmt.Errorf("exposed inlet %q: %w %q", inlet.Name, ErrInvalidDataType, inlet.Type)
		}
		targets := inlet.ResolvedTargets()
		if len(targets) == 0 {
			return fmt.Errorf("%w: exposed inlet %q requires at least one target", ErrInvalidSpec, inlet.Name)
		}
		for _, ref := range targets {
			if !ref.Valid() {
				return fmt.Errorf("exposed inlet %q: %w %q", inlet.Name, ErrInvalidPortRef, ref)
			}
			if _, ok := spec.Nodes[ref.Node]; !ok {
				return fmt.Errorf("exposed inlet %q: %w %q", inlet.Name, ErrUnknownNode, ref.Node)
			}
		}
		cp := cloneExposedPort(inlet)
		cp.Target, cp.Targets = canonicalizeTargets(cp.Target, cp.Targets)
		for i := range spec.Inlets {
			if spec.Inlets[i].Name == cp.Name {
				spec.Inlets[i] = cp
				return nil
			}
		}
		spec.Inlets = append(spec.Inlets, cp)
		return nil
	})
}

// RemoveInlet deletes a subgraph boundary inlet by Name.
func RemoveInlet(name string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		idx := slices.IndexFunc(spec.Inlets, func(p ExposedPortSpec) bool {
			return p.Name == name
		})
		if idx < 0 {
			return fmt.Errorf("%w: exposed inlet %q", ErrUnknownPort, name)
		}
		spec.Inlets = slices.Delete(spec.Inlets, idx, idx+1)
		if len(spec.Inlets) == 0 {
			spec.Inlets = nil
		}
		return nil
	})
}

// ExposeOutlet adds or replaces a subgraph boundary outlet by Name.
func ExposeOutlet(outlet ExposedPortSpec) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if !IsValidID(outlet.Name) {
			return fmt.Errorf("exposed outlet: %w %q", ErrInvalidID, outlet.Name)
		}
		if !outlet.Type.Valid() {
			return fmt.Errorf("exposed outlet %q: %w %q", outlet.Name, ErrInvalidDataType, outlet.Type)
		}
		targets := outlet.ResolvedTargets()
		if len(targets) != 1 || len(outlet.Targets) > 1 {
			return fmt.Errorf("%w: exposed outlet %q must specify exactly one target", ErrInvalidSpec, outlet.Name)
		}
		ref := targets[0]
		if !ref.Valid() {
			return fmt.Errorf("exposed outlet %q: %w %q", outlet.Name, ErrInvalidPortRef, ref)
		}
		if _, ok := spec.Nodes[ref.Node]; !ok {
			return fmt.Errorf("exposed outlet %q: %w %q", outlet.Name, ErrUnknownNode, ref.Node)
		}
		cp := ExposedPortSpec{
			Name:        outlet.Name,
			Type:        outlet.Type,
			Target:      ref,
			Description: outlet.Description,
		}
		for i := range spec.Outlets {
			if spec.Outlets[i].Name == cp.Name {
				spec.Outlets[i] = cp
				return nil
			}
		}
		spec.Outlets = append(spec.Outlets, cp)
		return nil
	})
}

// RemoveOutlet deletes a subgraph boundary outlet by Name.
func RemoveOutlet(name string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		idx := slices.IndexFunc(spec.Outlets, func(p ExposedPortSpec) bool {
			return p.Name == name
		})
		if idx < 0 {
			return fmt.Errorf("%w: exposed outlet %q", ErrUnknownPort, name)
		}
		spec.Outlets = slices.Delete(spec.Outlets, idx, idx+1)
		if len(spec.Outlets) == 0 {
			spec.Outlets = nil
		}
		return nil
	})
}

// ExposeParam adds or replaces a subgraph boundary parameter by Name.
func ExposeParam(param ExposedParamSpec) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		if !IsValidID(param.Name) {
			return fmt.Errorf("exposed param: %w %q", ErrInvalidID, param.Name)
		}
		if !param.Type.Valid() {
			return fmt.Errorf("%w: exposed param %q has invalid type %q", ErrInvalidParam, param.Name, param.Type)
		}
		targets := param.ResolvedTargets()
		if len(targets) == 0 {
			return fmt.Errorf("%w: exposed param %q requires at least one target", ErrInvalidSpec, param.Name)
		}
		for _, ref := range targets {
			if !ref.Valid() {
				return fmt.Errorf("exposed param %q: %w %q", param.Name, ErrInvalidPortRef, ref)
			}
			if _, ok := spec.Nodes[ref.Node]; !ok {
				return fmt.Errorf("exposed param %q: %w %q", param.Name, ErrUnknownNode, ref.Node)
			}
		}
		cp := cloneExposedParam(param)
		cp.Target, cp.Targets = canonicalizeTargets(cp.Target, cp.Targets)
		if cp.Default != nil {
			nv, err := normalizeParamValue(cp.Default, 0, DefaultMaxDepth)
			if err != nil {
				return fmt.Errorf("exposed param %q default: %w", param.Name, err)
			}
			cp.Default = nv
		}
		if cp.Min != nil && (math.IsNaN(*cp.Min) || math.IsInf(*cp.Min, 0)) {
			return fmt.Errorf("%w: exposed param %q non-finite min", ErrInvalidParam, param.Name)
		}
		if cp.Max != nil && (math.IsNaN(*cp.Max) || math.IsInf(*cp.Max, 0)) {
			return fmt.Errorf("%w: exposed param %q non-finite max", ErrInvalidParam, param.Name)
		}
		if cp.Min != nil && cp.Max != nil && *cp.Min > *cp.Max {
			return fmt.Errorf("%w: exposed param %q min > max", ErrInvalidParam, param.Name)
		}
		for i := range spec.Params {
			if spec.Params[i].Name == cp.Name {
				spec.Params[i] = cp
				return nil
			}
		}
		spec.Params = append(spec.Params, cp)
		return nil
	})
}

// RemoveParam deletes a subgraph boundary parameter by Name.
func RemoveParam(name string) EditOp {
	return EditFunc(func(spec *Spec) error {
		if spec == nil {
			return fmt.Errorf("%w: nil spec", ErrInvalidSpec)
		}
		idx := slices.IndexFunc(spec.Params, func(p ExposedParamSpec) bool {
			return p.Name == name
		})
		if idx < 0 {
			return fmt.Errorf("%w: exposed param %q", ErrInvalidParam, name)
		}
		spec.Params = slices.Delete(spec.Params, idx, idx+1)
		if len(spec.Params) == 0 {
			spec.Params = nil
		}
		return nil
	})
}
