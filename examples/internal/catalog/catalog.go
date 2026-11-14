// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Package catalog provides a standalone demo node-type Registry, BackendProfile,
// reusable graph/* component Library, and simulated on-card previews for
// ntnodegraph examples.
package catalog

import (
	"image"
	"image/color"
	"math"

	"nimbleterminal.dev/ntnodegraph"
	"nimbleterminal.dev/ntnodegraph/editor"
)

func f64(v float64) *float64 { return &v }

// Environment bundles a configured Registry, BackendProfile, and subgraph Library.
type Environment struct {
	Registry *ntnodegraph.Registry
	Backend  ntnodegraph.BackendProfile
	Library  *ntnodegraph.Library
}

// New constructs a demo Environment covering all standard ntnodegraph namespaces
// (source, transform, surface, wasm, gpu, graph, agent), cold-inlet cycle breaking,
// and two pre-registered graph/* components.
func New() *Environment {
	reg := ntnodegraph.NewRegistry()
	frameRGBA := ntnodegraph.MustParseDataType("frame<rgba8>")
	texRGBA := ntnodegraph.MustParseDataType("texture<rgba8>")

	coldInlets := map[string]map[string]bool{
		"agent/svg-coder@1": {"svg": true},
		"gpu/bloom@1":       {"mask": true},
	}

	defs := []ntnodegraph.TypeDef{
		{
			ID:          ntnodegraph.MustParseTypeID("source/prompt@1"),
			Description: "Emits creative prompt text and trigger pulses.",
			Params: []ntnodegraph.ParamDef{
				{Name: "prompt", Type: ntnodegraph.ParamString, Default: "Geometric neon rosette with radial symmetry"},
				{Name: "style", Type: ntnodegraph.ParamString, Default: "bauhaus", Enum: []string{"bauhaus", "blueprint", "synthwave", "minimal"}},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "prompt", Type: ntnodegraph.TypeString},
				{Name: "bang", Type: ntnodegraph.TypeEvent},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("source/http@1"),
			Description: "Fetches JSON payloads from capability-scoped HTTPS endpoints.",
			Capabilities: []ntnodegraph.CapabilityRule{
				{Kind: "net", RequireScope: true, HostFromURLParam: "url"},
			},
			Params: []ntnodegraph.ParamDef{
				{Name: "url", Type: ntnodegraph.ParamString, Required: true, Default: "https://hn.algolia.com/api/v1/search?tags=front_page"},
				{Name: "interval", Type: ntnodegraph.ParamDuration, Default: "30s"},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "body", Type: ntnodegraph.TypeJSON},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("agent/svg-coder@1"),
			Description: "Streams SVG drafts from an LLM with cold-inlet state and frame-delayed critique.",
			Capabilities: []ntnodegraph.CapabilityRule{
				{Kind: "net", RequireScope: true, HostFromURLParam: "endpoint"},
			},
			Params: []ntnodegraph.ParamDef{
				{Name: "endpoint", Type: ntnodegraph.ParamString, Default: "https://api.openai.com/v1"},
				{Name: "model", Type: ntnodegraph.ParamString, Default: "qwen3-coder-30b", Enum: []string{"qwen3-coder-30b", "claude-sonnet-4", "gemini-2.5-pro", "mock-procedural"}},
				{Name: "temperature", Type: ntnodegraph.ParamNumber, Default: 0.35, Min: f64(0), Max: f64(1.5)},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "prompt", Type: ntnodegraph.TypeString, Required: true},
				{Name: "svg", Type: ntnodegraph.TypeString, Description: "Cold state-only inlet holding current SVG draft."},
				{Name: "critique", Type: ntnodegraph.TypeString, Description: "Frame-delayed vision/repair feedback."},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "svg", Type: ntnodegraph.TypeString},
				{Name: "tokens", Type: ntnodegraph.TypeNumber},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("agent/vision-critic@1"),
			Description: "Inspects rasterized frames and emits bounded repair guidance.",
			Params: []ntnodegraph.ParamDef{
				{Name: "max_turns", Type: ntnodegraph.ParamInt, Default: int64(3), Min: f64(1), Max: f64(8)},
				{Name: "strict_bounds", Type: ntnodegraph.ParamBool, Default: true},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "frame", Type: frameRGBA, Required: true},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "critique", Type: ntnodegraph.TypeString},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("transform/svg-doc@1"),
			Description: "Validates XML/viewBox invariants and maintains undoable SVG state.",
			Params: []ntnodegraph.ParamDef{
				{Name: "sanitize", Type: ntnodegraph.ParamBool, Default: true},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "in", Type: ntnodegraph.TypeString, Required: true},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "svg", Type: ntnodegraph.TypeString},
				{Name: "valid", Type: ntnodegraph.TypeBool},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("transform/svg-raster@1"),
			Description: "Rasterizes validated SVG markup into an RGBA8 frame.",
			Params: []ntnodegraph.ParamDef{
				{Name: "width", Type: ntnodegraph.ParamInt, Default: int64(640), Min: f64(64), Max: f64(2048)},
				{Name: "height", Type: ntnodegraph.ParamInt, Default: int64(360), Min: f64(64), Max: f64(2048)},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "svg", Type: ntnodegraph.TypeString, Required: true},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "frame", Type: frameRGBA},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("transform/filter@1"),
			Description: "Filters and ranks structured JSON items by keyword and score.",
			Params: []ntnodegraph.ParamDef{
				{Name: "keyword", Type: ntnodegraph.ParamString, Default: ""},
				{Name: "limit", Type: ntnodegraph.ParamInt, Default: int64(12), Min: f64(1), Max: f64(50)},
				{Name: "min_points", Type: ntnodegraph.ParamInt, Default: int64(25), Min: f64(0), Max: f64(500)},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "in", Type: ntnodegraph.TypeJSON, Required: true},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "out", Type: ntnodegraph.TypeJSON},
				{Name: "count", Type: ntnodegraph.TypeNumber},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("wasm/hn-typesetter@1"),
			Description: "Sandboxed WASI guest formatting story items into rich HTML/Markdown documents.",
			Params: []ntnodegraph.ParamDef{
				{Name: "theme", Type: ntnodegraph.ParamString, Default: "editorial", Enum: []string{"editorial", "compact", "terminal"}},
				{Name: "show_urls", Type: ntnodegraph.ParamBool, Default: true},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "stories", Type: ntnodegraph.TypeJSON, Required: true},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "doc", Type: ntnodegraph.TypeDocument},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("gpu/grade@1"),
			Description: "Applies WGSL exposure, contrast, and filmic color grading on GPU textures.",
			Capabilities: []ntnodegraph.CapabilityRule{
				{Kind: "gpu"},
			},
			Params: []ntnodegraph.ParamDef{
				{Name: "preset", Type: ntnodegraph.ParamString, Default: "cinema_teal", Enum: []string{"cinema_teal", "warm_kodak", "cyber_neon", "monochrome"}},
				{Name: "exposure", Type: ntnodegraph.ParamNumber, Default: 1.08, Min: f64(0.2), Max: f64(2.5)},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "in", Type: frameRGBA, Required: true},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "out", Type: texRGBA},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("gpu/bloom@1"),
			Description: "WGSL dual-Kawase bloom and halation pass.",
			Capabilities: []ntnodegraph.CapabilityRule{
				{Kind: "gpu"},
			},
			Params: []ntnodegraph.ParamDef{
				{Name: "intensity", Type: ntnodegraph.ParamNumber, Default: 0.45, Min: f64(0), Max: f64(2.0)},
				{Name: "threshold", Type: ntnodegraph.ParamNumber, Default: 0.68, Min: f64(0), Max: f64(1.0)},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "in", Type: texRGBA, Required: true},
				{Name: "mask", Type: frameRGBA},
			},
			Outlets: []ntnodegraph.PortDef{
				{Name: "out", Type: frameRGBA},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("surface/ntpicture@1"),
			Description: "Presents RGBA frames via Kitty pixel graphics or half-block glyphs.",
			Params: []ntnodegraph.ParamDef{
				{Name: "fit", Type: ntnodegraph.ParamString, Default: "contain", Enum: []string{"contain", "cover", "fill"}},
				{Name: "title", Type: ntnodegraph.ParamString, Default: "Stage Output"},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "frame", Type: frameRGBA, Required: true},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("surface/nthtml@1"),
			Description: "Renders typeset documents into scrollable terminal panes.",
			Params: []ntnodegraph.ParamDef{
				{Name: "title", Type: ntnodegraph.ParamString, Default: "Reader"},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "doc", Type: ntnodegraph.TypeDocument, Required: true},
			},
		},
		{
			ID:          ntnodegraph.MustParseTypeID("surface/log@1"),
			Description: "Displays structured scalar, string, or JSON telemetry lines.",
			Params: []ntnodegraph.ParamDef{
				{Name: "prefix", Type: ntnodegraph.ParamString, Default: "telemetry"},
			},
			Inlets: []ntnodegraph.PortDef{
				{Name: "in", Type: ntnodegraph.TypeAny, Required: true},
			},
		},
	}

	for _, d := range defs {
		reg.MustRegister(d)
	}

	profile := ntnodegraph.BackendProfile{
		Name: "nebula-demo",
		Namespaces: []string{
			ntnodegraph.NamespaceSource,
			ntnodegraph.NamespaceTransform,
			ntnodegraph.NamespaceSurface,
			ntnodegraph.NamespaceWASM,
			ntnodegraph.NamespaceGPU,
			ntnodegraph.NamespaceGraph,
			ntnodegraph.NamespaceAgent,
		},
		EdgeKinds:              []string{"signal", "frame", "event"},
		DefaultEdgeKind:        "signal",
		CycleBreakingEdgeKinds: []string{"frame"},
		CycleBreakingEdge: func(edge ntnodegraph.EdgeSpec, _, toDef ntnodegraph.TypeDef) bool {
			if ports, ok := coldInlets[toDef.ID.String()]; ok {
				return ports[edge.To.Port]
			}
			return false
		},
		Capabilities: []string{"net", "fs", "gpu"},
	}

	lib := ntnodegraph.NewLibrary()
	env := &Environment{
		Registry: reg,
		Backend:  profile,
		Library:  lib,
	}

	for _, compYAML := range [][]byte{cinemaGradeComponentYAML, svgCriticComponentYAML} {
		comp, err := ntnodegraph.ParseYAML(compYAML)
		if err != nil {
			panic(err)
		}
		if _, err := env.RegisterComponent(comp); err != nil {
			panic(err)
		}
	}

	return env
}

