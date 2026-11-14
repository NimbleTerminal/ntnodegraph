// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Sentinel errors for registry operations.
var (
	ErrDuplicateNodeType = errors.New("ntnodegraph: node type already registered")
	ErrInvalidTypeDef    = errors.New("ntnodegraph: invalid node type definition")
)

// ParamType enumerates the static types available for node parameters.
type ParamType string

const (
	ParamString   ParamType = "string"
	ParamNumber   ParamType = "number"
	ParamInt      ParamType = "int"
	ParamBool     ParamType = "bool"
	ParamDuration ParamType = "duration"
	ParamStrings  ParamType = "strings"
	ParamJSON     ParamType = "json"
)

// Valid reports whether pt is a supported parameter schema type.
func (pt ParamType) Valid() bool {
	switch pt {
	case ParamString, ParamNumber, ParamInt, ParamBool, ParamDuration, ParamStrings, ParamJSON:
		return true
	default:
		return false
	}
}

// PortDef describes a named inlet or outlet on a node type.
type PortDef struct {
	Name        string   `json:"name"`
	Type        DataType `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Description string   `json:"description,omitempty"`
}

// ParamDef describes a typed static configuration parameter on a node type.
type ParamDef struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Required    bool      `json:"required,omitempty"`
	Default     any       `json:"default,omitempty"`
	Min         *float64  `json:"min,omitempty"`
	Max         *float64  `json:"max,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Description string    `json:"description,omitempty"`
}

// FloatBound returns a pointer to v for configuring ParamDef.Min or ParamDef.Max.
func FloatBound(v float64) *float64 {
	return &v
}

// CapabilityRule declares a host capability requirement for a node type.
//
// When HostFromURLParam is set (for example "url" on "source/http@1"),
// validation parses that parameter as a URL and requires the node's
// capabilities list to cover "<Kind>:<hostname>".
type CapabilityRule struct {
	Kind             string `json:"kind"`
	Scope            string `json:"scope,omitempty"`
	RequireScope     bool   `json:"require_scope,omitempty"`
	HostFromURLParam string `json:"host_from_url_param,omitempty"`
	Description      string `json:"description,omitempty"`
}

// TypeDef is the complete static schema of a node type in a Registry.
type TypeDef struct {
	ID           TypeID           `json:"id"`
	Description  string           `json:"description,omitempty"`
	Inlets       []PortDef        `json:"inlets,omitempty"`
	Outlets      []PortDef        `json:"outlets,omitempty"`
	Params       []ParamDef       `json:"params,omitempty"`
	Capabilities []CapabilityRule `json:"capabilities,omitempty"`
	Backends     []string         `json:"backends,omitempty"`
}

// Inlet looks up an inlet definition by port name.
func (d TypeDef) Inlet(name string) (PortDef, bool) {
	for _, p := range d.Inlets {
		if p.Name == name {
			return p, true
		}
	}
	return PortDef{}, false
}

// Outlet looks up an outlet definition by port name.
func (d TypeDef) Outlet(name string) (PortDef, bool) {
	for _, p := range d.Outlets {
		if p.Name == name {
			return p, true
		}
	}
	return PortDef{}, false
}

// Param looks up a parameter definition by name.
func (d TypeDef) Param(name string) (ParamDef, bool) {
	for _, p := range d.Params {
		if p.Name == name {
			return p, true
		}
	}
	return ParamDef{}, false
}

// SupportsBackend reports whether d may execute on backendName. An empty
// Backends slice means the node type is backend-agnostic.
func (d TypeDef) SupportsBackend(backendName string) bool {
	if len(d.Backends) == 0 || backendName == "" {
		return true
	}
	return slices.Contains(d.Backends, backendName)
}

