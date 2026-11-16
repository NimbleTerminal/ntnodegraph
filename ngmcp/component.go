// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

// RegisterComponentFunc validates, registers, and optionally persists a reusable
// graph/* component Spec into the host's component Library and Registry.
type RegisterComponentFunc func(ctx context.Context, spec *nodegraph.Spec, persist bool) (nodegraph.TypeDef, string, error)

type componentArgs struct {
	Action           string   `json:"action"`
	ID               string   `json:"id,omitempty"`
	Spec             string   `json:"spec,omitempty"`
	Target           string   `json:"target,omitempty"`
	Group            string   `json:"group,omitempty"`
	Nodes            []string `json:"nodes,omitempty"`
	ComponentName    string   `json:"component_name,omitempty"`
	ComponentVersion int      `json:"component_version,omitempty"`
	InstanceID       string   `json:"instance_id,omitempty"`
	Description      string   `json:"description,omitempty"`
	ExposeParams     []string `json:"expose_params,omitempty"`
	ReplaceInTarget  *bool    `json:"replace_in_target,omitempty"`
	Persist          *bool    `json:"persist,omitempty"`
	Save             bool     `json:"save,omitempty"`
}

func (s *Server) registerComponentTool() {
	s.RegisterTool(ToolDef{
		Name:        "component",
		Description: "Manage reusable composable subgraph components (graph/*): list registered components, inspect internal component specs, register new reusable components from YAML/JSON, or extract a group/node selection from a graph into a reusable component.",
		Annotations: &ToolAnnotations{Title: "Manage Subgraph Components"},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action"},
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"list", "inspect", "register", "extract"},
					"description": "Component library action: list, inspect, register, or extract.",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Component TypeID (e.g. 'graph/cinema-stage@1' or 'cinema-stage') for inspect.",
				},
				"spec": map[string]any{
					"type":        "string",
					"description": "YAML or JSON component specification for action='register'.",
				},
				"target": map[string]any{
					"type":        "string",
					"description": "Source/destination graph target for action='extract' (defaults to active graph).",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "UI group ID in target to promote into a reusable component (action='extract').",
				},
				"nodes": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Node IDs in target to extract into a reusable component (action='extract').",
				},
				"component_name": map[string]any{
					"type":        "string",
					"description": "Component name (<name> in graph/<name>@<major>) for action='extract'.",
				},
				"component_version": map[string]any{
					"type":        "integer",
					"description": "Optional major component version >= 1 (defaults to 1).",
				},
				"instance_id": map[string]any{
					"type":        "string",
					"description": "Optional replacement node ID in target when action='extract' replaces extracted nodes.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional human-readable description for an extracted component.",
				},
				"expose_params": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Internal '<node>.<param>' references to expose as 1:1 forwarded component parameters during action='extract'.",
				},
				"replace_in_target": map[string]any{
					"type":        "boolean",
					"description": "Replace extracted nodes/group in target with a graph/* instance (defaults to true).",
				},
				"persist": map[string]any{
					"type":        "boolean",
					"description": "Persist registered/extracted component YAML to the configured workspace library directory when available (defaults to true).",
				},
				"save": map[string]any{
					"type":        "boolean",
					"description": "Persist updated target graph to disk when connected to an interactive Studio session.",
				},
			},
		},
	}, s.handleComponent)
}

func (s *Server) handleComponent(ctx context.Context, rawArgs json.RawMessage) (CallToolResult, error) {
	if len(rawArgs) > s.limits.MaxMessageBytes {
		return ErrorResult(ErrMessageTooLarge, nil), nil
	}
	var args componentArgs
	if err := decodeStrictJSON(rawArgs, &args); err != nil {
		return ErrorResult(err, nil), nil
	}

	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "list":
		return s.handleComponentList()
	case "inspect":
		return s.handleComponentInspect(args)
	case "register":
		return s.handleComponentRegister(ctx, args)
	case "extract":
		return s.handleComponentExtract(ctx, args)
	default:
		return ErrorResult(fmt.Errorf("unknown component action %q (expected list, inspect, register, or extract)", args.Action), nil), nil
	}
}

