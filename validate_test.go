// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func testNebulaRegistryAndBackend(tb testing.TB) (*Registry, *BackendProfile) {
	tb.Helper()
	reg := NewRegistry()

	reg.MustRegister(TypeDef{
		ID:          MustParseTypeID("source/http@1"),
		Description: "Polls an HTTP(S) endpoint under host net capability control.",
		Outlets: []PortDef{
			{Name: "body", Type: TypeBytes, Description: "Response body bytes."},
			{Name: "status", Type: TypeNumber, Description: "HTTP status code."},
		},
		Params: []ParamDef{
			{Name: "url", Type: ParamString, Required: true, Description: "Target HTTP(S) URL."},
			{Name: "poll", Type: ParamDuration, Default: "60s", Description: "Polling interval."},
		},
		Capabilities: []CapabilityRule{
			{
				Kind:             "net",
				RequireScope:     true,
				HostFromURLParam: "url",
				Description:      "Host network permission for the target URL hostname.",
			},
		},
		Backends: []string{"nebula"},
	})

	reg.MustRegister(TypeDef{
		ID:          MustParseTypeID("wasm/neomantra.hn-typesetter@1"),
		Description: "Formats Hacker News top stories JSON into an NT HTML typeset document.",
		Inlets: []PortDef{
			{Name: "json", Type: TypeJSON, Required: true},
		},
		Outlets: []PortDef{
			{Name: "document", Type: TypeDocument},
		},
		Params: []ParamDef{
			{Name: "limit", Type: ParamInt, Default: 30, Min: FloatBound(1), Max: FloatBound(500)},
		},
		Backends: []string{"nebula"},
	})

	reg.MustRegister(TypeDef{
		ID:          MustParseTypeID("surface/nthtml@1"),
		Description: "Displays a typeset document in an NT HTML surface.",
		Inlets: []PortDef{
			{Name: "document", Type: TypeDocument, Required: true},
		},
		Backends: []string{"nebula"},
	})

	reg.MustRegister(TypeDef{
		ID:          MustParseTypeID("gpu/pbr-material@1"),
		Description: "Test GPU material node with numeric roughness inlet.",
		Inlets: []PortDef{
			{Name: "roughness", Type: TypeNumber, Required: true},
		},
		Outlets: []PortDef{
			{Name: "out", Type: MustParseDataType("texture<rgba8>")},
		},
		Capabilities: []CapabilityRule{
			{Kind: "gpu"},
		},
		Backends: []string{"nebula"},
	})

	reg.MustRegister(TypeDef{
		ID:          MustParseTypeID("transform/passthrough@1"),
		Description: "Passes json to json for cycle testing.",
		Inlets:      []PortDef{{Name: "in", Type: TypeJSON, Required: true}},
		Outlets:     []PortDef{{Name: "out", Type: TypeJSON}},
		Backends:    []string{"nebula"},
	})

	backend := &BackendProfile{
		Name:            "nebula",
		EdgeKinds:       []string{"signal", "frame", "event"},
		DefaultEdgeKind: "signal",
		AllowCycles:     false,
		Namespaces:      []string{NamespaceSource, NamespaceTransform, NamespaceSurface, NamespaceWASM, NamespaceGPU},
		Capabilities:    []string{"net", "fs", "gpu", "storage", "tty"},
	}
	return reg, backend
}

func TestValidateHackerNewsSpec(t *testing.T) {
	reg, backend := testNebulaRegistryAndBackend(t)
	spec, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	report, err := Validate(spec, reg, ValidateOptions{Backend: backend})
	if err != nil {
		t.Fatalf("Validate unexpected error: %v", err)
	}

	wantOrder := []string{"hn", "typeset", "view"}
	if !slices.Equal(report.TopologicalOrder, wantOrder) {
		t.Fatalf("TopologicalOrder = %v, want %v", report.TopologicalOrder, wantOrder)
	}
	if len(report.Edges) != 2 {
		t.Fatalf("len(report.Edges) = %d, want 2", len(report.Edges))
	}
	if report.Edges[0].Compatibility != CompatCoerce || report.Edges[0].Coercion != CoerceJSONParse {
		t.Fatalf("expected hn.body -> typeset.json to resolve CoerceJSONParse, got %+v", report.Edges[0])
	}
	if report.Edges[0].EffectiveKind != "signal" {
		t.Fatalf("expected default EffectiveKind 'signal', got %q", report.Edges[0].EffectiveKind)
	}
	if report.Edges[1].Compatibility != CompatExact {
		t.Fatalf("expected typeset.document -> view.document CompatExact, got %+v", report.Edges[1])
	}
}

