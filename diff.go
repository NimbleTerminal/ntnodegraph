// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"reflect"
	"slices"
)

// NodeChange describes how a single existing node changed between two Specs.
type NodeChange struct {
	ID                  string   `json:"id"`
	TypeChanged         bool     `json:"type_changed,omitempty"`
	CapabilitiesChanged bool     `json:"capabilities_changed,omitempty"`
	ChangedParams       []string `json:"changed_params,omitempty"`
	UIChanged           bool     `json:"ui_changed,omitempty"`
}

// RuntimeChanged reports whether the node's execution-relevant definition
// (type, capabilities, or parameters) changed. When false and UIChanged is
// true, hot-reload runtimes can keep the running node untouched.
func (c NodeChange) RuntimeChanged() bool {
	return c.TypeChanged || c.CapabilitiesChanged || len(c.ChangedParams) > 0
}

// SpecDiff is a deterministic structural diff between two Specs.
type SpecDiff struct {
	VersionChanged          bool         `json:"version_changed,omitempty"`
	NameChanged             bool         `json:"name_changed,omitempty"`
	ComponentVersionChanged bool         `json:"component_version_changed,omitempty"`
	DescriptionChanged      bool         `json:"description_changed,omitempty"`
	InletsChanged           bool         `json:"inlets_changed,omitempty"`
	OutletsChanged          bool         `json:"outlets_changed,omitempty"`
	ParamsChanged           bool         `json:"params_changed,omitempty"`
	GroupsChanged           bool         `json:"groups_changed,omitempty"`
	AddedNodes              []string     `json:"added_nodes,omitempty"`
	RemovedNodes            []string     `json:"removed_nodes,omitempty"`
	ModifiedNodes           []NodeChange `json:"modified_nodes,omitempty"`
	AddedEdges              []EdgeSpec   `json:"added_edges,omitempty"`
	RemovedEdges            []EdgeSpec   `json:"removed_edges,omitempty"`
}

// IsEmpty reports whether before and after are structurally identical after
// canonicalization.
func (d SpecDiff) IsEmpty() bool {
	return !d.VersionChanged &&
		!d.NameChanged &&
		!d.ComponentVersionChanged &&
		!d.DescriptionChanged &&
		!d.InletsChanged &&
		!d.OutletsChanged &&
		!d.ParamsChanged &&
		!d.GroupsChanged &&
		len(d.AddedNodes) == 0 &&
		len(d.RemovedNodes) == 0 &&
		len(d.ModifiedNodes) == 0 &&
		len(d.AddedEdges) == 0 &&
		len(d.RemovedEdges) == 0
}

// HasRuntimeChanges reports whether the diff affects spec version, runtime
// execution topology, node types, capabilities, parameters, or exposed boundary
// ports/parameters (ignoring pure UI layout, description, name, and group edits).
func (d SpecDiff) HasRuntimeChanges() bool {
	if d.VersionChanged || d.ComponentVersionChanged ||
		d.InletsChanged || d.OutletsChanged || d.ParamsChanged ||
		len(d.AddedNodes) > 0 || len(d.RemovedNodes) > 0 ||
		len(d.AddedEdges) > 0 || len(d.RemovedEdges) > 0 {
		return true
	}
	for _, mc := range d.ModifiedNodes {
		if mc.RuntimeChanged() {
			return true
		}
	}
	return false
}

// Diff computes the deterministic structural difference from before to after.
func Diff(before, after *Spec) SpecDiff {
	var a, b *Spec
	if before != nil {
		a = before.Canonicalize()
	} else {
		a = (&Spec{Version: SpecVersion}).Canonicalize()
	}
	if after != nil {
		b = after.Canonicalize()
	} else {
		b = (&Spec{Version: SpecVersion}).Canonicalize()
	}

	var out SpecDiff
	out.VersionChanged = a.Version != b.Version
	out.NameChanged = a.Name != b.Name
	out.ComponentVersionChanged = a.ComponentVersion != b.ComponentVersion
	out.DescriptionChanged = a.Description != b.Description
	out.InletsChanged = !equalDeepSlice(a.Inlets, b.Inlets)
	out.OutletsChanged = !equalDeepSlice(a.Outlets, b.Outlets)
	out.ParamsChanged = !equalDeepSlice(a.Params, b.Params)
	out.GroupsChanged = !reflect.DeepEqual(a.Groups, b.Groups)

	aNodes := a.SortedNodeIDs()
	bNodes := b.SortedNodeIDs()

	for _, id := range bNodes {
		if _, ok := a.Nodes[id]; !ok {
			out.AddedNodes = append(out.AddedNodes, id)
		}
	}
	for _, id := range aNodes {
		if _, ok := b.Nodes[id]; !ok {
			out.RemovedNodes = append(out.RemovedNodes, id)
		}
	}
	for _, id := range bNodes {
		an, exists := a.Nodes[id]
		if !exists {
			continue
		}
		bn := b.Nodes[id]
		nc := diffNode(id, an, bn)
		if nc.RuntimeChanged() || nc.UIChanged {
			out.ModifiedNodes = append(out.ModifiedNodes, nc)
		}
	}

	out.AddedEdges, out.RemovedEdges = diffEdges(a.Edges, b.Edges)
	return out
}

func diffNode(id string, a, b NodeSpec) NodeChange {
	nc := NodeChange{
		ID:                  id,
		TypeChanged:         a.Type != b.Type,
		CapabilitiesChanged: !slices.Equal(a.Capabilities, b.Capabilities),
		UIChanged:           !equalNodeUI(a.UI, b.UI),
	}

	keys := make(map[string]struct{}, len(a.Params)+len(b.Params))
	for k := range a.Params {
		keys[k] = struct{}{}
	}
	for k := range b.Params {
		keys[k] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	slices.Sort(sortedKeys)

	for _, k := range sortedKeys {
		av, aok := a.Params[k]
		bv, bok := b.Params[k]
		if aok != bok || !reflect.DeepEqual(av, bv) {
			nc.ChangedParams = append(nc.ChangedParams, k)
		}
	}
	return nc
}

func equalNodeUI(a, b *NodeUI) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func equalDeepSlice[T any](a, b []T) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func diffEdges(before, after []EdgeSpec) (added, removed []EdgeSpec) {
	// Both slices are already sorted by CompareEdgeSpec after Canonicalize.
	i, j := 0, 0
	for i < len(before) && j < len(after) {
		c := CompareEdgeSpec(before[i], after[j])
		switch {
		case c == 0:
			i++
			j++
		case c < 0:
			removed = append(removed, before[i])
			i++
		default:
			added = append(added, after[j])
			j++
		}
	}
	for ; i < len(before); i++ {
		removed = append(removed, before[i])
	}
	for ; j < len(after); j++ {
		added = append(added, after[j])
	}
	return added, removed
}