// RegisterComponent validates and registers a graph/* component into both the
// session Library and Registry (supporting live G group promotion and drill-in).
func (e *Environment) RegisterComponent(comp *ntnodegraph.Spec) (ntnodegraph.TypeDef, error) {
	p := e.Backend
	staged := e.Library.Clone()
	tid, err := staged.Put(comp, "session")
	if err != nil {
		return ntnodegraph.TypeDef{}, err
	}
	if _, err := staged.SyncRegistry(e.Registry, ntnodegraph.ValidateOptions{Backend: &p}); err != nil {
		return ntnodegraph.TypeDef{}, err
	}
	if _, err := e.Library.Put(comp, "session"); err != nil {
		return ntnodegraph.TypeDef{}, err
	}
	def, _ := e.Registry.Lookup(tid)
	return def, nil
}

var cinemaGradeComponentYAML = []byte(`version: 1
name: cinema-grade
component_version: 1
description: Reusable two-pass WGSL filmic grade + Kawase bloom stage.
inlets:
  - name: frame
    type: frame<rgba8>
    target: grade.in
    required: true
outlets:
  - name: out
    type: frame<rgba8>
    target: bloom.out
params:
  - name: preset
    type: string
    target: grade.preset
    default: cinema_teal
    enum: [cinema_teal, warm_kodak, cyber_neon, monochrome]
  - name: bloom
    type: number
    target: bloom.intensity
    default: 0.45
    min: 0
    max: 2
nodes:
  grade:
    type: gpu/grade@1
    capabilities: [gpu]
    ui: {x: 80, y: 90, label: Filmic Grade}
  bloom:
    type: gpu/bloom@1
    capabilities: [gpu]
    ui: {x: 380, y: 90, label: Kawase Bloom}
edges:
  - from: grade.out
    to: bloom.in
`)

