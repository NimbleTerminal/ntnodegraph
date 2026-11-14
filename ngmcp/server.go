// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

// ToolHandler executes a registered MCP tool call.
type ToolHandler func(ctx context.Context, rawArgs json.RawMessage) (CallToolResult, error)

// ResourceHandler resolves a registered MCP resource URI.
type ResourceHandler func(ctx context.Context, uri string) (ReadResourceResult, error)

// TypeAnnotation supplies optional host-specific metadata for a registered
// TypeDef (such as hot/cold inlet semantics or frame-bound scheduling hints)
// without coupling internal/nodegraph to runtime execution concepts.
type TypeAnnotation struct {
	FrameBound bool            `json:"frame_bound,omitempty"`
	HotInlets  map[string]bool `json:"-"`
}

// TypeAnnotator optionally enriches "list" output with host-level port metadata.
type TypeAnnotator func(id nodegraph.TypeID) (TypeAnnotation, bool)

// PolicyCheckFunc optionally checks host-level constraints (such as granted
// capabilities) during "validate" and returns structured issue strings if any
// declarations are denied by the host.
type PolicyCheckFunc func(spec *nodegraph.Spec) []string

// ReadMetadataFunc optionally attaches live host metadata (such as live surface
// previews or engine counters) to "read" results for a target.
type ReadMetadataFunc func(ctx context.Context, target string) map[string]any

// Options configures an ngmcp Server.
type Options struct {
	ServerInfo        ImplementationInfo
	Instructions      string
	Registry          *nodegraph.Registry
	Library           *nodegraph.Library
	Backend           *nodegraph.BackendProfile
	Store             DocumentStore
	Limits            Limits
	AutoLayout        AutoLayoutFunc
	AnnotateType      TypeAnnotator
	PolicyCheck       PolicyCheckFunc
	ReadMetadata      ReadMetadataFunc
	RegisterComponent RegisterComponentFunc
}

type registeredTool struct {
	def     ToolDef
	handler ToolHandler
}

type registeredResource struct {
	def     ResourceDef
	handler ResourceHandler
}

// Server is a concurrency-safe MCP JSON-RPC 2.0 authoring server for ntnodegraph.
type Server struct {
	mu           sync.RWMutex
	info         ImplementationInfo
	instructions string
	registry     *nodegraph.Registry
	library      *nodegraph.Library
	backend      *nodegraph.BackendProfile
	store        DocumentStore
	limits       Limits
	autoLayout   AutoLayoutFunc
	annotateType TypeAnnotator
	policyCheck  PolicyCheckFunc
	readMeta     ReadMetadataFunc
	registerComp RegisterComponentFunc

	tools     map[string]registeredTool
	resources map[string]registeredResource
}

// NewServer constructs an ngmcp Server pre-loaded with the core ntnodegraph
// authoring tools ("list", "read", "validate", "edit", "component") and
// schema/catalog/library MCP resources.
func NewServer(opts Options) *Server {
	info := opts.ServerInfo
	if info.Name == "" {
		info.Name = "ntnodegraph-mcp"
	}
	if info.Version == "" {
		info.Version = "1.0.0"
	}
	reg := opts.Registry
	if reg == nil {
		reg = nodegraph.NewRegistry()
	}
	lib := opts.Library
	if lib == nil {
		lib = nodegraph.NewLibrary()
	}
	store := opts.Store
	if store == nil {
		store = NewMemoryStore("default", nil)
	}
	instr := opts.Instructions
	if instr == "" {
		instr = "Declarative ntnodegraph authoring server. Discover node types and reusable graph/* components with 'list' and 'component', inspect specs with 'read', check wiring and capabilities with 'validate', and apply atomic graph patches with 'edit'."
	}

	s := &Server{
		info:         info,
		instructions: instr,
		registry:     reg,
		library:      lib,
		backend:      opts.Backend,
		store:        store,
		limits:       opts.Limits.withDefaults(),
		autoLayout:   opts.AutoLayout,
		annotateType: opts.AnnotateType,
		policyCheck:  opts.PolicyCheck,
		readMeta:     opts.ReadMetadata,
		registerComp: opts.RegisterComponent,
		tools:        make(map[string]registeredTool),
		resources:    make(map[string]registeredResource),
	}

	s.registerBuiltinTools()
	s.registerComponentTool()
	s.registerBuiltinResources()
	return s
}