func (s *Server) handleComponentList() (CallToolResult, error) {
	entries := s.library.List()
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id":                entry.ID.String(),
			"origin":            entry.Origin,
			"name":              entry.Spec.Name,
			"component_version": entry.Spec.EffectiveComponentVersion(),
			"description":       entry.Spec.Description,
			"node_count":        len(entry.Spec.Nodes),
			"edge_count":        len(entry.Spec.Edges),
			"inlets":            entry.Spec.Inlets,
			"outlets":           entry.Spec.Outlets,
			"params":            entry.Spec.Params,
		}
		if def, ok := s.registry.Lookup(entry.ID); ok {
			item["type_def"] = def
		}
		items = append(items, item)
	}
	return TextResult(map[string]any{
		"count":      len(items),
		"components": items,
	})
}

func normalizeComponentTypeID(raw string) (nodegraph.TypeID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nodegraph.TypeID{}, errors.New("component id is required")
	}
	if !strings.Contains(trimmed, "/") {
		trimmed = nodegraph.NamespaceGraph + "/" + trimmed
	}
	if !strings.Contains(trimmed, "@") {
		trimmed += "@1"
	}
	tid, err := nodegraph.ParseTypeID(trimmed)
	if err != nil {
		return nodegraph.TypeID{}, err
	}
	if tid.Namespace != nodegraph.NamespaceGraph {
		return nodegraph.TypeID{}, fmt.Errorf("%w: %s is not in namespace %q", nodegraph.ErrInvalidTypeID, tid, nodegraph.NamespaceGraph)
	}
	return tid, nil
}

func (s *Server) handleComponentInspect(args componentArgs) (CallToolResult, error) {
	tid, err := normalizeComponentTypeID(args.ID)
	if err != nil {
		return ErrorResult(err, nil), nil
	}
	entry, ok := s.library.Get(tid)
	if !ok {
		return ErrorResult(fmt.Errorf("%w: %s", nodegraph.ErrUnknownNodeType, tid), nil), nil
	}
	yamlBytes, err := nodegraph.MarshalYAML(entry.Spec)
	if err != nil {
		return ErrorResult(err, nil), nil
	}
	out := map[string]any{
		"id":        entry.ID.String(),
		"origin":    entry.Origin,
		"spec_yaml": string(yamlBytes),
		"spec":      entry.Spec,
	}
	if def, ok := s.registry.Lookup(entry.ID); ok {
		out["type_def"] = def
	}
	return TextResult(out)
}

func (s *Server) registerIntoLibrary(ctx context.Context, spec *nodegraph.Spec, persist bool) (nodegraph.TypeDef, string, error) {
	if s.registerComp != nil {
		def, origin, err := s.registerComp(ctx, spec, persist)
		if err != nil {
			return nodegraph.TypeDef{}, "", err
		}
		_ = s.registry.Upsert(def)
		return def, origin, nil
	}
	tid, err := spec.ComponentTypeID()
	if err != nil {
		return nodegraph.TypeDef{}, "", err
	}
	prevEntry, hadPrev := s.library.Get(tid)
	if _, err := s.library.Put(spec, "memory"); err != nil {
		return nodegraph.TypeDef{}, "", err
	}
	if _, err := s.library.SyncRegistry(s.registry, nodegraph.ValidateOptions{Backend: s.backend}); err != nil {
		if hadPrev {
			_, _ = s.library.Put(prevEntry.Spec, prevEntry.Origin)
		} else {
			s.library.Delete(tid)
		}
		_, _ = s.library.SyncRegistry(s.registry, nodegraph.ValidateOptions{Backend: s.backend})
		return nodegraph.TypeDef{}, "", err
	}
	def, _ := s.registry.Lookup(tid)
	return def, "memory", nil
}

func (s *Server) handleComponentRegister(ctx context.Context, args componentArgs) (CallToolResult, error) {
	if strings.TrimSpace(args.Spec) == "" {
		return ErrorResult(errors.New("component register requires non-empty spec"), nil), nil
	}
	spec, err := nodegraph.Parse([]byte(args.Spec))
	if err != nil {
		return ErrorResult(err, nil), nil
	}
	persist := true
	if args.Persist != nil {
		persist = *args.Persist
	}
	def, origin, err := s.registerIntoLibrary(ctx, spec, persist)
	if err != nil {
		return ErrorResult(err, nil), nil
	}
	entry, _ := s.library.Get(def.ID)
	canon := spec.Canonicalize()
	if entry.Spec != nil {
		canon = entry.Spec
	}
	yamlBytes, err := nodegraph.MarshalYAML(canon)
	if err != nil {
		return ErrorResult(err, nil), nil
	}
	return TextResult(map[string]any{
		"id":        def.ID.String(),
		"origin":    origin,
		"type_def":  def,
		"spec_yaml": string(yamlBytes),
	})
}

