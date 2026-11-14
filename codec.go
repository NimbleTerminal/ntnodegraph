// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Wire structs keep explicit required/presence tracking so YAML and JSON share
// identical strict decoding semantics.
type rawSpec struct {
	Version          *int                    `json:"version" yaml:"version"`
	Name             string                  `json:"name" yaml:"name"`
	ComponentVersion *int                    `json:"component_version,omitempty" yaml:"component_version,omitempty"`
	Description      string                  `json:"description,omitempty" yaml:"description,omitempty"`
	Inlets           []rawExposedInlet       `json:"inlets,omitempty" yaml:"inlets,omitempty"`
	Outlets          []rawExposedOutlet      `json:"outlets,omitempty" yaml:"outlets,omitempty"`
	Params           []rawExposedParam       `json:"params,omitempty" yaml:"params,omitempty"`
	Groups           map[string]rawGroupSpec `json:"groups,omitempty" yaml:"groups,omitempty"`
	Nodes            map[string]rawNodeSpec  `json:"nodes" yaml:"nodes"`
	Edges            []rawEdgeSpec           `json:"edges" yaml:"edges"`
}

type rawExposedInlet struct {
	Name        string    `json:"name" yaml:"name"`
	Type        string    `json:"type" yaml:"type"`
	Target      *string   `json:"target,omitempty" yaml:"target,omitempty"`
	Targets     *[]string `json:"targets,omitempty" yaml:"targets,omitempty"`
	Required    bool      `json:"required,omitempty" yaml:"required,omitempty"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
}

type rawExposedOutlet struct {
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Target      string `json:"target" yaml:"target"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type rawExposedParam struct {
	Name        string    `json:"name" yaml:"name"`
	Type        string    `json:"type" yaml:"type"`
	Target      *string   `json:"target,omitempty" yaml:"target,omitempty"`
	Targets     *[]string `json:"targets,omitempty" yaml:"targets,omitempty"`
	Required    bool      `json:"required,omitempty" yaml:"required,omitempty"`
	Default     any       `json:"default,omitempty" yaml:"default,omitempty"`
	Min         *float64  `json:"min,omitempty" yaml:"min,omitempty"`
	Max         *float64  `json:"max,omitempty" yaml:"max,omitempty"`
	Enum        []string  `json:"enum,omitempty" yaml:"enum,omitempty"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
}

type rawGroupSpec struct {
	Label     string      `json:"label,omitempty" yaml:"label,omitempty"`
	Nodes     []string    `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	Collapsed bool        `json:"collapsed,omitempty" yaml:"collapsed,omitempty"`
	UI        *rawGroupUI `json:"ui,omitempty" yaml:"ui,omitempty"`
}

type rawGroupUI struct {
	X      float64 `json:"x" yaml:"x"`
	Y      float64 `json:"y" yaml:"y"`
	Width  float64 `json:"width,omitempty" yaml:"width,omitempty"`
	Height float64 `json:"height,omitempty" yaml:"height,omitempty"`
}

type rawNodeSpec struct {
	Type         string         `json:"type" yaml:"type"`
	Capabilities []string       `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Params       map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
	UI           *rawNodeUI     `json:"ui,omitempty" yaml:"ui,omitempty"`
}

type rawNodeUI struct {
	X         float64 `json:"x" yaml:"x"`
	Y         float64 `json:"y" yaml:"y"`
	Label     string  `json:"label,omitempty" yaml:"label,omitempty"`
	Collapsed bool    `json:"collapsed,omitempty" yaml:"collapsed,omitempty"`
}

type rawEdgeSpec struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`
}

// ParseYAML parses and structurally normalizes a YAML node-graph Spec using DefaultParseLimits.
func ParseYAML(data []byte) (*Spec, error) {
	return ParseYAMLWithLimits(data, DefaultParseLimits)
}

// ParseYAMLWithLimits parses a YAML node-graph Spec under explicit resource bounds.
// Unknown fields, YAML aliases/anchors that expand unboundedly, and trailing
// YAML documents are rejected.
func ParseYAMLWithLimits(data []byte, limits ParseLimits) (*Spec, error) {
	lim := limits.withDefaults()
	if len(data) > lim.MaxBytes {
		return nil, fmt.Errorf("%w: %d bytes > %d max", ErrSpecTooLarge, len(data), lim.MaxBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty YAML input", ErrInvalidSpec)
	}

	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: yaml decode: %v", ErrInvalidSpec, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: multiple YAML documents are not allowed", ErrInvalidSpec)
	}
	if err := validateYAMLTree(&doc, 0, lim.MaxDepth+8); err != nil {
		return nil, err
	}

	strictDec := yaml.NewDecoder(bytes.NewReader(data))
	strictDec.KnownFields(true)
	var raw rawSpec
	if err := strictDec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: yaml decode: %v", ErrInvalidSpec, err)
	}
	return convertRawSpec(&raw, lim)
}