// RegisterTool registers (or replaces) an MCP tool on s.
func (s *Server) RegisterTool(def ToolDef, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[def.Name] = registeredTool{def: def, handler: handler}
}

// RegisterResource registers (or replaces) an MCP resource URI on s.
func (s *Server) RegisterResource(def ResourceDef, handler ResourceHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[def.URI] = registeredResource{def: def, handler: handler}
}

// ServeStdio serves MCP JSON-RPC 2.0 requests from r and writes newline-delimited
// JSON-RPC 2.0 responses to w until EOF or ctx cancellation.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var writeMu sync.Mutex

	// Close reader if possible when ctx cancels? Callers manage stream closure;
	// we check ctx between frames and pass ctx into handlers.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := readBoundedFrame(br, s.limits.MaxMessageBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, ErrMessageTooLarge) || errors.Is(err, ErrInvalidFrame) {
				resp := Response{
					JSONRPC: JSONRPCVersion,
					ID:      json.RawMessage("null"),
					Error: &RPCError{
						Code:    CodeParseError,
						Message: err.Error(),
					},
				}
				writeMu.Lock()
				_ = writeNewlineJSON(w, resp)
				writeMu.Unlock()
			}
			return err
		}
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}

		respBytes, hasReply := s.HandleJSON(ctx, frame)
		if !hasReply {
			continue
		}
		writeMu.Lock()
		_, wErr := w.Write(append(respBytes, '\n'))
		writeMu.Unlock()
		if wErr != nil {
			return wErr
		}
	}
}

// HandleJSON processes one raw JSON-RPC 2.0 message (single object) and returns
// the serialized JSON-RPC 2.0 response bytes (without trailing newline) when a
// reply is required, or (nil, false) for JSON-RPC notifications.
func (s *Server) HandleJSON(ctx context.Context, raw []byte) ([]byte, bool) {
	if len(raw) > s.limits.MaxMessageBytes {
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &RPCError{Code: CodeInvalidRequest, Message: ErrMessageTooLarge.Error()},
		}), true
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &RPCError{Code: CodeParseError, Message: "expected JSON-RPC 2.0 request object"},
		}), true
	}

	var req Request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &RPCError{Code: CodeParseError, Message: fmt.Sprintf("malformed JSON-RPC: %v", err)},
		}), true
	}

	if req.IsNotification() {
		return nil, false
	}

	replyID := req.ID
	var idCheck any
	if err := json.Unmarshal(replyID, &idCheck); err != nil {
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &RPCError{Code: CodeInvalidRequest, Message: "invalid JSON-RPC id"},
		}), true
	}
	switch idCheck.(type) {
	case string, float64, nil:
	default:
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &RPCError{Code: CodeInvalidRequest, Message: "JSON-RPC id must be string, number, or null"},
		}), true
	}

	if req.JSONRPC != JSONRPCVersion || strings.TrimSpace(req.Method) == "" {
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      replyID,
			Error:   &RPCError{Code: CodeInvalidRequest, Message: "invalid jsonrpc version or empty method"},
		}), true
	}

	res, rpcErr := s.dispatch(ctx, req)
	if rpcErr != nil {
		return marshalResponse(Response{
			JSONRPC: JSONRPCVersion,
			ID:      replyID,
			Error:   rpcErr,
		}), true
	}
	return marshalResponse(Response{
		JSONRPC: JSONRPCVersion,
		ID:      replyID,
		Result:  res,
	}), true
}

