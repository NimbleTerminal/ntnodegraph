// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

func buildTestRegistry(t *testing.T) *nodegraph.Registry {
	t.Helper()
	reg := nodegraph.NewRegistry()

	mustType := func(raw string) nodegraph.DataType {
		dt, err := nodegraph.ParseDataType(raw)
		if err != nil {
			t.Fatalf("ParseDataType(%q): %v", raw, err)
		}
		return dt
	}

	defs := []nodegraph.TypeDef{
		{
			ID:          nodegraph.MustParseTypeID("source/http@1"),
			Description: "Capability-scoped HTTP source",
			Inlets:      []nodegraph.PortDef{{Name: "trigger", Type: mustType("event")}},
			Outlets:     []nodegraph.PortDef{{Name: "body", Type: mustType("bytes")}},
			Params: []nodegraph.ParamDef{
				{Name: "url", Type: nodegraph.ParamString, Required: true},
				{Name: "poll", Type: nodegraph.ParamDuration, Default: "60s"},
			},
			Capabilities: []nodegraph.CapabilityRule{
				{Kind: "net", RequireScope: true, HostFromURLParam: "url"},
			},
		},
		{
			ID:          nodegraph.MustParseTypeID("transform/filter@1"),
			Description: "Substring JSON filter",
			Inlets: []nodegraph.PortDef{
				{Name: "in", Type: mustType("json"), Required: true},
				{Name: "keyword", Type: mustType("string")},
			},
			Outlets: []nodegraph.PortDef{{Name: "out", Type: mustType("json")}},
			Params: []nodegraph.ParamDef{
				{Name: "keyword", Type: nodegraph.ParamString},
			},
		},
		{
			ID:          nodegraph.MustParseTypeID("surface/log@1"),
			Description: "Structured line logger",
			Inlets:      []nodegraph.PortDef{{Name: "in", Type: mustType("any"), Required: true}},
		},
	}

	for _, d := range defs {
		if err := reg.Register(d); err != nil {
			t.Fatalf("Register(%s): %v", d.ID, err)
		}
	}
	return reg
}

func TestServerEndToEndAuthoringSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := buildTestRegistry(t)
	store := NewMemoryStore("demo.yaml", nil)
	srv := NewServer(Options{
		Registry: reg,
		Store:    store,
		AutoLayout: func(spec *nodegraph.Spec, _ *nodegraph.Registry, _ LayoutRequest) ([]nodegraph.EditOp, error) {
			var ops []nodegraph.EditOp
			for idx, id := range spec.SortedNodeIDs() {
				ops = append(ops, nodegraph.MoveNode(id, float64(40+idx*280), 120))
			}
			return ops, nil
		},
	})

	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeStdio(ctx, c2sR, s2cW)
		_ = s2cW.Close()
	}()

	client := NewClient(s2cR, c2sW)
	defer func() {
		_ = client.Close()
		<-done
	}()

	// 1. Handshake
	initRes, err := client.Initialize(ctx, ImplementationInfo{Name: "test-agent", Version: "0.1.0"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if initRes.ProtocolVersion != ProtocolVersion {
		t.Fatalf("expected protocolVersion %q, got %q", ProtocolVersion, initRes.ProtocolVersion)
	}

	// 2. Discover tools & resources
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 5 {
		t.Fatalf("expected 5 builtin authoring tools, got %d (%+v)", len(tools), tools)
	}
	resources, err := client.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 3 {
		t.Fatalf("expected 3 builtin resources, got %d", len(resources))
	}
	schemaRes, err := client.ReadResource(ctx, "schema://ntnodegraph/v1")
	if err != nil || len(schemaRes.Contents) != 1 || !strings.Contains(schemaRes.Contents[0].Text, "$schema") {
		t.Fatalf("ReadResource(schema): err=%v contents=%+v", err, schemaRes.Contents)
	}

	// 3. Call "list" with namespace filter
	listRes, err := client.CallTool(ctx, "list", map[string]any{"namespace": "transform"})
	if err != nil || listRes.IsError {
		t.Fatalf("list failed: err=%v res=%+v", err, listRes)
	}
	if !strings.Contains(listRes.Content[0].Text, "transform/filter@1") {
		t.Fatalf("expected transform/filter@1 in list output: %s", listRes.Content[0].Text)
	}

	// 4. Create initial 2-node graph via "edit"
	createRes, err := client.CallTool(ctx, "edit", map[string]any{
		"target":      "demo.yaml",
		"create":      true,
		"graph_name":  "demo-pipeline",
		"auto_layout": true,
		"ops": []map[string]any{
			{
				"op":           "add_node",
				"id":           "src",
				"type":         "source/http@1",
				"capabilities": []string{"net:example.com"},
				"params":       map[string]any{"url": "https://example.com/feed.json"},
			},
			{
				"op":   "add_node",
				"id":   "sink",
				"type": "surface/log@1",
			},
			{
				"op":   "connect",
				"from": "src.body",
				"to":   "sink.in",
			},
		},
	})
	if err != nil || createRes.IsError {
		t.Fatalf("create edit failed: err=%v res=%+v", err, createRes)
	}

	// 5. Splice transform/filter@1 into src.body -> sink.in with automatic port inference!
	spliceRes, err := client.CallTool(ctx, "edit", map[string]any{
		"target":      "demo.yaml",
		"auto_layout": true,
		"ops": []map[string]any{
			{
				"op":     "splice_node",
				"id":     "flt",
				"type":   "transform/filter@1",
				"from":   "src.body",
				"to":     "sink.in",
				"params": map[string]any{"keyword": "go"},
			},
			{
				"op":    "put_group",
				"id":    "core",
				"label": "Core Pipeline",
				"nodes": []string{"src", "flt", "sink"},
			},
		},
	})
	if err != nil || spliceRes.IsError {
		t.Fatalf("splice edit failed: err=%v res=%+v", err, spliceRes)
	}

	// 6. Verify invalid edit (cycle) rolls back atomically without corrupting stored spec!
	badRes, err := client.CallTool(ctx, "edit", map[string]any{
		"target": "demo.yaml",
		"ops": []map[string]any{
			{
				"op":   "connect",
				"from": "flt.out",
				"to":   "flt.in",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected RPC transport error on invalid edit: %v", err)
	}
	if !badRes.IsError {
		t.Fatalf("expected cycle edit to return IsError=true, got %+v", badRes)
	}

	// 7. Read & Validate confirm stored spec is intact and acyclic: [src flt sink]
	readRes, err := client.CallTool(ctx, "read", map[string]any{})
	if err != nil || readRes.IsError {
		t.Fatalf("read failed: err=%v res=%+v", err, readRes)
	}
	if !strings.Contains(readRes.Content[0].Text, `"topological_order"`) {
		t.Fatalf("expected topological_order in read output: %s", readRes.Content[0].Text)
	}
	valRes, err := client.CallTool(ctx, "validate", map[string]any{"target": "demo.yaml"})
	if err != nil || valRes.IsError || !strings.Contains(valRes.Content[0].Text, `"valid": true`) {
		t.Fatalf("validate failed: err=%v res=%+v", err, valRes)
	}
}

func TestContentLengthFramingAndMessageBounds(t *testing.T) {
	srv := NewServer(Options{
		Registry: buildTestRegistry(t),
		Limits:   Limits{MaxMessageBytes: 512, MaxOpsPerBatch: 4},
	})

	reqBody := `{"jsonrpc":"2.0","id":7,"method":"ping"}`
	framed := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(reqBody), reqBody)
	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), strings.NewReader(framed), &out); err != nil {
		t.Fatalf("ServeStdio Content-Length frame: %v", err)
	}
	if !strings.Contains(out.String(), `"id":7`) {
		t.Fatalf("expected id 7 response, got %q", out.String())
	}

	// Oversized Content-Length frame must fail closed immediately without allocating huge slice.
	huge := "Content-Length: 99999999\r\n\r\n{}"
	out.Reset()
	err := srv.ServeStdio(context.Background(), strings.NewReader(huge), &out)
	if err == nil {
		t.Fatal("expected error on oversized Content-Length frame")
	}
}

func FuzzHandleJSON(f *testing.F) {
	seeds := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":"a","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list","arguments":{"namespace":"source"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"validate","arguments":{"spec":"version: 1\nname: x\nnodes: {}\n"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"edit","arguments":{"create":true,"target":"g","ops":[{"op":"add_node","id":"s","type":"surface/log@1"}]}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`[]`,
		`{"jsonrpc":"2.0","id":{},"method":"ping"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	reg := nodegraph.NewRegistry()
	_ = reg.Register(nodegraph.TypeDef{
		ID:     nodegraph.MustParseTypeID("surface/log@1"),
		Inlets: []nodegraph.PortDef{{Name: "in", Type: nodegraph.DataType{Base: nodegraph.BaseAny}}},
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		srv := NewServer(Options{
			Registry: reg,
			Limits:   Limits{MaxMessageBytes: 64 * 1024, MaxOpsPerBatch: 32},
		})
		resp, hasReply := srv.HandleJSON(context.Background(), data)
		if hasReply {
			var check Response
			if err := json.Unmarshal(resp, &check); err != nil {
				t.Fatalf("HandleJSON emitted invalid JSON response %q: %v", string(resp), err)
			}
		}
	})
}

func FuzzDecodePatchOps(f *testing.F) {
	f.Add([]byte(`[{"op":"add_node","id":"n1","type":"surface/log@1"}]`))
	f.Add([]byte(`[{"op":"splice_node","id":"f","type":"transform/filter@1","from":"a.out","to":"b.in"}]`))
	f.Add([]byte(`[{"op":"set_param","node":"n1","key":"k","value":{"nested":[1,2,3]}}]`))
	f.Add([]byte(`[{"op":"expose_inlet","name":"in","type":"any","target":"n1.in","required":true},{"op":"expose_param","name":"k","type":"string","target":"n1.k"}]`))

	reg := nodegraph.NewRegistry()
	_ = reg.Register(nodegraph.TypeDef{
		ID:     nodegraph.MustParseTypeID("surface/log@1"),
		Inlets: []nodegraph.PortDef{{Name: "in", Type: nodegraph.DataType{Base: nodegraph.BaseAny}}},
	})
	base, _ := nodegraph.NewSpec("fuzz")

	f.Fuzz(func(_ *testing.T, data []byte) {
		ops, err := DecodePatchOps(data, Limits{MaxMessageBytes: 32 * 1024, MaxOpsPerBatch: 32})
		if err != nil {
			return
		}
		_, _ = ApplyPatchOps(base, ops, reg, nil)
	})
}

func TestServerComponentWorkflowAndBoundaryOps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := buildTestRegistry(t)
	lib := nodegraph.NewLibrary()
	store := NewMemoryStore("main.yaml", nil)
	srv := NewServer(Options{
		Registry: reg,
		Library:  lib,
		Store:    store,
	})

	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeStdio(ctx, c2sR, s2cW)
		_ = s2cW.Close()
	}()

	client := NewClient(s2cR, c2sW)
	defer func() {
		_ = client.Close()
		<-done
	}()

	if _, err := client.Initialize(ctx, ImplementationInfo{Name: "comp-agent", Version: "0.1.0"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// 1. Register a reusable component via component(action="register").
	regRes, err := client.CallTool(ctx, "component", map[string]any{
		"action": "register",
		"spec": `version: 1
name: json-gate
component_version: 1
description: Reusable keyword filter
nodes:
  f:
    type: transform/filter@1
edges: []
inlets:
  - name: in
    type: json
    target: f.in
    required: true
outlets:
  - name: out
    type: json
    target: f.out
params:
  - name: keyword
    type: string
    target: f.keyword
`,
	})
	if err != nil || regRes.IsError {
		t.Fatalf("component register failed: err=%v res=%+v", err, regRes)
	}
	if !strings.Contains(regRes.Content[0].Text, "graph/json-gate@1") {
		t.Fatalf("expected graph/json-gate@1 in register output: %s", regRes.Content[0].Text)
	}

	// 2. Read library resource & read("graph/json-gate@1") directly.
	libRes, err := client.ReadResource(ctx, "catalog://ntnodegraph/library")
	if err != nil || len(libRes.Contents) != 1 || !strings.Contains(libRes.Contents[0].Text, "graph/json-gate@1") {
		t.Fatalf("ReadResource(library): err=%v contents=%+v", err, libRes.Contents)
	}
	readCompRes, err := client.CallTool(ctx, "read", map[string]any{"target": "graph/json-gate@1"})
	if err != nil || readCompRes.IsError || !strings.Contains(readCompRes.Content[0].Text, "json-gate") {
		t.Fatalf("read(graph/json-gate@1) failed: err=%v res=%+v", err, readCompRes)
	}

	// 3. Build a 3-node graph and extract the middle section via component(action="extract").
	editRes, err := client.CallTool(ctx, "edit", map[string]any{
		"target": "main.yaml",
		"create": true,
		"ops": []map[string]any{
			{
				"op":           "add_node",
				"id":           "src",
				"type":         "source/http@1",
				"capabilities": []string{"net:example.com"},
				"params":       map[string]any{"url": "https://example.com/feed.json"},
			},
			{
				"op":     "add_node",
				"id":     "flt",
				"type":   "graph/json-gate@1",
				"params": map[string]any{"keyword": "go"},
			},
			{
				"op":   "add_node",
				"id":   "sink",
				"type": "surface/log@1",
			},
			{"op": "connect", "from": "src.body", "to": "flt.in"},
			{"op": "connect", "from": "flt.out", "to": "sink.in"},
		},
	})
	if err != nil || editRes.IsError {
		t.Fatalf("edit main.yaml failed: err=%v res=%+v", err, editRes)
	}

	extRes, err := client.CallTool(ctx, "component", map[string]any{
		"action":         "extract",
		"target":         "main.yaml",
		"nodes":          []string{"flt", "sink"},
		"component_name": "filtered-sink",
		"instance_id":    "out_stage",
		"expose_params":  []string{"flt.keyword"},
	})
	if err != nil || extRes.IsError {
		t.Fatalf("component extract failed: err=%v res=%+v", err, extRes)
	}
	if !strings.Contains(extRes.Content[0].Text, "graph/filtered-sink@1") {
		t.Fatalf("expected graph/filtered-sink@1 in extract output: %s", extRes.Content[0].Text)
	}

	// 4. Verify boundary PatchOps (expose_inlet, expose_outlet, expose_param, set_component_version).
	boundRes, err := client.CallTool(ctx, "edit", map[string]any{
		"target":           "comp-draft.yaml",
		"create":           true,
		"graph_name":       "custom-comp",
		"allow_incomplete": true,
		"ops": []map[string]any{
			{"op": "set_component_version", "component_version": 2},
			{"op": "add_node", "id": "f", "type": "transform/filter@1"},
			{"op": "expose_inlet", "name": "payload", "type": "json", "target": "f.in", "required": true},
			{"op": "expose_outlet", "name": "filtered", "type": "json", "target": "f.out"},
			{"op": "expose_param", "name": "kw", "type": "string", "target": "f.keyword", "default": "nebula"},
		},
	})
	if err != nil || boundRes.IsError {
		t.Fatalf("boundary edit failed: err=%v res=%+v", err, boundRes)
	}
	if !strings.Contains(boundRes.Content[0].Text, "component_version: 2") {
		t.Fatalf("expected component_version: 2 in YAML output: %s", boundRes.Content[0].Text)
	}
}