func validateYAMLTree(n *yaml.Node, depth, maxDepth int) error {
	if n == nil {
		return nil
	}
	if depth > maxDepth {
		return fmt.Errorf("%w: YAML nesting exceeds depth %d", ErrInvalidSpec, maxDepth)
	}
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("%w: YAML aliases are forbidden in spec documents", ErrInvalidSpec)
	}
	if n.Anchor != "" {
		return fmt.Errorf("%w: YAML anchors are forbidden in spec documents", ErrInvalidSpec)
	}
	if n.Tag == "!!merge" {
		return fmt.Errorf("%w: YAML merge keys are forbidden in spec documents", ErrInvalidSpec)
	}
	if n.Kind == yaml.ScalarNode {
		switch n.Tag {
		case "!!int":
			if !isCanonicalDecimalInt(n.Value) {
				return fmt.Errorf("%w: non-decimal or zero-padded YAML integer %q", ErrInvalidSpec, n.Value)
			}
			if _, err := strconv.ParseInt(n.Value, 10, 64); err != nil {
				return fmt.Errorf("%w: integer %q overflows int64", ErrInvalidSpec, n.Value)
			}
		case "!!float":
			if !isCanonicalDecimalFloat(n.Value) {
				return fmt.Errorf("%w: invalid YAML float literal %q", ErrInvalidSpec, n.Value)
			}
			f, err := strconv.ParseFloat(n.Value, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("%w: non-finite or out-of-range float %q", ErrInvalidSpec, n.Value)
			}
		}
	}
	for _, child := range n.Content {
		if err := validateYAMLTree(child, depth+1, maxDepth); err != nil {
			return err
		}
	}
	return nil
}

func isCanonicalDecimalInt(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	if s == "0" {
		return true
	}
	if s[0] < '1' || s[0] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isCanonicalDecimalFloat(s string) bool {
	if s == "" || !strings.ContainsAny(s, ".eE") {
		return false
	}
	i := 0
	if s[i] == '-' {
		i++
		if i >= len(s) {
			return false
		}
	}
	if s[i] == '0' {
		i++
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			return false
		}
	} else if s[i] >= '1' && s[i] <= '9' {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		startFrac := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == startFrac {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		startExp := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == startExp {
			return false
		}
	}
	return i == len(s)
}

// ParseJSON parses and structurally normalizes a JSON node-graph Spec using DefaultParseLimits.
func ParseJSON(data []byte) (*Spec, error) {
	return ParseJSONWithLimits(data, DefaultParseLimits)
}

// ParseJSONWithLimits parses a JSON node-graph Spec under explicit resource bounds.
// Unknown fields, duplicate object keys, and trailing tokens are rejected.
func ParseJSONWithLimits(data []byte, limits ParseLimits) (*Spec, error) {
	lim := limits.withDefaults()
	if len(data) > lim.MaxBytes {
		return nil, fmt.Errorf("%w: %d bytes > %d max", ErrSpecTooLarge, len(data), lim.MaxBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty JSON input", ErrInvalidSpec)
	}

	if err := checkJSONDuplicateKeysAndDepth(data, lim.MaxDepth+8); err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var raw rawSpec
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: json decode: %v", ErrInvalidSpec, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: unexpected trailing JSON data", ErrInvalidSpec)
	}
	if err := convertJSONNumbers(&raw); err != nil {
		return nil, err
	}
	return convertRawSpec(&raw, lim)
}

func checkJSONDuplicateKeysAndDepth(data []byte, maxDepth int) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return scanJSONValue(dec, 0, maxDepth)
}

func scanJSONValue(dec *json.Decoder, depth, maxDepth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: JSON nesting exceeds depth %d", ErrInvalidSpec, maxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: json syntax: %v", ErrInvalidSpec, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%w: json key: %v", ErrInvalidSpec, err)
			}
			key, ok := kt.(string)
			if !ok {
				return fmt.Errorf("%w: expected string object key", ErrInvalidSpec)
			}
			if _, dup := seen[key]; dup {
				return fmt.Errorf("%w: duplicate JSON object key %q", ErrInvalidSpec, key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("%w: json object close: %v", ErrInvalidSpec, err)
		}
	case '[':
		for dec.More() {
			if err := scanJSONValue(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("%w: json array close: %v", ErrInvalidSpec, err)
		}
	}
	return nil
}

func convertJSONNumbers(raw *rawSpec) error {
	for id, n := range raw.Nodes {
		if len(n.Params) == 0 {
			continue
		}
		converted, err := convertJSONNumbersMap(n.Params)
		if err != nil {
			return fmt.Errorf("node %q: %w", id, err)
		}
		n.Params = converted
		raw.Nodes[id] = n
	}
	for i := range raw.Params {
		if raw.Params[i].Default == nil {
			continue
		}
		cv, err := convertJSONNumberValue(raw.Params[i].Default)
		if err != nil {
			return fmt.Errorf("exposed param %q default: %w", raw.Params[i].Name, err)
		}
		raw.Params[i].Default = cv
	}
	return nil
}