func marshalResponse(r Response) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func (s *Server) dispatch(ctx context.Context, req Request) (any, *RPCError) {
	switch req.Method {
	case "initialize":
		var p InitializeParams
		if len(bytes.TrimSpace(req.Params)) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			}
		}
		protoVer := ProtocolVersion
		if p.ProtocolVersion == LegacyProtocolVersion {
			protoVer = LegacyProtocolVersion
		}
		return InitializeResult{
			ProtocolVersion: protoVer,
			Capabilities: ServerCapabilities{
				Tools:     &ToolsCapability{},
				Resources: &ResourcesCapability{},
			},
			ServerInfo:   s.info,
			Instructions: s.instructions,
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		s.mu.RLock()
		names := slices.Sorted(maps.Keys(s.tools))
		list := make([]ToolDef, 0, len(names))
		for _, name := range names {
			list = append(list, s.tools[name].def)
		}
		s.mu.RUnlock()
		return ListToolsResult{Tools: list}, nil

	case "tools/call":
		var p CallToolParams
		if err := decodeStrictJSON(req.Params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		s.mu.RLock()
		entry, ok := s.tools[p.Name]
		s.mu.RUnlock()
		if !ok {
			return nil, &RPCError{
				Code:    CodeInvalidParams,
				Message: fmt.Sprintf("unknown tool %q", p.Name),
			}
		}
		callRes, err := entry.handler(ctx, p.Arguments)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
		return callRes, nil

	case "resources/list":
		s.mu.RLock()
		uris := slices.Sorted(maps.Keys(s.resources))
		list := make([]ResourceDef, 0, len(uris))
		for _, u := range uris {
			list = append(list, s.resources[u].def)
		}
		s.mu.RUnlock()
		return ListResourcesResult{Resources: list}, nil

	case "resources/read":
		var p ReadResourceParams
		if err := decodeStrictJSON(req.Params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		s.mu.RLock()
		entry, ok := s.resources[p.URI]
		s.mu.RUnlock()
		if !ok {
			return nil, &RPCError{
				Code:    CodeInvalidParams,
				Message: fmt.Sprintf("unknown resource URI %q", p.URI),
			}
		}
		readRes, err := entry.handler(ctx, p.URI)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
		return readRes, nil

	default:
		return nil, &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("method %q not found", req.Method),
		}
	}
}

func (s *Server) registerBuiltinResources() {
	s.RegisterResource(ResourceDef{
		URI:         "schema://ntnodegraph/v1",
		Name:        "ntnodegraph Specification JSON Schema v1",
		Description: "Canonical Draft 2020-12 JSON Schema for ntnodegraph v1 YAML/JSON specs.",
		MIMEType:    "application/schema+json",
	}, func(_ context.Context, uri string) (ReadResourceResult, error) {
		return ReadResourceResult{
			Contents: []ResourceContents{{
				URI:      uri,
				MIMEType: "application/schema+json",
				Text:     string(nodegraph.SchemaV1JSON()),
			}},
		}, nil
	})

	s.RegisterResource(ResourceDef{
		URI:         "catalog://ntnodegraph/types",
		Name:        "Registered Node Type Catalog",
		Description: "Deterministic JSON catalog of all registered node types, ports, parameters, and capability rules.",
		MIMEType:    "application/json",
	}, func(_ context.Context, uri string) (ReadResourceResult, error) {
		data, err := s.registry.CatalogJSON()
		if err != nil {
			return ReadResourceResult{}, err
		}
		return ReadResourceResult{
			Contents: []ResourceContents{{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(data),
			}},
		}, nil
	})

	s.RegisterResource(ResourceDef{
		URI:         "catalog://ntnodegraph/library",
		Name:        "Registered Reusable Subgraph Component Library",
		Description: "Deterministic JSON catalog of reusable graph/* subgraph components, origins, boundary ports, forwarded parameters, and synthesized TypeDefs.",
		MIMEType:    "application/json",
	}, func(_ context.Context, uri string) (ReadResourceResult, error) {
		data, err := s.library.LibraryJSON(s.registry)
		if err != nil {
			return ReadResourceResult{}, err
		}
		return ReadResourceResult{
			Contents: []ResourceContents{{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(data),
			}},
		}, nil
	})
}

type listArgs struct {
	Namespace string `json:"namespace,omitempty"`
	Query     string `json:"query,omitempty"`
}

type portSummary struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Hot         *bool  `json:"hot,omitempty"`
	Description string `json:"description,omitempty"`
}

type nodeTypeSummary struct {
	ID              string                     `json:"id"`
	Namespace       string                     `json:"namespace"`
	Subgraph        bool                       `json:"subgraph,omitempty"`
	ComponentOrigin string                     `json:"component_origin,omitempty"`
	Description     string                     `json:"description,omitempty"`
	FrameBound      bool                       `json:"frame_bound,omitempty"`
	Inlets          []portSummary              `json:"inlets,omitempty"`
	Outlets         []portSummary              `json:"outlets,omitempty"`
	Params          []nodegraph.ParamDef       `json:"params,omitempty"`
	Capabilities    []nodegraph.CapabilityRule `json:"capabilities,omitempty"`
	Backends        []string                   `json:"backends,omitempty"`
}

type readArgs struct {
	Target string `json:"target,omitempty"`
	Spec   string `json:"spec,omitempty"`
}

