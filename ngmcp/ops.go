// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

// Sentinel errors returned by declarative MCP patch decoding and application.
var (
	ErrTooManyOps   = errors.New("ngmcp: edit batch exceeds maximum operation count")
	ErrUnknownPatch = errors.New("ngmcp: unknown edit operation")
	ErrNoLayoutHook = errors.New("ngmcp: auto_layout is not configured on this server")
)

// LayoutRequest carries optional auto-layout spacing and flow parameters.
type LayoutRequest struct {
	Direction string  `json:"direction,omitempty"`
	ColumnGap float64 `json:"column_gap,omitempty"`
	RowGap    float64 `json:"row_gap,omitempty"`
}

// AutoLayoutFunc computes MoveNode EditOps for spec using registry port metadata.
type AutoLayoutFunc func(spec *nodegraph.Spec, reg *nodegraph.Registry, req LayoutRequest) ([]nodegraph.EditOp, error)

// PatchOp is the declarative JSON representation of a single atomic Spec edit
// emitted by an LLM/MCP client. It contains zero expressions or control flow.
type PatchOp struct {
	Op               string             `json:"op"`
	ID               string             `json:"id,omitempty"`
	Node             string             `json:"node,omitempty"`
	Type             string             `json:"type,omitempty"`
	Key              string             `json:"key,omitempty"`
	Value            json.RawMessage    `json:"value,omitempty"`
	Params           map[string]any     `json:"params,omitempty"`
	Capabilities     []string           `json:"capabilities,omitempty"`
	From             string             `json:"from,omitempty"`
	To               string             `json:"to,omitempty"`
	Kind             string             `json:"kind,omitempty"`
	EdgeKind         string             `json:"edge_kind,omitempty"`
	Inlet            string             `json:"inlet,omitempty"`
	Outlet           string             `json:"outlet,omitempty"`
	X                *float64           `json:"x,omitempty"`
	Y                *float64           `json:"y,omitempty"`
	Label            *string            `json:"label,omitempty"`
	Collapsed        *bool              `json:"collapsed,omitempty"`
	Nodes            []string           `json:"nodes,omitempty"`
	UI               *nodegraph.NodeUI  `json:"ui,omitempty"`
	GroupUI          *nodegraph.GroupUI `json:"group_ui,omitempty"`
	Name             *string            `json:"name,omitempty"`
	Description      *string            `json:"description,omitempty"`
	ComponentVersion *int               `json:"component_version,omitempty"`
	Target           string             `json:"target,omitempty"`
	Targets          []string           `json:"targets,omitempty"`
	Required         *bool              `json:"required,omitempty"`
	Default          json.RawMessage    `json:"default,omitempty"`
	Min              *float64           `json:"min,omitempty"`
	Max              *float64           `json:"max,omitempty"`
	Enum             []string           `json:"enum,omitempty"`
	Direction        string             `json:"direction,omitempty"`
	ColumnGap        float64            `json:"column_gap,omitempty"`
	RowGap           float64            `json:"row_gap,omitempty"`
}

// DecodePatchOps strictly decodes a bounded JSON array of PatchOp values,
// rejecting unknown fields, trailing tokens, and oversized batches.
func DecodePatchOps(raw []byte, limits Limits) ([]PatchOp, error) {
	limits = limits.withDefaults()
	if len(raw) > limits.MaxMessageBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, len(raw), limits.MaxMessageBytes)
	}
	var ops []PatchOp
	if err := decodeStrictJSON(raw, &ops); err != nil {
		return nil, err
	}
	if len(ops) > limits.MaxOpsPerBatch {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyOps, len(ops), limits.MaxOpsPerBatch)
	}
	return ops, nil
}

func decodeStrictJSON(raw []byte, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON arguments: unexpected trailing data")
	}
	return nil
}

func convertJSONNumbers(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		f, err := x.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("invalid numeric literal %q", x.String())
		}
		return f, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			cv, err := convertJSONNumbers(val)
			if err != nil {
				return nil, err
			}
			out[k] = cv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			cv, err := convertJSONNumbers(val)
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	default:
		return v, nil
	}
}

// ApplyPatchOps applies a batch of declarative PatchOps transactionally to spec,
// returning a canonicalized copy while leaving the input spec untouched on error.
func ApplyPatchOps(spec *nodegraph.Spec, ops []PatchOp, reg *nodegraph.Registry, layoutFn AutoLayoutFunc) (*nodegraph.Spec, error) {
	return ApplyPatchOpsWithBackend(spec, ops, reg, layoutFn, nil)
}

