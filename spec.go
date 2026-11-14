// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// SpecVersion is the current schema version for ntnodegraph documents.
const SpecVersion = 1

// Resource bounds enforced when parsing untrusted specs.
const (
	DefaultMaxSpecBytes = 1 << 20 // 1 MiB
	DefaultMaxNodes     = 4096
	DefaultMaxEdges     = 16384
	DefaultMaxGroups    = 512
	DefaultMaxDepth     = 32
)

// Sentinel errors for spec parsing and structural limits.
var (
	ErrSpecTooLarge       = errors.New("ntnodegraph: spec exceeds byte limit")
	ErrUnsupportedVersion = errors.New("ntnodegraph: unsupported spec version")
	ErrInvalidSpec        = errors.New("ntnodegraph: invalid spec")
)

// ParseLimits bounds input size and graph cardinality during parsing.
// Zero fields fall back to DefaultParseLimits.
type ParseLimits struct {
	MaxBytes  int
	MaxNodes  int
	MaxEdges  int
	MaxGroups int
	MaxDepth  int
}

// DefaultParseLimits are the default resource ceilings applied by ParseYAML and ParseJSON.
var DefaultParseLimits = ParseLimits{
	MaxBytes:  DefaultMaxSpecBytes,
	MaxNodes:  DefaultMaxNodes,
	MaxEdges:  DefaultMaxEdges,
	MaxGroups: DefaultMaxGroups,
	MaxDepth:  DefaultMaxDepth,
}

func (l ParseLimits) withDefaults() ParseLimits {
	out := l
	if out.MaxBytes <= 0 {
		out.MaxBytes = DefaultParseLimits.MaxBytes
	}
	if out.MaxNodes <= 0 {
		out.MaxNodes = DefaultParseLimits.MaxNodes
	}
	if out.MaxEdges <= 0 {
		out.MaxEdges = DefaultParseLimits.MaxEdges
	}
	if out.MaxGroups <= 0 {
		out.MaxGroups = DefaultParseLimits.MaxGroups
	}
	if out.MaxDepth <= 0 {
		out.MaxDepth = DefaultParseLimits.MaxDepth
	}
	return out
}

// Spec is the top-level declarative node-graph document.
//
// Inlets, Outlets, and Params expose boundary ports and 1:1 forwarded static
// parameters for flatten-at-load subgraphs (namespace "graph/"). Groups hold
// editor-only layout metadata and are ignored by execution backends.
type Spec struct {
	Version          int                  `json:"version"`
	Name             string               `json:"name"`
	ComponentVersion int                  `json:"component_version,omitempty"`
	Description      string               `json:"description,omitempty"`
	Inlets           []ExposedPortSpec    `json:"inlets,omitempty"`
	Outlets          []ExposedPortSpec    `json:"outlets,omitempty"`
	Params           []ExposedParamSpec   `json:"params,omitempty"`
	Groups           map[string]GroupSpec `json:"groups,omitempty"`
	Nodes            map[string]NodeSpec  `json:"nodes"`
	Edges            []EdgeSpec           `json:"edges"`
}

// EffectiveComponentVersion returns ComponentVersion when >= 1, defaulting to 1.
func (s *Spec) EffectiveComponentVersion() int {
	if s != nil && s.ComponentVersion >= 1 {
		return s.ComponentVersion
	}
	return 1
}

// ComponentTypeID derives the canonical "graph/<lower-name>@<major>" TypeID for
// s when registered in a component Library.
func (s *Spec) ComponentTypeID() (TypeID, error) {
	if s == nil {
		return TypeID{}, fmt.Errorf("%w: nil spec", ErrInvalidSpec)
	}
	if s.ComponentVersion < 0 {
		return TypeID{}, fmt.Errorf("%w: negative component_version %d", ErrInvalidSpec, s.ComponentVersion)
	}
	tid := TypeID{
		Namespace: NamespaceGraph,
		Name:      strings.ToLower(s.Name),
		Major:     s.EffectiveComponentVersion(),
	}
	if !tid.Valid() {
		return TypeID{}, fmt.Errorf("%w: spec name %q cannot form a valid %s/* TypeID", ErrInvalidTypeID, s.Name, NamespaceGraph)
	}
	return tid, nil
}

// ExposedPortSpec declares a subgraph boundary port mapped to one internal node
// port (Target) or, for inlets, fanned out to multiple internal node ports
// (Targets).
type ExposedPortSpec struct {
	Name        string    `json:"name"`
	Type        DataType  `json:"type"`
	Target      PortRef   `json:"target,omitzero"`
	Targets     []PortRef `json:"targets,omitempty"`
	Required    bool      `json:"required,omitempty"`
	Description string    `json:"description,omitempty"`
}