var svgCriticComponentYAML = []byte(`version: 1
name: svg-critic-loop
component_version: 1
description: Self-correcting LLM SVG coder + XML validator + vision critic loop.
inlets:
  - name: prompt
    type: string
    target: coder.prompt
    required: true
outlets:
  - name: frame
    type: frame<rgba8>
    target: raster.frame
  - name: svg
    type: string
    target: doc.svg
params:
  - name: model
    type: string
    target: coder.model
    default: qwen3-coder-30b
    enum: [qwen3-coder-30b, claude-sonnet-4, gemini-2.5-pro, mock-procedural]
nodes:
  coder:
    type: agent/svg-coder@1
    capabilities: ["net:api.openai.com"]
    params:
      endpoint: https://api.openai.com/v1
      temperature: 0.35
    ui: {x: 70, y: 90, label: SVG Coder}
  doc:
    type: transform/svg-doc@1
    ui: {x: 360, y: 70, label: SVG Draft Buffer}
  raster:
    type: transform/svg-raster@1
    params:
      width: 640
      height: 360
    ui: {x: 650, y: 90, label: Pure-Go Raster}
  critic:
    type: agent/vision-critic@1
    params:
      max_turns: 3
    ui: {x: 650, y: 290, label: Vision Critic}
edges:
  - from: coder.svg
    to: doc.in
  - from: doc.svg
    to: coder.svg
  - from: doc.svg
    to: raster.svg
  - from: raster.frame
    to: critic.frame
  - from: critic.critique
    to: coder.critique
    kind: frame
`)

