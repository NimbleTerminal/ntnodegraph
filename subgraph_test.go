// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSubgraphSynthesizeAndFlattenNested(t *testing.T) {
	reg, backend := testNebulaRegistryAndBackend(t)

	// Add a dual-inlet join/splitter transform for testing multi-target inlet & param fan-out.
	reg.MustRegister(TypeDef{
		ID:          MustParseTypeID("transform/tagger@1"),
		Description: "Tags JSON payloads with a static label and max items.",
		Inlets: []PortDef{
			{Name: "in", Type: TypeJSON, Required: true},
		},
		Outlets: []PortDef{
			{Name: "out", Type: TypeJSON},
		},
		Params: []ParamDef{
			{Name: "label", Type: ParamString, Default: "default"},
			{Name: "limit", Type: ParamInt, Default: 10, Min: FloatBound(1), Max: FloatBound(500)},
		},
		Backends: []string{"nebula"},
	})

	// Level-1 component: graph/json-prep@1 (fan-out inlet to two taggers, fan-out limit param).
	innerYAML := `
version: 1
name: json-prep
description: Dual-branch JSON tagger component.
inlets:
  - name: payload
    type: json
    targets: [tag_a.in, tag_b.in]
outlets:
  - name: primary
    type: json
    target: tag_a.out
  - name: secondary
    type: json
    target: tag_b.out
params:
  - name: limit
    type: int
    targets: [tag_a.limit, tag_b.limit]
    default: 20
    min: 1
    max: 200
  - name: primary_label
    type: string
    target: tag_a.label
    default: alpha
nodes:
  tag_a:
    type: transform/tagger@1
  tag_b:
    type: transform/tagger@1
edges: []
`
	innerSpec, err := ParseYAML([]byte(innerYAML))
	if err != nil {
		t.Fatalf("ParseYAML(inner): %v", err)
	}

	// Level-2 component wrapping graph/json-prep@1 + wasm typesetter: graph/story-card@1.
	outerCompYAML := `
version: 1
name: story-card
component_version: 1
description: Nested story typesetter card.
inlets:
  - name: body
    type: json
    target: prep.payload
outlets:
  - name: doc
    type: document
    target: typeset.document
params:
  - name: max_stories
    type: int
    targets: [prep.limit, typeset.limit]
    default: 15
    min: 1
    max: 100
nodes:
  prep:
    type: graph/json-prep@1
  typeset:
    type: wasm/neomantra.hn-typesetter@1
edges:
  - from: prep.primary
    to: typeset.json
`
	outerCompSpec, err := ParseYAML([]byte(outerCompYAML))
	if err != nil {
		t.Fatalf("ParseYAML(outerComp): %v", err)
	}

	lib := NewLibrary()
	if _, err := lib.Put(innerSpec, "test"); err != nil {
		t.Fatalf("lib.Put(inner): %v", err)
	}
	if _, err := lib.Put(outerCompSpec, "test"); err != nil {
		t.Fatalf("lib.Put(outerComp): %v", err)
	}

	defs, err := lib.SyncRegistry(reg, ValidateOptions{Backend: backend})
	if err != nil {
		t.Fatalf("SyncRegistry: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected 2 synthesized defs, got %d", len(defs))
	}

	storyDef, ok := reg.Lookup(MustParseTypeID("graph/story-card@1"))
	if !ok {
		t.Fatal("expected graph/story-card@1 in registry")
	}
	bodyInlet, ok := storyDef.Inlet("body")
	if !ok || !bodyInlet.Required {
		t.Fatalf("expected required 'body' inlet on graph/story-card@1, got %+v", bodyInlet)
	}

	// Root graph instantiating graph/story-card@1.
	rootYAML := `
version: 1
name: hn-nested-root
nodes:
  hn:
    type: source/http@1
    capabilities: [net:hacker-news.firebaseio.com]
    params:
      url: https://hacker-news.firebaseio.com/v0/topstories.json
  card:
    type: graph/story-card@1
    params:
      max_stories: 42
  view:
    type: surface/nthtml@1
edges:
  - from: hn.body
    to: card.body
  - from: card.doc
    to: view.document
`
	rootSpec, err := ParseYAML([]byte(rootYAML))
	if err != nil {
		t.Fatalf("ParseYAML(root): %v", err)
	}

	// Base concrete registry (without graph/* pre-registered) must flatten cleanly via lib.
	concreteReg, _ := testNebulaRegistryAndBackend(t)
	concreteReg.MustRegister(TypeDef{
		ID:       MustParseTypeID("transform/tagger@1"),
		Inlets:   []PortDef{{Name: "in", Type: TypeJSON, Required: true}},
		Outlets:  []PortDef{{Name: "out", Type: TypeJSON}},
		Params:   []ParamDef{{Name: "label", Type: ParamString, Default: "default"}, {Name: "limit", Type: ParamInt, Default: 10}},
		Backends: []string{"nebula"},
	})

	flat, report, err := Flatten(rootSpec, concreteReg, lib, FlattenOptions{Backend: backend})
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	if report.ExpandedInstances != 2 {
		t.Fatalf("ExpandedInstances = %d, want 2", report.ExpandedInstances)
	}

	wantNodes := []string{
		"card__prep__tag_a",
		"card__prep__tag_b",
		"card__typeset",
		"hn",
		"view",
	}
	if got := flat.SortedNodeIDs(); !slices.Equal(got, wantNodes) {
		t.Fatalf("flattened nodes = %v, want %v", got, wantNodes)
	}

	// Verify 2-level parameter forwarding + default inheritance.
	if got := flat.Nodes["card__prep__tag_a"].Params["limit"]; got != int64(42) {
		t.Fatalf("card__prep__tag_a.limit = %v (%T), want int64(42)", got, got)
	}
	if got := flat.Nodes["card__prep__tag_b"].Params["limit"]; got != int64(42) {
		t.Fatalf("card__prep__tag_b.limit = %v (%T), want int64(42)", got, got)
	}
	if got := flat.Nodes["card__typeset"].Params["limit"]; got != int64(42) {
		t.Fatalf("card__typeset.limit = %v (%T), want int64(42)", got, got)
	}
	if got := flat.Nodes["card__prep__tag_a"].Params["label"]; got != "alpha" {
		t.Fatalf("card__prep__tag_a.label = %v, want \"alpha\"", got)
	}

	// Verify report origin path and boundary port rewrite tables.
	orig := report.NodeOrigins["card__prep__tag_a"]
	if !slices.Equal(orig.InstancePath, []string{"card", "prep"}) || orig.LeafNode != "tag_a" {
		t.Fatalf("unexpected origin for card__prep__tag_a: %+v", orig)
	}
	gotInTargets := report.InletTargets[MustParsePortRef("card.body")]
	wantInTargets := []PortRef{
		MustParsePortRef("card__prep__tag_a.in"),
		MustParsePortRef("card__prep__tag_b.in"),
	}
	if !slices.Equal(gotInTargets, wantInTargets) {
		t.Fatalf("InletTargets[card.body] = %v, want %v", gotInTargets, wantInTargets)
	}
	if gotOut := report.OutletSources[MustParsePortRef("card.doc")]; gotOut != MustParsePortRef("card__typeset.document") {
		t.Fatalf("OutletSources[card.doc] = %v, want card__typeset.document", gotOut)
	}

	// LibraryJSON must serialize deterministically.
	libJSON1, err := lib.LibraryJSON(reg)
	if err != nil {
		t.Fatalf("LibraryJSON: %v", err)
	}
	libJSON2, err := lib.LibraryJSON(reg)
	if err != nil {
		t.Fatalf("LibraryJSON(2): %v", err)
	}
	if !bytes.Equal(libJSON1, libJSON2) {
		t.Fatal("LibraryJSON is not byte-for-byte deterministic")
	}
}