func convertJSONNumbersMap(m map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		cv, err := convertJSONNumberValue(v)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		out[k] = cv
	}
	return out, nil
}

func convertJSONNumberValue(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		s := x.String()
		if !strings.ContainsAny(s, ".eE") {
			i, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: integer %q overflows int64", ErrInvalidSpec, s)
			}
			return i, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%w: invalid number %q", ErrInvalidSpec, s)
		}
		return f, nil
	case map[string]any:
		return convertJSONNumbersMap(x)
	case []any:
		out := make([]any, len(x))
		for i, elem := range x {
			ce, err := convertJSONNumberValue(elem)
			if err != nil {
				return nil, err
			}
			out[i] = ce
		}
		return out, nil
	default:
		return v, nil
	}
}

// Parse auto-detects JSON (leading '{') vs YAML and parses a Spec.
func Parse(data []byte) (*Spec, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return ParseJSON(data)
	}
	return ParseYAML(data)
}

func convertRawSpec(raw *rawSpec, lim ParseLimits) (*Spec, error) {
	if raw.Version == nil {
		return nil, fmt.Errorf("%w: missing required field \"version\"", ErrInvalidSpec)
	}
	if *raw.Version != SpecVersion {
		return nil, fmt.Errorf("%w: got version %d, supported is %d", ErrUnsupportedVersion, *raw.Version, SpecVersion)
	}
	if !IsValidID(raw.Name) {
		return nil, fmt.Errorf("%w: invalid spec name %q", ErrInvalidID, raw.Name)
	}
	if raw.ComponentVersion != nil && *raw.ComponentVersion < 1 {
		return nil, fmt.Errorf("%w: component_version must be >= 1, got %d", ErrInvalidSpec, *raw.ComponentVersion)
	}
	if len(raw.Nodes) > lim.MaxNodes {
		return nil, fmt.Errorf("%w: %d nodes > %d max", ErrInvalidSpec, len(raw.Nodes), lim.MaxNodes)
	}
	if len(raw.Edges) > lim.MaxEdges {
		return nil, fmt.Errorf("%w: %d edges > %d max", ErrInvalidSpec, len(raw.Edges), lim.MaxEdges)
	}
	if len(raw.Groups) > lim.MaxGroups {
		return nil, fmt.Errorf("%w: %d groups > %d max", ErrInvalidSpec, len(raw.Groups), lim.MaxGroups)
	}

	spec := &Spec{
		Version:     *raw.Version,
		Name:        raw.Name,
		Description: raw.Description,
		Nodes:       make(map[string]NodeSpec, len(raw.Nodes)),
		Edges:       make([]EdgeSpec, 0, len(raw.Edges)),
	}
	if raw.ComponentVersion != nil {
		spec.ComponentVersion = *raw.ComponentVersion
	}

	inlets, err := convertExposedInlets(raw.Inlets)
	if err != nil {
		return nil, err
	}
	spec.Inlets = inlets

	outlets, err := convertExposedOutlets(raw.Outlets)
	if err != nil {
		return nil, err
	}
	spec.Outlets = outlets

	params, err := convertExposedParams(raw.Params, lim.MaxDepth)
	if err != nil {
		return nil, err
	}
	spec.Params = params

	if len(raw.Groups) > 0 {
		spec.Groups = make(map[string]GroupSpec, len(raw.Groups))
		groupIDs := make([]string, 0, len(raw.Groups))
		for id := range raw.Groups {
			groupIDs = append(groupIDs, id)
		}
		slices.Sort(groupIDs)
		for _, id := range groupIDs {
			if !IsValidID(id) {
				return nil, fmt.Errorf("%w: invalid group id %q", ErrInvalidID, id)
			}
			rg := raw.Groups[id]
			for _, member := range rg.Nodes {
				if !IsValidID(member) {
					return nil, fmt.Errorf("%w: group %q references invalid node id %q", ErrInvalidID, id, member)
				}
			}
			g := GroupSpec{
				Label:     rg.Label,
				Nodes:     append([]string(nil), rg.Nodes...),
				Collapsed: rg.Collapsed,
			}
			if rg.UI != nil {
				if err := checkFiniteUI(rg.UI.X, rg.UI.Y); err != nil {
					return nil, fmt.Errorf("group %q: %w", id, err)
				}
				if err := checkFiniteUI(rg.UI.Width, rg.UI.Height); err != nil {
					return nil, fmt.Errorf("group %q: %w", id, err)
				}
				if rg.UI.Width < 0 || rg.UI.Height < 0 {
					return nil, fmt.Errorf("%w: group %q has negative dimensions", ErrInvalidSpec, id)
				}
				g.UI = &GroupUI{
					X:      rg.UI.X,
					Y:      rg.UI.Y,
					Width:  rg.UI.Width,
					Height: rg.UI.Height,
				}
			}
			spec.Groups[id] = g
		}
	}

	nodeIDs := make([]string, 0, len(raw.Nodes))
	for id := range raw.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	slices.Sort(nodeIDs)
	for _, id := range nodeIDs {
		if !IsValidID(id) {
			return nil, fmt.Errorf("%w: invalid node id %q", ErrInvalidID, id)
		}
		rn := raw.Nodes[id]
		tid, err := ParseTypeID(rn.Type)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", id, err)
		}
		var caps []Capability
		if len(rn.Capabilities) > 0 {
			caps = make([]Capability, 0, len(rn.Capabilities))
			for _, cs := range rn.Capabilities {
				c, err := ParseCapability(cs)
				if err != nil {
					return nil, fmt.Errorf("node %q: %w", id, err)
				}
				caps = append(caps, c)
			}
		}
		params, err := normalizeNodeParams(rn.Params, lim.MaxDepth)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", id, err)
		}
		var ui *NodeUI
		if rn.UI != nil {
			if err := checkFiniteUI(rn.UI.X, rn.UI.Y); err != nil {
				return nil, fmt.Errorf("node %q: %w", id, err)
			}
			ui = &NodeUI{
				X:         rn.UI.X,
				Y:         rn.UI.Y,
				Label:     rn.UI.Label,
				Collapsed: rn.UI.Collapsed,
			}
		}
		spec.Nodes[id] = NodeSpec{
			Type:         tid,
			Capabilities: caps,
			Params:       params,
			UI:           ui,
		}
	}

	for i, re := range raw.Edges {
		fromRef, err := ParsePortRef(re.From)
		if err != nil {
			return nil, fmt.Errorf("edge[%d].from: %w", i, err)
		}
		toRef, err := ParsePortRef(re.To)
		if err != nil {
			return nil, fmt.Errorf("edge[%d].to: %w", i, err)
		}
		if re.Kind != "" && !isValidNamespace(re.Kind) {
			return nil, fmt.Errorf("%w: edge[%d] has invalid edge kind %q", ErrInvalidSpec, i, re.Kind)
		}
		spec.Edges = append(spec.Edges, EdgeSpec{
			From: fromRef,
			To:   toRef,
			Kind: re.Kind,
		})
	}

	return spec, nil
}