// EditorSpec returns the flagship multi-namespace Agent + GPU pipeline Spec.
func EditorSpec() *ntnodegraph.Spec {
	raw := []byte(`version: 1
name: svgpad-vision-rig
description: Declarative agent/* SVG coder with cold-inlet state feedback and WGSL post-FX.
groups:
  agent_loop:
    label: Agentic SVG Synthesis
    nodes: [coder, doc, critic]
    ui: {x: 310, y: 40, width: 610, height: 470}
  gpu_post:
    label: GPU Filmic Post-FX
    nodes: [grade, bloom]
    ui: {x: 960, y: 60, width: 540, height: 340}
nodes:
  prompt:
    type: source/prompt@1
    params:
      prompt: Geometric neon rosette with 12-fold symmetry
      style: synthwave
    ui: {x: 50, y: 110, label: Creative Prompt}
  coder:
    type: agent/svg-coder@1
    capabilities: ["net:api.openai.com"]
    params:
      endpoint: https://api.openai.com/v1
      model: qwen3-coder-30b
      temperature: 0.35
    ui: {x: 340, y: 95, label: SVG Coder Agent}
  doc:
    type: transform/svg-doc@1
    params:
      sanitize: true
    ui: {x: 640, y: 80, label: SVG Validator}
  raster:
    type: transform/svg-raster@1
    params:
      width: 640
      height: 360
    ui: {x: 640, y: 300, label: oksvg Rasterizer}
  critic:
    type: agent/vision-critic@1
    params:
      max_turns: 3
      strict_bounds: true
    ui: {x: 340, y: 365, label: Vision Critic}
  grade:
    type: gpu/grade@1
    capabilities: [gpu]
    params:
      preset: cyber_neon
      exposure: 1.15
    ui: {x: 990, y: 105, label: WGSL Grade}
  bloom:
    type: gpu/bloom@1
    capabilities: [gpu]
    params:
      intensity: 0.55
      threshold: 0.62
    ui: {x: 1255, y: 105, label: Kawase Bloom}
  stage:
    type: surface/ntpicture@1
    params:
      fit: contain
      title: Live Vector Preview
    ui: {x: 1545, y: 105, label: Kitty Stage}
edges:
  - from: prompt.prompt
    to: coder.prompt
  - from: coder.svg
    to: doc.in
  - from: doc.svg
    to: coder.svg
  - from: doc.svg
    to: raster.svg
  - from: raster.frame
    to: critic.frame
  - from: critic.critique
    to: coder.critique
    kind: frame
  - from: raster.frame
    to: grade.in
  - from: grade.out
    to: bloom.in
  - from: bloom.out
    to: stage.frame
`)
	spec, err := ntnodegraph.ParseYAML(raw)
	if err != nil {
		panic(err)
	}
	return spec
}