type validateArgs struct {
	Target            string `json:"target,omitempty"`
	Spec              string `json:"spec,omitempty"`
	CheckCapabilities *bool  `json:"check_capabilities,omitempty"`
}

type editArgs struct {
	Target          string    `json:"target,omitempty"`
	Create          bool      `json:"create,omitempty"`
	GraphName       string    `json:"graph_name,omitempty"`
	Save            bool      `json:"save,omitempty"`
	AllowIncomplete bool      `json:"allow_incomplete,omitempty"`
	AutoLayout      bool      `json:"auto_layout,omitempty"`
	LayoutDirection string    `json:"layout_direction,omitempty"`
	Ops             []PatchOp `json:"ops"`
}

func (s *Server) registerBuiltinTools() {
	// 1. list
	s.RegisterTool(ToolDef{
		Name:        "list",
		Description: "List registered node types (ports, parameters, capability rules) and available graph targets.",
		Annotations: &ToolAnnotations{Title: "List Node Types & Graphs", ReadOnlyHint: true, IdempotentHint: true},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"namespace": map[string]any{
					"type":        "string",
					"description": "Optional namespace filter (e.g. source, transform, surface, wasm).",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring filter on type ID or description.",
				},
			},
		},
	}, s.handleList)

	// 2. read
	s.RegisterTool(ToolDef{
		Name:        "read",
		Description: "Read a graph specification as canonical diff-stable YAML and structured JSON alongside static validation status.",
		Annotations: &ToolAnnotations{Title: "Read Graph Specification", ReadOnlyHint: true, IdempotentHint: true},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "Graph file path or store target name (defaults to the active graph when omitted).",
				},
				"spec": map[string]any{
					"type":        "string",
					"description": "Optional inline YAML or JSON spec string to parse and inspect instead of reading a stored target.",
				},
			},
		},
	}, s.handleRead)

	// 3. validate
	s.RegisterTool(ToolDef{
		Name:        "validate",
		Description: "Validate a graph specification against registered node types, port type compatibility, acyclicity, backend profile, and host capability policy.",
		Annotations: &ToolAnnotations{Title: "Validate Graph Specification", ReadOnlyHint: true, IdempotentHint: true},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "Graph file path or store target name to validate.",
				},
				"spec": map[string]any{
					"type":        "string",
					"description": "Optional inline YAML or JSON spec string to validate without modifying stored files.",
				},
				"check_capabilities": map[string]any{
					"type":        "boolean",
					"description": "Whether to also check declared node capabilities against the host's granted capability policy (defaults to true when configured).",
				},
			},
		},
	}, s.handleValidate)

	// 4. edit
	s.RegisterTool(ToolDef{
		Name:        "edit",
		Description: "Transactionally apply declarative graph edit operations (add_node, remove_node, set_param, delete_param, set_capabilities, move_node, set_node_label, set_node_collapsed, connect, disconnect, splice_node, put_group, remove_group, set_metadata, auto_layout), validate the result, and commit canonical YAML.",
		Annotations: &ToolAnnotations{Title: "Edit Graph Specification"},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ops"},
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "Target graph file path or store target name (defaults to the active graph when omitted).",
				},
				"create": map[string]any{
					"type":        "boolean",
					"description": "Create a new v1 graph if target does not exist yet.",
				},
				"graph_name": map[string]any{
					"type":        "string",
					"description": "Graph name when creating a new spec (derived from target if omitted).",
				},
				"save": map[string]any{
					"type":        "boolean",
					"description": "Persist to disk when connected to an interactive Studio session (file stores always persist).",
				},
				"allow_incomplete": map[string]any{
					"type":        "boolean",
					"description": "Allow committing a draft graph whose only validation issues are missing required inlets.",
				},
				"auto_layout": map[string]any{
					"type":        "boolean",
					"description": "Automatically compute clean layered node coordinates after applying ops.",
				},
				"layout_direction": map[string]any{
					"type":        "string",
					"description": "Optional auto-layout flow direction ('lr' or 'tb') when auto_layout is true.",
				},
				"ops": map[string]any{
					"type":        "array",
					"description": "Ordered list of atomic declarative PatchOp objects.",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"op"},
						"properties": map[string]any{
							"op": map[string]any{
								"type": "string",
								"enum": []string{
									"add_node",
									"remove_node",
									"set_param",
									"delete_param",
									"set_capabilities",
									"move_node",
									"set_node_label",
									"set_node_collapsed",
									"connect",
									"disconnect",
									"splice_node",
									"put_group",
									"remove_group",
									"set_metadata",
									"set_component_version",
									"expose_inlet",
									"remove_inlet",
									"expose_outlet",
									"remove_outlet",
									"expose_param",
									"remove_param",
									"auto_layout",
								},
							},
							"id":                map[string]any{"type": "string"},
							"node":              map[string]any{"type": "string"},
							"type":              map[string]any{"type": "string"},
							"key":               map[string]any{"type": "string"},
							"value":             map[string]any{"description": "JSON parameter value for set_param."},
							"params":            map[string]any{"type": "object"},
							"capabilities":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"from":              map[string]any{"type": "string", "description": "Outlet reference '<node>.<port>'."},
							"to":                map[string]any{"type": "string", "description": "Inlet reference '<node>.<port>'."},
							"kind":              map[string]any{"type": "string", "description": "Optional edge delivery kind (signal, frame, event)."},
							"edge_kind":         map[string]any{"type": "string"},
							"inlet":             map[string]any{"type": "string", "description": "Splice or boundary inlet port name."},
							"outlet":            map[string]any{"type": "string", "description": "Splice or boundary outlet port name."},
							"x":                 map[string]any{"type": "number"},
							"y":                 map[string]any{"type": "number"},
							"label":             map[string]any{"type": "string"},
							"collapsed":         map[string]any{"type": "boolean"},
							"nodes":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"name":              map[string]any{"type": "string"},
							"description":       map[string]any{"type": "string"},
							"component_version": map[string]any{"type": "integer"},
							"target":            map[string]any{"type": "string", "description": "Boundary target '<node>.<port>' or '<node>.<param>'."},
							"targets":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"required":          map[string]any{"type": "boolean"},
							"default":           map[string]any{"description": "Optional default JSON value for expose_param."},
							"min":               map[string]any{"type": "number"},
							"max":               map[string]any{"type": "number"},
							"enum":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"direction":         map[string]any{"type": "string"},
							"column_gap":        map[string]any{"type": "number"},
							"row_gap":           map[string]any{"type": "number"},
						},
					},
				},
			},
		},
	}, s.handleEdit)
}