func TestValidateRejectsInvalidGraphs(t *testing.T) {
	reg, backend := testNebulaRegistryAndBackend(t)

	t.Run("unknown node type", func(t *testing.T) {
		spec, _ := ParseYAML([]byte(`
version: 1
name: unknown-type
nodes:
  mystery:
    type: transform/nonexistent@1
edges: []
`))
		_, err := Validate(spec, reg, ValidateOptions{Backend: backend})
		if !errors.Is(err, ErrUnknownNodeType) {
			t.Fatalf("got %v, want ErrUnknownNodeType", err)
		}
	})

	t.Run("reserved graph namespace unresolved", func(t *testing.T) {
		spec, _ := ParseYAML([]byte(`
version: 1
name: subgraph-ref
nodes:
  sub:
    type: graph/hn-card@1
edges: []
`))
		_, err := Validate(spec, reg, ValidateOptions{Backend: backend})
		if !errors.Is(err, ErrUnresolvedSubgraph) {
			t.Fatalf("got %v, want ErrUnresolvedSubgraph", err)
		}
	})

	t.Run("type mismatch document into roughness number", func(t *testing.T) {
		spec, _ := ParseYAML([]byte(hnBriefYAML))
		spec, err := ApplyEdits(
			spec,
			AddNode("mat", NodeSpec{
				Type:         MustParseTypeID("gpu/pbr-material@1"),
				Capabilities: []Capability{MustParseCapability("gpu")},
			}),
			Connect(MustParsePortRef("typeset.document"), MustParsePortRef("mat.roughness"), ""),
		)
		if err != nil {
			t.Fatalf("ApplyEdits: %v", err)
		}
		_, err = Validate(spec, reg, ValidateOptions{Backend: backend})
		if !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("got %v, want ErrTypeMismatch", err)
		}
	})

	t.Run("missing required inlet", func(t *testing.T) {
		spec, _ := ParseYAML([]byte(`
version: 1
name: disconnected-view
nodes:
  view:
    type: surface/nthtml@1
edges: []
`))
		_, err := Validate(spec, reg, ValidateOptions{Backend: backend})
		if !errors.Is(err, ErrMissingRequiredInlet) {
			t.Fatalf("got %v, want ErrMissingRequiredInlet", err)
		}

		// Exposing view.document as a subgraph boundary inlet satisfies the inlet requirement.
		spec.Inlets = []ExposedPortSpec{
			{Name: "doc", Type: TypeDocument, Target: MustParsePortRef("view.document")},
		}
		if _, err := Validate(spec, reg, ValidateOptions{Backend: backend}); err != nil {
			t.Fatalf("expected exposed inlet to satisfy required inlet, got: %v", err)
		}
	})

	t.Run("undeclared or mismatched capability", func(t *testing.T) {
		spec, _ := ParseYAML([]byte(hnBriefYAML))
		noCap, _ := ApplyEdits(spec, SetCapabilities("hn"))
		if _, err := Validate(noCap, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrUndeclaredCapability) {
			t.Fatalf("missing capability: got %v, want ErrUndeclaredCapability", err)
		}

		wrongHost, _ := ApplyEdits(spec, SetCapabilities("hn", MustParseCapability("net:example.com")))
		if _, err := Validate(wrongHost, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrUndeclaredCapability) {
			t.Fatalf("wrong host capability: got %v, want ErrUndeclaredCapability", err)
		}

		wildcardHost, _ := ApplyEdits(spec, SetCapabilities("hn", MustParseCapability("net:*.firebaseio.com")))
		if _, err := Validate(wildcardHost, reg, ValidateOptions{Backend: backend}); err != nil {
			t.Fatalf("wildcard host capability should succeed, got: %v", err)
		}

		// Empty, missing, hostless, or non-string URL param must fail closed with ErrInvalidParam,
		// even when another net:* capability is present.
		emptyURL, _ := ApplyEdits(
			spec,
			SetParam("hn", "url", ""),
			SetCapabilities("hn", MustParseCapability("net:example.com")),
		)
		if _, err := Validate(emptyURL, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrInvalidParam) {
			t.Fatalf("empty url param: got %v, want ErrInvalidParam", err)
		}

		hostlessURL, _ := ApplyEdits(
			spec,
			SetParam("hn", "url", "https:///v0/topstories.json"),
			SetCapabilities("hn", MustParseCapability("net:example.com")),
		)
		if _, err := Validate(hostlessURL, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrInvalidParam) {
			t.Fatalf("hostless url param: got %v, want ErrInvalidParam", err)
		}

		missingURL, _ := ApplyEdits(
			spec,
			DeleteParam("hn", "url"),
			SetCapabilities("hn", MustParseCapability("net:example.com")),
		)
		if _, err := Validate(missingURL, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrInvalidParam) {
			t.Fatalf("missing url param: got %v, want ErrInvalidParam", err)
		}

		nonStringURL, _ := ApplyEdits(
			spec,
			SetParam("hn", "url", 123),
			SetCapabilities("hn", MustParseCapability("net:example.com")),
		)
		if _, err := Validate(nonStringURL, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrInvalidParam) {
			t.Fatalf("non-string url param: got %v, want ErrInvalidParam", err)
		}
	})

	t.Run("cycle forbidden vs cycle breaking edge kind", func(t *testing.T) {
		spec, err := ParseYAML([]byte(`
version: 1
name: feedback-loop
nodes:
  a:
    type: transform/passthrough@1
  b:
    type: transform/passthrough@1
edges:
  - from: a.out
    to: b.in
  - from: b.out
    to: a.in
    kind: frame
`))
		if err != nil {
			t.Fatalf("ParseYAML: %v", err)
		}
		_, err = Validate(spec, reg, ValidateOptions{Backend: backend})
		if !errors.Is(err, ErrCycleDetected) {
			t.Fatalf("got %v, want ErrCycleDetected", err)
		}
		if !strings.Contains(err.Error(), "a -> b -> a") {
			t.Fatalf("expected deterministic cycle path 'a -> b -> a', got %v", err)
		}

		delayedBackend := *backend
		delayedBackend.CycleBreakingEdgeKinds = []string{"frame"}
		if _, err := Validate(spec, reg, ValidateOptions{Backend: &delayedBackend}); err != nil {
			t.Fatalf("expected frame feedback edge to break cycle when configured, got: %v", err)
		}

		allowCyclesBackend := *backend
		allowCyclesBackend.AllowCycles = true
		cyclicReport, err := Validate(spec, reg, ValidateOptions{Backend: &allowCyclesBackend})
		if err != nil {
			t.Fatalf("expected AllowCycles backend to accept cycle, got: %v", err)
		}
		wantOrder := []string{"a", "b"}
		if !slices.Equal(cyclicReport.TopologicalOrder, wantOrder) {
			t.Fatalf("AllowCycles TopologicalOrder = %v, want %v", cyclicReport.TopologicalOrder, wantOrder)
		}
	})

	t.Run("invalid parameter bounds and unsupported edge kind", func(t *testing.T) {
		spec, _ := ParseYAML([]byte(hnBriefYAML))
		bad, _ := ApplyEdits(
			spec,
			SetParam("typeset", "limit", 0), // min is 1
			Disconnect(MustParsePortRef("typeset.document"), MustParsePortRef("view.document"), ""),
			Connect(MustParsePortRef("typeset.document"), MustParsePortRef("view.document"), "quantum"),
		)
		_, err := Validate(bad, reg, ValidateOptions{Backend: backend})
		if !errors.Is(err, ErrInvalidParam) {
			t.Fatalf("expected ErrInvalidParam in %v", err)
		}
		if !errors.Is(err, ErrUnsupportedBackend) {
			t.Fatalf("expected ErrUnsupportedBackend in %v", err)
		}
	})

	t.Run("agent namespace and CycleBreakingEdge cold-inlet predicate", func(t *testing.T) {
		agentReg := NewRegistry()
		agentReg.MustRegister(TypeDef{
			ID: MustParseTypeID("agent/svg-coder@1"),
			Inlets: []PortDef{
				{Name: "prompt", Type: TypeString},
				{Name: "svg", Type: TypeString},
				{Name: "repair", Type: TypeString},
			},
			Outlets: []PortDef{
				{Name: "svg", Type: TypeString},
			},
		})
		agentReg.MustRegister(TypeDef{
			ID: MustParseTypeID("transform/svg-doc@1"),
			Inlets: []PortDef{
				{Name: "svg", Type: TypeString, Required: true},
			},
			Outlets: []PortDef{
				{Name: "svg", Type: TypeString},
				{Name: "error", Type: TypeString},
			},
		})

		coldAllowedSpec, err := ParseYAML([]byte(`
version: 1
name: svgpad-cold-feedback
nodes:
  coder:
    type: agent/svg-coder@1
  doc:
    type: transform/svg-doc@1
edges:
  - from: coder.svg
    to: doc.svg
  - from: doc.svg
    to: coder.svg
  - from: doc.error
    to: coder.repair
    kind: frame
`))
		if err != nil {
			t.Fatalf("ParseYAML: %v", err)
		}

		// Without NamespaceAgent in BackendProfile.Namespaces, backend check rejects it.
		if _, err := Validate(coldAllowedSpec, agentReg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrUnsupportedBackend) {
			t.Fatalf("missing NamespaceAgent = %v, want ErrUnsupportedBackend", err)
		}

		agentBackend := &BackendProfile{
			Name:                   "nebula",
			EdgeKinds:              []string{"signal", "frame", "event"},
			DefaultEdgeKind:        "signal",
			AllowCycles:            false,
			CycleBreakingEdgeKinds: []string{"frame"},
			Namespaces:             []string{NamespaceSource, NamespaceTransform, NamespaceSurface, NamespaceWASM, NamespaceGPU, NamespaceGraph, NamespaceAgent},
			CycleBreakingEdge: func(edge EdgeSpec, _, toDef TypeDef) bool {
				// Simulate push dataflow where coder.svg is cold, while coder.prompt & coder.repair are hot.
				if toDef.ID.String() == "agent/svg-coder@1" {
					return edge.To.Port == "svg"
				}
				return len(toDef.Inlets) > 0 && toDef.Inlets[0].Name != edge.To.Port
			},
		}

		report, err := Validate(coldAllowedSpec, agentReg, ValidateOptions{Backend: agentBackend})
		if err != nil {
			t.Fatalf("Validate cold-inlet feedback failed: %v", err)
		}
		if !slices.Equal(report.TopologicalOrder, []string{"coder", "doc"}) {
			t.Fatalf("TopologicalOrder = %v, want [coder doc]", report.TopologicalOrder)
		}

		// Wiring doc.error -> coder.repair over synchronous signal (instead of frame) must still fail!
		hotCycleSpec, err := ApplyEdits(
			coldAllowedSpec,
			Disconnect(MustParsePortRef("doc.error"), MustParsePortRef("coder.repair"), "frame"),
			Connect(MustParsePortRef("doc.error"), MustParsePortRef("coder.repair"), "signal"),
		)
		if err != nil {
			t.Fatalf("ApplyEdits: %v", err)
		}
		if _, err := Validate(hotCycleSpec, agentReg, ValidateOptions{Backend: agentBackend}); !errors.Is(err, ErrCycleDetected) {
			t.Fatalf("hot-inlet signal cycle = %v, want ErrCycleDetected", err)
		}
	})
}
