// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"context"
	"testing"

	ng "nimbleterminal.dev/ntnodegraph"
)

func TestSpliceUsesHostCompatibility(t *testing.T) {
	reg := ng.NewRegistry()
	source := ng.MustParseTypeID("test/source@1")
	sink := ng.MustParseTypeID("test/sink@1")
	transform := ng.MustParseTypeID("test/transform@1")
	reg.MustRegister(ng.TypeDef{ID: source, Outlets: []ng.PortDef{{Name: "out", Type: ng.TypeString}}})
	reg.MustRegister(ng.TypeDef{ID: sink, Inlets: []ng.PortDef{{Name: "in", Type: ng.TypeString}}})
	reg.MustRegister(ng.TypeDef{ID: transform,
		Inlets:  []ng.PortDef{{Name: "bytes", Type: ng.TypeBytes}, {Name: "text", Type: ng.TypeString}},
		Outlets: []ng.PortDef{{Name: "out", Type: ng.TypeString}},
	})
	sp, _ := ng.NewSpec("splice")
	sp.Nodes["source"] = ng.NodeSpec{Type: source}
	sp.Nodes["sink"] = ng.NodeSpec{Type: sink}
	sp.Edges = []ng.EdgeSpec{{From: ng.MustParsePortRef("source.out"), To: ng.MustParsePortRef("sink.in")}}
	store := NewMemoryStore("splice", map[string]*ng.Spec{"splice": sp})
	server := NewServer(Options{Registry: reg, Store: store, Backend: &ng.BackendProfile{Compatibility: ng.ExactCompatibility}})
	result, err := server.handleEdit(context.Background(), []byte(`{"ops":[{"op":"splice_node","id":"middle","type":"test/transform@1","from":"source.out","to":"sink.in"}]}`))
	if err != nil || result.IsError {
		t.Fatalf("splice failed: %+v %v", result, err)
	}
	got, _, _ := store.Get(context.Background(), "splice")
	for _, edge := range got.Edges {
		if edge.To.Node == "middle" && edge.To.Port != "text" {
			t.Fatalf("splice used rejected conversion: %+v", edge)
		}
	}
}

func TestListReturnsOpaqueHostAnnotations(t *testing.T) {
	reg := ng.NewRegistry()
	reg.MustRegister(ng.TypeDef{ID: ng.MustParseTypeID("test/node@1")})
	server := NewServer(Options{Registry: reg, AnnotateType: func(id ng.TypeID) (TypeAnnotation, bool) {
		return TypeAnnotation{"example.org/runtime": map[string]any{"mode": "batch"}}, true
	}})
	result, err := server.handleList(context.Background(), []byte(`{}`))
	if err != nil || result.IsError {
		t.Fatalf("list failed: %+v %v", result, err)
	}
	summaries := result.StructuredContent.(map[string]any)["node_types"].([]nodeTypeSummary)
	metadata := summaries[0].Annotations["example.org/runtime"].(map[string]any)
	if metadata["mode"] != "batch" {
		t.Fatalf("host annotations lost: %+v", metadata)
	}
	server.annotateType = func(id ng.TypeID) (TypeAnnotation, bool) {
		return TypeAnnotation{"example.org/runtime": "ignored"}, false
	}
	result, err = server.handleList(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	summaries = result.StructuredContent.(map[string]any)["node_types"].([]nodeTypeSummary)
	if len(summaries[0].Annotations) != 0 {
		t.Fatal("unavailable annotation was included")
	}
}
