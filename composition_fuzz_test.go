// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"fmt"
	"testing"
)

// Compare the independent schema and Go grammars, including rejected inputs.
func FuzzDataTypeSchemaParity(f *testing.F) {
	for _, s := range []string{"bytes", "frame<rgba8>", "audio/pcm", "finance/order.v2", "audio/a..b", "a/b\n", "audio/pcm<stereo>", ""} {
		f.Add(s)
	}
	sch := compileSpecSchema(f)
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 512 {
			t.Skip()
		}
		_, err := ParseDataType(name)
		doc := map[string]any{
			"version": 1, "name": "fuzz", "nodes": map[string]any{}, "edges": []any{},
			"inlets": []any{map[string]any{"name": "in", "type": name, "target": "node.in"}},
		}
		schemaErr := sch.Validate(doc)
		if (err == nil) != (schemaErr == nil) {
			t.Fatalf("type %q: parser=%v schema=%v", name, err, schemaErr)
		}
	})
}

// Generate valid DAGs so each iteration reaches extraction and flattening.
// Extract a contiguous range in topological order to avoid introducing cycles
// through a component's external boundary. Compare after undoing only ID renames.
func FuzzExtractFlattenIdentity(f *testing.F) {
	f.Add([]byte{6, 1, 3, 0, 1, 7, 3, 9})
	f.Add([]byte{8, 0, 7, 2, 255, 0, 12})
	f.Add([]byte{2, 1, 1, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 4 || len(data) > 256 {
			t.Skip()
		}
		n := 2 + int(data[0]%9)
		start := int(data[1]) % n
		count := 1 + int(data[2])%(n-start)
		types := []DataType{TypeString, TypeJSON, MustParseDataType("audio/pcm"), MustParseDataType("frame<rgba8>")}
		dt := types[int(data[3])%len(types)]
		tid := MustParseTypeID("test/pass@1")
		reg := NewRegistry()
		reg.MustRegister(TypeDef{ID: tid, Inlets: []PortDef{{Name: "in", Type: dt}}, Outlets: []PortDef{{Name: "out", Type: dt}}, Params: []ParamDef{{Name: "value", Type: ParamInt}}})
		sp, _ := NewSpec("fuzz")
		var selected []string
		for i := range n {
			id := fmt.Sprintf("n%d", i)
			sp.Nodes[id] = NodeSpec{Type: tid, Params: map[string]any{"value": int64(i)}}
			if i >= start && i < start+count {
				selected = append(selected, id)
			}
		}
		for i := range n {
			for j := i + 1; j < n; j++ {
				if j == i+1 || data[(i*n+j)%len(data)]&1 != 0 {
					sp.Edges = append(sp.Edges, EdgeSpec{From: PortRef{Node: fmt.Sprintf("n%d", i), Port: "out"}, To: PortRef{Node: fmt.Sprintf("n%d", j), Port: "in"}})
				}
			}
		}
		before := sp.Clone()
		backend := &BackendProfile{Compatibility: ExactCompatibility}
		comp, parent, _, err := ExtractSubgraph(sp, selected, reg, ExtractOptions{ComponentName: "part", InstanceID: "instance", Backend: backend})
		if err != nil {
			t.Fatal(err)
		}
		lib := NewLibrary()
		if _, err := lib.Put(comp, "fuzz"); err != nil {
			t.Fatal(err)
		}
		flat, report, err := Flatten(parent, reg, lib, FlattenOptions{Backend: backend, MaxNodes: 32, MaxEdges: 128, MaxDepth: 4})
		if err != nil {
			t.Fatal(err)
		}
		restore := func(id string) string {
			if origin, ok := report.NodeOrigins[id]; ok {
				return origin.LeafNode
			}
			return id
		}
		nodes := make(map[string]NodeSpec, len(flat.Nodes))
		for id, node := range flat.Nodes {
			nodes[restore(id)] = node
		}
		flat.Nodes = nodes
		for i := range flat.Edges {
			flat.Edges[i].From.Node = restore(flat.Edges[i].From.Node)
			flat.Edges[i].To.Node = restore(flat.Edges[i].To.Node)
		}
		// Extraction supplies layout coordinates for the new instance. Layout is
		// editor metadata, so compare execution-relevant content only.
		for id, node := range flat.Nodes {
			node.UI = nil
			flat.Nodes[id] = node
		}
		if d := Diff(before, flat); !d.IsEmpty() {
			t.Fatalf("extract/flatten changed graph: %+v", d)
		}
		if d := Diff(before, sp); !d.IsEmpty() {
			t.Fatalf("extraction mutated input: %+v", d)
		}
	})
}