// ResolvedTargets returns the target PortRefs for p.
func (p ExposedPortSpec) ResolvedTargets() []PortRef {
	if len(p.Targets) > 0 {
		if !p.Target.IsZero() && !slices.Contains(p.Targets, p.Target) {
			out := make([]PortRef, 0, 1+len(p.Targets))
			out = append(out, p.Target)
			out = append(out, p.Targets...)
			return out
		}
		return slices.Clone(p.Targets)
	}
	if !p.Target.IsZero() {
		return []PortRef{p.Target}
	}
	return nil
}

// ExposedParamSpec declares a subgraph static parameter forwarded 1:1 onto one
// or more internal "<node>.<param>" targets without expressions or interpolation.
type ExposedParamSpec struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Target      PortRef   `json:"target,omitzero"`
	Targets     []PortRef `json:"targets,omitempty"`
	Required    bool      `json:"required,omitempty"`
	Default     any       `json:"default,omitempty"`
	Min         *float64  `json:"min,omitempty"`
	Max         *float64  `json:"max,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Description string    `json:"description,omitempty"`
}

// ResolvedTargets returns the target "<node>.<param>" references for p.
func (p ExposedParamSpec) ResolvedTargets() []PortRef {
	if len(p.Targets) > 0 {
		if !p.Target.IsZero() && !slices.Contains(p.Targets, p.Target) {
			out := make([]PortRef, 0, 1+len(p.Targets))
			out = append(out, p.Target)
			out = append(out, p.Targets...)
			return out
		}
		return slices.Clone(p.Targets)
	}
	if !p.Target.IsZero() {
		return []PortRef{p.Target}
	}
	return nil
}

// GroupSpec holds editor-only visual grouping metadata. Runtimes ignore groups.
type GroupSpec struct {
	Label     string   `json:"label,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
	Collapsed bool     `json:"collapsed,omitempty"`
	UI        *GroupUI `json:"ui,omitempty"`
}

// GroupUI stores optional bounding-box geometry for an editor group.
type GroupUI struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
}

// NodeSpec describes one node instance in a Spec.
type NodeSpec struct {
	Type         TypeID         `json:"type"`
	Capabilities []Capability   `json:"capabilities,omitempty"`
	Params       map[string]any `json:"params,omitempty"`
	UI           *NodeUI        `json:"ui,omitempty"`
}

// NodeUI holds editor-only layout and presentation metadata for a node.
// Keeping all visual properties inside UI allows hot-reload runtimes to ignore
// pure layout edits without restarting running nodes.
type NodeUI struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Label     string  `json:"label,omitempty"`
	Collapsed bool    `json:"collapsed,omitempty"`
}

// EdgeSpec connects an outlet (From) to an inlet (To), with an optional
// backend-interpreted delivery Kind (for example "signal", "frame", "event").
type EdgeSpec struct {
	From PortRef `json:"from"`
	To   PortRef `json:"to"`
	Kind string  `json:"kind,omitempty"`
}

// CompareEdgeSpec orders edges deterministically by From, To, then Kind.
func CompareEdgeSpec(a, b EdgeSpec) int {
	if c := ComparePortRef(a.From, b.From); c != 0 {
		return c
	}
	if c := ComparePortRef(a.To, b.To); c != 0 {
		return c
	}
	return cmp.Compare(a.Kind, b.Kind)
}

// SortedNodeIDs returns the node IDs of s in deterministic lexicographical order.
func (s *Spec) SortedNodeIDs() []string {
	if s == nil || len(s.Nodes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.Nodes))
	for id := range s.Nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// SortedGroupIDs returns the group IDs of s in deterministic lexicographical order.