// ValidateTypeDef checks that a TypeDef itself is internally consistent before
// registration.
func ValidateTypeDef(def TypeDef) error {
	if !def.ID.Valid() {
		return fmt.Errorf("%w: invalid TypeID %q", ErrInvalidTypeDef, def.ID)
	}
	seenIn := make(map[string]struct{}, len(def.Inlets))
	for _, in := range def.Inlets {
		if !IsValidID(in.Name) {
			return fmt.Errorf("%w: %s: invalid inlet name %q", ErrInvalidTypeDef, def.ID, in.Name)
		}
		if _, dup := seenIn[in.Name]; dup {
			return fmt.Errorf("%w: %s: duplicate inlet %q", ErrInvalidTypeDef, def.ID, in.Name)
		}
		seenIn[in.Name] = struct{}{}
		if !in.Type.Valid() {
			return fmt.Errorf("%w: %s inlet %q: invalid type %q", ErrInvalidTypeDef, def.ID, in.Name, in.Type)
		}
	}
	seenOut := make(map[string]struct{}, len(def.Outlets))
	for _, out := range def.Outlets {
		if !IsValidID(out.Name) {
			return fmt.Errorf("%w: %s: invalid outlet name %q", ErrInvalidTypeDef, def.ID, out.Name)
		}
		if _, dup := seenOut[out.Name]; dup {
			return fmt.Errorf("%w: %s: duplicate outlet %q", ErrInvalidTypeDef, def.ID, out.Name)
		}
		seenOut[out.Name] = struct{}{}
		if !out.Type.Valid() {
			return fmt.Errorf("%w: %s outlet %q: invalid type %q", ErrInvalidTypeDef, def.ID, out.Name, out.Type)
		}
	}
	seenParam := make(map[string]struct{}, len(def.Params))
	for _, p := range def.Params {
		if !IsValidID(p.Name) {
			return fmt.Errorf("%w: %s: invalid param name %q", ErrInvalidTypeDef, def.ID, p.Name)
		}
		if _, dup := seenParam[p.Name]; dup {
			return fmt.Errorf("%w: %s: duplicate param %q", ErrInvalidTypeDef, def.ID, p.Name)
		}
		seenParam[p.Name] = struct{}{}
		if !p.Type.Valid() {
			return fmt.Errorf("%w: %s param %q: invalid param type %q", ErrInvalidTypeDef, def.ID, p.Name, p.Type)
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("%w: %s param %q: min > max", ErrInvalidTypeDef, def.ID, p.Name)
		}
		if p.Default != nil {
			if err := validateParamValue(p, p.Default); err != nil {
				return fmt.Errorf("%w: %s param %q default: %v", ErrInvalidTypeDef, def.ID, p.Name, err)
			}
		}
	}
	for _, cr := range def.Capabilities {
		if !isValidNamespace(cr.Kind) {
			return fmt.Errorf("%w: %s: invalid capability rule kind %q", ErrInvalidTypeDef, def.ID, cr.Kind)
		}
		if cr.HostFromURLParam != "" {
			if _, ok := seenParam[cr.HostFromURLParam]; !ok {
				return fmt.Errorf("%w: %s: capability rule references unknown param %q", ErrInvalidTypeDef, def.ID, cr.HostFromURLParam)
			}
		}
	}
	for _, b := range def.Backends {
		if !isValidNamespace(b) {
			return fmt.Errorf("%w: %s: invalid backend name %q", ErrInvalidTypeDef, def.ID, b)
		}
	}
	return nil
}

func cloneTypeDef(d TypeDef) TypeDef {
	out := TypeDef{
		ID:          d.ID,
		Description: d.Description,
	}
	if len(d.Inlets) > 0 {
		out.Inlets = append([]PortDef(nil), d.Inlets...)
	}
	if len(d.Outlets) > 0 {
		out.Outlets = append([]PortDef(nil), d.Outlets...)
	}
	if len(d.Params) > 0 {
		out.Params = make([]ParamDef, len(d.Params))
		for i, p := range d.Params {
			pc := p
			if p.Min != nil {
				v := *p.Min
				pc.Min = &v
			}
			if p.Max != nil {
				v := *p.Max
				pc.Max = &v
			}
			if len(p.Enum) > 0 {
				pc.Enum = append([]string(nil), p.Enum...)
			}
			pc.Default = cloneParamValue(p.Default)
			out.Params[i] = pc
		}
	}
	if len(d.Capabilities) > 0 {
		out.Capabilities = append([]CapabilityRule(nil), d.Capabilities...)
	}
	if len(d.Backends) > 0 {
		out.Backends = append([]string(nil), d.Backends...)
	}
	return out
}