func parseOneOfTargets(label, name string, target *string, targets *[]string) (PortRef, []PortRef, error) {
	if target != nil && targets != nil {
		return PortRef{}, nil, fmt.Errorf("%w: exposed %s %q must specify either target or targets, not both", ErrInvalidSpec, label, name)
	}
	if target == nil && targets == nil {
		return PortRef{}, nil, fmt.Errorf("%w: exposed %s %q must specify target or targets", ErrInvalidSpec, label, name)
	}
	if target != nil {
		ref, err := ParsePortRef(*target)
		if err != nil {
			return PortRef{}, nil, fmt.Errorf("exposed %s %q: %w", label, name, err)
		}
		return ref, nil, nil
	}
	rawList := *targets
	if len(rawList) == 0 {
		return PortRef{}, nil, fmt.Errorf("%w: exposed %s %q targets must be non-empty", ErrInvalidSpec, label, name)
	}
	parsed := make([]PortRef, 0, len(rawList))
	seen := make(map[PortRef]struct{}, len(rawList))
	for j, ts := range rawList {
		ref, err := ParsePortRef(ts)
		if err != nil {
			return PortRef{}, nil, fmt.Errorf("exposed %s %q targets[%d]: %w", label, name, j, err)
		}
		if _, dup := seen[ref]; dup {
			return PortRef{}, nil, fmt.Errorf("%w: exposed %s %q has duplicate target %q", ErrInvalidSpec, label, name, ref)
		}
		seen[ref] = struct{}{}
		parsed = append(parsed, ref)
	}
	single, multi := canonicalizeTargets(PortRef{}, parsed)
	return single, multi, nil
}

func convertExposedInlets(raw []rawExposedInlet) ([]ExposedPortSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]ExposedPortSpec, 0, len(raw))
	for i, rp := range raw {
		if !IsValidID(rp.Name) {
			return nil, fmt.Errorf("exposed inlet[%d]: %w %q", i, ErrInvalidID, rp.Name)
		}
		if _, dup := seen[rp.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate exposed inlet %q", ErrInvalidSpec, rp.Name)
		}
		seen[rp.Name] = struct{}{}
		dt, err := ParseDataType(rp.Type)
		if err != nil {
			return nil, fmt.Errorf("exposed inlet %q: %w", rp.Name, err)
		}
		single, multi, err := parseOneOfTargets("inlet", rp.Name, rp.Target, rp.Targets)
		if err != nil {
			return nil, err
		}
		out = append(out, ExposedPortSpec{
			Name:        rp.Name,
			Type:        dt,
			Target:      single,
			Targets:     multi,
			Required:    rp.Required,
			Description: rp.Description,
		})
	}
	return out, nil
}