func TestSubgraphCapabilityRollupAndFailClosedSmuggling(t *testing.T) {
	reg, backend := testNebulaRegistryAndBackend(t)
	reg.MustRegister(TypeDef{
		ID:       MustParseTypeID("source/const-num@1"),
		Outlets:  []PortDef{{Name: "val", Type: TypeNumber}},
		Backends: []string{"nebula"},
	})
	reg.MustRegister(TypeDef{
		ID:       MustParseTypeID("surface/tex-sink@1"),
		Inlets:   []PortDef{{Name: "in", Type: TypeTexture, Required: true}},
		Backends: []string{"nebula"},
	})

	// 1. GPU capability inside component cannot be smuggled without outer capabilities: [gpu].
	gpuCompYAML := `
version: 1
name: gpu-stage
inlets:
  - name: roughness
    type: number
    target: mat.roughness
outlets:
  - name: tex
    type: texture<rgba8>
    target: mat.out
nodes:
  mat:
    type: gpu/pbr-material@1
    capabilities: [gpu]
edges: []
`
	gpuComp, err := ParseYAML([]byte(gpuCompYAML))
	if err != nil {
		t.Fatalf("ParseYAML(gpuComp): %v", err)
	}
	lib := NewLibrary()
	if _, err := lib.Put(gpuComp, "stdlib"); err != nil {
		t.Fatalf("lib.Put(gpuComp): %v", err)
	}

	unprivilegedRootYAML := `
version: 1
name: missing-gpu-cap
nodes:
  src:
    type: source/const-num@1
  stage:
    type: graph/gpu-stage@1
  sink:
    type: surface/tex-sink@1
edges:
  - from: src.val
    to: stage.roughness
  - from: stage.tex
    to: sink.in
`
	unprivRoot, err := ParseYAML([]byte(unprivilegedRootYAML))
	if err != nil {
		t.Fatalf("ParseYAML(unprivRoot): %v", err)
	}
	if _, _, err := Flatten(unprivRoot, reg, lib, FlattenOptions{Backend: backend}); !errors.Is(err, ErrUndeclaredCapability) {
		t.Fatalf("expected ErrUndeclaredCapability when outer graph/* omits gpu, got: %v", err)
	}

	privRoot, err := ApplyEdits(unprivRoot, SetCapabilities("stage", MustParseCapability("gpu")))
	if err != nil {
		t.Fatalf("ApplyEdits(SetCapabilities): %v", err)
	}
	if _, _, err := Flatten(privRoot, reg, lib, FlattenOptions{Backend: backend}); err != nil {
		t.Fatalf("expected Flatten to succeed with outer gpu capability, got: %v", err)
	}

	// 2. WASI node-level fs:/assets/* capability on wasm/* node rolls up even though
	// wasm/neomantra.hn-typesetter@1 has empty TypeDef.Capabilities.
	wasmFSCompYAML := `
version: 1
name: wasm-fs-box
inlets:
  - name: json
    type: json
    target: ts.json
outlets:
  - name: doc
    type: document
    target: ts.document
nodes:
  ts:
    type: wasm/neomantra.hn-typesetter@1
    capabilities: ["fs:/assets/*"]
edges: []
`
	wasmComp, err := ParseYAML([]byte(wasmFSCompYAML))
	if err != nil {
		t.Fatalf("ParseYAML(wasmComp): %v", err)
	}
	if _, err := lib.Put(wasmComp, "test"); err != nil {
		t.Fatalf("lib.Put(wasmComp): %v", err)
	}
	reg.MustRegister(TypeDef{
		ID:       MustParseTypeID("source/const-json@1"),
		Outlets:  []PortDef{{Name: "out", Type: TypeJSON}},
		Backends: []string{"nebula"},
	})
	wasmRootYAML := `
version: 1
name: wasm-root
nodes:
  src:
    type: source/const-json@1
  box:
    type: graph/wasm-fs-box@1
  view:
    type: surface/nthtml@1
edges:
  - from: src.out
    to: box.json
  - from: box.doc
    to: view.document
`
	wasmRoot, err := ParseYAML([]byte(wasmRootYAML))
	if err != nil {
		t.Fatalf("ParseYAML(wasmRoot): %v", err)
	}
	if _, _, err := Flatten(wasmRoot, reg, lib, FlattenOptions{Backend: backend}); !errors.Is(err, ErrUndeclaredCapability) {
		t.Fatalf("expected ErrUndeclaredCapability when outer omits fs:/assets/*, got: %v", err)
	}
	wasmRootOK, err := ApplyEdits(wasmRoot, SetCapabilities("box", MustParseCapability("fs:/assets/*")))
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if _, _, err := Flatten(wasmRootOK, reg, lib, FlattenOptions{Backend: backend}); err != nil {
		t.Fatalf("expected Flatten to pass with fs:/assets/*, got: %v", err)
	}

	// 3. Dynamic HostFromURLParam forwarding swaps required net:<host> cleanly.
	httpCompYAML := `
version: 1
name: json-poller
outlets:
  - name: body
    type: bytes
    target: fetch.body
params:
  - name: endpoint
    type: string
    target: fetch.url
    default: https://hacker-news.firebaseio.com/v0/topstories.json
nodes:
  fetch:
    type: source/http@1
    capabilities: [net:hacker-news.firebaseio.com]
    params:
      url: https://hacker-news.firebaseio.com/v0/topstories.json
edges: []
`
	httpComp, err := ParseYAML([]byte(httpCompYAML))
	if err != nil {
		t.Fatalf("ParseYAML(httpComp): %v", err)
	}
	if _, err := lib.Put(httpComp, "test"); err != nil {
		t.Fatalf("lib.Put(httpComp): %v", err)
	}

	httpRootWrongCapYAML := `
version: 1
name: override-host
nodes:
  poll:
    type: graph/json-poller@1
    capabilities: [net:hacker-news.firebaseio.com]
    params:
      endpoint: https://api.github.com/events
  ts:
    type: wasm/neomantra.hn-typesetter@1
  view:
    type: surface/nthtml@1
edges:
  - from: poll.body
    to: ts.json
  - from: ts.document
    to: view.document
`
	httpRootWrong, err := ParseYAML([]byte(httpRootWrongCapYAML))
	if err != nil {
		t.Fatalf("ParseYAML(httpRootWrong): %v", err)
	}
	if _, _, err := Flatten(httpRootWrong, reg, lib, FlattenOptions{Backend: backend}); !errors.Is(err, ErrUndeclaredCapability) {
		t.Fatalf("expected ErrUndeclaredCapability when overridden URL host does not match outer capability, got: %v", err)
	}

	httpRootRight, err := ApplyEdits(httpRootWrong, SetCapabilities("poll", MustParseCapability("net:api.github.com")))
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	flatHTTP, _, err := Flatten(httpRootRight, reg, lib, FlattenOptions{Backend: backend})
	if err != nil {
		t.Fatalf("Flatten(httpRootRight): %v", err)
	}
	gotCaps := flatHTTP.Nodes["poll__fetch"].Capabilities
	wantCaps := []Capability{MustParseCapability("net:api.github.com")}
	if !slices.Equal(gotCaps, wantCaps) {
		t.Fatalf("poll__fetch capabilities = %v, want %v", gotCaps, wantCaps)
	}
}

