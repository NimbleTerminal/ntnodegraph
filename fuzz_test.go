// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"testing"
)

func FuzzParseYAML(f *testing.F) {
	f.Add([]byte(hnBriefYAML))
	f.Add([]byte("version: 1\nname: minimal\nnodes: {}\nedges: []\n"))
	f.Add([]byte("version: 1\nname: sub\ninlets:\n  - name: x\n    type: frame<rgba8>\n    target: a.in\nnodes:\n  a:\n    type: surface/image@1\nedges: []\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		spec, err := ParseYAMLWithLimits(data, ParseLimits{
			MaxBytes:  64 << 10,
			MaxNodes:  128,
			MaxEdges:  256,
			MaxGroups: 32,
			MaxDepth:  16,
		})
		if err != nil {
			return
		}
		yaml1, err := MarshalYAML(spec)
		if err != nil {
			t.Fatalf("MarshalYAML failed after successful ParseYAML: %v", err)
		}
		spec2, err := ParseYAML(yaml1)
		if err != nil {
			t.Fatalf("ParseYAML failed on canonical output: %v\n%s", err, yaml1)
		}
		yaml2, err := MarshalYAML(spec2)
		if err != nil {
			t.Fatalf("second MarshalYAML failed: %v", err)
		}
		if !bytes.Equal(yaml1, yaml2) {
			t.Fatalf("MarshalYAML not idempotent:\n--- 1 ---\n%s\n--- 2 ---\n%s", yaml1, yaml2)
		}
		if d := Diff(spec, spec2); !d.IsEmpty() {
			t.Fatalf("YAML round-trip structural diff not empty: %+v", d)
		}
	})
}

func FuzzParseJSON(f *testing.F) {
	spec, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		f.Fatalf("seed ParseYAML: %v", err)
	}
	seedJSON, err := MarshalJSON(spec)
	if err != nil {
		f.Fatalf("seed MarshalJSON: %v", err)
	}
	f.Add(seedJSON)
	f.Add([]byte(`{"version":1,"name":"empty","nodes":{},"edges":[]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseJSONWithLimits(data, ParseLimits{
			MaxBytes:  64 << 10,
			MaxNodes:  128,
			MaxEdges:  256,
			MaxGroups: 32,
			MaxDepth:  16,
		})
		if err != nil {
			return
		}
		json1, err := MarshalJSON(parsed)
		if err != nil {
			t.Fatalf("MarshalJSON failed after successful ParseJSON: %v", err)
		}
		parsed2, err := ParseJSON(json1)
		if err != nil {
			t.Fatalf("ParseJSON failed on canonical output: %v\n%s", err, json1)
		}
		json2, err := MarshalJSON(parsed2)
		if err != nil {
			t.Fatalf("second MarshalJSON failed: %v", err)
		}
		if !bytes.Equal(json1, json2) {
			t.Fatalf("MarshalJSON not idempotent:\n--- 1 ---\n%s\n--- 2 ---\n%s", json1, json2)
		}
		if d := Diff(parsed, parsed2); !d.IsEmpty() {
			t.Fatalf("JSON round-trip structural diff not empty: %+v", d)
		}
	})
}

func FuzzParseDataType(f *testing.F) {
	for _, s := range []string{"bytes", "document", "frame<rgba8>", "texture<rgba16f>", "any", "event<string>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		dt, err := ParseDataType(s)
		if err != nil {
			return
		}
		if !dt.Valid() {
			t.Fatalf("ParseDataType(%q) returned invalid DataType %+v", s, dt)
		}
		if dt.String() != s {
			t.Fatalf("ParseDataType(%q).String() = %q", s, dt.String())
		}
	})
}

func FuzzParseTypeID(f *testing.F) {
	for _, s := range []string{"source/http@1", "wasm/neomantra.hn-typesetter@1", "graph/sub@2"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		tid, err := ParseTypeID(s)
		if err != nil {
			return
		}
		if !tid.Valid() {
			t.Fatalf("ParseTypeID(%q) returned invalid TypeID %+v", s, tid)
		}
		if tid.String() != s {
			t.Fatalf("ParseTypeID(%q).String() = %q", s, tid.String())
		}
	})
}

func FuzzSubgraphFlatten(f *testing.F) {
	compSeed := []byte("version: 1\nname: fmt\ninlets:\n  - name: in\n    type: json\n    target: p.in\noutlets:\n  - name: out\n    type: json\n    target: p.out\nnodes:\n  p:\n    type: transform/passthrough@1\nedges: []\n")
	rootSeed := []byte("version: 1\nname: root\nnodes:\n  hn:\n    type: source/http@1\n    capabilities: [net:example.com]\n    params:\n      url: https://example.com/a.json\n  f:\n    type: graph/fmt@1\n  t:\n    type: wasm/neomantra.hn-typesetter@1\n  v:\n    type: surface/nthtml@1\nedges:\n  - from: hn.body\n    to: f.in\n  - from: f.out\n    to: t.json\n  - from: t.document\n    to: v.document\n")
	f.Add(compSeed, rootSeed)
	f.Add([]byte("version: 1\nname: cyc\nnodes:\n  s:\n    type: graph/cyc@1\nedges: []\n"), []byte("version: 1\nname: r\nnodes:\n  a:\n    type: graph/cyc@1\nedges: []\n"))

	f.Fuzz(func(t *testing.T, compYAML, rootYAML []byte) {
		limits := ParseLimits{
			MaxBytes:  16 << 10,
			MaxNodes:  32,
			MaxEdges:  64,
			MaxGroups: 8,
			MaxDepth:  8,
		}
		compSpec, err := ParseYAMLWithLimits(compYAML, limits)
		if err != nil {
			return
		}
		rootSpec, err := ParseYAMLWithLimits(rootYAML, limits)
		if err != nil {
			return
		}
		reg, backend := testNebulaRegistryAndBackend(t)
		lib := NewLibrary()
		if _, err := lib.Put(compSpec, "fuzz"); err != nil {
			return
		}
		flat1, _, err := Flatten(rootSpec, reg, lib, FlattenOptions{
			MaxDepth: 4,
			MaxNodes: 64,
			MaxEdges: 128,
			Backend:  backend,
		})
		if err != nil {
			return
		}
		for _, nid := range flat1.SortedNodeIDs() {
			if flat1.Nodes[nid].Type.Namespace == NamespaceGraph {
				t.Fatalf("flattened graph still contains graph/* node %q (%s)", nid, flat1.Nodes[nid].Type)
			}
		}
		flat2, _, err := Flatten(rootSpec, reg, lib, FlattenOptions{
			MaxDepth: 4,
			MaxNodes: 64,
			MaxEdges: 128,
			Backend:  backend,
		})
		if err != nil {
			t.Fatalf("second Flatten failed: %v", err)
		}
		y1, err := MarshalYAML(flat1)
		if err != nil {
			t.Fatalf("MarshalYAML(flat1): %v", err)
		}
		y2, err := MarshalYAML(flat2)
		if err != nil {
			t.Fatalf("MarshalYAML(flat2): %v", err)
		}
		if !bytes.Equal(y1, y2) {
			t.Fatalf("Flatten output is non-deterministic:\n%s\nvs\n%s", y1, y2)
		}
	})
}