func (s *Spec) SortedGroupIDs() []string {
	if s == nil || len(s.Groups) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.Groups))
	for id := range s.Groups {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Clone returns an independent deep copy of s.
func (s *Spec) Clone() *Spec {
	if s == nil {
		return nil
	}
	out := &Spec{
		Version:          s.Version,
		Name:             s.Name,
		ComponentVersion: s.ComponentVersion,
		Description:      s.Description,
		Nodes:            make(map[string]NodeSpec, len(s.Nodes)),
		Edges:            append([]EdgeSpec(nil), s.Edges...),
	}
	if len(s.Inlets) > 0 {
		out.Inlets = make([]ExposedPortSpec, len(s.Inlets))
		for i, p := range s.Inlets {
			out.Inlets[i] = cloneExposedPort(p)
		}
	}
	if len(s.Outlets) > 0 {
		out.Outlets = make([]ExposedPortSpec, len(s.Outlets))
		for i, p := range s.Outlets {
			out.Outlets[i] = cloneExposedPort(p)
		}
	}
	if len(s.Params) > 0 {
		out.Params = make([]ExposedParamSpec, len(s.Params))
		for i, p := range s.Params {
			out.Params[i] = cloneExposedParam(p)
		}
	}
	if len(s.Groups) > 0 {
		out.Groups = make(map[string]GroupSpec, len(s.Groups))
		for id, g := range s.Groups {
			gc := GroupSpec{
				Label:     g.Label,
				Collapsed: g.Collapsed,
			}
			if len(g.Nodes) > 0 {
				gc.Nodes = append([]string(nil), g.Nodes...)
			}
			if g.UI != nil {
				uiCopy := *g.UI
				gc.UI = &uiCopy
			}
			out.Groups[id] = gc
		}
	}
	for id, n := range s.Nodes {
		out.Nodes[id] = cloneNodeSpec(n)
	}
	return out
}

func cloneExposedPort(p ExposedPortSpec) ExposedPortSpec {
	out := p
	if len(p.Targets) > 0 {
		out.Targets = append([]PortRef(nil), p.Targets...)
	} else {
		out.Targets = nil
	}
	return out
}

func cloneExposedParam(p ExposedParamSpec) ExposedParamSpec {
	out := p
	if len(p.Targets) > 0 {
		out.Targets = append([]PortRef(nil), p.Targets...)
	} else {
		out.Targets = nil
	}
	out.Default = cloneParamValue(p.Default)
	if p.Min != nil {
		v := *p.Min
		out.Min = &v
	}
	if p.Max != nil {
		v := *p.Max
		out.Max = &v
	}
	if len(p.Enum) > 0 {
		out.Enum = append([]string(nil), p.Enum...)
	} else {
		out.Enum = nil
	}
	return out
}

func canonicalizeTargets(single PortRef, multi []PortRef) (PortRef, []PortRef) {
	var all []PortRef
	if !single.IsZero() {
		all = append(all, single)
	}
	all = append(all, multi...)
	if len(all) == 0 {
		return PortRef{}, nil
	}
	slices.SortFunc(all, ComparePortRef)
	all = slices.Compact(all)
	if len(all) == 1 {
		return all[0], nil
	}
	return PortRef{}, all
}

func cloneNodeSpec(n NodeSpec) NodeSpec {
	out := NodeSpec{
		Type: n.Type,
	}
	if len(n.Capabilities) > 0 {
		out.Capabilities = append([]Capability(nil), n.Capabilities...)
	}
	if len(n.Params) > 0 {
		out.Params = cloneParamsMap(n.Params)
	}
	if n.UI != nil {
		uiCopy := *n.UI
		out.UI = &uiCopy
	}
	return out
}

func cloneParamsMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneParamValue(v)
	}
	return out
}

func cloneParamValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneParamsMap(x)
	case []any:
		cp := make([]any, len(x))
		for i, elem := range x {
			cp[i] = cloneParamValue(elem)
		}
		return cp
	case []string:
		cp := make([]any, len(x))
		for i, elem := range x {
			cp[i] = elem
		}
		return cp
	default:
		return x
	}
}