func TestSubgraphCycleDepthAndIDBounds(t *testing.T) {
	reg, backend := testNebulaRegistryAndBackend(t)

	// 1. Mutual cycle A -> B -> A.
	compA, _ := ParseYAML([]byte("version: 1\nname: comp-a\nnodes:\n  b:\n    type: graph/comp-b@1\nedges: []\n"))
	compB, _ := ParseYAML([]byte("version: 1\nname: comp-b\nnodes:\n  a:\n    type: graph/comp-a@1\nedges: []\n"))
	cycLib := NewLibrary()
	_, _ = cycLib.Put(compA, "test")
	_, _ = cycLib.Put(compB, "test")

	if _, err := cycLib.SyncRegistry(reg.Clone(), ValidateOptions{Backend: backend}); !errors.Is(err, ErrSubgraphCycle) {
		t.Fatalf("expected ErrSubgraphCycle from SyncRegistry, got: %v", err)
	}
	rootCyc, _ := ParseYAML([]byte("version: 1\nname: root\nnodes:\n  x:\n    type: graph/comp-a@1\nedges: []\n"))
	if _, _, err := Flatten(rootCyc, reg, cycLib, FlattenOptions{Backend: backend}); !errors.Is(err, ErrSubgraphCycle) {
		t.Fatalf("expected ErrSubgraphCycle from Flatten, got: %v", err)
	}

	// 2. Depth exceeded.
	d1, _ := ParseYAML([]byte("version: 1\nname: d1\ninlets:\n  - name: in\n    type: json\n    target: p.in\noutlets:\n  - name: out\n    type: json\n    target: p.out\nnodes:\n  p:\n    type: transform/passthrough@1\nedges: []\n"))
	d2, _ := ParseYAML([]byte("version: 1\nname: d2\ninlets:\n  - name: in\n    type: json\n    target: c.in\noutlets:\n  - name: out\n    type: json\n    target: c.out\nnodes:\n  c:\n    type: graph/d1@1\nedges: []\n"))
	d3, _ := ParseYAML([]byte("version: 1\nname: d3\ninlets:\n  - name: in\n    type: json\n    target: c.in\noutlets:\n  - name: out\n    type: json\n    target: c.out\nnodes:\n  c:\n    type: graph/d2@1\nedges: []\n"))
	depthLib := NewLibrary()
	_, _ = depthLib.Put(d1, "test")
	_, _ = depthLib.Put(d2, "test")
	_, _ = depthLib.Put(d3, "test")

	reg.MustRegister(TypeDef{
		ID:       MustParseTypeID("source/const-json@1"),
		Outlets:  []PortDef{{Name: "out", Type: TypeJSON}},
		Backends: []string{"nebula"},
	})
	deepRoot, _ := ParseYAML([]byte("version: 1\nname: deep\nnodes:\n  s:\n    type: source/const-json@1\n  x:\n    type: graph/d3@1\nedges:\n  - from: s.out\n    to: x.in\n"))
	if _, _, err := Flatten(deepRoot, reg, depthLib, FlattenOptions{MaxDepth: 2, Backend: backend}); !errors.Is(err, ErrSubgraphDepthExceeded) {
		t.Fatalf("expected ErrSubgraphDepthExceeded with MaxDepth=2, got: %v", err)
	}
	if _, _, err := Flatten(deepRoot, reg, depthLib, FlattenOptions{MaxDepth: 3, Backend: backend}); err != nil {
		t.Fatalf("expected MaxDepth=3 to succeed, got: %v", err)
	}

	// 3. Long ID hashing stays <= MaxIDLen (128) and valid.
	longOuter := strings.Repeat("o", 90)
	longInner := strings.Repeat("i", 90)
	longComp := &Spec{
		Version: SpecVersion,
		Name:    "long-id",
		Inlets:  []ExposedPortSpec{{Name: "in", Type: TypeJSON, Target: PortRef{Node: longInner, Port: "in"}}},
		Nodes: map[string]NodeSpec{
			longInner: {Type: MustParseTypeID("transform/passthrough@1")},
		},
	}
	longLib := NewLibrary()
	if _, err := longLib.Put(longComp, "test"); err != nil {
		t.Fatalf("longLib.Put: %v", err)
	}
	longRoot := &Spec{
		Version: SpecVersion,
		Name:    "long-root",
		Nodes: map[string]NodeSpec{
			"src":     {Type: MustParseTypeID("source/const-json@1")},
			longOuter: {Type: MustParseTypeID("graph/long-id@1")},
		},
		Edges: []EdgeSpec{{From: MustParsePortRef("src.out"), To: PortRef{Node: longOuter, Port: "in"}}},
	}
	flatLong, _, err := Flatten(longRoot, reg, longLib, FlattenOptions{Backend: backend})
	if err != nil {
		t.Fatalf("Flatten(longRoot): %v", err)
	}
	for _, nid := range flatLong.SortedNodeIDs() {
		if !IsValidID(nid) || len(nid) > MaxIDLen {
			t.Fatalf("invalid flattened long node ID %q (len=%d)", nid, len(nid))
		}
	}
}