func (s *Server) handleList(ctx context.Context, rawArgs json.RawMessage) (CallToolResult, error) {
	var args listArgs
	if err := decodeStrictJSON(rawArgs, &args); err != nil {
		return ErrorResult(err, nil), nil
	}
	nsFilter := strings.ToLower(strings.TrimSpace(args.Namespace))
	qFilter := strings.ToLower(strings.TrimSpace(args.Query))

	types := s.registry.Types() // already sorted deterministically by TypeID
	summaries := make([]nodeTypeSummary, 0, len(types))
	for _, td := range types {
		if nsFilter != "" && td.ID.Namespace != nsFilter {
			continue
		}
		if qFilter != "" {
			haystack := strings.ToLower(td.ID.String() + " " + td.Description)
			if !strings.Contains(haystack, qFilter) {
				continue
			}
		}

		var ann TypeAnnotation
		hasAnn := false
		if s.annotateType != nil {
			ann, hasAnn = s.annotateType(td.ID)
		}

		inlets := make([]portSummary, len(td.Inlets))
		for i, in := range td.Inlets {
			ps := portSummary{
				Name:        in.Name,
				Type:        in.Type.String(),
				Required:    in.Required,
				Description: in.Description,
			}
			if hasAnn && ann.HotInlets != nil {
				hot := ann.HotInlets[in.Name]
				ps.Hot = &hot
			}
			inlets[i] = ps
		}

		outlets := make([]portSummary, len(td.Outlets))
		for i, out := range td.Outlets {
			outlets[i] = portSummary{
				Name:        out.Name,
				Type:        out.Type.String(),
				Description: out.Description,
			}
		}

		isSubgraph := td.ID.Namespace == nodegraph.NamespaceGraph
		compOrigin := ""
		if isSubgraph {
			if entry, ok := s.library.Get(td.ID); ok {
				compOrigin = entry.Origin
			}
		}

		summaries = append(summaries, nodeTypeSummary{
			ID:              td.ID.String(),
			Namespace:       td.ID.Namespace,
			Subgraph:        isSubgraph,
			ComponentOrigin: compOrigin,
			Description:     td.Description,
			FrameBound:      ann.FrameBound,
			Inlets:          inlets,
			Outlets:         outlets,
			Params:          td.Params,
			Capabilities:    td.Capabilities,
			Backends:        td.Backends,
		})
	}

	targets, _ := s.store.ListTargets(ctx)
	payload := map[string]any{
		"default_target":  s.store.DefaultTarget(),
		"targets":         targets,
		"component_count": len(s.library.List()),
		"node_types":      summaries,
	}
	if s.backend != nil {
		payload["backend"] = s.backend.Name
	}
	return TextResult(payload)
}