// ApplyPatchOpsWithBackend uses the host compatibility policy for inferred wiring.
// Callers still validate the resulting document before committing it.
func ApplyPatchOpsWithBackend(spec *nodegraph.Spec, ops []PatchOp, reg *nodegraph.Registry, layoutFn AutoLayoutFunc, backend *nodegraph.BackendProfile) (*nodegraph.Spec, error) {
	if spec == nil {
		return nil, fmt.Errorf("%w: nil spec", nodegraph.ErrInvalidSpec)
	}
	working := spec.Clone()
	for i, p := range ops {
		next, err := applySinglePatch(working, p, reg, layoutFn, backend)
		if err != nil {
			return nil, fmt.Errorf("ops[%d] (%s): %w", i, p.Op, err)
		}
		working = next
	}
	return working.Canonicalize(), nil
}

func applySinglePatch(spec *nodegraph.Spec, p PatchOp, reg *nodegraph.Registry, layoutFn AutoLayoutFunc, backend *nodegraph.BackendProfile) (*nodegraph.Spec, error) {
	opName := strings.ToLower(strings.TrimSpace(p.Op))
	switch opName {
	case "add_node":
		id := firstNonEmpty(p.ID, p.Node)
		tid, err := nodegraph.ParseTypeID(p.Type)
		if err != nil {
			return nil, err
		}
		caps, err := parseCapabilityList(p.Capabilities)
		if err != nil {
			return nil, err
		}
		normParams, err := normalizeMapNumbers(p.Params)
		if err != nil {
			return nil, err
		}
		ui := p.UI
		if ui == nil && (p.X != nil || p.Y != nil || p.Label != nil || p.Collapsed != nil) {
			u := nodegraph.NodeUI{}
			if p.X != nil {
				u.X = *p.X
			}
			if p.Y != nil {
				u.Y = *p.Y
			}
			if p.Label != nil {
				u.Label = *p.Label
			}
			if p.Collapsed != nil {
				u.Collapsed = *p.Collapsed
			}
			ui = &u
		}
		return nodegraph.ApplyEdits(spec, nodegraph.AddNode(id, nodegraph.NodeSpec{
			Type:         tid,
			Params:       normParams,
			Capabilities: caps,
			UI:           ui,
		}))

	case "remove_node":
		id := firstNonEmpty(p.ID, p.Node)
		return nodegraph.ApplyEdits(spec, nodegraph.RemoveNode(id))

	case "set_param":
		nodeID := firstNonEmpty(p.Node, p.ID)
		if len(bytes.TrimSpace(p.Value)) == 0 {
			return nil, errors.New("set_param requires value")
		}
		var rawVal any
		dec := json.NewDecoder(bytes.NewReader(p.Value))
		dec.UseNumber()
		if err := dec.Decode(&rawVal); err != nil {
			return nil, fmt.Errorf("invalid value JSON: %w", err)
		}
		val, err := convertJSONNumbers(rawVal)
		if err != nil {
			return nil, err
		}
		return nodegraph.ApplyEdits(spec, nodegraph.SetParam(nodeID, p.Key, val))

	case "delete_param":
		nodeID := firstNonEmpty(p.Node, p.ID)
		return nodegraph.ApplyEdits(spec, nodegraph.DeleteParam(nodeID, p.Key))

	case "set_capabilities":
		nodeID := firstNonEmpty(p.Node, p.ID)
		caps, err := parseCapabilityList(p.Capabilities)
		if err != nil {
			return nil, err
		}
		return nodegraph.ApplyEdits(spec, nodegraph.SetCapabilities(nodeID, caps...))

	case "move_node":
		nodeID := firstNonEmpty(p.Node, p.ID)
		if p.X == nil || p.Y == nil {
			return nil, errors.New("move_node requires x and y coordinates")
		}
		return nodegraph.ApplyEdits(spec, nodegraph.MoveNode(nodeID, *p.X, *p.Y))

	case "set_node_label":
		nodeID := firstNonEmpty(p.Node, p.ID)
		lbl := ""
		if p.Label != nil {
			lbl = *p.Label
		}
		return nodegraph.ApplyEdits(spec, nodegraph.SetNodeLabel(nodeID, lbl))

	case "set_node_collapsed":
		nodeID := firstNonEmpty(p.Node, p.ID)
		if p.Collapsed == nil {
			return nil, errors.New("set_node_collapsed requires boolean collapsed")
		}
		return nodegraph.ApplyEdits(spec, nodegraph.SetNodeCollapsed(nodeID, *p.Collapsed))

	case "connect":
		fromRef, err := nodegraph.ParsePortRef(p.From)
		if err != nil {
			return nil, err
		}
		toRef, err := nodegraph.ParsePortRef(p.To)
		if err != nil {
			return nil, err
		}
		return nodegraph.ApplyEdits(spec, nodegraph.Connect(fromRef, toRef, firstNonEmpty(p.Kind, p.EdgeKind)))

	case "disconnect":
		fromRef, err := nodegraph.ParsePortRef(p.From)
		if err != nil {
			return nil, err
		}
		toRef, err := nodegraph.ParsePortRef(p.To)
		if err != nil {
			return nil, err
		}
		return nodegraph.ApplyEdits(spec, nodegraph.Disconnect(fromRef, toRef, firstNonEmpty(p.Kind, p.EdgeKind)))

	case "splice_node":
		return applySpliceNode(spec, p, reg, backend)

	case "put_group":
		groupID := p.ID
		lbl := ""
		if p.Label != nil {
			lbl = *p.Label
		}
		collapsed := false
		if p.Collapsed != nil {
			collapsed = *p.Collapsed
		}
		return nodegraph.ApplyEdits(spec, nodegraph.PutGroup(groupID, nodegraph.GroupSpec{
			Label:     lbl,
			Nodes:     p.Nodes,
			Collapsed: collapsed,
			UI:        p.GroupUI,
		}))

	case "remove_group":
		return nodegraph.ApplyEdits(spec, nodegraph.RemoveGroup(p.ID))

	case "set_metadata":
		return nodegraph.ApplyEdits(spec, nodegraph.EditFunc(func(s *nodegraph.Spec) error {
			if p.Name != nil {
				if !nodegraph.IsValidID(*p.Name) {
					return fmt.Errorf("%w: %q", nodegraph.ErrInvalidID, *p.Name)
				}
				s.Name = *p.Name
			}
			if p.Description != nil {
				s.Description = *p.Description
			}
			if p.ComponentVersion != nil {
				if *p.ComponentVersion < 0 {
					return fmt.Errorf("%w: component_version %d must be >= 0", nodegraph.ErrInvalidSpec, *p.ComponentVersion)
				}
				s.ComponentVersion = *p.ComponentVersion
			}
			return nil
		}))

	case "set_component_version":
		if p.ComponentVersion == nil {
			return nil, errors.New("set_component_version requires component_version")
		}
		return nodegraph.ApplyEdits(spec, nodegraph.SetComponentVersion(*p.ComponentVersion))

	case "expose_inlet":
		return applyExposeInlet(spec, p)

	case "remove_inlet":
		name := patchBoundaryName(p, p.Inlet)
		return nodegraph.ApplyEdits(spec, nodegraph.RemoveInlet(name))

	case "expose_outlet":
		return applyExposeOutlet(spec, p)

	case "remove_outlet":
		name := patchBoundaryName(p, p.Outlet)
		return nodegraph.ApplyEdits(spec, nodegraph.RemoveOutlet(name))

	case "expose_param":
		return applyExposeParam(spec, p)

	case "remove_param":
		name := patchBoundaryName(p, p.Key)
		return nodegraph.ApplyEdits(spec, nodegraph.RemoveParam(name))

	case "auto_layout":
		if layoutFn == nil {
			return nil, ErrNoLayoutHook
		}
		editOps, err := layoutFn(spec, reg, LayoutRequest{
			Direction: p.Direction,
			ColumnGap: p.ColumnGap,
			RowGap:    p.RowGap,
		})
		if err != nil {
			return nil, err
		}
		return nodegraph.ApplyEdits(spec, editOps...)

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownPatch, p.Op)
	}
}