func TestExtractSubgraphAndPromoteGroupRoundTrip(t *testing.T) {
	reg, backend := testNebulaRegistryAndBackend(t)

	orig, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	withGroup, err := ApplyEdits(
		orig,
		MoveNode("hn", 100, 100),
		MoveNode("typeset", 360, 100),
		MoveNode("view", 620, 100),
		PutGroup("formatter_grp", GroupSpec{
			Label: "HN Formatter",
			Nodes: []string{"typeset"},
			UI:    &GroupUI{X: 340, Y: 80, Width: 240, Height: 160},
		}),
	)
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}

	comp, updatedParent, compDef, err := PromoteGroupToSubgraph(withGroup, "formatter_grp", reg, ExtractOptions{
		ExposeParams: []PortRef{MustParsePortRef("typeset.limit")},
		Backend:      backend,
	})
	if err != nil {
		t.Fatalf("PromoteGroupToSubgraph: %v", err)
	}
	if compDef.ID != MustParseTypeID("graph/hn-formatter@1") {
		t.Fatalf("compDef.ID = %s, want graph/hn-formatter@1", compDef.ID)
	}
	if _, stillHasGroup := updatedParent.Groups["formatter_grp"]; stillHasGroup {
		t.Fatal("expected promoted group to be removed from updatedParent")
	}
	if _, hasInst := updatedParent.Nodes["formatter_grp"]; !hasInst {
		t.Fatalf("expected instance node formatter_grp in updatedParent, got %v", updatedParent.SortedNodeIDs())
	}

	lib := NewLibrary()
	if _, err := lib.Put(comp, "studio"); err != nil {
		t.Fatalf("lib.Put: %v", err)
	}
	flat, _, err := Flatten(updatedParent, reg, lib, FlattenOptions{Backend: backend})
	if err != nil {
		t.Fatalf("Flatten(updatedParent): %v", err)
	}
	if len(flat.Nodes) != 3 || len(flat.Edges) != 2 {
		t.Fatalf("expected 3 nodes and 2 edges after round-trip flatten, got %d nodes, %d edges", len(flat.Nodes), len(flat.Edges))
	}
}