func (s *Server) resolveSpecInput(ctx context.Context, target, inlineSpec string) (*nodegraph.Spec, string, error) {
	if strings.TrimSpace(inlineSpec) != "" {
		sp, err := nodegraph.Parse([]byte(inlineSpec))
		if err != nil {
			return nil, "inline", err
		}
		return sp, "inline", nil
	}
	effTarget := strings.TrimSpace(target)
	if strings.HasPrefix(effTarget, nodegraph.NamespaceGraph+"/") {
		if tid, err := normalizeComponentTypeID(effTarget); err == nil {
			if entry, ok := s.library.Get(tid); ok {
				return entry.Spec, entry.ID.String(), nil
			}
		}
	}
	if effTarget == "" {
		effTarget = s.store.DefaultTarget()
	}
	sp, err := s.store.Get(ctx, effTarget)
	if err != nil {
		return nil, effTarget, err
	}
	return sp, effTarget, nil
}

func (s *Server) handleRead(ctx context.Context, rawArgs json.RawMessage) (CallToolResult, error) {
	var args readArgs
	if err := decodeStrictJSON(rawArgs, &args); err != nil {
		return ErrorResult(err, nil), nil
	}
	spec, effTarget, err := s.resolveSpecInput(ctx, args.Target, args.Spec)
	if err != nil {
		return ErrorResult(err, nil), nil
	}

	yamlBytes, err := nodegraph.MarshalYAML(spec)
	if err != nil {
		return ErrorResult(err, nil), nil
	}
	report, valErr := nodegraph.Validate(spec, s.registry, nodegraph.ValidateOptions{
		Backend: s.backend,
	})

	out := map[string]any{
		"target":    effTarget,
		"spec_yaml": string(yamlBytes),
		"spec":      spec,
		"valid":     valErr == nil,
	}
	if valErr == nil {
		out["topological_order"] = report.TopologicalOrder
		out["resolved_edges"] = report.Edges
	} else {
		out["validation_issues"] = extractValidationIssues(valErr)
	}
	if s.readMeta != nil && effTarget != "inline" {
		if meta := s.readMeta(ctx, effTarget); len(meta) > 0 {
			out["host"] = meta
		}
	}
	return TextResult(out)
}

func (s *Server) handleValidate(ctx context.Context, rawArgs json.RawMessage) (CallToolResult, error) {
	var args validateArgs
	if err := decodeStrictJSON(rawArgs, &args); err != nil {
		return ErrorResult(err, nil), nil
	}
	spec, effTarget, err := s.resolveSpecInput(ctx, args.Target, args.Spec)
	if err != nil {
		return ErrorResult(err, map[string]any{
			"target":      effTarget,
			"valid":       false,
			"parse_error": err.Error(),
		}), nil
	}

	report, valErr := nodegraph.Validate(spec, s.registry, nodegraph.ValidateOptions{
		Backend: s.backend,
	})

	checkCaps := true
	if args.CheckCapabilities != nil {
		checkCaps = *args.CheckCapabilities
	}
	var policyIssues []string
	if checkCaps && s.policyCheck != nil {
		policyIssues = s.policyCheck(spec)
	}

	valid := valErr == nil && len(policyIssues) == 0
	out := map[string]any{
		"target": effTarget,
		"valid":  valid,
	}
	if valErr == nil {
		out["topological_order"] = report.TopologicalOrder
		out["edges"] = report.Edges
	} else {
		out["issues"] = extractValidationIssues(valErr)
	}
	if len(policyIssues) > 0 {
		out["capability_policy_issues"] = policyIssues
	}
	return TextResult(out)
}