func applySpliceNode(spec *nodegraph.Spec, p PatchOp, reg *nodegraph.Registry, backend *nodegraph.BackendProfile) (*nodegraph.Spec, error) {
	id := firstNonEmpty(p.ID, p.Node)
	tid, err := nodegraph.ParseTypeID(p.Type)
	if err != nil {
		return nil, err
	}
	fromRef, err := nodegraph.ParsePortRef(p.From)
	if err != nil {
		return nil, err
	}
	toRef, err := nodegraph.ParsePortRef(p.To)
	if err != nil {
		return nil, err
	}

	// Preserve existing edge delivery kind if omitted.
	edgeKind := firstNonEmpty(p.EdgeKind, p.Kind)
	if edgeKind == "" {
		for _, e := range spec.Edges {
			if e.From == fromRef && e.To == toRef {
				edgeKind = e.Kind
				break
			}
		}
	}

	inletName := strings.TrimSpace(p.Inlet)
	outletName := strings.TrimSpace(p.Outlet)
	if (inletName == "" || outletName == "") && reg != nil {
		if def, ok := reg.Lookup(tid); ok {
			inletName, outletName = inferSplicePorts(spec, reg, def, fromRef, toRef, inletName, outletName, backend)
		}
	}
	if inletName == "" || outletName == "" {
		return nil, errors.New("splice_node requires explicit inlet and outlet port names when they cannot be inferred from the registry")
	}

	caps, err := parseCapabilityList(p.Capabilities)
	if err != nil {
		return nil, err
	}
	normParams, err := normalizeMapNumbers(p.Params)
	if err != nil {
		return nil, err
	}

	ui := p.UI
	if ui == nil {
		if fromNode, okF := spec.Nodes[fromRef.Node]; okF {
			if toNode, okT := spec.Nodes[toRef.Node]; okT && fromNode.UI != nil && toNode.UI != nil {
				ui = &nodegraph.NodeUI{
					X: (fromNode.UI.X + toNode.UI.X) * 0.5,
					Y: (fromNode.UI.Y + toNode.UI.Y) * 0.5,
				}
			}
		}
	}

	return nodegraph.ApplyEdits(
		spec,
		nodegraph.Disconnect(fromRef, toRef, ""),
		nodegraph.AddNode(id, nodegraph.NodeSpec{
			Type:         tid,
			Params:       normParams,
			Capabilities: caps,
			UI:           ui,
		}),
		nodegraph.Connect(fromRef, nodegraph.PortRef{Node: id, Port: inletName}, edgeKind),
		nodegraph.Connect(nodegraph.PortRef{Node: id, Port: outletName}, toRef, edgeKind),
	)
}