func convertExposedOutlets(raw []rawExposedOutlet) ([]ExposedPortSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]ExposedPortSpec, 0, len(raw))
	for i, rp := range raw {
		if !IsValidID(rp.Name) {
			return nil, fmt.Errorf("exposed outlet[%d]: %w %q", i, ErrInvalidID, rp.Name)
		}
		if _, dup := seen[rp.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate exposed outlet %q", ErrInvalidSpec, rp.Name)
		}
		seen[rp.Name] = struct{}{}
		dt, err := ParseDataType(rp.Type)
		if err != nil {
			return nil, fmt.Errorf("exposed outlet %q: %w", rp.Name, err)
		}
		target, err := ParsePortRef(rp.Target)
		if err != nil {
			return nil, fmt.Errorf("exposed outlet %q: %w", rp.Name, err)
		}
		out = append(out, ExposedPortSpec{
			Name:        rp.Name,
			Type:        dt,
			Target:      target,
			Description: rp.Description,
		})
	}
	return out, nil
}

func convertExposedParams(raw []rawExposedParam, maxDepth int) ([]ExposedParamSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]ExposedParamSpec, 0, len(raw))
	for i, rp := range raw {
		if !IsValidID(rp.Name) {
			return nil, fmt.Errorf("exposed param[%d]: %w %q", i, ErrInvalidID, rp.Name)
		}
		if _, dup := seen[rp.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate exposed param %q", ErrInvalidSpec, rp.Name)
		}
		seen[rp.Name] = struct{}{}
		pt := ParamType(rp.Type)
		if !pt.Valid() {
			return nil, fmt.Errorf("%w: exposed param %q has invalid type %q", ErrInvalidSpec, rp.Name, rp.Type)
		}
		single, multi, err := parseOneOfTargets("param", rp.Name, rp.Target, rp.Targets)
		if err != nil {
			return nil, err
		}
		var normDefault any
		if rp.Default != nil {
			nv, err := normalizeParamValue(rp.Default, 0, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("exposed param %q default: %w", rp.Name, err)
			}
			normDefault = nv
		}
		var minPtr, maxPtr *float64
		if rp.Min != nil {
			if math.IsNaN(*rp.Min) || math.IsInf(*rp.Min, 0) {
				return nil, fmt.Errorf("%w: exposed param %q has non-finite min", ErrInvalidSpec, rp.Name)
			}
			v := *rp.Min
			minPtr = &v
		}
		if rp.Max != nil {
			if math.IsNaN(*rp.Max) || math.IsInf(*rp.Max, 0) {
				return nil, fmt.Errorf("%w: exposed param %q has non-finite max", ErrInvalidSpec, rp.Name)
			}
			v := *rp.Max
			maxPtr = &v
		}
		if minPtr != nil && maxPtr != nil && *minPtr > *maxPtr {
			return nil, fmt.Errorf("%w: exposed param %q min %v > max %v", ErrInvalidSpec, rp.Name, *minPtr, *maxPtr)
		}
		var enumCopy []string
		if len(rp.Enum) > 0 {
			enumCopy = append([]string(nil), rp.Enum...)
		}
		out = append(out, ExposedParamSpec{
			Name:        rp.Name,
			Type:        pt,
			Target:      single,
			Targets:     multi,
			Required:    rp.Required,
			Default:     normDefault,
			Min:         minPtr,
			Max:         maxPtr,
			Enum:        enumCopy,
			Description: rp.Description,
		})
	}
	return out, nil
}