func (s *Server) handleEdit(ctx context.Context, rawArgs json.RawMessage) (CallToolResult, error) {
	if len(rawArgs) > s.limits.MaxMessageBytes {
		return ErrorResult(ErrMessageTooLarge, nil), nil
	}
	var args editArgs
	if err := decodeStrictJSON(rawArgs, &args); err != nil {
		return ErrorResult(err, nil), nil
	}
	if len(args.Ops) > s.limits.MaxOpsPerBatch {
		return ErrorResult(fmt.Errorf("%w: %d > %d", ErrTooManyOps, len(args.Ops), s.limits.MaxOpsPerBatch), nil), nil
	}

	effTarget := strings.TrimSpace(args.Target)
	if effTarget == "" {
		effTarget = s.store.DefaultTarget()
	}
	if effTarget == "" {
		return ErrorResult(ErrTargetRequired, nil), nil
	}

	before, getErr := s.store.Get(ctx, effTarget)
	if getErr != nil {
		if !args.Create || !errors.Is(getErr, ErrGraphNotFound) {
			return ErrorResult(getErr, nil), nil
		}
		gName := strings.TrimSpace(args.GraphName)
		if gName == "" {
			gName = deriveGraphName(effTarget)
		}
		var newErr error
		before, newErr = nodegraph.NewSpec(gName)
		if newErr != nil {
			return ErrorResult(newErr, nil), nil
		}
	}

	ops := args.Ops
	if args.AutoLayout {
		ops = append(append([]PatchOp(nil), ops...), PatchOp{
			Op:        "auto_layout",
			Direction: args.LayoutDirection,
		})
	}

	after, patchErr := ApplyPatchOps(before, ops, s.registry, s.autoLayout)
	if patchErr != nil {
		return ErrorResult(patchErr, nil), nil
	}

	report, valErr := nodegraph.Validate(after, s.registry, nodegraph.ValidateOptions{
		Backend: s.backend,
	})
	if valErr != nil && !(args.AllowIncomplete && onlyMissingRequiredInlets(valErr)) {
		return ErrorResult(valErr, map[string]any{
			"target": effTarget,
			"valid":  false,
			"issues": extractValidationIssues(valErr),
		}), nil
	}

	diff := nodegraph.Diff(before, after)
	putRes, putErr := s.store.Put(ctx, effTarget, after, diff, PutOptions{
		Create: args.Create,
		Save:   args.Save,
	})
	if putErr != nil {
		return ErrorResult(putErr, map[string]any{
			"target":       effTarget,
			"commit_error": putErr.Error(),
		}), nil
	}

	yamlBytes, err := nodegraph.MarshalYAML(after)
	if err != nil {
		return ErrorResult(err, nil), nil
	}

	out := map[string]any{
		"target":          firstNonEmpty(putRes.Target, effTarget),
		"saved":           putRes.Saved,
		"valid":           valErr == nil,
		"runtime_changes": diff.HasRuntimeChanges(),
		"diff":            diff,
		"spec_yaml":       string(yamlBytes),
	}
	if valErr == nil {
		out["topological_order"] = report.TopologicalOrder
	} else {
		out["validation_issues"] = extractValidationIssues(valErr)
	}
	if len(putRes.Metadata) > 0 {
		out["host"] = putRes.Metadata
	}
	return TextResult(out)
}

func extractValidationIssues(err error) []nodegraph.Issue {
	var verr *nodegraph.ValidationError
	if errors.As(err, &verr) {
		return verr.Issues
	}
	return []nodegraph.Issue{{
		Code:    nodegraph.IssueInvalidSpec,
		Message: err.Error(),
	}}
}

func onlyMissingRequiredInlets(err error) bool {
	var verr *nodegraph.ValidationError
	if !errors.As(err, &verr) || len(verr.Issues) == 0 {
		return false
	}
	for _, iss := range verr.Issues {
		if iss.Code != nodegraph.IssueMissingRequiredInlet {
			return false
		}
	}
	return true
}

func deriveGraphName(target string) string {
	base := target
	if idx := strings.LastIndexAny(base, "/\\"); idx >= 0 {
		base = base[idx+1:]
	}
	for _, ext := range []string{".yaml", ".yml", ".json"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	cand := strings.Trim(b.String(), "-_")
	if cand == "" || cand[0] < 'a' || cand[0] > 'z' {
		cand = "graph-" + cand
		cand = strings.TrimRight(cand, "-_")
	}
	if !nodegraph.IsValidID(cand) {
		return "graph"
	}
	return cand
}