// SubgraphSpec returns a Spec composed from reusable graph/* library components
// plus a promotable group ("telemetry_pack") ready for pressing G.
func SubgraphSpec() *ntnodegraph.Spec {
	raw := []byte(`version: 1
name: composable-studio
description: Composed from graph/svg-critic-loop@1 and graph/cinema-grade@1 components.
groups:
  telemetry_pack:
    label: SVG Audit Sink (select + press G to promote)
    nodes: [audit_doc, audit_log]
    ui: {x: 690, y: 395, width: 560, height: 185}
nodes:
  prompt:
    type: source/prompt@1
    params:
      prompt: Bauhaus orbital chronometer with brass ticks
      style: bauhaus
    ui: {x: 60, y: 120, label: Prompt Source}
  synth:
    type: graph/svg-critic-loop@1
    capabilities: ["net:api.openai.com"]
    params:
      model: claude-sonnet-4
    ui: {x: 365, y: 110, label: SVG Critic Subgraph}
  cinema:
    type: graph/cinema-grade@1
    capabilities: [gpu]
    params:
      preset: warm_kodak
      bloom: 0.52
    ui: {x: 715, y: 95, label: Cinema Grade Subgraph}
  audit_doc:
    type: transform/svg-doc@1
    params:
      sanitize: true
    ui: {x: 720, y: 435, label: Boundary Audit}
  audit_log:
    type: surface/log@1
    params:
      prefix: svg-xml
    ui: {x: 1005, y: 435, label: Audit Telemetry}
  stage:
    type: surface/ntpicture@1
    params:
      fit: contain
      title: Composed Stage
    ui: {x: 1045, y: 95, label: Stage Viewer}
edges:
  - from: prompt.prompt
    to: synth.prompt
  - from: synth.frame
    to: cinema.frame
  - from: cinema.out
    to: stage.frame
  - from: synth.svg
    to: audit_doc.in
  - from: audit_doc.svg
    to: audit_log.in
`)
	spec, err := ntnodegraph.ParseYAML(raw)
	if err != nil {
		panic(err)
	}
	return spec
}

// MCPSpec returns the starter Spec for the live ngmcp co-editing demo.
func MCPSpec() *ntnodegraph.Spec {
	raw := []byte(`version: 1
name: hn-editorial-desk
description: Co-edited live by editor.Model and ngmcp JSON-RPC 2.0 tools.
nodes:
  hn:
    type: source/http@1
    capabilities: ["net:hn.algolia.com"]
    params:
      url: https://hn.algolia.com/api/v1/search?tags=front_page
      interval: 30s
    ui: {x: 60, y: 110, label: Algolia Frontpage}
  filter:
    type: transform/filter@1
    params:
      keyword: wasm
      limit: 10
      min_points: 40
    ui: {x: 370, y: 110, label: Topic Filter}
  typeset:
    type: wasm/hn-typesetter@1
    params:
      theme: editorial
      show_urls: true
    ui: {x: 670, y: 110, label: WASI Typesetter}
  reader:
    type: surface/nthtml@1
    params:
      title: Hacker News Digest
    ui: {x: 975, y: 110, label: Terminal Reader}
edges:
  - from: hn.body
    to: filter.in
  - from: filter.out
    to: typeset.stories
  - from: typeset.doc
    to: reader.doc
`)
	spec, err := ntnodegraph.ParseYAML(raw)
	if err != nil {
		panic(err)
	}
	return spec
}