func (s *Server) handleComponentExtract(ctx context.Context, args componentArgs) (CallToolResult, error) {
	effTarget := strings.TrimSpace(args.Target)
	if effTarget == "" {
		effTarget = s.store.DefaultTarget()
	}
	if effTarget == "" {
		return ErrorResult(ErrTargetRequired, nil), nil
	}

	hasGroup := strings.TrimSpace(args.Group) != ""
	hasNodes := len(args.Nodes) > 0
	if hasGroup == hasNodes {
		return ErrorResult(errors.New("component extract requires either group or nodes (not both)"), nil), nil
	}

	var exposeRefs []nodegraph.PortRef
	for _, raw := range args.ExposeParams {
		ref, err := nodegraph.ParsePortRef(strings.TrimSpace(raw))
		if err != nil {
			return ErrorResult(fmt.Errorf("expose_params %q: %w", raw, err), nil), nil
		}
		exposeRefs = append(exposeRefs, ref)
	}

	before, revision, err := s.store.Get(ctx, effTarget)
	if err != nil {
		return ErrorResult(err, nil), nil
	}

	extOpts := nodegraph.ExtractOptions{
		ComponentName:    strings.TrimSpace(args.ComponentName),
		ComponentVersion: args.ComponentVersion,
		Description:      args.Description,
		InstanceID:       strings.TrimSpace(args.InstanceID),
		ExposeParams:     exposeRefs,
		Backend:          s.backend,
	}

	var (
		compSpec   *nodegraph.Spec
		nextParent *nodegraph.Spec
	)
	if hasGroup {
		compSpec, nextParent, _, err = nodegraph.PromoteGroupToSubgraph(before, strings.TrimSpace(args.Group), s.registry, extOpts)
	} else {
		compSpec, nextParent, _, err = nodegraph.ExtractSubgraph(before, args.Nodes, s.registry, extOpts)
	}
	if err != nil {
		return ErrorResult(err, nil), nil
	}

	persist := true
	if args.Persist != nil {
		persist = *args.Persist
	}
	def, origin, err := s.registerIntoLibrary(ctx, compSpec, persist)
	if err != nil {
		return ErrorResult(err, nil), nil
	}

	compYAML, err := nodegraph.MarshalYAML(compSpec)
	if err != nil {
		return ErrorResult(err, nil), nil
	}

	// Locate replacement instance node ID in nextParent.
	instanceID := ""
	for _, nid := range nextParent.SortedNodeIDs() {
		if _, existed := before.Nodes[nid]; !existed && nextParent.Nodes[nid].Type == def.ID {
			instanceID = nid
			break
		}
	}

	replaceInTarget := true
	if args.ReplaceInTarget != nil {
		replaceInTarget = *args.ReplaceInTarget
	}

	out := map[string]any{
		"component_id":   def.ID.String(),
		"origin":         origin,
		"type_def":       def,
		"component_yaml": string(compYAML),
		"instance_id":    instanceID,
	}

	if replaceInTarget {
		if _, valErr := nodegraph.Validate(nextParent, s.registry, nodegraph.ValidateOptions{Backend: s.backend}); valErr != nil && !onlyMissingRequiredInlets(valErr) {
			return ErrorResult(valErr, map[string]any{
				"target": effTarget,
				"issues": extractValidationIssues(valErr),
			}), nil
		}
		diff := nodegraph.Diff(before, nextParent)
		putRes, putErr := s.store.Put(ctx, effTarget, nextParent, diff, PutOptions{
			Save:             args.Save,
			ExpectedRevision: revision,
		})
		if putErr != nil {
			return ErrorResult(putErr, nil), nil
		}
		parentYAML, mErr := nodegraph.MarshalYAML(nextParent)
		if mErr != nil {
			return ErrorResult(mErr, nil), nil
		}
		out["target"] = firstNonEmpty(putRes.Target, effTarget)
		out["saved"] = putRes.Saved
		out["parent_diff"] = diff
		out["parent_yaml"] = string(parentYAML)
		if len(putRes.Metadata) > 0 {
			out["host"] = putRes.Metadata
		}
	}

	return TextResult(out)
}