// BackendProfile announces what a particular runtime backend (such as Nebula)
// supports when validating a Spec.
type BackendProfile struct {
	Name                   string   `json:"name"`
	EdgeKinds              []string `json:"edge_kinds,omitempty"`
	DefaultEdgeKind        string   `json:"default_edge_kind,omitempty"`
	AllowCycles            bool     `json:"allow_cycles,omitempty"`
	CycleBreakingEdgeKinds []string `json:"cycle_breaking_edge_kinds,omitempty"`
	Namespaces             []string `json:"namespaces,omitempty"`
	Capabilities           []string `json:"capabilities,omitempty"`

	// CycleBreakingEdge optionally reports whether a resolved edge breaks
	// instantaneous evaluation cycles on this backend (for example, an edge
	// targeting a cold/state-only inlet in a push dataflow runtime).
	CycleBreakingEdge func(edge EdgeSpec, fromDef, toDef TypeDef) bool `json:"-"`
}

// Registry is a concurrency-safe catalog of registered node TypeDefs for
// validation, editor palettes, and LLM/MCP introspection.
type Registry struct {
	mu    sync.RWMutex
	types map[TypeID]TypeDef
}

// NewRegistry creates an empty node type Registry.
func NewRegistry() *Registry {
	return &Registry{
		types: make(map[TypeID]TypeDef),
	}
}

// Register validates and inserts def into r, rejecting duplicates.
func (r *Registry) Register(def TypeDef) error {
	if err := ValidateTypeDef(def); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.types[def.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateNodeType, def.ID)
	}
	r.types[def.ID] = cloneTypeDef(def)
	return nil
}

// MustRegister registers def and panics on error.
func (r *Registry) MustRegister(def TypeDef) {
	if err := r.Register(def); err != nil {
		panic(err)
	}
}

// Upsert validates and inserts or replaces def in r. Used when live-updating
// reusable graph/* component definitions.
func (r *Registry) Upsert(def TypeDef) error {
	if err := ValidateTypeDef(def); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types[def.ID] = cloneTypeDef(def)
	return nil
}

// Delete removes id from r, returning true if it was present.
func (r *Registry) Delete(id TypeID) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.types[id]; !ok {
		return false
	}
	delete(r.types, id)
	return true
}

// Clone returns an independent deep copy of r.
func (r *Registry) Clone() *Registry {
	out := NewRegistry()
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, def := range r.types {
		out.types[id] = cloneTypeDef(def)
	}
	return out
}

// Lookup returns a copy of the TypeDef registered under id.
func (r *Registry) Lookup(id TypeID) (TypeDef, bool) {
	if r == nil {
		return TypeDef{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.types[id]
	if !ok {
		return TypeDef{}, false
	}
	return cloneTypeDef(def), true
}

// Types returns all registered TypeDefs in deterministic TypeID order.
func (r *Registry) Types() []TypeDef {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TypeDef, 0, len(r.types))
	for _, def := range r.types {
		out = append(out, cloneTypeDef(def))
	}
	slices.SortFunc(out, func(a, b TypeDef) int {
		return CompareTypeID(a.ID, b.ID)
	})
	return out
}

// ForBackend returns all registered TypeDefs compatible with backendName in
// deterministic TypeID order.
func (r *Registry) ForBackend(backendName string) []TypeDef {
	all := r.Types()
	out := make([]TypeDef, 0, len(all))
	for _, def := range all {
		if def.SupportsBackend(backendName) {
			out = append(out, def)
		}
	}
	slices.SortFunc(out, func(a, b TypeDef) int {
		if c := cmp.Compare(a.ID.Namespace, b.ID.Namespace); c != 0 {
			return c
		}
		return CompareTypeID(a.ID, b.ID)
	})
	return out
}

// CatalogJSON exports the deterministic JSON representation of all registered
// node types for editor palettes and LLM tools.
func (r *Registry) CatalogJSON() ([]byte, error) {
	payload := struct {
		Types []TypeDef `json:"types"`
	}{
		Types: r.Types(),
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