// MarshalYAML encodes s into canonical, diff-stable YAML (2-space indentation,
// deterministic field, map-key, capability, and edge ordering). It does not
// mutate s.
func MarshalYAML(s *Spec) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil spec", ErrInvalidSpec)
	}
	canon := s.Canonicalize()
	if err := preflightEncode(canon); err != nil {
		return nil, err
	}

	root := &yaml.Node{Kind: yaml.MappingNode}
	appendYAMLKV(root, "version", yamlInt(canon.Version))
	appendYAMLKV(root, "name", yamlScalarString(canon.Name))
	if canon.ComponentVersion > 0 {
		appendYAMLKV(root, "component_version", yamlInt(canon.ComponentVersion))
	}
	if canon.Description != "" {
		appendYAMLKV(root, "description", yamlScalarString(canon.Description))
	}
	if len(canon.Inlets) > 0 {
		appendYAMLKV(root, "inlets", encodeExposedPortsYAML(canon.Inlets))
	}
	if len(canon.Outlets) > 0 {
		appendYAMLKV(root, "outlets", encodeExposedPortsYAML(canon.Outlets))
	}
	if len(canon.Params) > 0 {
		paramsNode, err := encodeExposedParamsYAML(canon.Params)
		if err != nil {
			return nil, err
		}
		appendYAMLKV(root, "params", paramsNode)
	}
	if len(canon.Groups) > 0 {
		groupsMap := &yaml.Node{Kind: yaml.MappingNode}
		for _, gid := range canon.SortedGroupIDs() {
			appendYAMLKV(groupsMap, gid, encodeGroupYAML(canon.Groups[gid]))
		}
		appendYAMLKV(root, "groups", groupsMap)
	}

	nodesMap := &yaml.Node{Kind: yaml.MappingNode}
	for _, nid := range canon.SortedNodeIDs() {
		nNode, err := encodeNodeYAML(canon.Nodes[nid])
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", nid, err)
		}
		appendYAMLKV(nodesMap, nid, nNode)
	}
	appendYAMLKV(root, "nodes", nodesMap)

	edgesSeq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, e := range canon.Edges {
		em := &yaml.Node{Kind: yaml.MappingNode}
		appendYAMLKV(em, "from", yamlScalarString(e.From.String()))
		appendYAMLKV(em, "to", yamlScalarString(e.To.String()))
		if e.Kind != "" {
			appendYAMLKV(em, "kind", yamlScalarString(e.Kind))
		}
		edgesSeq.Content = append(edgesSeq.Content, em)
	}
	appendYAMLKV(root, "edges", edgesSeq)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func preflightEncode(s *Spec) error {
	if s.Version != SpecVersion {
		return fmt.Errorf("%w: got version %d, supported is %d", ErrUnsupportedVersion, s.Version, SpecVersion)
	}
	if !IsValidID(s.Name) {
		return fmt.Errorf("%w: invalid spec name %q", ErrInvalidID, s.Name)
	}
	if s.ComponentVersion < 0 {
		return fmt.Errorf("%w: invalid negative component_version %d", ErrInvalidSpec, s.ComponentVersion)
	}
	for _, id := range s.SortedNodeIDs() {
		if !IsValidID(id) {
			return fmt.Errorf("%w: invalid node id %q", ErrInvalidID, id)
		}
		n := s.Nodes[id]
		if !n.Type.Valid() {
			return fmt.Errorf("node %q: %w: %q", id, ErrInvalidTypeID, n.Type)
		}
		for _, c := range n.Capabilities {
			if !c.Valid() {
				return fmt.Errorf("node %q: %w: %q", id, ErrInvalidCapability, c)
			}
		}
		if _, err := normalizeNodeParams(n.Params, DefaultMaxDepth); err != nil {
			return fmt.Errorf("node %q: %w", id, err)
		}
		if n.UI != nil {
			if err := checkFiniteUI(n.UI.X, n.UI.Y); err != nil {
				return fmt.Errorf("node %q: %w", id, err)
			}
		}
	}
	for i, e := range s.Edges {
		if !e.From.Valid() {
			return fmt.Errorf("edge[%d].from: %w: %q", i, ErrInvalidPortRef, e.From)
		}
		if !e.To.Valid() {
			return fmt.Errorf("edge[%d].to: %w: %q", i, ErrInvalidPortRef, e.To)
		}
		if e.Kind != "" && !isValidNamespace(e.Kind) {
			return fmt.Errorf("%w: edge[%d] invalid kind %q", ErrInvalidSpec, i, e.Kind)
		}
	}
	for _, id := range s.SortedGroupIDs() {
		if !IsValidID(id) {
			return fmt.Errorf("%w: invalid group id %q", ErrInvalidID, id)
		}
		g := s.Groups[id]
		for _, member := range g.Nodes {
			if !IsValidID(member) {
				return fmt.Errorf("group %q: %w %q", id, ErrInvalidID, member)
			}
		}
		if g.UI != nil {
			if err := checkFiniteUI(g.UI.X, g.UI.Y); err != nil {
				return fmt.Errorf("group %q: %w", id, err)
			}
			if err := checkFiniteUI(g.UI.Width, g.UI.Height); err != nil {
				return fmt.Errorf("group %q: %w", id, err)
			}
		}
	}
	seenIn := make(map[string]struct{}, len(s.Inlets))
	for _, p := range s.Inlets {
		if !IsValidID(p.Name) || !p.Type.Valid() {
			return fmt.Errorf("%w: invalid exposed inlet %q", ErrInvalidSpec, p.Name)
		}
		if _, dup := seenIn[p.Name]; dup {
			return fmt.Errorf("%w: duplicate exposed inlet %q", ErrInvalidSpec, p.Name)
		}
		seenIn[p.Name] = struct{}{}
		targets := p.ResolvedTargets()
		if len(targets) == 0 {
			return fmt.Errorf("%w: exposed inlet %q has no target", ErrInvalidSpec, p.Name)
		}
		for _, ref := range targets {
			if !ref.Valid() {
				return fmt.Errorf("%w: exposed inlet %q has invalid target %q", ErrInvalidSpec, p.Name, ref)
			}
		}
	}
	seenOut := make(map[string]struct{}, len(s.Outlets))
	for _, p := range s.Outlets {
		if !IsValidID(p.Name) || !p.Type.Valid() || len(p.Targets) > 0 || !p.Target.Valid() {
			return fmt.Errorf("%w: invalid exposed outlet %q", ErrInvalidSpec, p.Name)
		}
		if _, dup := seenOut[p.Name]; dup {
			return fmt.Errorf("%w: duplicate exposed outlet %q", ErrInvalidSpec, p.Name)
		}
		seenOut[p.Name] = struct{}{}
	}
	seenParam := make(map[string]struct{}, len(s.Params))
	for _, p := range s.Params {
		if !IsValidID(p.Name) || !p.Type.Valid() {
			return fmt.Errorf("%w: invalid exposed param %q", ErrInvalidSpec, p.Name)
		}
		if _, dup := seenParam[p.Name]; dup {
			return fmt.Errorf("%w: duplicate exposed param %q", ErrInvalidSpec, p.Name)
		}
		seenParam[p.Name] = struct{}{}
		targets := p.ResolvedTargets()
		if len(targets) == 0 {
			return fmt.Errorf("%w: exposed param %q has no target", ErrInvalidSpec, p.Name)
		}
		for _, ref := range targets {
			if !ref.Valid() {
				return fmt.Errorf("%w: exposed param %q has invalid target %q", ErrInvalidSpec, p.Name, ref)
			}
		}
		if p.Default != nil {
			if _, err := normalizeParamValue(p.Default, 0, DefaultMaxDepth); err != nil {
				return fmt.Errorf("exposed param %q default: %w", p.Name, err)
			}
		}
		if p.Min != nil && (math.IsNaN(*p.Min) || math.IsInf(*p.Min, 0)) {
			return fmt.Errorf("%w: exposed param %q non-finite min", ErrInvalidSpec, p.Name)
		}
		if p.Max != nil && (math.IsNaN(*p.Max) || math.IsInf(*p.Max, 0)) {
			return fmt.Errorf("%w: exposed param %q non-finite max", ErrInvalidSpec, p.Name)
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("%w: exposed param %q min > max", ErrInvalidSpec, p.Name)
		}
	}
	return nil
}