// SeedPreviews attaches deterministic, procedural RGBA thumbnails and structured
// text lines to nodes in ed so toggling previews (o / O) showcases rich card drawers.
func SeedPreviews(ed *editor.Model) {
	spec := ed.Spec()
	for _, id := range spec.SortedNodeIDs() {
		ns := spec.Nodes[id]
		switch ns.Type.String() {
		case "source/prompt@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "prompt",
				Summary: "string · 44B",
				Lines: []string{
					`"Geometric neon rosette"`,
					`style = synthwave · bang #1`,
				},
				Active: true,
			})
		case "agent/svg-coder@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "svg",
				Summary: "svg · 1.8 KiB · 412 tok",
				Lines: []string{
					`<svg viewBox="0 0 640 360">`,
					`  <g stroke="#22d3ee" fill="none">`,
					`  <!-- 12-fold rosette -->`,
				},
				Active: true,
			})
		case "transform/svg-doc@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "svg",
				Summary: "valid=true · rev #3",
				Lines: []string{
					`XML strict: PASS`,
					`viewBox: 0 0 640 360 (24 els)`,
				},
				Active: true,
			})
		case "agent/vision-critic@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "critique",
				Summary: "turn 2/3 · converged",
				Lines: []string{
					`✓ Contrast 11.4:1 within bounds`,
					`✓ Radial symmetry centered`,
				},
				Active: true,
			})
		case "transform/svg-raster@1", "graph/svg-critic-loop@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "frame",
				Summary: "frame<rgba8> 640x360",
				Image:   proceduralRosetteThumb(96, 54, false),
				Active:  true,
			})
		case "gpu/grade@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "out",
				Summary: "texture<rgba8> 640x360",
				Image:   proceduralRosetteThumb(96, 54, true),
				Active:  true,
			})
		case "gpu/bloom@1", "graph/cinema-grade@1":
			ed.SetNodePreview(id, editor.NodePreview{
				Outlet:  "out",
				Summary: "frame<rgba8> 640x360 · HDR",
				Image:   proceduralRosetteThumb(96, 54, true),
				Active:  true,
			})
		}
	}
}

func proceduralRosetteThumb(w, h int, bloom bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cx := float64(w) * 0.5
	cy := float64(h) * 0.5
	scale := float64(min(w, h)) * 0.44

	for y := range h {
		for x := range w {
			dx := (float64(x) - cx) / scale
			dy := (float64(y) - cy) / scale
			r := math.Hypot(dx, dy)
			theta := math.Atan2(dy, dx)

			petals := 0.55 + 0.32*math.Cos(12*theta)
			dr := (r - petals) * 5.2
			cr := r * 3.6
			ring := math.Exp(-(dr * dr))
			core := math.Exp(-(cr * cr))
			grid := 0.06 * (math.Cos(dx*14) + math.Cos(dy*14))

			var rr, gg, bb float64
			if bloom {
				rr = 18 + 225*ring + 240*core + 30*grid
				gg = 24 + 135*ring + 220*core
				bb = 46 + 245*ring + 255*core
			} else {
				rr = 14 + 60*ring + 210*core
				gg = 22 + 210*ring + 230*core
				bb = 38 + 235*ring + 245*core
			}
			img.SetRGBA(x, y, color.RGBA{
				R: clampByte(rr),
				G: clampByte(gg),
				B: clampByte(bb),
				A: 255,
			})
		}
	}
	return img
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}