func TestBoundaryEditOpsAndMultiTargetPruning(t *testing.T) {
	spec, err := NewSpec("comp-edit")
	if err != nil {
		t.Fatalf("NewSpec: %v", err)
	}
	edited, err := ApplyEdits(
		spec,
		SetComponentVersion(2),
		AddNode("a", NodeSpec{Type: MustParseTypeID("transform/passthrough@1")}),
		AddNode("b", NodeSpec{Type: MustParseTypeID("transform/passthrough@1")}),
		ExposeInlet(ExposedPortSpec{
			Name:    "in",
			Type:    TypeJSON,
			Targets: []PortRef{MustParsePortRef("a.in"), MustParsePortRef("b.in")},
		}),
		ExposeOutlet(ExposedPortSpec{
			Name:   "out",
			Type:   TypeJSON,
			Target: MustParsePortRef("a.out"),
		}),
		ExposeParam(ExposedParamSpec{
			Name:    "cfg",
			Type:    ParamJSON,
			Targets: []PortRef{MustParsePortRef("a.cfg"), MustParsePortRef("b.cfg")},
			Default: map[string]any{"k": "v"},
		}),
	)
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if d := Diff(spec, edited); !d.ComponentVersionChanged || !d.InletsChanged || !d.OutletsChanged || !d.ParamsChanged || !d.HasRuntimeChanges() {
		t.Fatalf("expected Diff to report boundary changes, got %+v", d)
	}

	// Removing node "b" should prune "b" from multi-target inlet & param, leaving single Target "a.*".
	afterRemoveB, err := ApplyEdits(edited, RemoveNode("b"))
	if err != nil {
		t.Fatalf("RemoveNode(b): %v", err)
	}
	if len(afterRemoveB.Inlets) != 1 || afterRemoveB.Inlets[0].Target != MustParsePortRef("a.in") || len(afterRemoveB.Inlets[0].Targets) != 0 {
		t.Fatalf("expected single canonical target a.in on inlet, got %+v", afterRemoveB.Inlets)
	}
	if len(afterRemoveB.Params) != 1 || afterRemoveB.Params[0].Target != MustParsePortRef("a.cfg") || len(afterRemoveB.Params[0].Targets) != 0 {
		t.Fatalf("expected single canonical target a.cfg on param, got %+v", afterRemoveB.Params)
	}

	// Removing remaining boundary items cleanly empties slices.
	cleaned, err := ApplyEdits(
		afterRemoveB,
		RemoveInlet("in"),
		RemoveOutlet("out"),
		RemoveParam("cfg"),
		SetComponentVersion(0),
	)
	if err != nil {
		t.Fatalf("cleanup ApplyEdits: %v", err)
	}
	if len(cleaned.Inlets) != 0 || len(cleaned.Outlets) != 0 || len(cleaned.Params) != 0 || cleaned.ComponentVersion != 0 {
		t.Fatalf("expected all boundary items removed, got %+v", cleaned)
	}
}