func inferSplicePorts(
	spec *nodegraph.Spec,
	reg *nodegraph.Registry,
	spliceDef nodegraph.TypeDef,
	fromRef, toRef nodegraph.PortRef,
	inletName, outletName string,
	backend *nodegraph.BackendProfile,
) (string, string) {
	var srcOutType *nodegraph.DataType
	if srcNode, ok := spec.Nodes[fromRef.Node]; ok {
		if srcDef, okDef := reg.Lookup(srcNode.Type); okDef {
			if outDef, okOut := srcDef.Outlet(fromRef.Port); okOut {
				t := outDef.Type
				srcOutType = &t
			}
		}
	}
	var dstInType *nodegraph.DataType
	if dstNode, ok := spec.Nodes[toRef.Node]; ok {
		if dstDef, okDef := reg.Lookup(dstNode.Type); okDef {
			if inDef, okIn := dstDef.Inlet(toRef.Port); okIn {
				t := inDef.Type
				dstInType = &t
			}
		}
	}

	if inletName == "" {
		for _, in := range spliceDef.Inlets {
			if srcOutType == nil {
				inletName = in.Name
				break
			}
			compat, _, _ := backend.CheckCompatibility(*srcOutType, in.Type)
			if compat != nodegraph.CompatIncompatible {
				inletName = in.Name
				break
			}
		}
	}
	if outletName == "" {
		for _, out := range spliceDef.Outlets {
			if dstInType == nil {
				outletName = out.Name
				break
			}
			compat, _, _ := backend.CheckCompatibility(out.Type, *dstInType)
			if compat != nodegraph.CompatIncompatible {
				outletName = out.Name
				break
			}
		}
	}
	return inletName, outletName
}