func appendYAMLKV(mapping *yaml.Node, key string, val *yaml.Node) {
	mapping.Content = append(mapping.Content, yamlScalarString(key), val)
}

func yamlScalarString(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func yamlInt(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
}

func yamlInt64(v int64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}
}

func yamlFloat64(v float64) *yaml.Node {
	if i := int64(v); float64(i) == v && v >= float64(math.MinInt64) && v < float64(math.MaxInt64) {
		return yamlInt64(i)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(v, 'g', -1, 64)}
}

func encodeExposedPortsYAML(ports []ExposedPortSpec) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, p := range ports {
		m := &yaml.Node{Kind: yaml.MappingNode}
		appendYAMLKV(m, "name", yamlScalarString(p.Name))
		appendYAMLKV(m, "type", yamlScalarString(p.Type.String()))
		if len(p.Targets) > 0 {
			ts := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, ref := range p.Targets {
				ts.Content = append(ts.Content, yamlScalarString(ref.String()))
			}
			appendYAMLKV(m, "targets", ts)
		} else {
			appendYAMLKV(m, "target", yamlScalarString(p.Target.String()))
		}
		if p.Required {
			appendYAMLKV(m, "required", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		}
		if p.Description != "" {
			appendYAMLKV(m, "description", yamlScalarString(p.Description))
		}
		seq.Content = append(seq.Content, m)
	}
	return seq
}

