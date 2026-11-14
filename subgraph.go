// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// DefaultMaxSubgraphDepth bounds nested graph/* expansion depth.
const DefaultMaxSubgraphDepth = 8

// Sentinel errors for composable graph/* subgraphs.
var (
	ErrSubgraphCycle         = errors.New("ntnodegraph: subgraph component cycle")
	ErrSubgraphDepthExceeded = errors.New("ntnodegraph: subgraph nesting depth exceeded")
)

// ComponentEntry stores a canonical subgraph Spec alongside its registration
// TypeID and optional origin label (e.g. "stdlib", file path, or "mcp").
type ComponentEntry struct {
	ID     TypeID `json:"id"`
	Origin string `json:"origin,omitempty"`
	Spec   *Spec  `json:"spec"`
}

// Library is a concurrency-safe collection of reusable graph/* subgraph Specs.
type Library struct {
	mu      sync.RWMutex
	entries map[TypeID]ComponentEntry
}

// NewLibrary creates an empty component Library.
func NewLibrary() *Library {
	return &Library{
		entries: make(map[TypeID]ComponentEntry),
	}
}

// Put canonicalizes spec and stores it in l under spec.ComponentTypeID().
func (l *Library) Put(spec *Spec, origin string) (TypeID, error) {
	if l == nil {
		return TypeID{}, fmt.Errorf("%w: nil library", ErrInvalidSpec)
	}
	if spec == nil {
		return TypeID{}, fmt.Errorf("%w: nil component spec", ErrInvalidSpec)
	}
	canon := spec.Canonicalize()
	if err := preflightEncode(canon); err != nil {
		return TypeID{}, err
	}
	tid, err := canon.ComponentTypeID()
	if err != nil {
		return TypeID{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[tid] = ComponentEntry{
		ID:     tid,
		Origin: origin,
		Spec:   canon,
	}
	return tid, nil
}

// Get returns a deep-copied ComponentEntry for id.
func (l *Library) Get(id TypeID) (ComponentEntry, bool) {
	if l == nil {
		return ComponentEntry{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	entry, ok := l.entries[id]
	if !ok {
		return ComponentEntry{}, false
	}
	return ComponentEntry{
		ID:     entry.ID,
		Origin: entry.Origin,
		Spec:   entry.Spec.Clone(),
	}, true
}

// LookupSpec returns a deep copy of the component Spec registered under id.
func (l *Library) LookupSpec(id TypeID) (*Spec, bool) {
	entry, ok := l.Get(id)
	if !ok {
		return nil, false
	}
	return entry.Spec, true
}

// Delete removes id from l, returning true if it was present.
func (l *Library) Delete(id TypeID) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.entries[id]; !ok {
		return false
	}
	delete(l.entries, id)
	return true
}

// List returns all registered ComponentEntries in deterministic TypeID order.
func (l *Library) List() []ComponentEntry {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]ComponentEntry, 0, len(l.entries))
	for _, entry := range l.entries {
		out = append(out, ComponentEntry{
			ID:     entry.ID,
			Origin: entry.Origin,
			Spec:   entry.Spec.Clone(),
		})
	}
	slices.SortFunc(out, func(a, b ComponentEntry) int {
		return CompareTypeID(a.ID, b.ID)
	})
	return out
}

// Clone returns an independent deep copy of l.
func (l *Library) Clone() *Library {
	out := NewLibrary()
	if l == nil {
		return out
	}
	for _, entry := range l.List() {
		out.entries[entry.ID] = entry
	}
	return out
}

// SyncRegistry validates all components in l in dependency order and upserts
// their synthesized TypeDefs into reg. Stale graph/* TypeDefs in reg that are
// no longer in l are removed.
func (l *Library) SyncRegistry(reg *Registry, opts ValidateOptions) ([]TypeDef, error) {
	if reg == nil {
		return nil, fmt.Errorf("%w: nil registry", ErrInvalidSpec)
	}
	entries := l.List()
	entryByID := make(map[TypeID]ComponentEntry, len(entries))
	for _, e := range entries {
		entryByID[e.ID] = e
	}

	// Order components from leaf subgraphs to enclosing subgraphs, rejecting cycles.
	ordered, err := orderLibraryComponents(entries, entryByID)
	if err != nil {
		return nil, err
	}

	// Stage into a clone first so SyncRegistry is atomic on error.
	staged := reg.Clone()
	for _, td := range staged.Types() {
		if td.ID.Namespace == NamespaceGraph {
			if _, keep := entryByID[td.ID]; !keep {
				staged.Delete(td.ID)
			}
		}
	}

	valOpts := withGraphNamespaceAllowed(opts)
	defs := make([]TypeDef, 0, len(ordered))
	for _, id := range ordered {
		comp := entryByID[id].Spec
		def, err := SynthesizeTypeDef(comp, staged, valOpts)
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", id, err)
		}
		if err := staged.Upsert(def); err != nil {
			return nil, fmt.Errorf("component %s: %w", id, err)
		}
		defs = append(defs, def)
	}

	// Commit staged changes into reg.
	for _, td := range reg.Types() {
		if td.ID.Namespace == NamespaceGraph {
			if _, keep := entryByID[td.ID]; !keep {
				reg.Delete(td.ID)
			}
		}
	}
	for _, def := range defs {
		if err := reg.Upsert(def); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(defs, func(a, b TypeDef) int {
		return CompareTypeID(a.ID, b.ID)
	})
	return defs, nil
}

// BuildRegistry returns a new Registry containing base's concrete types plus
// synthesized TypeDefs for every component in l.
func (l *Library) BuildRegistry(base *Registry, opts ValidateOptions) (*Registry, error) {
	cloned := base.Clone()
	if l == nil {
		return cloned, nil
	}
	if _, err := l.SyncRegistry(cloned, opts); err != nil {
		return nil, err
	}
	return cloned, nil
}

// ComponentSummary is the deterministic JSON metadata entry emitted by
// Library.LibraryJSON for catalog://ntnodegraph/library.
type ComponentSummary struct {
	ID               TypeID             `json:"id"`
	Origin           string             `json:"origin,omitempty"`
	Name             string             `json:"name"`
	ComponentVersion int                `json:"component_version"`
	Description      string             `json:"description,omitempty"`
	NodeCount        int                `json:"node_count"`
	EdgeCount        int                `json:"edge_count"`
	Inlets           []ExposedPortSpec  `json:"inlets,omitempty"`
	Outlets          []ExposedPortSpec  `json:"outlets,omitempty"`
	Params           []ExposedParamSpec `json:"params,omitempty"`
	TypeDef          *TypeDef           `json:"type_def,omitempty"`
}

// LibraryJSON exports a deterministic JSON catalog of all components in l.
func (l *Library) LibraryJSON(reg *Registry) ([]byte, error) {
	entries := l.List()
	summaries := make([]ComponentSummary, 0, len(entries))
	for _, entry := range entries {
		cs := ComponentSummary{
			ID:               entry.ID,
			Origin:           entry.Origin,
			Name:             entry.Spec.Name,
			ComponentVersion: entry.Spec.EffectiveComponentVersion(),
			Description:      entry.Spec.Description,
			NodeCount:        len(entry.Spec.Nodes),
			EdgeCount:        len(entry.Spec.Edges),
			Inlets:           entry.Spec.Inlets,
			Outlets:          entry.Spec.Outlets,
			Params:           entry.Spec.Params,
		}
		if reg != nil {
			if td, ok := reg.Lookup(entry.ID); ok {
				cs.TypeDef = &td
			}
		}
		summaries = append(summaries, cs)
	}
	payload := struct {
		Components []ComponentSummary `json:"components"`
	}{
		Components: summaries,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func orderLibraryComponents(entries []ComponentEntry, byID map[TypeID]ComponentEntry) ([]TypeID, error) {
	adj := make(map[TypeID][]TypeID, len(entries))
	indeg := make(map[TypeID]int, len(entries))
	for _, e := range entries {
		indeg[e.ID] = 0
	}
	for _, e := range entries {
		seenDeps := make(map[TypeID]struct{})
		for _, nid := range e.Spec.SortedNodeIDs() {
			dep := e.Spec.Nodes[nid].Type
			if dep.Namespace != NamespaceGraph {
				continue
			}
			if dep == e.ID {
				return nil, fmt.Errorf("%w: %s -> %s", ErrSubgraphCycle, e.ID, dep)
			}
			if _, inLib := byID[dep]; !inLib {
				continue
			}
			if _, dup := seenDeps[dep]; dup {
				continue
			}
			seenDeps[dep] = struct{}{}
			adj[dep] = append(adj[dep], e.ID)
			indeg[e.ID]++
		}
	}
	for k := range adj {
		slices.SortFunc(adj[k], CompareTypeID)
	}

	ready := make([]TypeID, 0, len(entries))
	for _, e := range entries {
		if indeg[e.ID] == 0 {
			ready = append(ready, e.ID)
		}
	}
	slices.SortFunc(ready, CompareTypeID)

	ordered := make([]TypeID, 0, len(entries))
	for len(ready) > 0 {
		u := ready[0]
		ready = ready[1:]
		ordered = append(ordered, u)
		for _, v := range adj[u] {
			indeg[v]--
			if indeg[v] == 0 {
				pos, _ := slices.BinarySearchFunc(ready, v, CompareTypeID)
				ready = slices.Insert(ready, pos, v)
			}
		}
	}
	if len(ordered) != len(entries) {
		cyc := findComponentTypeCycle(entries, byID)
		return nil, fmt.Errorf("%w: %s", ErrSubgraphCycle, cyc)
	}
	return ordered, nil
}

func findComponentTypeCycle(entries []ComponentEntry, byID map[TypeID]ComponentEntry) string {
	const (
		unvisited uint8 = 0
		visiting  uint8 = 1
		visited   uint8 = 2
	)
	state := make(map[TypeID]uint8, len(entries))
	var stack []TypeID
	pos := make(map[TypeID]int, len(entries))

	var dfs func(u TypeID) []TypeID
	dfs = func(u TypeID) []TypeID {
		state[u] = visiting
		pos[u] = len(stack)
		stack = append(stack, u)
		entry := byID[u]
		var deps []TypeID
		for _, nid := range entry.Spec.SortedNodeIDs() {
			t := entry.Spec.Nodes[nid].Type
			if t.Namespace == NamespaceGraph {
				if _, ok := byID[t]; ok && !slices.Contains(deps, t) {
					deps = append(deps, t)
				}
			}
		}
		slices.SortFunc(deps, CompareTypeID)
		for _, v := range deps {
			switch state[v] {
			case visiting:
				out := append([]TypeID(nil), stack[pos[v]:]...)
				out = append(out, v)
				return out
			case unvisited:
				if cyc := dfs(v); len(cyc) > 0 {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		delete(pos, u)
		state[u] = visited
		return nil
	}

	for _, e := range entries {
		if state[e.ID] == unvisited {
			if cyc := dfs(e.ID); len(cyc) > 0 {
				parts := make([]string, len(cyc))
				for i, id := range cyc {
					parts[i] = id.String()
				}
				return strings.Join(parts, " -> ")
			}
		}
	}
	return "cycle detected"
}

func withGraphNamespaceAllowed(opts ValidateOptions) ValidateOptions {
	if opts.Backend == nil || len(opts.Backend.Namespaces) == 0 {
		return opts
	}
	if slices.Contains(opts.Backend.Namespaces, NamespaceGraph) {
		return opts
	}
	bCopy := *opts.Backend
	bCopy.Namespaces = append(append([]string(nil), opts.Backend.Namespaces...), NamespaceGraph)
	return ValidateOptions{Backend: &bCopy}
}

// SynthesizeTypeDef validates comp against reg and synthesizes the reusable
// "graph/<name>@<major>" TypeDef, rolling up boundary ports, 1:1 exposed
// parameters, required host capabilities (including HostFromURLParam rules),
// and backend constraints.
func SynthesizeTypeDef(comp *Spec, reg *Registry, opts ValidateOptions) (TypeDef, error) {
	if comp == nil {
		return TypeDef{}, fmt.Errorf("%w: nil component spec", ErrInvalidSpec)
	}
	if reg == nil {
		return TypeDef{}, fmt.Errorf("%w: nil registry", ErrInvalidSpec)
	}
	canon := comp.Canonicalize()
	tid, err := canon.ComponentTypeID()
	if err != nil {
		return TypeDef{}, err
	}
	if _, err := Validate(canon, reg, withGraphNamespaceAllowed(opts)); err != nil {
		return TypeDef{}, err
	}

	internallyWired := make(map[PortRef]bool, len(canon.Edges))
	for _, e := range canon.Edges {
		internallyWired[e.To] = true
	}

	inlets := make([]PortDef, 0, len(canon.Inlets))
	for _, ex := range canon.Inlets {
		targets := ex.ResolvedTargets()
		req := ex.Required
		desc := ex.Description
		for _, target := range targets {
			ndef, _ := reg.Lookup(canon.Nodes[target.Node].Type)
			if pdef, ok := ndef.Inlet(target.Port); ok {
				if pdef.Required && !internallyWired[target] {
					req = true
				}
				if desc == "" && len(targets) == 1 {
					desc = pdef.Description
				}
			}
		}
		inlets = append(inlets, PortDef{
			Name:        ex.Name,
			Type:        ex.Type,
			Required:    req,
			Description: desc,
		})
	}

	outlets := make([]PortDef, 0, len(canon.Outlets))
	for _, ex := range canon.Outlets {
		desc := ex.Description
		if desc == "" {
			ndef, _ := reg.Lookup(canon.Nodes[ex.Target.Node].Type)
			if pdef, ok := ndef.Outlet(ex.Target.Port); ok {
				desc = pdef.Description
			}
		}
		outlets = append(outlets, PortDef{
			Name:        ex.Name,
			Type:        ex.Type,
			Description: desc,
		})
	}

	internalParamToExposed := make(map[PortRef]ExposedParamSpec, len(canon.Params))
	params := make([]ParamDef, 0, len(canon.Params))
	for _, ex := range canon.Params {
		targets := ex.ResolvedTargets()
		for _, t := range targets {
			internalParamToExposed[t] = ex
		}
		p0 := targets[0]
		n0 := canon.Nodes[p0.Node]
		ndef0, _ := reg.Lookup(n0.Type)
		tParam0, _ := ndef0.Param(p0.Port)

		defVal := cloneParamValue(ex.Default)
		if defVal == nil && !ex.Required {
			if nv, ok := n0.Params[p0.Port]; ok && nv != nil {
				defVal = cloneParamValue(nv)
			} else if tParam0.Default != nil {
				defVal = cloneParamValue(tParam0.Default)
			}
			if defVal != nil {
				probe := ParamDef{
					Name: ex.Name,
					Type: ex.Type,
					Min:  ex.Min,
					Max:  ex.Max,
					Enum: ex.Enum,
				}
				if validateParamValue(probe, defVal) != nil {
					defVal = nil
				}
			}
		}

		req := ex.Required
		if !req && defVal == nil {
			for _, t := range targets {
				nt := canon.Nodes[t.Node]
				ndeft, _ := reg.Lookup(nt.Type)
				tp, _ := ndeft.Param(t.Port)
				if tp.Required && tp.Default == nil {
					if v, ok := nt.Params[t.Port]; !ok || v == nil {
						req = true
						break
					}
				}
			}
		}

		var minPtr, maxPtr *float64
		if ex.Min != nil {
			v := *ex.Min
			minPtr = &v
		} else if len(targets) == 1 && tParam0.Min != nil {
			v := *tParam0.Min
			minPtr = &v
		}
		if ex.Max != nil {
			v := *ex.Max
			maxPtr = &v
		} else if len(targets) == 1 && tParam0.Max != nil {
			v := *tParam0.Max
			maxPtr = &v
		}

		var enumCopy []string
		if len(ex.Enum) > 0 {
			enumCopy = append([]string(nil), ex.Enum...)
		} else if len(targets) == 1 && len(tParam0.Enum) > 0 {
			enumCopy = append([]string(nil), tParam0.Enum...)
		}

		desc := ex.Description
		if desc == "" && len(targets) == 1 {
			desc = tParam0.Description
		}

		params = append(params, ParamDef{
			Name:        ex.Name,
			Type:        ex.Type,
			Required:    req,
			Default:     defVal,
			Min:         minPtr,
			Max:         maxPtr,
			Enum:        enumCopy,
			Description: desc,
		})
	}

	capRules := rollUpComponentCapabilities(canon, reg, internalParamToExposed)
	backends, err := intersectComponentBackends(canon, reg)
	if err != nil {
		return TypeDef{}, err
	}

	def := TypeDef{
		ID:           tid,
		Description:  canon.Description,
		Inlets:       inlets,
		Outlets:      outlets,
		Params:       params,
		Capabilities: capRules,
		Backends:     backends,
	}
	if err := ValidateTypeDef(def); err != nil {
		return TypeDef{}, err
	}
	return def, nil
}

// SynthesizeExecutionHints derives hot-inlet and frame-bound execution metadata
// for a component Spec from per-type lookup callbacks.
func SynthesizeExecutionHints(
	comp *Spec,
	hotLookup func(TypeID) map[string]bool,
	frameBoundLookup func(TypeID) bool,
) (hotInlets map[string]bool, frameBound bool) {
	if comp == nil {
		return nil, false
	}
	if frameBoundLookup != nil {
		for _, nid := range comp.SortedNodeIDs() {
			if frameBoundLookup(comp.Nodes[nid].Type) {
				frameBound = true
				break
			}
		}
	}
	if hotLookup != nil && len(comp.Inlets) > 0 {
		hotInlets = make(map[string]bool, len(comp.Inlets))
		for _, ex := range comp.Inlets {
			isHot := false
			for _, t := range ex.ResolvedTargets() {
				node, ok := comp.Nodes[t.Node]
				if !ok {
					continue
				}
				hm := hotLookup(node.Type)
				if hm == nil || hm[t.Port] {
					isHot = true
					break
				}
			}
			hotInlets[ex.Name] = isHot
		}
	}
	return hotInlets, frameBound
}

func rollUpComponentCapabilities(
	canon *Spec,
	reg *Registry,
	internalParamToExposed map[PortRef]ExposedParamSpec,
) []CapabilityRule {
	var rules []CapabilityRule

	for _, nid := range canon.SortedNodeIDs() {
		node := canon.Nodes[nid]
		ndef, ok := reg.Lookup(node.Type)
		if !ok {
			continue
		}

		// Track default URL host capabilities whose URL parameter was exposed on
		// the component boundary so overriding the exposed URL parameter swaps the
		// required host cleanly rather than retaining the internal default host.
		dynamicDefaultCaps := make(map[Capability]struct{})

		for _, rule := range ndef.Capabilities {
			if rule.HostFromURLParam != "" {
				targetRef := PortRef{Node: nid, Port: rule.HostFromURLParam}
				if ex, exposed := internalParamToExposed[targetRef]; exposed && ex.Type == ParamString {
					rules = append(rules, CapabilityRule{
						Kind:             rule.Kind,
						RequireScope:     true,
						HostFromURLParam: ex.Name,
						Description:      rule.Description,
					})
					if defHost := resolveNodeURLHost(node, ndef, rule.HostFromURLParam, ex.Default); defHost != "" {
						dynamicDefaultCaps[Capability{Kind: rule.Kind, Scope: defHost}] = struct{}{}
					}
					continue
				}
				if host := resolveNodeURLHost(node, ndef, rule.HostFromURLParam, nil); host != "" {
					rules = append(rules, CapabilityRule{
						Kind:         rule.Kind,
						Scope:        host,
						RequireScope: true,
						Description:  rule.Description,
					})
					continue
				}
			}
			if rule.Scope != "" {
				rules = append(rules, CapabilityRule{
					Kind:         rule.Kind,
					Scope:        rule.Scope,
					RequireScope: rule.RequireScope,
					Description:  rule.Description,
				})
				continue
			}
			if rule.RequireScope {
				matchedScoped := false
				for _, c := range node.Capabilities {
					if c.Kind == rule.Kind && c.Scope != "" {
						rules = append(rules, CapabilityRule{
							Kind:         c.Kind,
							Scope:        c.Scope,
							RequireScope: true,
							Description:  rule.Description,
						})
						matchedScoped = true
					}
				}
				if !matchedScoped {
					rules = append(rules, CapabilityRule{
						Kind:         rule.Kind,
						RequireScope: true,
						Description:  rule.Description,
					})
				}
				continue
			}
			rules = append(rules, CapabilityRule{
				Kind:        rule.Kind,
				Description: rule.Description,
			})
		}

		// Also roll up explicit node-level capabilities (e.g. WASI fs:/... grants
		// on wasm/* nodes that have no static TypeDef.Capabilities).
		for _, c := range node.Capabilities {
			if _, skip := dynamicDefaultCaps[c]; skip {
				continue
			}
			rules = append(rules, CapabilityRule{
				Kind:         c.Kind,
				Scope:        c.Scope,
				RequireScope: c.Scope != "",
			})
		}
	}

	if len(rules) == 0 {
		return nil
	}
	return compactCapabilityRules(rules)
}

func resolveNodeURLHost(node NodeSpec, def TypeDef, paramName string, fallback any) string {
	raw, ok := node.Params[paramName]
	if !ok || raw == nil {
		raw = fallback
	}
	if raw == nil {
		if pdef, has := def.Param(paramName); has {
			raw = pdef.Default
		}
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func compactCapabilityRules(in []CapabilityRule) []CapabilityRule {
	slices.SortFunc(in, compareCapabilityRule)
	var out []CapabilityRule
	for _, r := range in {
		if len(out) > 0 && equalCapabilityRuleKey(out[len(out)-1], r) {
			if out[len(out)-1].Description == "" && r.Description != "" {
				out[len(out)-1].Description = r.Description
			}
			continue
		}
		// Drop redundant unscoped rule when an identical-Kind concrete scoped rule
		// with no HostFromURLParam is also present (since Kind:Scope covers Kind).
		out = append(out, r)
	}
	filtered := make([]CapabilityRule, 0, len(out))
	for i, r := range out {
		if r.Scope == "" && !r.RequireScope && r.HostFromURLParam == "" {
			redundant := false
			for j, other := range out {
				if i != j && other.Kind == r.Kind && other.HostFromURLParam == "" && (other.Scope != "" || other.RequireScope) {
					redundant = true
					break
				}
			}
			if redundant {
				continue
			}
		}
		filtered = append(filtered, r)
	}
	return filtered
}

func compareCapabilityRule(a, b CapabilityRule) int {
	if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Scope, b.Scope); c != 0 {
		return c
	}
	if c := cmp.Compare(a.HostFromURLParam, b.HostFromURLParam); c != 0 {
		return c
	}
	if a.RequireScope != b.RequireScope {
		if a.RequireScope {
			return 1
		}
		return -1
	}
	return 0
}

func equalCapabilityRuleKey(a, b CapabilityRule) bool {
	return a.Kind == b.Kind &&
		a.Scope == b.Scope &&
		a.RequireScope == b.RequireScope &&
		a.HostFromURLParam == b.HostFromURLParam
}

func intersectComponentBackends(canon *Spec, reg *Registry) ([]string, error) {
	var current []string
	constrained := false
	for _, nid := range canon.SortedNodeIDs() {
		ndef, ok := reg.Lookup(canon.Nodes[nid].Type)
		if !ok || len(ndef.Backends) == 0 {
			continue
		}
		bs := append([]string(nil), ndef.Backends...)
		slices.Sort(bs)
		bs = slices.Compact(bs)
		if !constrained {
			current = bs
			constrained = true
			continue
		}
		next := make([]string, 0, len(current))
		for _, b := range current {
			if slices.Contains(bs, b) {
				next = append(next, b)
			}
		}
		current = next
		if len(current) == 0 {
			return nil, fmt.Errorf("%w: internal nodes of %q have disjoint backend requirements", ErrUnsupportedBackend, canon.Name)
		}
	}
	return current, nil
}

// FlattenOptions configures recursive flatten-at-load subgraph expansion.
type FlattenOptions struct {
	MaxDepth int
	MaxNodes int
	MaxEdges int
	Backend  *BackendProfile
}

func (o FlattenOptions) withDefaults() FlattenOptions {
	out := o
	if out.MaxDepth <= 0 {
		out.MaxDepth = DefaultMaxSubgraphDepth
	}
	if out.MaxNodes <= 0 {
		out.MaxNodes = DefaultMaxNodes
	}
	if out.MaxEdges <= 0 {
		out.MaxEdges = DefaultMaxEdges
	}
	return out
}

// SubgraphNodeOrigin traces a concrete flattened node back to its enclosing
// graph/* instance path and leaf component node.
type SubgraphNodeOrigin struct {
	InstancePath []string `json:"instance_path"`
	Component    TypeID   `json:"component"`
	LeafNode     string   `json:"leaf_node"`
}

// FlattenReport records how authoring-level graph/* instances and ports map to
// concrete flattened nodes and ports.
type FlattenReport struct {
	ExpandedInstances int                           `json:"expanded_instances"`
	NodeOrigins       map[string]SubgraphNodeOrigin `json:"node_origins,omitempty"`
	InletTargets      map[PortRef][]PortRef         `json:"inlet_targets,omitempty"`
	OutletSources     map[PortRef]PortRef           `json:"outlet_sources,omitempty"`
	InstanceNodes     map[string][]string           `json:"instance_nodes,omitempty"`
}

// Flatten expands all graph/* subgraph nodes in spec recursively up to
// opts.MaxDepth, enforcing fail-closed capability coverage across every
// subgraph boundary and validating both the authoring graph and final concrete
// graph.
func Flatten(spec *Spec, reg *Registry, lib *Library, opts FlattenOptions) (*Spec, *FlattenReport, error) {
	if spec == nil {
		return nil, nil, &ValidationError{Issues: []Issue{{
			Code:    IssueInvalidSpec,
			Message: "nil spec",
			err:     fmt.Errorf("%w: nil spec", ErrInvalidSpec),
		}}}
	}
	if reg == nil {
		return nil, nil, &ValidationError{Issues: []Issue{{
			Code:    IssueInvalidSpec,
			Message: "nil registry",
			err:     fmt.Errorf("%w: nil registry", ErrInvalidSpec),
		}}}
	}

	cfg := opts.withDefaults()
	work := spec.Canonicalize()
	if len(work.Nodes) > cfg.MaxNodes {
		return nil, nil, fmt.Errorf("%w: %d nodes > %d max", ErrInvalidSpec, len(work.Nodes), cfg.MaxNodes)
	}
	if len(work.Edges) > cfg.MaxEdges {
		return nil, nil, fmt.Errorf("%w: %d edges > %d max", ErrInvalidSpec, len(work.Edges), cfg.MaxEdges)
	}

	hasGraphNodes := false
	for _, nid := range work.SortedNodeIDs() {
		if work.Nodes[nid].Type.Namespace == NamespaceGraph {
			hasGraphNodes = true
			break
		}
	}

	if !hasGraphNodes && len(work.Params) == 0 {
		if _, err := Validate(work, reg, ValidateOptions{Backend: cfg.Backend}); err != nil {
			return nil, nil, err
		}
		return work, &FlattenReport{}, nil
	}

	// Build a working registry containing synthesized TypeDefs for all reachable
	// graph/* components, verifying recursion depth and acyclicity up front.
	workReg := reg.Clone()
	for _, td := range workReg.Types() {
		if td.ID.Namespace == NamespaceGraph {
			workReg.Delete(td.ID)
		}
	}
	if hasGraphNodes {
		if err := synthesizeReachableSubgraphs(work, workReg, lib, cfg, nil, 0); err != nil {
			return nil, nil, err
		}
	}

	// Validate the authoring graph against synthesized component TypeDefs so
	// authoring mistakes surface with clean top-level node/edge paths.
	if _, err := Validate(work, workReg, withGraphNamespaceAllowed(ValidateOptions{Backend: cfg.Backend})); err != nil {
		return nil, nil, err
	}

	// Apply any root-level exposed parameter defaults onto root nodes before
	// expanding nested subgraphs.
	applyRootParamDefaults(work)
	work.Params = nil
	work.Groups = nil

	report := &FlattenReport{
		NodeOrigins:   make(map[string]SubgraphNodeOrigin),
		InletTargets:  make(map[PortRef][]PortRef),
		OutletSources: make(map[PortRef]PortRef),
		InstanceNodes: make(map[string][]string),
	}

	for depth := 1; ; depth++ {
		var graphInstanceIDs []string
		for _, nid := range work.SortedNodeIDs() {
			if work.Nodes[nid].Type.Namespace == NamespaceGraph {
				graphInstanceIDs = append(graphInstanceIDs, nid)
			}
		}
		if len(graphInstanceIDs) == 0 {
			break
		}
		if depth > cfg.MaxDepth {
			return nil, nil, fmt.Errorf("%w: depth %d > max %d", ErrSubgraphDepthExceeded, depth, cfg.MaxDepth)
		}

		for _, instID := range graphInstanceIDs {
			if err := expandSingleInstance(work, instID, workReg, lib, cfg, report); err != nil {
				return nil, nil, err
			}
		}
	}

	flat := work.Canonicalize()
	if len(flat.Nodes) > cfg.MaxNodes {
		return nil, nil, fmt.Errorf("%w: flattened graph has %d nodes > %d max", ErrInvalidSpec, len(flat.Nodes), cfg.MaxNodes)
	}
	if len(flat.Edges) > cfg.MaxEdges {
		return nil, nil, fmt.Errorf("%w: flattened graph has %d edges > %d max", ErrInvalidSpec, len(flat.Edges), cfg.MaxEdges)
	}

	if _, err := Validate(flat, reg, ValidateOptions{Backend: cfg.Backend}); err != nil {
		return nil, nil, err
	}

	finalizeFlattenReport(flat, report)
	return flat, report, nil
}

func synthesizeReachableSubgraphs(
	cur *Spec,
	workReg *Registry,
	lib *Library,
	cfg FlattenOptions,
	stack []TypeID,
	depth int,
) error {
	for _, nid := range cur.SortedNodeIDs() {
		tid := cur.Nodes[nid].Type
		if tid.Namespace != NamespaceGraph {
			continue
		}
		nextDepth := depth + 1
		if slices.Contains(stack, tid) {
			chain := make([]string, len(stack)+1)
			for i, id := range stack {
				chain[i] = id.String()
			}
			chain[len(stack)] = tid.String()
			return fmt.Errorf("%w: %s", ErrSubgraphCycle, strings.Join(chain, " -> "))
		}
		if nextDepth > cfg.MaxDepth {
			return fmt.Errorf("%w: depth %d > max %d at %s", ErrSubgraphDepthExceeded, nextDepth, cfg.MaxDepth, tid)
		}
		if lib == nil {
			// Leave unregistered in workReg so Validate reports IssueUnresolvedSubgraph
			// with exact node path.
			continue
		}
		compSpec, ok := lib.LookupSpec(tid)
		if !ok {
			continue
		}
		nextStack := append(append([]TypeID(nil), stack...), tid)
		if err := synthesizeReachableSubgraphs(compSpec, workReg, lib, cfg, nextStack, nextDepth); err != nil {
			return err
		}
		if _, exists := workReg.Lookup(tid); !exists {
			def, err := SynthesizeTypeDef(compSpec, workReg, ValidateOptions{Backend: cfg.Backend})
			if err != nil {
				return fmt.Errorf("component %s: %w", tid, err)
			}
			if err := workReg.Upsert(def); err != nil {
				return fmt.Errorf("component %s: %w", tid, err)
			}
		}
	}
	return nil
}

func applyRootParamDefaults(work *Spec) {
	for _, ep := range work.Params {
		if ep.Default == nil {
			continue
		}
		for _, target := range ep.ResolvedTargets() {
			node, ok := work.Nodes[target.Node]
			if !ok {
				continue
			}
			if cur, has := node.Params[target.Port]; has && cur != nil {
				continue
			}
			if node.Params == nil {
				node.Params = make(map[string]any)
			}
			node.Params[target.Port] = cloneParamValue(ep.Default)
			work.Nodes[target.Node] = node
		}
	}
}

func expandSingleInstance(
	work *Spec,
	instID string,
	workReg *Registry,
	lib *Library,
	cfg FlattenOptions,
	report *FlattenReport,
) error {
	outerNode := work.Nodes[instID]
	compSpec, ok := lib.LookupSpec(outerNode.Type)
	if !ok {
		return &ValidationError{Issues: []Issue{{
			Code:    IssueUnresolvedSubgraph,
			Path:    "nodes." + instID + ".type",
			Message: fmt.Sprintf("unresolved subgraph component %q", outerNode.Type),
			err:     fmt.Errorf("%w: %s", ErrUnresolvedSubgraph, outerNode.Type),
		}}}
	}
	comp := compSpec.Canonicalize()

	projectedNodes := len(work.Nodes) - 1 + len(comp.Nodes)
	if projectedNodes > cfg.MaxNodes {
		return fmt.Errorf("%w: flattening %q (%s) exceeds max nodes (%d > %d)", ErrInvalidSpec, instID, outerNode.Type, projectedNodes, cfg.MaxNodes)
	}

	// 1. Forward 1:1 exposed parameters onto cloned internal nodes and adjust
	// dynamic HostFromURLParam capability scopes before verifying outer coverage.
	for _, ep := range comp.Params {
		var effVal any
		hasEff := false
		if rawVal, set := outerNode.Params[ep.Name]; set && rawVal != nil {
			effVal = cloneParamValue(rawVal)
			hasEff = true
		} else if ep.Default != nil {
			effVal = cloneParamValue(ep.Default)
			hasEff = true
		}
		if !hasEff {
			continue
		}
		for _, target := range ep.ResolvedTargets() {
			inNode := comp.Nodes[target.Node]
			ndef, hasDef := workReg.Lookup(inNode.Type)
			var oldDynamicHosts []Capability
			if hasDef {
				for _, rule := range ndef.Capabilities {
					if rule.HostFromURLParam == target.Port {
						if prevHost := resolveNodeURLHost(inNode, ndef, target.Port, ep.Default); prevHost != "" {
							oldDynamicHosts = append(oldDynamicHosts, Capability{Kind: rule.Kind, Scope: prevHost})
						}
					}
				}
			}

			if inNode.Params == nil {
				inNode.Params = make(map[string]any)
			}
			inNode.Params[target.Port] = cloneParamValue(effVal)

			if hasDef {
				for _, rule := range ndef.Capabilities {
					if rule.HostFromURLParam == target.Port {
						newHost := resolveNodeURLHost(inNode, ndef, target.Port, nil)
						if newHost != "" {
							inNode.Capabilities = replaceDynamicHostCapability(
								inNode.Capabilities,
								oldDynamicHosts,
								Capability{Kind: rule.Kind, Scope: newHost},
							)
						}
					}
				}
			}
			comp.Nodes[target.Node] = inNode
		}
	}

	// 2. Fail-closed capability boundary check: every internal node capability
	// and rule requirement must be covered by outerNode.Capabilities.
	if err := verifySubgraphCapabilityCoverage(instID, outerNode, comp, workReg); err != nil {
		return err
	}

	// 3. Allocate deterministic prefixed IDs for internal nodes.
	parentOrigin, hadParentOrigin := report.NodeOrigins[instID]
	delete(work.Nodes, instID)
	delete(report.NodeOrigins, instID)

	idMap := make(map[string]string, len(comp.Nodes))
	for _, innerID := range comp.SortedNodeIDs() {
		flatID := joinSubgraphID(instID, innerID, work.Nodes)
		idMap[innerID] = flatID

		inNode := cloneNodeSpec(comp.Nodes[innerID])
		if outerNode.UI != nil {
			if inNode.UI == nil {
				inNode.UI = &NodeUI{X: outerNode.UI.X, Y: outerNode.UI.Y}
			} else {
				inNode.UI.X += outerNode.UI.X
				inNode.UI.Y += outerNode.UI.Y
			}
		}
		work.Nodes[flatID] = inNode

		instPath := []string{instID}
		if hadParentOrigin {
			instPath = append(append([]string(nil), parentOrigin.InstancePath...), parentOrigin.LeafNode)
		}
		report.NodeOrigins[flatID] = SubgraphNodeOrigin{
			InstancePath: instPath,
			Component:    outerNode.Type,
			LeafNode:     innerID,
		}
	}

	// 4. Build boundary port maps for instID and update report chains.
	inletMap := make(map[string][]PortRef, len(comp.Inlets))
	for _, ex := range comp.Inlets {
		targets := ex.ResolvedTargets()
		mapped := make([]PortRef, len(targets))
		for i, t := range targets {
			mapped[i] = PortRef{Node: idMap[t.Node], Port: t.Port}
		}
		inletMap[ex.Name] = mapped
		report.InletTargets[PortRef{Node: instID, Port: ex.Name}] = slices.Clone(mapped)
	}
	// Compose any earlier outer->instID inlet chains that pointed at instID.
	for outerRef, prevTargets := range report.InletTargets {
		if outerRef.Node == instID {
			continue
		}
		changed := false
		var nextTargets []PortRef
		for _, pt := range prevTargets {
			if pt.Node == instID {
				changed = true
				nextTargets = append(nextTargets, inletMap[pt.Port]...)
			} else {
				nextTargets = append(nextTargets, pt)
			}
		}
		if changed {
			slices.SortFunc(nextTargets, ComparePortRef)
			report.InletTargets[outerRef] = slices.Compact(nextTargets)
		}
	}

	outletMap := make(map[string]PortRef, len(comp.Outlets))
	for _, ex := range comp.Outlets {
		mapped := PortRef{Node: idMap[ex.Target.Node], Port: ex.Target.Port}
		outletMap[ex.Name] = mapped
		report.OutletSources[PortRef{Node: instID, Port: ex.Name}] = mapped
	}
	// Compose any earlier outer->instID outlet chains that pointed at instID.
	for outerRef, prevSrc := range report.OutletSources {
		if outerRef.Node == instID {
			continue
		}
		if prevSrc.Node == instID {
			if nextSrc, ok := outletMap[prevSrc.Port]; ok {
				report.OutletSources[outerRef] = nextSrc
			}
		}
	}

	// 5. Rewrite root exposed Inlets / Outlets if they targeted instID.
	for i := range work.Inlets {
		targets := work.Inlets[i].ResolvedTargets()
		var next []PortRef
		changed := false
		for _, t := range targets {
			if t.Node == instID {
				changed = true
				next = append(next, inletMap[t.Port]...)
			} else {
				next = append(next, t)
			}
		}
		if changed {
			work.Inlets[i].Target, work.Inlets[i].Targets = canonicalizeTargets(PortRef{}, next)
		}
	}
	for i := range work.Outlets {
		if work.Outlets[i].Target.Node == instID {
			if mapped, ok := outletMap[work.Outlets[i].Target.Port]; ok {
				work.Outlets[i].Target = mapped
			}
		}
	}

	// 6. Rewrite incident edges and append prefixed internal edges.
	rewrittenEdges := make([]EdgeSpec, 0, len(work.Edges)+len(comp.Edges))
	for _, e := range work.Edges {
		from := e.From
		if from.Node == instID {
			mappedFrom, ok := outletMap[from.Port]
			if !ok {
				return &ValidationError{Issues: []Issue{{
					Code:    IssueUnknownPort,
					Path:    "edges",
					Message: fmt.Sprintf("subgraph %q (%s) has no exposed outlet %q", instID, outerNode.Type, from.Port),
					err:     fmt.Errorf("%w: %s.%s", ErrUnknownPort, instID, from.Port),
				}}}
			}
			from = mappedFrom
		}
		if e.To.Node == instID {
			mappedTos, ok := inletMap[e.To.Port]
			if !ok {
				return &ValidationError{Issues: []Issue{{
					Code:    IssueUnknownPort,
					Path:    "edges",
					Message: fmt.Sprintf("subgraph %q (%s) has no exposed inlet %q", instID, outerNode.Type, e.To.Port),
					err:     fmt.Errorf("%w: %s.%s", ErrUnknownPort, instID, e.To.Port),
				}}}
			}
			for _, toRef := range mappedTos {
				rewrittenEdges = append(rewrittenEdges, EdgeSpec{
					From: from,
					To:   toRef,
					Kind: e.Kind,
				})
			}
			continue
		}
		rewrittenEdges = append(rewrittenEdges, EdgeSpec{
			From: from,
			To:   e.To,
			Kind: e.Kind,
		})
	}
	for _, ie := range comp.Edges {
		rewrittenEdges = append(rewrittenEdges, EdgeSpec{
			From: PortRef{Node: idMap[ie.From.Node], Port: ie.From.Port},
			To:   PortRef{Node: idMap[ie.To.Node], Port: ie.To.Port},
			Kind: ie.Kind,
		})
	}

	slices.SortFunc(rewrittenEdges, CompareEdgeSpec)
	rewrittenEdges = compactExpandedEdges(rewrittenEdges, cfg.Backend)
	if len(rewrittenEdges) > cfg.MaxEdges {
		return fmt.Errorf("%w: flattening %q (%s) exceeds max edges (%d > %d)", ErrInvalidSpec, instID, outerNode.Type, len(rewrittenEdges), cfg.MaxEdges)
	}
	work.Edges = rewrittenEdges
	report.ExpandedInstances++
	return nil
}

func replaceDynamicHostCapability(caps []Capability, oldCaps []Capability, newCap Capability) []Capability {
	out := make([]Capability, 0, len(caps)+1)
	for _, c := range caps {
		if slices.Contains(oldCaps, c) && c != newCap {
			continue
		}
		out = append(out, c)
	}
	hasCover := false
	for _, c := range out {
		if c.Covers(newCap) {
			hasCover = true
			break
		}
	}
	if !hasCover {
		out = append(out, newCap)
	}
	slices.SortFunc(out, CompareCapability)
	return slices.Compact(out)
}

func verifySubgraphCapabilityCoverage(
	instID string,
	outerNode NodeSpec,
	comp *Spec,
	workReg *Registry,
) error {
	var issues []Issue
	checkCap := func(innerID string, req Capability) {
		for _, outerCap := range outerNode.Capabilities {
			if outerCap.Covers(req) {
				return
			}
		}
		msg := fmt.Sprintf(
			"subgraph node %q (%s) must declare capability %q required by internal node %q",
			instID, outerNode.Type, req, innerID,
		)
		issues = append(issues, Issue{
			Code:    IssueUndeclaredCapability,
			Path:    "nodes." + instID + ".capabilities",
			Message: msg,
			err:     fmt.Errorf("%w: %s", ErrUndeclaredCapability, msg),
		})
	}

	for _, innerID := range comp.SortedNodeIDs() {
		inNode := comp.Nodes[innerID]
		for _, c := range inNode.Capabilities {
			checkCap(innerID, c)
		}
		ndef, ok := workReg.Lookup(inNode.Type)
		if !ok {
			continue
		}
		for _, rule := range ndef.Capabilities {
			if rule.HostFromURLParam != "" {
				host := resolveNodeURLHost(inNode, ndef, rule.HostFromURLParam, nil)
				if host != "" {
					checkCap(innerID, Capability{Kind: rule.Kind, Scope: host})
				}
				continue
			}
			if rule.Scope != "" {
				checkCap(innerID, Capability{Kind: rule.Kind, Scope: rule.Scope})
			} else if !rule.RequireScope {
				checkCap(innerID, Capability{Kind: rule.Kind})
			}
		}
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func compactExpandedEdges(edges []EdgeSpec, backend *BackendProfile) []EdgeSpec {
	if len(edges) <= 1 {
		return edges
	}
	defKind := ""
	if backend != nil {
		defKind = backend.DefaultEdgeKind
	}
	seen := make(map[EdgeSpec]struct{}, len(edges))
	out := make([]EdgeSpec, 0, len(edges))
	for _, e := range edges {
		eff := e.Kind
		if eff == "" {
			eff = defKind
		}
		key := EdgeSpec{From: e.From, To: e.To, Kind: eff}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}

func joinSubgraphID(outerID, innerID string, existing map[string]NodeSpec) string {
	cand := outerID + "__" + innerID
	if len(cand) <= MaxIDLen {
		if _, taken := existing[cand]; !taken && IsValidID(cand) {
			return cand
		}
	}
	sum := sha256.Sum256([]byte(outerID + "\x00" + innerID))
	hashSuffix := hex.EncodeToString(sum[:4]) // 8 hex chars
	prefix := outerID + "__" + innerID
	maxPrefix := MaxIDLen - 2 - len(hashSuffix)
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "_-")
		if prefix == "" {
			prefix = "n"
		}
	}
	base := prefix + "__" + hashSuffix
	if _, taken := existing[base]; !taken {
		return base
	}
	for n := 2; ; n++ {
		suf := "_" + strconv.Itoa(n)
		p := base
		if len(p)+len(suf) > MaxIDLen {
			p = p[:MaxIDLen-len(suf)]
		}
		alt := p + suf
		if _, taken := existing[alt]; !taken {
			return alt
		}
	}
}

func finalizeFlattenReport(flat *Spec, report *FlattenReport) {
	// Keep only top-level authoring port mappings whose target concrete nodes
	// exist in flat.Nodes, and populate InstanceNodes per top-level instance.
	nodeIDs := flat.SortedNodeIDs()
	for _, nid := range nodeIDs {
		orig, ok := report.NodeOrigins[nid]
		if !ok || len(orig.InstancePath) == 0 {
			continue
		}
		topInst := orig.InstancePath[0]
		report.InstanceNodes[topInst] = append(report.InstanceNodes[topInst], nid)
	}
	for ref, targets := range report.InletTargets {
		kept := make([]PortRef, 0, len(targets))
		for _, t := range targets {
			if _, ok := flat.Nodes[t.Node]; ok {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(report.InletTargets, ref)
		} else {
			report.InletTargets[ref] = kept
		}
	}
	for ref, src := range report.OutletSources {
		if _, ok := flat.Nodes[src.Node]; !ok {
			delete(report.OutletSources, ref)
		}
	}
	if len(report.NodeOrigins) == 0 {
		report.NodeOrigins = nil
	}
	if len(report.InletTargets) == 0 {
		report.InletTargets = nil
	}
	if len(report.OutletSources) == 0 {
		report.OutletSources = nil
	}
	if len(report.InstanceNodes) == 0 {
		report.InstanceNodes = nil
	}
}

// ExtractOptions configures extraction of a subset of nodes (or UI group) into
// a reusable graph/* component Spec.
type ExtractOptions struct {
	ComponentName    string
	ComponentVersion int
	Description      string
	InstanceID       string
	ExposeParams     []PortRef
	Backend          *BackendProfile
}

// ExtractSubgraph extracts nodeIDs from parent into a canonical component Spec,
// synthesizes its TypeDef, and returns an updated parent Spec where nodeIDs are
// replaced by a single graph/<name>@<ver> node wired to all boundary edges.
func ExtractSubgraph(
	parent *Spec,
	nodeIDs []string,
	reg *Registry,
	opts ExtractOptions,
) (compSpec *Spec, updatedParent *Spec, compDef TypeDef, err error) {
	if parent == nil {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: nil parent spec", ErrInvalidSpec)
	}
	if reg == nil {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: nil registry", ErrInvalidSpec)
	}

	selected := append([]string(nil), nodeIDs...)
	slices.Sort(selected)
	selected = slices.Compact(selected)
	if len(selected) == 0 {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: extract requires at least one node", ErrInvalidSpec)
	}

	canonParent := parent.Canonicalize()
	inSet := make(map[string]bool, len(selected))
	resolvedDefs := make(map[string]TypeDef, len(selected))
	for _, nid := range selected {
		node, ok := canonParent.Nodes[nid]
		if !ok {
			return nil, nil, TypeDef{}, fmt.Errorf("%w: %q", ErrUnknownNode, nid)
		}
		def, ok := reg.Lookup(node.Type)
		if !ok {
			return nil, nil, TypeDef{}, fmt.Errorf("node %q: %w: %s", nid, ErrUnknownNodeType, node.Type)
		}
		inSet[nid] = true
		resolvedDefs[nid] = def
	}

	compName := sanitizeComponentID(opts.ComponentName)
	if !IsValidID(compName) {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: invalid component name %q", ErrInvalidID, opts.ComponentName)
	}
	compVer := opts.ComponentVersion
	if compVer < 0 {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: negative component_version %d", ErrInvalidSpec, compVer)
	}

	comp := &Spec{
		Version:          SpecVersion,
		Name:             compName,
		ComponentVersion: compVer,
		Description:      opts.Description,
		Nodes:            make(map[string]NodeSpec, len(selected)),
		Edges:            []EdgeSpec{},
	}
	if _, err := comp.ComponentTypeID(); err != nil {
		return nil, nil, TypeDef{}, err
	}

	// Compute UI bounding box & centroid so both component and parent remain
	// cleanly laid out on the canvas.
	minX, minY := math.Inf(1), math.Inf(1)
	sumX, sumY := 0.0, 0.0
	uiCount := 0
	for _, nid := range selected {
		if ui := canonParent.Nodes[nid].UI; ui != nil {
			if ui.X < minX {
				minX = ui.X
			}
			if ui.Y < minY {
				minY = ui.Y
			}
			sumX += ui.X
			sumY += ui.Y
			uiCount++
		}
	}
	for _, nid := range selected {
		ns := cloneNodeSpec(canonParent.Nodes[nid])
		if ns.UI != nil && uiCount > 0 {
			ns.UI.X = math.Round(ns.UI.X-minX) + 80
			ns.UI.Y = math.Round(ns.UI.Y-minY) + 80
		}
		comp.Nodes[nid] = ns
	}

	// Partition edges into internal, incoming, outgoing, and external.
	var incomingEdges, outgoingEdges, externalEdges []EdgeSpec
	internallyWired := make(map[PortRef]bool)
	internallySourced := make(map[PortRef]bool)
	for _, e := range canonParent.Edges {
		fromIn := inSet[e.From.Node]
		toIn := inSet[e.To.Node]
		switch {
		case fromIn && toIn:
			comp.Edges = append(comp.Edges, e)
			internallyWired[e.To] = true
			internallySourced[e.From] = true
		case !fromIn && toIn:
			incomingEdges = append(incomingEdges, e)
		case fromIn && !toIn:
			outgoingEdges = append(outgoingEdges, e)
		default:
			externalEdges = append(externalEdges, e)
		}
	}

	// Group incoming boundary edges that share (From, Kind, target DataType)
	// into fan-out exposed inlets.
	type incomingGroupKey struct {
		From PortRef
		Kind string
		Type DataType
	}
	type incomingGroup struct {
		key     incomingGroupKey
		targets []PortRef
	}
	var inGroups []incomingGroup
	inGroupIdx := make(map[incomingGroupKey]int)
	wiredBoundaryTargets := make(map[PortRef]bool)

	for _, e := range incomingEdges {
		tdef := resolvedDefs[e.To.Node]
		inPort, ok := tdef.Inlet(e.To.Port)
		if !ok {
			return nil, nil, TypeDef{}, fmt.Errorf("%w: %s", ErrUnknownPort, e.To)
		}
		gk := incomingGroupKey{From: e.From, Kind: e.Kind, Type: inPort.Type}
		if idx, exists := inGroupIdx[gk]; exists {
			if !slices.Contains(inGroups[idx].targets, e.To) {
				inGroups[idx].targets = append(inGroups[idx].targets, e.To)
			}
		} else {
			inGroupIdx[gk] = len(inGroups)
			inGroups = append(inGroups, incomingGroup{
				key:     gk,
				targets: []PortRef{e.To},
			})
		}
		wiredBoundaryTargets[e.To] = true
	}

	// Also expose any unwired Required internal inlets so incomplete groups can
	// be promoted cleanly and wired afterwards.
	type unwiredInlet struct {
		target PortRef
		pdef   PortDef
	}
	var unwiredReqInlets []unwiredInlet
	for _, nid := range selected {
		tdef := resolvedDefs[nid]
		for _, inPort := range tdef.Inlets {
			ref := PortRef{Node: nid, Port: inPort.Name}
			if inPort.Required && !internallyWired[ref] && !wiredBoundaryTargets[ref] {
				unwiredReqInlets = append(unwiredReqInlets, unwiredInlet{target: ref, pdef: inPort})
			}
		}
	}

	// Assign collision-free exposed inlet names.
	baseInNameCount := make(map[string]int)
	for _, g := range inGroups {
		baseInNameCount[g.targets[0].Port]++
	}
	for _, u := range unwiredReqInlets {
		baseInNameCount[u.target.Port]++
	}
	usedInNames := make(map[string]bool)
	allocPortName := func(ref PortRef, counts map[string]int, used map[string]bool) string {
		cand := ref.Port
		if counts[cand] > 1 || used[cand] {
			cand = sanitizePortID(ref.Node + "_" + ref.Port)
		}
		base := cand
		for n := 2; used[cand]; n++ {
			cand = sanitizePortID(base + "_" + strconv.Itoa(n))
		}
		used[cand] = true
		return cand
	}

	inGroupPortNames := make([]string, len(inGroups))
	for i, g := range inGroups {
		name := allocPortName(g.targets[0], baseInNameCount, usedInNames)
		inGroupPortNames[i] = name
		single, multi := canonicalizeTargets(PortRef{}, g.targets)
		comp.Inlets = append(comp.Inlets, ExposedPortSpec{
			Name:    name,
			Type:    g.key.Type,
			Target:  single,
			Targets: multi,
		})
	}
	for _, u := range unwiredReqInlets {
		name := allocPortName(u.target, baseInNameCount, usedInNames)
		comp.Inlets = append(comp.Inlets, ExposedPortSpec{
			Name:        name,
			Type:        u.pdef.Type,
			Target:      u.target,
			Required:    true,
			Description: u.pdef.Description,
		})
	}

	// Expose outlets for all outgoing boundary edges (or, if none are wired yet,
	// any unwired internal outlets so newly promoted groups can be wired).
	var outSources []PortRef
	for _, e := range outgoingEdges {
		if !slices.Contains(outSources, e.From) {
			outSources = append(outSources, e.From)
		}
	}
	if len(outSources) == 0 {
		for _, nid := range selected {
			tdef := resolvedDefs[nid]
			for _, outPort := range tdef.Outlets {
				ref := PortRef{Node: nid, Port: outPort.Name}
				if !internallySourced[ref] {
					outSources = append(outSources, ref)
				}
			}
		}
	}
	slices.SortFunc(outSources, ComparePortRef)

	baseOutNameCount := make(map[string]int, len(outSources))
	for _, src := range outSources {
		baseOutNameCount[src.Port]++
	}
	usedOutNames := make(map[string]bool, len(outSources))
	outPortNameBySource := make(map[PortRef]string, len(outSources))
	for _, src := range outSources {
		tdef := resolvedDefs[src.Node]
		outPort, ok := tdef.Outlet(src.Port)
		if !ok {
			return nil, nil, TypeDef{}, fmt.Errorf("%w: %s", ErrUnknownPort, src)
		}
		name := allocPortName(src, baseOutNameCount, usedOutNames)
		outPortNameBySource[src] = name
		comp.Outlets = append(comp.Outlets, ExposedPortSpec{
			Name:        name,
			Type:        outPort.Type,
			Target:      src,
			Description: outPort.Description,
		})
	}

	// Expose explicitly requested internal parameters.
	if len(opts.ExposeParams) > 0 {
		reqParams := append([]PortRef(nil), opts.ExposeParams...)
		slices.SortFunc(reqParams, ComparePortRef)
		reqParams = slices.Compact(reqParams)

		baseParamCount := make(map[string]int, len(reqParams))
		for _, ref := range reqParams {
			baseParamCount[ref.Port]++
		}
		usedParamNames := make(map[string]bool, len(reqParams))
		for _, ref := range reqParams {
			if !inSet[ref.Node] {
				return nil, nil, TypeDef{}, fmt.Errorf("expose param %q: %w (not in extracted node set)", ref, ErrUnknownNode)
			}
			tdef := resolvedDefs[ref.Node]
			pdef, ok := tdef.Param(ref.Port)
			if !ok {
				return nil, nil, TypeDef{}, fmt.Errorf("expose param %q: %w", ref, ErrInvalidParam)
			}
			name := allocPortName(ref, baseParamCount, usedParamNames)
			var defVal any
			if v, has := comp.Nodes[ref.Node].Params[ref.Port]; has && v != nil {
				defVal = cloneParamValue(v)
			} else if pdef.Default != nil {
				defVal = cloneParamValue(pdef.Default)
			}
			var minCopy, maxCopy *float64
			if pdef.Min != nil {
				v := *pdef.Min
				minCopy = &v
			}
			if pdef.Max != nil {
				v := *pdef.Max
				maxCopy = &v
			}
			comp.Params = append(comp.Params, ExposedParamSpec{
				Name:        name,
				Type:        pdef.Type,
				Target:      ref,
				Required:    pdef.Required && defVal == nil,
				Default:     defVal,
				Min:         minCopy,
				Max:         maxCopy,
				Enum:        append([]string(nil), pdef.Enum...),
				Description: pdef.Description,
			})
		}
	}

	comp = comp.Canonicalize()
	valOpts := withGraphNamespaceAllowed(ValidateOptions{Backend: opts.Backend})
	synthDef, err := SynthesizeTypeDef(comp, reg, valOpts)
	if err != nil {
		return nil, nil, TypeDef{}, err
	}

	// Build updatedParent with a single replacement graph/* instance node.
	nextParent := canonParent.Clone()
	var outerCaps []Capability
	for _, nid := range selected {
		outerCaps = append(outerCaps, nextParent.Nodes[nid].Capabilities...)
		delete(nextParent.Nodes, nid)
	}
	if len(outerCaps) > 0 {
		slices.SortFunc(outerCaps, CompareCapability)
		outerCaps = slices.Compact(outerCaps)
	}

	instID := opts.InstanceID
	if instID == "" {
		instID = sanitizePortID(strings.ReplaceAll(compName, ".", "_"))
	}
	if !IsValidID(instID) {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: invalid instance id %q", ErrInvalidID, instID)
	}
	if _, collision := nextParent.Nodes[instID]; collision {
		base := instID
		for n := 2; ; n++ {
			cand := sanitizePortID(base + "_" + strconv.Itoa(n))
			if _, taken := nextParent.Nodes[cand]; !taken {
				instID = cand
				break
			}
		}
	}

	var instUI *NodeUI
	if uiCount > 0 {
		instUI = &NodeUI{
			X: math.Round(sumX / float64(uiCount)),
			Y: math.Round(sumY / float64(uiCount)),
		}
	}
	nextParent.Nodes[instID] = NodeSpec{
		Type:         synthDef.ID,
		Capabilities: outerCaps,
		UI:           instUI,
	}

	// Update groups: replace extracted members in any enclosing group with instID,
	// or drop groups that had only extracted members if empty.
	for gid, g := range nextParent.Groups {
		hadSelected := false
		kept := make([]string, 0, len(g.Nodes))
		for _, m := range g.Nodes {
			if inSet[m] {
				hadSelected = true
				continue
			}
			kept = append(kept, m)
		}
		if hadSelected && len(kept) > 0 {
			kept = append(kept, instID)
		}
		if len(kept) == 0 {
			delete(nextParent.Groups, gid)
			continue
		}
		g.Nodes = kept
		nextParent.Groups[gid] = g
	}
	if len(nextParent.Groups) == 0 {
		nextParent.Groups = nil
	}

	// Rebuild parent edges: external + incoming to instID + outgoing from instID.
	newParentEdges := make([]EdgeSpec, 0, len(externalEdges)+len(inGroups)+len(outgoingEdges))
	newParentEdges = append(newParentEdges, externalEdges...)
	for i, g := range inGroups {
		newParentEdges = append(newParentEdges, EdgeSpec{
			From: g.key.From,
			To:   PortRef{Node: instID, Port: inGroupPortNames[i]},
			Kind: g.key.Kind,
		})
	}
	for _, e := range outgoingEdges {
		newParentEdges = append(newParentEdges, EdgeSpec{
			From: PortRef{Node: instID, Port: outPortNameBySource[e.From]},
			To:   e.To,
			Kind: e.Kind,
		})
	}
	nextParent.Edges = newParentEdges

	// Rewrite parent boundary Inlets/Outlets/Params if parent itself was a component!
	for i := range nextParent.Inlets {
		targets := nextParent.Inlets[i].ResolvedTargets()
		changed := false
		var rem []PortRef
		for _, t := range targets {
			if inSet[t.Node] {
				changed = true
				// Find matching exposed inlet on comp or create pass-through.
				for _, ci := range comp.Inlets {
					if slices.Contains(ci.ResolvedTargets(), t) {
						rem = append(rem, PortRef{Node: instID, Port: ci.Name})
						break
					}
				}
			} else {
				rem = append(rem, t)
			}
		}
		if changed && len(rem) > 0 {
			nextParent.Inlets[i].Target, nextParent.Inlets[i].Targets = canonicalizeTargets(PortRef{}, rem)
		}
	}
	for i := range nextParent.Outlets {
		t := nextParent.Outlets[i].Target
		if inSet[t.Node] {
			if outName, ok := outPortNameBySource[t]; ok {
				nextParent.Outlets[i].Target = PortRef{Node: instID, Port: outName}
			}
		}
	}

	nextParent = nextParent.Canonicalize()
	checkReg := reg.Clone()
	if err := checkReg.Upsert(synthDef); err != nil {
		return nil, nil, TypeDef{}, err
	}
	// Verify updatedParent (skipping only pre-existing missing-required-inlet
	// errors when extracting from an incomplete work-in-progress graph).
	if _, valErr := Validate(nextParent, checkReg, valOpts); valErr != nil {
		if !onlyMissingRequiredInletIssues(valErr) {
			return nil, nil, TypeDef{}, valErr
		}
	}

	return comp, nextParent, synthDef, nil
}

// PromoteGroupToSubgraph promotes an editor UI group into a reusable graph/*
// component Spec and replaces the group and its member nodes in parent with a
// single component instance node.
func PromoteGroupToSubgraph(
	parent *Spec,
	groupID string,
	reg *Registry,
	opts ExtractOptions,
) (compSpec *Spec, updatedParent *Spec, compDef TypeDef, err error) {
	if parent == nil {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: nil parent spec", ErrInvalidSpec)
	}
	group, ok := parent.Groups[groupID]
	if !ok {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: %q", ErrUnknownGroup, groupID)
	}
	if len(group.Nodes) == 0 {
		return nil, nil, TypeDef{}, fmt.Errorf("%w: group %q has no member nodes", ErrInvalidSpec, groupID)
	}
	extOpts := opts
	if extOpts.ComponentName == "" {
		if group.Label != "" {
			extOpts.ComponentName = sanitizeComponentID(group.Label)
		}
		if extOpts.ComponentName == "" {
			extOpts.ComponentName = sanitizeComponentID(groupID)
		}
	}
	if extOpts.InstanceID == "" {
		extOpts.InstanceID = sanitizePortID(groupID)
	}
	if extOpts.Description == "" && group.Label != "" {
		extOpts.Description = group.Label
	}

	// Detach the promoted group up front so ExtractSubgraph doesn't retain it.
	baseParent := parent.Clone()
	delete(baseParent.Groups, groupID)
	if len(baseParent.Groups) == 0 {
		baseParent.Groups = nil
	}

	comp, nextParent, def, err := ExtractSubgraph(baseParent, group.Nodes, reg, extOpts)
	if err != nil {
		return nil, nil, TypeDef{}, err
	}
	if group.Label != "" {
		for _, nid := range nextParent.SortedNodeIDs() {
			if _, existedBefore := baseParent.Nodes[nid]; existedBefore {
				continue
			}
			ns := nextParent.Nodes[nid]
			if ns.Type == def.ID && ns.UI != nil && ns.UI.Label == "" {
				ns.UI.Label = group.Label
				nextParent.Nodes[nid] = ns
				break
			}
		}
	}
	return comp, nextParent, def, nil
}

func onlyMissingRequiredInletIssues(err error) bool {
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Issues) == 0 {
		return false
	}
	for _, iss := range ve.Issues {
		if iss.Code != IssueMissingRequiredInlet {
			return false
		}
	}
	return true
}

func sanitizeComponentID(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevSep := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			prevSep = false
			continue
		}
		if b.Len() > 0 && !prevSep {
			b.WriteByte('-')
			prevSep = true
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > MaxIDLen {
		out = strings.Trim(out[:MaxIDLen], "-_")
	}
	return out
}

func sanitizePortID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "port"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if isAlphaNum {
			b.WriteByte(c)
		} else if b.Len() > 0 && (c == '_' || c == '-' || c == ' ' || c == '.') {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		return "port"
	}
	if len(out) > MaxIDLen {
		out = strings.Trim(out[:MaxIDLen], "_-")
	}
	return out
}