func parseCapabilityList(raw []string) ([]nodegraph.Capability, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]nodegraph.Capability, 0, len(raw))
	for _, s := range raw {
		c, err := nodegraph.ParseCapability(s)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func normalizeMapNumbers(m map[string]any) (map[string]any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	cv, err := convertJSONNumbers(m)
	if err != nil {
		return nil, err
	}
	res, _ := cv.(map[string]any)
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

func patchBoundaryName(p PatchOp, fallback string) string {
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		return strings.TrimSpace(*p.Name)
	}
	if strings.TrimSpace(p.ID) != "" {
		return strings.TrimSpace(p.ID)
	}
	return strings.TrimSpace(fallback)
}

func parsePortTargets(single string, multi []string) (nodegraph.PortRef, []nodegraph.PortRef, error) {
	hasSingle := strings.TrimSpace(single) != ""
	hasMulti := len(multi) > 0
	if hasSingle == hasMulti {
		return nodegraph.PortRef{}, nil, errors.New("specify exactly one of target or targets")
	}
	if hasSingle {
		ref, err := nodegraph.ParsePortRef(strings.TrimSpace(single))
		if err != nil {
			return nodegraph.PortRef{}, nil, err
		}
		return ref, nil, nil
	}
	refs := make([]nodegraph.PortRef, 0, len(multi))
	for _, raw := range multi {
		ref, err := nodegraph.ParsePortRef(strings.TrimSpace(raw))
		if err != nil {
			return nodegraph.PortRef{}, nil, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 1 {
		return refs[0], nil, nil
	}
	return nodegraph.PortRef{}, refs, nil
}

func applyExposeInlet(spec *nodegraph.Spec, p PatchOp) (*nodegraph.Spec, error) {
	name := patchBoundaryName(p, p.Inlet)
	dt, err := nodegraph.ParseDataType(strings.TrimSpace(p.Type))
	if err != nil {
		return nil, err
	}
	target, targets, err := parsePortTargets(p.Target, p.Targets)
	if err != nil {
		return nil, err
	}
	desc := ""
	if p.Description != nil {
		desc = *p.Description
	}
	req := false
	if p.Required != nil {
		req = *p.Required
	}
	return nodegraph.ApplyEdits(spec, nodegraph.ExposeInlet(nodegraph.ExposedPortSpec{
		Name:        name,
		Type:        dt,
		Target:      target,
		Targets:     targets,
		Required:    req,
		Description: desc,
	}))
}

func applyExposeOutlet(spec *nodegraph.Spec, p PatchOp) (*nodegraph.Spec, error) {
	name := patchBoundaryName(p, p.Outlet)
	dt, err := nodegraph.ParseDataType(strings.TrimSpace(p.Type))
	if err != nil {
		return nil, err
	}
	if len(p.Targets) > 0 {
		return nil, errors.New("expose_outlet supports single target only")
	}
	target, err := nodegraph.ParsePortRef(strings.TrimSpace(p.Target))
	if err != nil {
		return nil, err
	}
	desc := ""
	if p.Description != nil {
		desc = *p.Description
	}
	return nodegraph.ApplyEdits(spec, nodegraph.ExposeOutlet(nodegraph.ExposedPortSpec{
		Name:        name,
		Type:        dt,
		Target:      target,
		Description: desc,
	}))
}

func applyExposeParam(spec *nodegraph.Spec, p PatchOp) (*nodegraph.Spec, error) {
	name := patchBoundaryName(p, p.Key)
	pt := nodegraph.ParamType(strings.TrimSpace(p.Type))
	if !pt.Valid() {
		return nil, fmt.Errorf("%w: invalid parameter type %q", nodegraph.ErrInvalidParam, p.Type)
	}
	target, targets, err := parsePortTargets(p.Target, p.Targets)
	if err != nil {
		return nil, err
	}
	var defVal any
	if len(bytes.TrimSpace(p.Default)) > 0 {
		var rawDef any
		dec := json.NewDecoder(bytes.NewReader(p.Default))
		dec.UseNumber()
		if err := dec.Decode(&rawDef); err != nil {
			return nil, fmt.Errorf("invalid default JSON: %w", err)
		}
		defVal, err = convertJSONNumbers(rawDef)
		if err != nil {
			return nil, err
		}
	}
	desc := ""
	if p.Description != nil {
		desc = *p.Description
	}
	req := false
	if p.Required != nil {
		req = *p.Required
	}
	return nodegraph.ApplyEdits(spec, nodegraph.ExposeParam(nodegraph.ExposedParamSpec{
		Name:        name,
		Type:        pt,
		Target:      target,
		Targets:     targets,
		Default:     defVal,
		Required:    req,
		Min:         p.Min,
		Max:         p.Max,
		Enum:        append([]string(nil), p.Enum...),
		Description: desc,
	}))
}