func encodeExposedParamsYAML(params []ExposedParamSpec) (*yaml.Node, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, p := range params {
		m := &yaml.Node{Kind: yaml.MappingNode}
		appendYAMLKV(m, "name", yamlScalarString(p.Name))
		appendYAMLKV(m, "type", yamlScalarString(string(p.Type)))
		if len(p.Targets) > 0 {
			ts := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, ref := range p.Targets {
				ts.Content = append(ts.Content, yamlScalarString(ref.String()))
			}
			appendYAMLKV(m, "targets", ts)
		} else {
			appendYAMLKV(m, "target", yamlScalarString(p.Target.String()))
		}
		if p.Required {
			appendYAMLKV(m, "required", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		}
		if p.Default != nil {
			dv, err := encodeParamValueYAML(p.Default)
			if err != nil {
				return nil, fmt.Errorf("exposed param %q default: %w", p.Name, err)
			}
			appendYAMLKV(m, "default", dv)
		}
		if p.Min != nil {
			appendYAMLKV(m, "min", yamlFloat64(*p.Min))
		}
		if p.Max != nil {
			appendYAMLKV(m, "max", yamlFloat64(*p.Max))
		}
		if len(p.Enum) > 0 {
			es := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, item := range p.Enum {
				es.Content = append(es.Content, yamlScalarString(item))
			}
			appendYAMLKV(m, "enum", es)
		}
		if p.Description != "" {
			appendYAMLKV(m, "description", yamlScalarString(p.Description))
		}
		seq.Content = append(seq.Content, m)
	}
	return seq, nil
}

func encodeGroupYAML(g GroupSpec) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if g.Label != "" {
		appendYAMLKV(m, "label", yamlScalarString(g.Label))
	}
	if len(g.Nodes) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, id := range g.Nodes {
			seq.Content = append(seq.Content, yamlScalarString(id))
		}
		appendYAMLKV(m, "nodes", seq)
	}
	if g.Collapsed {
		appendYAMLKV(m, "collapsed", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	}
	if g.UI != nil {
		ui := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		appendYAMLKV(ui, "x", yamlFloat64(g.UI.X))
		appendYAMLKV(ui, "y", yamlFloat64(g.UI.Y))
		if g.UI.Width != 0 {
			appendYAMLKV(ui, "width", yamlFloat64(g.UI.Width))
		}
		if g.UI.Height != 0 {
			appendYAMLKV(ui, "height", yamlFloat64(g.UI.Height))
		}
		appendYAMLKV(m, "ui", ui)
	}
	return m
}

func encodeNodeYAML(n NodeSpec) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	appendYAMLKV(m, "type", yamlScalarString(n.Type.String()))
	if len(n.Capabilities) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, c := range n.Capabilities {
			seq.Content = append(seq.Content, yamlScalarString(c.String()))
		}
		appendYAMLKV(m, "capabilities", seq)
	}
	if len(n.Params) > 0 {
		pm, err := encodeParamsYAML(n.Params)
		if err != nil {
			return nil, err
		}
		appendYAMLKV(m, "params", pm)
	}
	if n.UI != nil {
		style := yaml.Style(0)
		if n.UI.Label == "" && !n.UI.Collapsed {
			style = yaml.FlowStyle
		}
		ui := &yaml.Node{Kind: yaml.MappingNode, Style: style}
		appendYAMLKV(ui, "x", yamlFloat64(n.UI.X))
		appendYAMLKV(ui, "y", yamlFloat64(n.UI.Y))
		if n.UI.Label != "" {
			appendYAMLKV(ui, "label", yamlScalarString(n.UI.Label))
		}
		if n.UI.Collapsed {
			appendYAMLKV(ui, "collapsed", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		}
		appendYAMLKV(m, "ui", ui)
	}
	return m, nil
}

func encodeParamsYAML(params map[string]any) (*yaml.Node, error) {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		vn, err := encodeParamValueYAML(params[k])
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		appendYAMLKV(m, k, vn)
	}
	return m, nil
}

func encodeParamValueYAML(v any) (*yaml.Node, error) {
	switch x := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case bool:
		if x {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}, nil
	case int64:
		return yamlInt64(x), nil
	case int:
		return yamlInt(x), nil
	case float64:
		return yamlFloat64(x), nil
	case string:
		return yamlScalarString(x), nil
	case []any:
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, elem := range x {
			en, err := encodeParamValueYAML(elem)
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, en)
		}
		return seq, nil
	case map[string]any:
		return encodeParamsYAML(x)
	default:
		return nil, fmt.Errorf("%w: unsupported parameter type %T", ErrInvalidSpec, v)
	}
}

type canonicalJSONFloat float64

func (f canonicalJSONFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, fmt.Errorf("%w: non-finite float", ErrInvalidSpec)
	}
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return []byte(s), nil
}

func wrapParamsForJSON(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = wrapValueForJSON(v)
	}
	return out
}

func wrapValueForJSON(v any) any {
	switch x := v.(type) {
	case float64:
		return canonicalJSONFloat(x)
	case map[string]any:
		if len(x) == 0 {
			return map[string]any{}
		}
		return wrapParamsForJSON(x)
	case []any:
		cp := make([]any, len(x))
		for i, elem := range x {
			cp[i] = wrapValueForJSON(elem)
		}
		return cp
	default:
		return v
	}
}

// MarshalJSON encodes s into canonical, two-space-indented, trailing-newline
// JSON with HTML escaping disabled (so '<' in types like "frame<rgba8>" stays
// readable and diff-friendly). It does not mutate s.
func MarshalJSON(s *Spec) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil spec", ErrInvalidSpec)
	}
	canon := s.Canonicalize()
	if err := preflightEncode(canon); err != nil {
		return nil, err
	}
	for id, n := range canon.Nodes {
		if len(n.Params) > 0 {
			n.Params = wrapParamsForJSON(n.Params)
			canon.Nodes[id] = n
		}
	}
	for i := range canon.Params {
		if canon.Params[i].Default != nil {
			canon.Params[i].Default = wrapValueForJSON(canon.Params[i].Default)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(canon); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