// Canonicalize returns a deep-copied Spec with capabilities, group memberships,
// exposed ports, edges, and numeric parameter representations normalized into
// canonical diff-stable form.
func (s *Spec) Canonicalize() *Spec {
	if s == nil {
		return nil
	}
	cp := s.Clone()
	if cp.Nodes == nil {
		cp.Nodes = map[string]NodeSpec{}
	}
	if cp.Edges == nil {
		cp.Edges = []EdgeSpec{}
	}
	if len(cp.Groups) == 0 {
		cp.Groups = nil
	}
	for id, g := range cp.Groups {
		if len(g.Nodes) > 0 {
			slices.Sort(g.Nodes)
			g.Nodes = slices.Compact(g.Nodes)
		} else {
			g.Nodes = nil
		}
		cp.Groups[id] = g
	}
	for id, n := range cp.Nodes {
		if len(n.Capabilities) > 0 {
			slices.SortFunc(n.Capabilities, CompareCapability)
			n.Capabilities = slices.Compact(n.Capabilities)
		} else {
			n.Capabilities = nil
		}
		if len(n.Params) == 0 {
			n.Params = nil
		} else if norm, err := normalizeNodeParams(n.Params, DefaultMaxDepth); err == nil {
			n.Params = norm
		}
		cp.Nodes[id] = n
	}
	for i := range cp.Inlets {
		cp.Inlets[i].Target, cp.Inlets[i].Targets = canonicalizeTargets(cp.Inlets[i].Target, cp.Inlets[i].Targets)
	}
	for i := range cp.Outlets {
		cp.Outlets[i].Target, cp.Outlets[i].Targets = canonicalizeTargets(cp.Outlets[i].Target, cp.Outlets[i].Targets)
		cp.Outlets[i].Required = false
	}
	for i := range cp.Params {
		cp.Params[i].Target, cp.Params[i].Targets = canonicalizeTargets(cp.Params[i].Target, cp.Params[i].Targets)
		if cp.Params[i].Default != nil {
			if nv, err := normalizeParamValue(cp.Params[i].Default, 0, DefaultMaxDepth); err == nil {
				cp.Params[i].Default = nv
			}
		}
		if len(cp.Params[i].Enum) == 0 {
			cp.Params[i].Enum = nil
		}
	}
	slices.SortFunc(cp.Inlets, func(a, b ExposedPortSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	slices.SortFunc(cp.Outlets, func(a, b ExposedPortSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	slices.SortFunc(cp.Params, func(a, b ExposedParamSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	slices.SortFunc(cp.Edges, CompareEdgeSpec)
	return cp
}

// normalizeNodeParams validates top-level node parameter keys and normalizes
// values into uniform Go types (bool, int64, float64, string, []any,
// map[string]any, nil), rejecting NaN/Inf, out-of-range integers, and
// container nesting beyond maxDepth.
func normalizeNodeParams(in map[string]any, maxDepth int) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if !IsValidID(k) {
			return nil, fmt.Errorf("%w: invalid parameter key %q", ErrInvalidSpec, k)
		}
		nv, err := normalizeParamValue(v, 0, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		out[k] = nv
	}
	return out, nil
}

// normalizeParamValue normalizes a parameter value, counting one depth level
// per array or object container entered inside the parameter value.
func normalizeParamValue(v any, depth, maxDepth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: parameter nesting depth %d exceeds max %d", ErrInvalidSpec, depth, maxDepth)
	}
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case int:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int64:
		return x, nil
	case uint:
		if uint64(x) > math.MaxInt64 {
			return nil, fmt.Errorf("%w: integer %d overflows int64", ErrInvalidSpec, x)
		}
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint64:
		if x > math.MaxInt64 {
			return nil, fmt.Errorf("%w: integer %d overflows int64", ErrInvalidSpec, x)
		}
		return int64(x), nil
	case float32:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%w: non-finite float parameter", ErrInvalidSpec)
		}
		return normalizeFloat(f), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%w: non-finite float parameter", ErrInvalidSpec)
		}
		return normalizeFloat(x), nil
	case []any:
		nextDepth := depth + 1
		if nextDepth > maxDepth {
			return nil, fmt.Errorf("%w: parameter nesting depth %d exceeds max %d", ErrInvalidSpec, nextDepth, maxDepth)
		}
		out := make([]any, len(x))
		for i, elem := range x {
			ne, err := normalizeParamValue(elem, nextDepth, maxDepth)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	case []string:
		nextDepth := depth + 1
		if nextDepth > maxDepth {
			return nil, fmt.Errorf("%w: parameter nesting depth %d exceeds max %d", ErrInvalidSpec, nextDepth, maxDepth)
		}
		out := make([]any, len(x))
		for i, elem := range x {
			out[i] = elem
		}
		return out, nil
	case map[string]any:
		nextDepth := depth + 1
		if nextDepth > maxDepth {
			return nil, fmt.Errorf("%w: parameter nesting depth %d exceeds max %d", ErrInvalidSpec, nextDepth, maxDepth)
		}
		if len(x) == 0 {
			return map[string]any{}, nil
		}
		out := make(map[string]any, len(x))
		for k, elem := range x {
			if k == "" || len(k) > MaxIDLen {
				return nil, fmt.Errorf("%w: invalid nested parameter key %q", ErrInvalidSpec, k)
			}
			ne, err := normalizeParamValue(elem, nextDepth, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", k, err)
			}
			out[k] = ne
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: unsupported parameter value type %T", ErrInvalidSpec, v)
	}
}

func normalizeFloat(f float64) any {
	if f == 0 {
		return int64(0)
	}
	if f >= float64(math.MinInt64) && f < float64(math.MaxInt64) {
		i := int64(f)
		if float64(i) == f {
			return i
		}
	}
	return f
}

func checkFiniteUI(x, y float64) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return fmt.Errorf("%w: non-finite UI coordinates", ErrInvalidSpec)
	}
	return nil
}
