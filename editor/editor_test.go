// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"nimbleterminal.dev/ntpicture"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

func newTestRegistry() (*nodegraph.Registry, nodegraph.BackendProfile) {
	reg := nodegraph.NewRegistry()
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("source/http@1"),
		Description: "Polls an HTTP endpoint.",
		Inlets:      []nodegraph.PortDef{{Name: "trigger", Type: nodegraph.TypeEvent}},
		Outlets:     []nodegraph.PortDef{{Name: "body", Type: nodegraph.TypeBytes}},
		Params: []nodegraph.ParamDef{
			{Name: "url", Type: nodegraph.ParamString, Required: true, Default: "https://example.com/feed.json"},
			{Name: "poll", Type: nodegraph.ParamDuration, Default: "30s"},
		},
		Capabilities: []nodegraph.CapabilityRule{
			{Kind: "net", RequireScope: true, HostFromURLParam: "url"},
		},
		Backends: []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("transform/filter@1"),
		Description: "Filters JSON arrays by substring.",
		Inlets: []nodegraph.PortDef{
			{Name: "in", Type: nodegraph.TypeJSON, Required: true},
			{Name: "keyword", Type: nodegraph.TypeString},
		},
		Outlets: []nodegraph.PortDef{
			{Name: "out", Type: nodegraph.TypeJSON},
			{Name: "count", Type: nodegraph.TypeNumber},
		},
		Params: []nodegraph.ParamDef{
			{Name: "keyword", Type: nodegraph.ParamString, Default: ""},
			{Name: "case_sensitive", Type: nodegraph.ParamBool, Default: false},
			{Name: "mode", Type: nodegraph.ParamString, Default: "include", Enum: []string{"include", "exclude"}},
		},
		Backends: []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("transform/typeset@1"),
		Description: "Typesets JSON stories into a document.",
		Inlets:      []nodegraph.PortDef{{Name: "json", Type: nodegraph.TypeJSON, Required: true}},
		Outlets:     []nodegraph.PortDef{{Name: "document", Type: nodegraph.TypeDocument}},
		Params: []nodegraph.ParamDef{
			{Name: "limit", Type: nodegraph.ParamInt, Default: int64(20), Min: nodegraph.FloatBound(1), Max: nodegraph.FloatBound(100)},
		},
		Backends: []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("surface/view@1"),
		Description: "Displays a typeset document.",
		Inlets: []nodegraph.PortDef{
			{Name: "document", Type: nodegraph.TypeDocument, Required: true},
			{Name: "columns", Type: nodegraph.TypeNumber},
		},
		Backends: []string{"nebula"},
	})

	profile := nodegraph.BackendProfile{
		Name:                   "nebula",
		EdgeKinds:              []string{"signal", "frame", "event"},
		DefaultEdgeKind:        "signal",
		AllowCycles:            true,
		CycleBreakingEdgeKinds: []string{"frame"},
		Namespaces:             []string{"source", "transform", "surface"},
		Capabilities:           []string{"net"},
	}
	return reg, profile
}

func newSampleSpec(t *testing.T) *nodegraph.Spec {
	t.Helper()
	spec, err := nodegraph.ParseYAML([]byte(`version: 1
name: editor-test
groups:
  pipeline:
    label: Core Pipeline
    nodes: [src, typeset]
nodes:
  src:
    type: source/http@1
    capabilities: [net:example.com]
    params:
      url: https://example.com/feed.json
    ui: {x: 40, y: 100}
  typeset:
    type: transform/typeset@1
    params:
      limit: 15
    ui: {x: 340, y: 100}
  view:
    type: surface/view@1
    ui: {x: 640, y: 100}
edges:
  - from: src.body
    to: typeset.json
  - from: typeset.document
    to: view.document
    kind: frame
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	return spec
}

func TestEditorSceneHitTestAndDualModeRender(t *testing.T) {
	reg, profile := newTestRegistry()
	spec := newSampleSpec(t)

	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderVector,
		Width:          110,
		Height:         32,
		ShowInspector:  true,
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	sc := ed.Scene()
	if len(sc.Nodes) != 3 || len(sc.Wires) != 2 || len(sc.Groups) != 1 {
		t.Fatalf("unexpected scene counts: nodes=%d wires=%d groups=%d", len(sc.Nodes), len(sc.Wires), len(sc.Groups))
	}

	// 1. Port socket hit-test priority.
	srcIdx := sc.NodeByID["src"]
	srcOut := sc.Nodes[srcIdx].Outlets[0]
	hit := sc.HitTest(srcOut.Center.X, srcOut.Center.Y)
	if hit.Kind != HitPort || hit.NodeID != "src" || hit.Port.Name != "body" || !hit.Port.IsOutlet {
		t.Fatalf("expected HitPort on src.body, got %+v", hit)
	}

	// 2. Node card body hit-test.
	srcBounds := sc.Nodes[srcIdx].Bounds
	hit = sc.HitTest(srcBounds.X+50, srcBounds.Y+12)
	if hit.Kind != HitNode || hit.NodeID != "src" || !hit.InHeader {
		t.Fatalf("expected HitNode header on src, got %+v", hit)
	}

	// 3. Bezier wire midpoint hit-test.
	w0 := sc.Wires[0]
	mid := evalCubicBezier(w0.FromPos, w0.Ctrl1, w0.Ctrl2, w0.ToPos, 0.5)
	hit = sc.HitTest(mid.X, mid.Y)
	if hit.Kind != HitWire || hit.Edge != w0.Edge {
		t.Fatalf("expected HitWire on %v, got %+v", w0.Edge, hit)
	}

	// 4. Group header hit-test.
	gb := sc.Groups[0]
	hit = sc.HitTest(gb.Bounds.X+16, gb.Bounds.Y+10)
	if hit.Kind != HitGroup || hit.GroupID != "pipeline" {
		t.Fatalf("expected HitGroup on pipeline, got %+v", hit)
	}

	// 5. Vector rasterization produces non-uniform pixels across background, cards, and wires.
	img := RenderSceneVector(sc, ed.Camera(), 800, 480, DefaultTheme(), "VECTOR TEST")
	if img.Bounds().Dx() != 800 || img.Bounds().Dy() != 480 {
		t.Fatalf("unexpected vector image bounds: %v", img.Bounds())
	}
	bg := img.RGBAAt(0, 0)
	nonBG := 0
	for y := 0; y < 480; y += 4 {
		for x := 0; x < 800; x += 4 {
			if img.RGBAAt(x, y) != bg {
				nonBG++
			}
		}
	}
	if nonBG < 50 {
		t.Fatalf("expected rich vector geometry pixels, found only %d non-background samples", nonBG)
	}

	// 6. Grid mode View renders node titles and chrome cleanly.
	_ = ed.SetMode(RenderGrid)
	gridView := ed.View()
	for _, want := range []string{"editor-test", "src", "typeset", "view", "INSPECTOR"} {
		if !strings.Contains(gridView, want) {
			t.Fatalf("grid View missing %q:\n%s", want, gridView)
		}
	}
}

func TestEditorMouseDragNodeGroupAndWireConnect(t *testing.T) {
	reg, profile := newTestRegistry()
	spec := newSampleSpec(t)

	var commits []nodegraph.SpecDiff
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderGrid,
		Width:          120,
		Height:         36,
		ShowInspector:  false,
		Commit: func(_ *nodegraph.Spec, diff nodegraph.SpecDiff) error {
			commits = append(commits, diff)
			return nil
		},
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	// Helper to convert a world coordinate to terminal cell (X, Y) in Grid mode.
	worldToCell := func(wx, wy float64) (int, int) {
		c, r := ed.Camera().WorldToGrid(wx, wy)
		return c, r + 1 // +1 for header row
	}

	// 1. Drag node "view" by (+40, +40) world units -> RuntimeChanged MUST be false!
	viewIdx := ed.Scene().NodeByID["view"]
	vb := ed.Scene().Nodes[viewIdx].Bounds
	startX, startY := worldToCell(vb.X+40, vb.Y+10)
	endX, endY := startX+4, startY+2

	ed.Update(tea.MouseClickMsg{X: startX, Y: startY, Button: tea.MouseLeft})
	ed.Update(tea.MouseMotionMsg{X: endX, Y: endY, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: endX, Y: endY, Button: tea.MouseLeft})

	if len(commits) != 1 {
		t.Fatalf("expected 1 commit after node drag, got %d", len(commits))
	}
	if commits[0].HasRuntimeChanges() {
		t.Fatalf("dragging a node must have HasRuntimeChanges()==false, got %+v", commits[0])
	}

	// 2. Drag group header "pipeline" -> moves both "src" and "typeset" together!
	srcBefore := ed.Spec().Nodes["src"].UI.X
	typBefore := ed.Spec().Nodes["typeset"].UI.X
	gb := ed.Scene().Groups[0].Bounds
	gx, gy := worldToCell(gb.X+12, gb.Y+6)
	ed.Update(tea.MouseClickMsg{X: gx, Y: gy, Button: tea.MouseLeft})
	ed.Update(tea.MouseMotionMsg{X: gx + 5, Y: gy + 1, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: gx + 5, Y: gy + 1, Button: tea.MouseLeft})

	if ed.Spec().Nodes["src"].UI.X <= srcBefore || ed.Spec().Nodes["typeset"].UI.X <= typBefore {
		t.Fatalf("expected group drag to move both src and typeset: src=%.1f->%.1f typeset=%.1f->%.1f",
			srcBefore, ed.Spec().Nodes["src"].UI.X, typBefore, ed.Spec().Nodes["typeset"].UI.X)
	}

	// 3. Add a filter node, then drag a wire from filter.count (number) -> view.columns (number)!
	filterID, _, err := ed.AddNodeFromType(nodegraph.MustParseTypeID("transform/filter@1"))
	if err != nil {
		t.Fatalf("AddNodeFromType: %v", err)
	}
	if ed.LiveSynced() {
		t.Fatal("expected editor to be in DRAFT mode until filter.in required inlet is connected")
	}

	// Connect src.body -> filter.in first so filter is valid, then drag filter.count -> view.columns via mouse!
	if _, err := ed.ApplyEdits(nodegraph.Connect(
		nodegraph.PortRef{Node: "src", Port: "body"},
		nodegraph.PortRef{Node: filterID, Port: "in"},
		"",
	)); err != nil {
		t.Fatalf("Connect src.body -> filter.in: %v", err)
	}
	if !ed.LiveSynced() {
		t.Fatalf("expected LiveSynced()==true once required inlet connected, validationErr=%v", ed.ValidationError())
	}

	fIdx := ed.Scene().NodeByID[filterID]
	var countPort PortAnchor
	for _, out := range ed.Scene().Nodes[fIdx].Outlets {
		if out.Name == "count" {
			countPort = out
		}
	}
	vIdx := ed.Scene().NodeByID["view"]
	var colsPort PortAnchor
	for _, in := range ed.Scene().Nodes[vIdx].Inlets {
		if in.Name == "columns" {
			colsPort = in
		}
	}

	p0x, p0y := worldToCell(countPort.Center.X, countPort.Center.Y)
	p1x, p1y := worldToCell(colsPort.Center.X, colsPort.Center.Y)
	ed.Update(tea.MouseClickMsg{X: p0x, Y: p0y, Button: tea.MouseLeft})
	ed.Update(tea.MouseMotionMsg{X: p1x, Y: p1y, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: p1x, Y: p1y, Button: tea.MouseLeft})

	wantEdge := nodegraph.EdgeSpec{
		From: nodegraph.PortRef{Node: filterID, Port: "count"},
		To:   nodegraph.PortRef{Node: "view", Port: "columns"},
	}
	if !slices.Contains(ed.Spec().Edges, wantEdge) {
		t.Fatalf("expected mouse wire drag to connect %v; edges=%v", wantEdge, ed.Spec().Edges)
	}

	// 4. Incompatible wire (typeset.document -> view.columns) is rejected fail-closed!
	if _, err := ed.ApplyEdits(nodegraph.Connect(
		nodegraph.PortRef{Node: "typeset", Port: "document"},
		nodegraph.PortRef{Node: "view", Port: "columns"},
		"",
	)); !errors.Is(err, nodegraph.ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch, got %v", err)
	}
}

func TestEditorSpliceInspectorUndoRedoAndHostRollback(t *testing.T) {
	reg, profile := newTestRegistry()
	spec := newSampleSpec(t)

	denyEvilCap := false
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderGrid,
		Width:          120,
		Height:         32,
		ShowInspector:  true,
		Commit: func(next *nodegraph.Spec, _ nodegraph.SpecDiff) error {
			if denyEvilCap {
				for _, n := range next.Nodes {
					for _, c := range n.Capabilities {
						if c.Scope == "evil.example.com" {
							return errors.New("capability denied: net:evil.example.com")
						}
					}
				}
			}
			return nil
		},
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	// 1. Select edge src.body -> typeset.json and splice transform/filter@1 in one atomic edit!
	ed.SelectEdge(nodegraph.EdgeSpec{
		From: nodegraph.PortRef{Node: "src", Port: "body"},
		To:   nodegraph.PortRef{Node: "typeset", Port: "json"},
	})
	filterID, _, err := ed.SpliceNodeOnSelectedEdge(nodegraph.MustParseTypeID("transform/filter@1"))
	if err != nil {
		t.Fatalf("SpliceNodeOnSelectedEdge: %v", err)
	}
	if !ed.LiveSynced() {
		t.Fatalf("atomic splice should stay LiveSynced, got err=%v", ed.ValidationError())
	}

	// 2. Edit filter params via Inspector keyboard flow: toggle case_sensitive (bool) & cycle mode (enum).
	ed.SelectNode(filterID)
	ed.Update(tea.KeyPressMsg{Code: 'i', Text: "i"}) // focus inspector
	// Items: [0]=Label, [1]=Collapsed, [2]=Capabilities, [3]=keyword, [4]=case_sensitive, [5]=mode
	for range 4 {
		ed.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // toggle case_sensitive -> true
	if got := ed.Spec().Nodes[filterID].Params["case_sensitive"]; got != true {
		t.Fatalf("case_sensitive = %v, want true", got)
	}
	ed.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // cycle mode include -> exclude
	if got := ed.Spec().Nodes[filterID].Params["mode"]; got != "exclude" {
		t.Fatalf("mode = %v, want exclude", got)
	}

	// Edit keyword via inline text field ("wazero").
	ed.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	ed.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // start editing keyword
	ed.Update(tea.KeyPressMsg{Code: 'w', Text: "wazero"})
	ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // commit keyword
	if got := ed.Spec().Nodes[filterID].Params["keyword"]; got != "wazero" {
		t.Fatalf("keyword = %v, want wazero", got)
	}

	// 3. Host CommitFunc rejection rolls back atomically!
	denyEvilCap = true
	beforeRollbackYAML, _ := nodegraph.MarshalYAML(ed.Spec())
	_, err = ed.ApplyEdits(
		nodegraph.SetParam("src", "url", "https://evil.example.com/feed.json"),
		nodegraph.SetCapabilities("src", nodegraph.MustParseCapability("net:evil.example.com")),
	)
	if err == nil {
		t.Fatal("expected host capability denial to reject edit")
	}
	afterRollbackYAML, _ := nodegraph.MarshalYAML(ed.Spec())
	if string(beforeRollbackYAML) != string(afterRollbackYAML) {
		t.Fatal("host-rejected edit mutated editor spec")
	}
	denyEvilCap = false

	// 4. Undo & Redo walk back and forth cleanly.
	if _, err := ed.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := ed.Spec().Nodes[filterID].Params["keyword"]; got == "wazero" {
		t.Fatalf("expected Undo to revert keyword=wazero, got %v", got)
	}
	if _, err := ed.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := ed.Spec().Nodes[filterID].Params["keyword"]; got != "wazero" {
		t.Fatalf("expected Redo to restore keyword=wazero, got %v", got)
	}
}

func TestEditorRichMouseInteractions(t *testing.T) {
	reg, profile := newTestRegistry()
	spec := newSampleSpec(t)

	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderGrid,
		Width:          124,
		Height:         34,
		ShowInspector:  true,
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	worldToCell := func(wx, wy float64) (int, int) {
		c, r := ed.Camera().WorldToGrid(wx, wy)
		return c, r + 1 // +1 for editor header row
	}
	clickAt := func(x, y int, btn tea.MouseButton, mod tea.KeyMod) {
		ed.Update(tea.MouseClickMsg{X: x, Y: y, Button: btn, Mod: mod})
		ed.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: btn, Mod: mod})
	}
	findHeaderBtn := func(id string) int {
		_ = ed.View()
		for _, b := range ed.headerButtons {
			if b.id == id {
				return b.startCol + 1
			}
		}
		t.Fatalf("header button %q not found in %+v", id, ed.headerButtons)
		return 0
	}

	// 1. Header toolbar click: "+Add" opens Palette; clicking a Palette row adds the node; "[×]" closes Inspector.
	clickAt(findHeaderBtn("add"), 0, tea.MouseLeft, 0)
	if !strings.Contains(ed.View(), "NODE PALETTE") {
		t.Fatalf("expected NODE PALETTE after clicking +Add toolbar button")
	}
	canvasCols, inspCols := ed.splitColumns()
	// Find the Palette row corresponding to "transform/filter@1".
	filterPaletteIdx := slices.IndexFunc(ed.filteredPaletteTypes(), func(d nodegraph.TypeDef) bool {
		return d.ID.Name == "filter"
	})
	var filterRowY int
	for lineIdx, itemIdx := range ed.paletteLineItem {
		if itemIdx == filterPaletteIdx {
			filterRowY = lineIdx + 2 // +1 editor header, +1 panel header
			break
		}
	}
	if filterRowY == 0 {
		t.Fatalf("paletteLineItem missing filter item %d: %+v", filterPaletteIdx, ed.paletteLineItem)
	}
	clickAt(canvasCols+4, filterRowY, tea.MouseLeft, 0)
	if _, ok := ed.Spec().Nodes["filter"]; !ok {
		t.Fatalf("expected clicking Palette row to instantiate 'filter', got nodes=%v", ed.Spec().SortedNodeIDs())
	}

	// Click "[×]" on side-panel header to close Inspector, then click "Inspect" toolbar button to reopen.
	clickAt(canvasCols+inspCols-2, 1, tea.MouseLeft, 0)
	_, inspAfterClose := ed.splitColumns()
	if inspAfterClose != 0 {
		t.Fatalf("expected [×] click to close side panel, got inspCols=%d", inspAfterClose)
	}
	clickAt(findHeaderBtn("inspect"), 0, tea.MouseLeft, 0)
	canvasCols, inspCols = ed.splitColumns()
	if inspCols == 0 {
		t.Fatal("expected Inspect toolbar button to reopen Inspector")
	}

	// 2. Inspector mouse clicks: toggle boolean, cycle enum, and click [-][+] numeric steppers!
	ed.SelectNode("filter")
	_ = ed.View()
	items := ed.buildInspectorItems()
	rowLineForKey := func(key string) int {
		for idx, it := range items {
			if it.key == key {
				for lineIdx, mapped := range ed.inspectorLineItem {
					if mapped == idx {
						return lineIdx + 2
					}
				}
			}
		}
		t.Fatalf("inspector row for %q not found", key)
		return 0
	}
	clickAt(canvasCols+4, rowLineForKey("case_sensitive"), tea.MouseLeft, 0)
	if got := ed.Spec().Nodes["filter"].Params["case_sensitive"]; got != true {
		t.Fatalf("clicking case_sensitive row: got %v, want true", got)
	}
	clickAt(canvasCols+4, rowLineForKey("mode"), tea.MouseLeft, 0)
	if got := ed.Spec().Nodes["filter"].Params["mode"]; got != "exclude" {
		t.Fatalf("clicking mode enum row: got %v, want exclude", got)
	}

	// Select "typeset" (limit: int = 15) and click [+] twice then [-] once on its numeric stepper!
	ed.SelectNode("typeset")
	_ = ed.View()
	items = ed.buildInspectorItems()
	limitY := rowLineForKey("limit")
	plusX := canvasCols + inspCols - 2  // inside "[+]"
	minusX := canvasCols + inspCols - 5 // inside "[-]"
	clickAt(plusX, limitY, tea.MouseLeft, 0)
	clickAt(plusX, limitY, tea.MouseLeft, 0)
	if got := ed.Spec().Nodes["typeset"].Params["limit"]; got != int64(17) {
		t.Fatalf("after 2x [+] stepper clicks, limit = %v (%T), want int64(17)", got, got)
	}
	clickAt(minusX, limitY, tea.MouseLeft, 0)
	if got := ed.Spec().Nodes["typeset"].Params["limit"]; got != int64(16) {
		t.Fatalf("after 1x [-] stepper click, limit = %v, want int64(16)", got)
	}

	// 3. Connected-inlet wire detach & rewire: drag from connected typeset.json inlet -> filter.in inlet!
	tIdx := ed.Scene().NodeByID["typeset"]
	tJsonIn := ed.Scene().Nodes[tIdx].Inlets[0]
	fIdx := ed.Scene().NodeByID["filter"]
	var fIn PortAnchor
	for _, in := range ed.Scene().Nodes[fIdx].Inlets {
		if in.Name == "in" {
			fIn = in
		}
	}
	tx, ty := worldToCell(tJsonIn.Center.X, tJsonIn.Center.Y)
	fx, fy := worldToCell(fIn.Center.X, fIn.Center.Y)
	ed.Update(tea.MouseClickMsg{X: tx, Y: ty, Button: tea.MouseLeft})
	ed.Update(tea.MouseMotionMsg{X: fx, Y: fy, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: fx, Y: fy, Button: tea.MouseLeft})

	rewiredEdge := nodegraph.EdgeSpec{
		From: nodegraph.PortRef{Node: "src", Port: "body"},
		To:   nodegraph.PortRef{Node: "filter", Port: "in"},
	}
	oldEdge := nodegraph.EdgeSpec{
		From: nodegraph.PortRef{Node: "src", Port: "body"},
		To:   nodegraph.PortRef{Node: "typeset", Port: "json"},
	}
	if !slices.Contains(ed.Spec().Edges, rewiredEdge) || slices.Contains(ed.Spec().Edges, oldEdge) {
		t.Fatalf("expected connected-inlet drag to rewire src.body -> filter.in; edges=%v", ed.Spec().Edges)
	}

	// Dragging from connected filter.in and releasing on empty canvas disconnects it!
	fIdx = ed.Scene().NodeByID["filter"]
	for _, in := range ed.Scene().Nodes[fIdx].Inlets {
		if in.Name == "in" {
			fIn = in
		}
	}
	fx, fy = worldToCell(fIn.Center.X, fIn.Center.Y)
	emptyX, emptyY := worldToCell(20, 320)
	ed.Update(tea.MouseClickMsg{X: fx, Y: fy, Button: tea.MouseLeft})
	ed.Update(tea.MouseMotionMsg{X: emptyX, Y: emptyY, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: emptyX, Y: emptyY, Button: tea.MouseLeft})
	if slices.Contains(ed.Spec().Edges, rewiredEdge) {
		t.Fatalf("expected dropping detached inlet wire on empty canvas to disconnect it; edges=%v", ed.Spec().Edges)
	}

	// Restore clean baseline via Undo twice + remove extra filter node so graph is LiveSynced again.
	_, _ = ed.DeleteSelected()
	ed.SelectNode("filter")
	_, _ = ed.DeleteSelected()
	_, _ = ed.ApplyEdits(nodegraph.Connect(
		nodegraph.PortRef{Node: "src", Port: "body"},
		nodegraph.PortRef{Node: "typeset", Port: "json"},
		"",
	))
	if !ed.LiveSynced() {
		t.Fatalf("expected clean baseline to be LiveSynced, err=%v", ed.ValidationError())
	}

	// 4. Shift + Left-Drag marquee box selection across src and typeset!
	m0x, m0y := worldToCell(10, 60)
	m1x, m1y := worldToCell(580, 260)
	ed.Update(tea.MouseClickMsg{X: m0x, Y: m0y, Button: tea.MouseLeft, Mod: tea.ModShift})
	ed.Update(tea.MouseMotionMsg{X: m1x, Y: m1y, Button: tea.MouseLeft, Mod: tea.ModShift})
	if ed.Scene().Marquee == nil {
		t.Fatal("expected Scene().Marquee != nil during Shift+Drag")
	}
	_ = RenderSceneVector(ed.Scene(), ed.Camera(), 640, 360, DefaultTheme(), "MARQUEE")
	_ = ed.View()
	ed.Update(tea.MouseReleaseMsg{X: m1x, Y: m1y, Button: tea.MouseLeft, Mod: tea.ModShift})
	if !ed.Selection().HasNode("src") || !ed.Selection().HasNode("typeset") {
		t.Fatalf("expected marquee selection to include src and typeset, got %+v", ed.Selection().Nodes)
	}

	// 5. Double-click on wire opens Palette in Splice mode, and clicking transform/filter@1 splices it!
	var w0 WirePath
	for _, w := range ed.Scene().Wires {
		if w.Edge.From.Node == "src" && w.Edge.To.Node == "typeset" {
			w0 = w
		}
	}
	mid := evalCubicBezier(w0.FromPos, w0.Ctrl1, w0.Ctrl2, w0.ToPos, 0.5)
	wxCell, wyCell := worldToCell(mid.X, mid.Y)
	clickAt(wxCell, wyCell, tea.MouseLeft, 0)
	clickAt(wxCell, wyCell, tea.MouseLeft, 0) // stationary double-click!
	if ed.focus != focusPalette || !ed.paletteSpliceMode {
		t.Fatalf("expected wire double-click to open Palette in splice mode (focus=%v splice=%v)", ed.focus, ed.paletteSpliceMode)
	}
	_ = ed.View()
	canvasCols, _ = ed.splitColumns()
	filterRowY = 0
	for lineIdx, itemIdx := range ed.paletteLineItem {
		if itemIdx == filterPaletteIdx {
			filterRowY = lineIdx + 2
			break
		}
	}
	clickAt(canvasCols+4, filterRowY, tea.MouseLeft, 0)
	if !ed.LiveSynced() || ed.Spec().Nodes["filter"].Type.Name != "filter" {
		t.Fatalf("expected double-click splice to keep graph LiveSynced with filter node, err=%v", ed.ValidationError())
	}

	// 6. Stationary Right-Click opens floating Context Menu vs Right-Drag pans Camera!
	vIdx := ed.Scene().NodeByID["view"]
	vb := ed.Scene().Nodes[vIdx].Bounds
	vx, vy := worldToCell(vb.X+40, vb.Y+12)
	clickAt(vx, vy, tea.MouseRight, 0)
	if ed.Scene().ContextMenu == nil {
		t.Fatal("expected stationary right-click on node to open ContextMenu")
	}
	// Render context menu in both Vector and Grid modes to verify zero panics & clean bounds.
	_ = RenderSceneVector(ed.Scene(), ed.Camera(), 640, 360, DefaultTheme(), "CTX MENU")
	if !strings.Contains(ed.View(), "Duplicate Node") {
		t.Fatalf("expected Grid View to render context menu with 'Duplicate Node'")
	}
	// Click "Duplicate Node" (item index 2 -> canvasRow = r0 + 2 + 2, editor Y = canvasRow + 1)!
	c0, r0, _, _ := ed.Scene().ContextMenu.CellBounds(canvasCols, max(4, ed.rows-2))
	clickAt(c0+3, r0+2+2+1, tea.MouseLeft, 0)
	if _, ok := ed.Spec().Nodes["view_copy"]; !ok {
		t.Fatalf("expected clicking 'Duplicate Node' in context menu to create view_copy; nodes=%v", ed.Spec().SortedNodeIDs())
	}

	// Right-click + drag pans Camera without opening ContextMenu!
	panBefore := ed.Camera().PanX
	ed.Update(tea.MouseClickMsg{X: 8, Y: 5, Button: tea.MouseRight})
	ed.Update(tea.MouseMotionMsg{X: 16, Y: 8, Button: tea.MouseRight})
	ed.Update(tea.MouseReleaseMsg{X: 16, Y: 8, Button: tea.MouseRight})
	if ed.Scene().ContextMenu != nil {
		t.Fatal("expected right-drag pan NOT to open ContextMenu")
	}
	if ed.Camera().PanX == panBefore {
		t.Fatal("expected right-drag to pan Camera.PanX")
	}

	// 7. Passive hover motion highlights port/node & updates footer; mouse wheel over side panel scrolls Inspector!
	vIdx = ed.Scene().NodeByID["view"]
	vb = ed.Scene().Nodes[vIdx].Bounds
	hx, hy := worldToCell(vb.X+30, vb.Y+12)
	ed.Update(tea.MouseMotionMsg{X: hx, Y: hy, Button: tea.MouseNone})
	if !ed.Scene().Nodes[ed.Scene().NodeByID["view"]].Hovered {
		t.Fatal("expected passive MouseMotionMsg over node to mark NodeBox.Hovered=true")
	}
	if !strings.Contains(ed.View(), "Node view") {
		t.Fatal("expected footer hover readout to mention hovered node")
	}

	ed.SelectNode("filter")
	ed.focus = focusInspector
	rowBefore := ed.inspectorRow
	ed.Update(tea.MouseWheelMsg{X: canvasCols + 4, Y: 6, Button: tea.MouseWheelDown})
	if ed.inspectorRow <= rowBefore {
		t.Fatalf("expected MouseWheelDown over Inspector to advance inspectorRow (%d -> %d)", rowBefore, ed.inspectorRow)
	}

	// 8. Vector drag motion coalescing: 25 rapid drag-motion events schedule only 1 frame tick instead of 25 synchronous rasterizations!
	_ = ed.SetMode(RenderVector)
	ed.Update(tea.MouseClickMsg{X: 10, Y: 10, Button: tea.MouseMiddle})
	scheduledCount := 0
	for step := 1; step <= 25; step++ {
		_, cmd := ed.Update(tea.MouseMotionMsg{X: 10 + step, Y: 10, Button: tea.MouseMiddle})
		if cmd != nil {
			scheduledCount++
		}
	}
	if scheduledCount != 1 || !ed.vectorTickScheduled || !ed.vectorDirty {
		t.Fatalf("expected 25 rapid vector drag motions to schedule exactly 1 tick (got %d, scheduled=%v, dirty=%v)",
			scheduledCount, ed.vectorTickScheduled, ed.vectorDirty)
	}
	_, _ = ed.Update(vectorRefreshTickMsg{})
	if ed.vectorDirty || ed.vectorTickScheduled {
		t.Fatalf("expected vectorRefreshTickMsg to perform single coalesced raster pass (dirty=%v, scheduled=%v)",
			ed.vectorDirty, ed.vectorTickScheduled)
	}
	ed.Update(tea.MouseReleaseMsg{X: 35, Y: 10, Button: tea.MouseMiddle})

	// 9. Custom LayoutOptions: configurable ColumnGap / RowGap / Direction with zero card overlaps!
	ed.SetLayoutOptions(LayoutOptions{
		Direction: LayoutLeftToRight,
		ColumnGap: 180,
		RowGap:    80,
	})
	if _, err := ed.AutoLayout(); err != nil {
		t.Fatalf("AutoLayout with custom LayoutOptions: %v", err)
	}
	boxes := ed.Scene().Nodes
	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			if boxes[i].Bounds.Expand(12, 12).Intersects(boxes[j].Bounds) {
				t.Fatalf("AutoLayout cards %s (%+v) and %s (%+v) overlap or violate padding",
					boxes[i].ID, boxes[i].Bounds, boxes[j].ID, boxes[j].Bounds)
			}
		}
	}

	// 10. Proportional MaxVectorEdgePx clamping keeps Vector click coordinates 1:1 on wide/HiDPI viewports!
	ed.SetSize(600, 24) // canvasCols ~558 -> rawW 4464 > MaxVectorEdgePx (4096)
	ed.FitToView()
	wideCols, _ := ed.splitColumns()
	wideRows := max(4, ed.rows-2)
	pxW, pxH := ed.canvasVectorPixelSize(wideCols, wideRows)
	if pxW != MaxVectorEdgePx {
		t.Fatalf("expected wide viewport pxW == %d, got %d", MaxVectorEdgePx, pxW)
	}
	vBox := ed.Scene().Nodes[ed.Scene().NodeByID["view"]].Bounds
	centerScreen := ed.Camera().WorldToScreen(vBox.X+vBox.W*0.5, vBox.Y+vBox.H*0.5)
	targetCol := int(centerScreen.X / (float64(pxW) / float64(wideCols)))
	targetRow := int(centerScreen.Y/(float64(pxH)/float64(wideRows))) + 1
	ed.SelectNode("src")
	clickAt(targetCol, targetRow, tea.MouseLeft, 0)
	if !ed.Selection().HasNode("view") {
		t.Fatalf("expected clamped HiDPI Vector click at (%d,%d) to select 'view', got %+v",
			targetCol, targetRow, ed.Selection().Nodes)
	}
}

func FuzzEditorInteractions(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	f.Add([]byte{5, 12, 3, 8, 1, 14, 9, 2, 6, 11, 4, 7, 13, 0})

	reg, profile := newTestRegistry()

	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 48 {
			ops = ops[:48]
		}
		base, err := nodegraph.ParseYAML([]byte("version: 1\nname: fuzz\nnodes:\n  src:\n    type: source/http@1\n    capabilities: [net:example.com]\n    params:\n      url: https://example.com/a\n  typeset:\n    type: transform/typeset@1\n  view:\n    type: surface/view@1\nedges:\n  - from: src.body\n    to: typeset.json\n  - from: typeset.document\n    to: view.document\n"))
		if err != nil {
			t.Fatal(err)
		}

		ed := New(base, reg, Options{
			BackendProfile: profile,
			Mode:           RenderGrid,
			Width:          96,
			Height:         28,
			ShowInspector:  true,
		})
		defer func() {
			if cmd := ed.Close(); cmd != nil {
				_ = cmd()
			}
		}()

		keys := []string{"a", "c", "d", "i", "v", "w", "o", "t", "G", "z", "L", "0", "+", "-", "n", "e", "x", "u", "U", "?"}

		for idx, b := range ops {
			x := int(b) % 96
			y := (idx * 7) % 28
			switch b % 8 {
			case 0:
				k := keys[int(b>>3)%len(keys)]
				ed.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
			case 1:
				ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			case 2:
				ed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			case 3:
				mod := tea.KeyMod(0)
				if b&0x40 != 0 {
					mod = tea.ModShift
				}
				ed.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: mod})
				ed.Update(tea.MouseMotionMsg{X: (x + 6) % 96, Y: (y + 3) % 28, Button: tea.MouseLeft, Mod: mod})
				ed.Update(tea.MouseReleaseMsg{X: (x + 6) % 96, Y: (y + 3) % 28, Button: tea.MouseLeft, Mod: mod})
			case 4:
				// Stationary right-click or right-drag.
				ed.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
				if b&0x20 != 0 {
					ed.Update(tea.MouseMotionMsg{X: (x + 5) % 96, Y: (y + 2) % 28, Button: tea.MouseRight})
				}
				ed.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseRight})
			case 5:
				// Passive hover motion.
				ed.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseNone})
			case 6:
				btn := tea.MouseWheelUp
				if b&0x10 != 0 {
					btn = tea.MouseWheelDown
				}
				ed.Update(tea.MouseWheelMsg{X: x, Y: y, Button: btn})
			case 7:
				_ = ed.View()
			}

			// Invariant 1: Current spec must always serialize canonically without error.
			cur := ed.Spec()
			if _, err := nodegraph.MarshalYAML(cur); err != nil {
				t.Fatalf("step %d produced unserializable spec: %v", idx, err)
			}
			// Invariant 2: Whenever LiveSynced is true, Validate must pass with 0 issues.
			if ed.LiveSynced() {
				if _, err := nodegraph.Validate(cur, reg, nodegraph.ValidateOptions{Backend: &profile}); err != nil {
					t.Fatalf("step %d LiveSynced==true but Validate failed: %v", idx, err)
				}
			}
		}
	})
}

func newFiveStageBranchingSpec(t *testing.T) (*nodegraph.Spec, *nodegraph.Registry, nodegraph.BackendProfile) {
	t.Helper()
	reg, profile := newTestRegistry()
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("surface/log@1"),
		Description: "Logs scalar events.",
		Inlets:      []nodegraph.PortDef{{Name: "in", Type: nodegraph.TypeAny, Required: true}},
		Params:      []nodegraph.ParamDef{{Name: "prefix", Type: nodegraph.ParamString, Default: "matches"}},
		Backends:    []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("surface/raster@1"),
		Description: "Rasters a document into frames.",
		Inlets:      []nodegraph.PortDef{{Name: "document", Type: nodegraph.TypeDocument, Required: true}},
		Outlets:     []nodegraph.PortDef{{Name: "frame", Type: nodegraph.TypeBytes}},
		Params:      []nodegraph.ParamDef{{Name: "width", Type: nodegraph.ParamInt, Default: int64(560)}},
		Backends:    []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("surface/image@1"),
		Description: "Displays raster frames.",
		Inlets:      []nodegraph.PortDef{{Name: "frame", Type: nodegraph.TypeBytes, Required: true}},
		Params:      []nodegraph.ParamDef{{Name: "title", Type: nodegraph.ParamString, Default: "Preview"}},
		Backends:    []string{"nebula"},
	})

	spec, err := nodegraph.ParseYAML([]byte(`version: 1
name: five-stage-pipeline
groups:
  ingest:
    label: Ingest Stage
    nodes: [hn, filter]
nodes:
  hn:
    type: source/http@1
    capabilities: [net:example.com]
    params:
      url: https://example.com/feed.json
  filter:
    type: transform/filter@1
    params:
      keyword: Go
  typeset:
    type: transform/typeset@1
    params:
      limit: 12
  matched_log:
    type: surface/log@1
    params:
      prefix: frontpage_matches
  view:
    type: surface/view@1
  raster:
    type: surface/raster@1
    params:
      width: 560
  preview:
    type: surface/image@1
    params:
      title: Live Thumbnail
edges:
  - from: hn.body
    to: filter.in
  - from: filter.out
    to: typeset.json
  - from: filter.count
    to: matched_log.in
  - from: typeset.document
    to: view.document
  - from: typeset.document
    to: raster.document
  - from: raster.frame
    to: preview.frame
    kind: frame
`))
	if err != nil {
		t.Fatalf("ParseYAML five-stage-pipeline: %v", err)
	}
	laidOut, err := nodegraph.ApplyEdits(spec, AutoLayoutOpsWith(spec, reg, DefaultLayoutOptions())...)
	if err != nil {
		t.Fatalf("ApplyEdits AutoLayoutOpsWith: %v", err)
	}
	return laidOut, reg, profile
}

func rgbDistance(a, b color.Color) float64 {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	dr := float64(ar>>8) - float64(br>>8)
	dg := float64(ag>>8) - float64(bg>>8)
	db := float64(ab>>8) - float64(bb>>8)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func TestLayeredAutoLayoutNoOverlapAndPortOrder(t *testing.T) {
	spec, reg, _ := newFiveStageBranchingSpec(t)
	sc := BuildSceneWithLayout(spec, reg, Selection{}, nil, DefaultLayoutOptions())

	// 1. No two node bounding boxes overlap, and horizontal/vertical gaps are respected.
	for i := 0; i < len(sc.Nodes); i++ {
		for j := i + 1; j < len(sc.Nodes); j++ {
			a, b := sc.Nodes[i], sc.Nodes[j]
			if a.Bounds.Intersects(b.Bounds) {
				t.Fatalf("nodes %q (%+v) and %q (%+v) overlap", a.ID, a.Bounds, b.ID, b.Bounds)
			}
			if a.Bounds.X == b.Bounds.X {
				top, bot := a.Bounds, b.Bounds
				if bot.Y < top.Y {
					top, bot = bot, top
				}
				vGap := bot.Y - (top.Y + top.H)
				if vGap < DefaultRowGap-1 {
					t.Fatalf("vertical gap between %q and %q too small: %.1f < %.1f", a.ID, b.ID, vGap, DefaultRowGap)
				}
			}
		}
	}

	// 2. Port-aware barycentric ordering places typeset (fed by top outlet filter.out)
	// above matched_log (fed by bottom outlet filter.count) so wires don't cross!
	typesetBox := sc.Nodes[sc.NodeByID["typeset"]].Bounds
	logBox := sc.Nodes[sc.NodeByID["matched_log"]].Bounds
	if typesetBox.Y >= logBox.Y {
		t.Fatalf("expected typeset (Y=%.1f) above matched_log (Y=%.1f) to avoid outlet wire crossing", typesetBox.Y, logBox.Y)
	}

	// 3. Top-to-bottom layout arranges stages vertically with custom gaps.
	tbSpec, err := nodegraph.ApplyEdits(spec, AutoLayoutOpsWith(spec, reg, LayoutOptions{
		Direction: LayoutTopToBottom,
		ColumnGap: 180,
		RowGap:    90,
	})...)
	if err != nil {
		t.Fatalf("TopToBottom ApplyEdits: %v", err)
	}
	tbScene := BuildScene(tbSpec, reg, Selection{}, nil)
	hnY := tbScene.Nodes[tbScene.NodeByID["hn"]].Bounds.Y
	filterY := tbScene.Nodes[tbScene.NodeByID["filter"]].Bounds.Y
	previewY := tbScene.Nodes[tbScene.NodeByID["preview"]].Bounds.Y
	if !(hnY < filterY && filterY < previewY) {
		t.Fatalf("expected top-to-bottom Y progression hn(%.1f) < filter(%.1f) < preview(%.1f)", hnY, filterY, previewY)
	}
}

func TestVectorCardTypographyAndCameraRefitRegression(t *testing.T) {
	spec, reg, profile := newFiveStageBranchingSpec(t)
	theme := DefaultTheme()
	sc := BuildSceneWithLayout(spec, reg, Selection{Nodes: []string{"hn"}}, nil, DefaultLayoutOptions())

	// 1. Verify EVERY card header renders visible contrasting text glyphs at ALL zoom
	// levels down to MinZoom (0.15), and port labels render cleanly at Zoom >= 0.34.
	zoomCases := []struct {
		name      string
		zoom      float64
		pxW, pxH  int
		wantPorts bool
	}{
		{name: "min_zoom_0.15", zoom: MinZoom, pxW: 360, pxH: 200, wantPorts: false},
		{name: "low_zoom_0.25", zoom: 0.25, pxW: 520, pxH: 260, wantPorts: false},
		{name: "split_pane_0.34", zoom: 0.34, pxW: 680, pxH: 320, wantPorts: true},
		{name: "mid_zoom_0.48", zoom: 0.48, pxW: 920, pxH: 420, wantPorts: true},
		{name: "full_zoom_1.00", zoom: 1.00, pxW: 1800, pxH: 640, wantPorts: true},
	}

	for _, tc := range zoomCases {
		t.Run(tc.name, func(t *testing.T) {
			var cam Camera
			cam.FitBounds(sc.WorldBounds, tc.pxW, tc.pxH)
			// Pin exact zoom centered on WorldBounds so every node lies inside the frame.
			padded := sc.WorldBounds.Expand(40, 32)
			cam.Zoom = tc.zoom
			cam.PanX = padded.X + (padded.W-float64(tc.pxW)/cam.Zoom)*0.5
			cam.PanY = padded.Y + (padded.H-float64(tc.pxH)/cam.Zoom)*0.5

			img := RenderSceneVector(sc, cam, tc.pxW, tc.pxH, theme, "TYPOGRAPHY REGRESSION")

			// Check group header text contrast.
			for _, gb := range sc.Groups {
				tl := cam.WorldToScreen(gb.Bounds.X, gb.Bounds.Y)
				br := cam.WorldToScreen(gb.Bounds.X+gb.Bounds.W, gb.Bounds.Y+gb.Bounds.H)
				hdrH := min(br.Y-tl.Y, max(13.0, GroupHeaderHeight*cam.Zoom))
				x0 := max(0, int(math.Ceil(tl.X+max(5, 8*cam.Zoom))))
				x1 := min(tc.pxW, int(math.Floor(br.X-max(5, 8*cam.Zoom))))
				y0 := max(0, int(math.Ceil(tl.Y+2)))
				y1 := min(tc.pxH, int(math.Floor(tl.Y+hdrH-2)))
				glyphPx := 0
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						if rgbDistance(img.RGBAAt(x, y), theme.GroupHeader) >= 65 {
							glyphPx++
						}
					}
				}
				if glyphPx < 6 {
					t.Fatalf("group %q header missing visible text glyphs at zoom %.2f (found %d contrasting pixels)", gb.ID, tc.zoom, glyphPx)
				}
			}

			// Check every node card header (and port rows when wantPorts is true).
			for _, nb := range sc.Nodes {
				tl := cam.WorldToScreen(nb.Bounds.X, nb.Bounds.Y)
				br := cam.WorldToScreen(nb.Bounds.X+nb.Bounds.W, nb.Bounds.Y+nb.Bounds.H)
				hdrH := min(br.Y-tl.Y, max(13.0, HeaderHeight*cam.Zoom))
				hdrCol := theme.NamespaceColor(nb.Namespace)

				leftPad := max(4.0, 8.0*cam.Zoom)
				rightReserve := max(10.0, 18.0*cam.Zoom)
				if br.X-tl.X < 42 {
					rightReserve = 3.0
				}
				hx0 := max(0, int(math.Ceil(tl.X+leftPad)))
				hx1 := min(tc.pxW, int(math.Floor(br.X-rightReserve)))
				hy0 := max(0, int(math.Ceil(tl.Y+2)))
				hy1 := min(tc.pxH, int(math.Floor(tl.Y+hdrH-2)))

				headerGlyphPx := 0
				for y := hy0; y < hy1; y++ {
					for x := hx0; x < hx1; x++ {
						if rgbDistance(img.RGBAAt(x, y), hdrCol) >= 80 {
							headerGlyphPx++
						}
					}
				}
				if headerGlyphPx < 8 {
					t.Fatalf("node %q (%s) header has blank/missing title text at zoom %.2f: only %d contrasting pixels in [%d..%d, %d..%d]",
						nb.ID, nb.Title, tc.zoom, headerGlyphPx, hx0, hx1, hy0, hy1)
				}

				if tc.wantPorts && (len(nb.Inlets) > 0 || len(nb.Outlets) > 0) {
					bodyCol := theme.CardBG
					if nb.Selected {
						bodyCol = theme.CardBGSelect
					}
					portRows := max(len(nb.Inlets), len(nb.Outlets))
					// Sample interior of port rows well inside card borders and away from port sockets.
					insetX := max(8.0, 12.0*cam.Zoom)
					px0 := max(0, int(math.Ceil(tl.X+insetX)))
					px1 := min(tc.pxW, int(math.Floor(br.X-insetX)))
					py0 := max(0, int(math.Ceil(tl.Y+hdrH+2)))
					py1 := min(tc.pxH, int(math.Floor(min(br.Y-3, tl.Y+hdrH+float64(portRows)*PortRowHeight*cam.Zoom+2))))

					portGlyphPx := 0
					for y := py0; y < py1; y++ {
						for x := px0; x < px1; x++ {
							if rgbDistance(img.RGBAAt(x, y), bodyCol) >= 55 {
								portGlyphPx++
							}
						}
					}
					if portGlyphPx < 6 {
						t.Fatalf("node %q missing visible port label glyphs at zoom %.2f: only %d contrasting pixels in [%d..%d, %d..%d]",
							nb.ID, tc.zoom, portGlyphPx, px0, px1, py0, py1)
					}
				}
			}
		})
	}

	// 2. Startup camera auto-refit regression:
	// Constructing with narrow pre-WindowSizeMsg fallback dimensions starts at MinZoom,
	// then receiving WindowSizeMsg automatically refits the camera to the real viewport
	// UNLESS the user has manually zoomed or panned!
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderVector,
		Width:          28, // narrow pre-WindowSizeMsg fallback clamping to MinZoom
		Height:         24,
		ShowInspector:  true,
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	if ed.Camera().Zoom != MinZoom {
		t.Fatalf("expected initial narrow fallback camera at MinZoom=%.2f, got %.2f", MinZoom, ed.Camera().Zoom)
	}

	// Simulate Bubble Tea delivering the real wide terminal WindowSizeMsg on startup.
	ed.Update(tea.WindowSizeMsg{Width: 240, Height: 56})
	autoFitZoom := ed.Camera().Zoom
	if autoFitZoom <= 0.55 {
		t.Fatalf("expected startup WindowSizeMsg to auto-refit camera Zoom > 0.55, stayed stuck at %.2f", autoFitZoom)
	}

	// Once the user manually zooms, subsequent WindowSizeMsgs must preserve manual framing.
	ed.Update(tea.KeyPressMsg{Code: '+', Text: "+"})
	manualZoom := ed.Camera().Zoom
	if manualZoom <= autoFitZoom {
		t.Fatalf("expected '+' key to zoom in above %.2f, got %.2f", autoFitZoom, manualZoom)
	}
	ed.Update(tea.WindowSizeMsg{Width: 200, Height: 48})
	if math.Abs(ed.Camera().Zoom-manualZoom) > 1e-6 {
		t.Fatalf("expected manual camera zoom %.3f to survive resize, got %.3f", manualZoom, ed.Camera().Zoom)
	}

	// Pressing '0' (FitToView) re-engages automatic viewport fitting.
	ed.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
	refitZoom := ed.Camera().Zoom
	ed.Update(tea.WindowSizeMsg{Width: 260, Height: 60})
	if ed.Camera().Zoom <= refitZoom {
		t.Fatalf("expected FitToView ('0') to re-enable auto-refit on wider terminal: %.3f -> %.3f", refitZoom, ed.Camera().Zoom)
	}
}

func TestEditorCanvasZoomControls(t *testing.T) {
	spec, reg, profile := newFiveStageBranchingSpec(t)
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderGrid,
		Width:          140,
		Height:         40,
		ShowInspector:  true,
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	_ = ed.View() // populates headerButtons & panel geometry

	findHeaderBtnX := func(id string) int {
		t.Helper()
		_ = ed.View()
		for _, btn := range ed.headerButtons {
			if btn.id == id {
				return (btn.startCol + btn.endCol) / 2
			}
		}
		t.Fatalf("header button %q not found in toolbar (%+v)", id, ed.headerButtons)
		return 0
	}
	clickHeaderBtn := func(id string) {
		t.Helper()
		x := findHeaderBtnX(id)
		ed.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
		ed.Update(tea.MouseReleaseMsg{X: x, Y: 0, Button: tea.MouseLeft})
	}

	// 1. Toolbar buttons: zoom_in (+), zoom_out (−), zoom_reset (<pct>% <-> 100%/Fit), and fit.
	fitZoom := ed.Camera().Zoom
	clickHeaderBtn("zoom_in")
	if ed.Camera().Zoom <= fitZoom {
		t.Fatalf("expected toolbar '+' button to zoom in above %.3f, got %.3f", fitZoom, ed.Camera().Zoom)
	}
	afterIn := ed.Camera().Zoom
	clickHeaderBtn("zoom_out")
	if ed.Camera().Zoom >= afterIn {
		t.Fatalf("expected toolbar '−' button to zoom out below %.3f, got %.3f", afterIn, ed.Camera().Zoom)
	}
	clickHeaderBtn("zoom_reset")
	if math.Abs(ed.Camera().Zoom-1.0) > 1e-6 {
		t.Fatalf("expected toolbar zoom percentage pill to jump to 100%% (1.00), got %.3f", ed.Camera().Zoom)
	}
	// Clicking zoom_reset again when already at ~100% toggles back to FitToView!
	clickHeaderBtn("zoom_reset")
	if math.Abs(ed.Camera().Zoom-fitZoom) > 1e-4 {
		t.Fatalf("expected second click on zoom percentage pill at 100%% to FitToView (%.3f), got %.3f", fitZoom, ed.Camera().Zoom)
	}

	// 2. Plain mouse wheel over canvas zooms smoothly around the cursor's world anchor.
	cursorCol, cursorRow := 24, 12
	anchorBefore, _, ok := ed.mouseToCanvasWorld(tea.Mouse{X: cursorCol, Y: cursorRow})
	if !ok {
		t.Fatalf("expected (%d, %d) to be inside canvas", cursorCol, cursorRow)
	}
	zBefore := ed.Camera().Zoom
	ed.Update(tea.MouseWheelMsg{X: cursorCol, Y: cursorRow, Button: tea.MouseWheelUp})
	if ed.Camera().Zoom <= zBefore {
		t.Fatalf("expected MouseWheelUp over canvas to increase zoom (%.3f -> %.3f)", zBefore, ed.Camera().Zoom)
	}
	anchorAfterUp, _, _ := ed.mouseToCanvasWorld(tea.Mouse{X: cursorCol, Y: cursorRow})
	if math.Hypot(anchorAfterUp.X-anchorBefore.X, anchorAfterUp.Y-anchorBefore.Y) > 1e-4 {
		t.Fatalf("expected cursor world point to stay anchored during wheel zoom: before=%+v after=%+v", anchorBefore, anchorAfterUp)
	}
	zUp := ed.Camera().Zoom
	ed.Update(tea.MouseWheelMsg{X: cursorCol, Y: cursorRow, Button: tea.MouseWheelDown})
	if ed.Camera().Zoom >= zUp {
		t.Fatalf("expected MouseWheelDown over canvas to decrease zoom (%.3f -> %.3f)", zUp, ed.Camera().Zoom)
	}

	// 3. Ctrl/Alt + MouseWheel over the docked Inspector zooms the canvas instead of scrolling Inspector!
	ed.SelectNode("hn")
	ed.focus = focusInspector
	_ = ed.View()
	canvasCols, _ := ed.splitColumns()
	inspRowBefore := ed.inspectorRow
	zBeforeCtrlWheel := ed.Camera().Zoom
	ed.Update(tea.MouseWheelMsg{X: canvasCols + 4, Y: 8, Button: tea.MouseWheelUp, Mod: tea.ModCtrl})
	if ed.Camera().Zoom <= zBeforeCtrlWheel {
		t.Fatalf("expected Ctrl+MouseWheelUp over Inspector to zoom canvas (%.3f -> %.3f)", zBeforeCtrlWheel, ed.Camera().Zoom)
	}
	if ed.inspectorRow != inspRowBefore {
		t.Fatalf("expected Ctrl+MouseWheelUp over Inspector not to scroll inspectorRow (%d -> %d)", inspRowBefore, ed.inspectorRow)
	}

	// 4. Alt/Option + Left-Drag scrub-zoom: dragging upward zooms in, dragging downward zooms out.
	zScrubStart := ed.Camera().Zoom
	ed.Update(tea.MouseClickMsg{X: 20, Y: 18, Button: tea.MouseLeft, Mod: tea.ModAlt})
	ed.Update(tea.MouseMotionMsg{X: 20, Y: 8, Button: tea.MouseLeft, Mod: tea.ModAlt})
	zScrubUp := ed.Camera().Zoom
	if zScrubUp <= zScrubStart {
		t.Fatalf("expected Alt+Drag upward to zoom in (%.3f -> %.3f)", zScrubStart, zScrubUp)
	}
	ed.Update(tea.MouseMotionMsg{X: 20, Y: 26, Button: tea.MouseLeft, Mod: tea.ModAlt})
	zScrubDown := ed.Camera().Zoom
	if zScrubDown >= zScrubStart {
		t.Fatalf("expected Alt+Drag downward to zoom out below start (%.3f -> %.3f)", zScrubStart, zScrubDown)
	}
	ed.Update(tea.MouseReleaseMsg{X: 20, Y: 26, Button: tea.MouseLeft, Mod: tea.ModAlt})

	// 5. Kitty-protocol shifted '+' ('shift+=' with Text "+"), '-', '1' (100%), 'f' (FocusSelection), '0' (FitToView).
	ed.focus = focusCanvas
	zBeforeKey := ed.Camera().Zoom
	ed.Update(tea.KeyPressMsg{Code: '=', Mod: tea.ModShift, Text: "+"})
	if ed.Camera().Zoom <= zBeforeKey {
		t.Fatalf("expected Kitty shifted '+' (shift+= / Text='+') to zoom in (%.3f -> %.3f)", zBeforeKey, ed.Camera().Zoom)
	}
	zAfterPlus := ed.Camera().Zoom
	ed.Update(tea.KeyPressMsg{Code: '-', Mod: tea.ModShift, Text: "_"})
	if ed.Camera().Zoom >= zAfterPlus {
		t.Fatalf("expected shifted '_' (shift+-) to zoom out (%.3f -> %.3f)", zAfterPlus, ed.Camera().Zoom)
	}
	ed.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if math.Abs(ed.Camera().Zoom-1.0) > 1e-6 {
		t.Fatalf("expected '1' key to set 100%% zoom (1.00), got %.3f", ed.Camera().Zoom)
	}
	ed.SelectNode("hn")
	ed.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
	wholeFit := ed.Camera().Zoom
	ed.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if ed.Camera().Zoom <= wholeFit {
		t.Fatalf("expected 'f' (FocusSelection on single node) to zoom tighter than whole-graph fit (%.3f -> %.3f)", wholeFit, ed.Camera().Zoom)
	}

	// 6. Right-click empty canvas opens Context Menu with "Zoom In (+)" and clicking it zooms in!
	ed.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
	zCtxBefore := ed.Camera().Zoom
	// Top-left corner (X=2, Y=2) is empty canvas background outside node cards.
	ed.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseRight})
	ed.Update(tea.MouseReleaseMsg{X: 2, Y: 2, Button: tea.MouseRight})
	if ed.Scene().ContextMenu == nil {
		t.Fatal("expected right-click on empty canvas to open ContextMenu")
	}
	c0, r0, _, _ := ed.Scene().ContextMenu.CellBounds(canvasCols, max(4, ed.rows-2))
	// Item index 1 in Canvas context menu is "Zoom In (+)" -> canvasRow = r0 + 2 + 1, editor Y = canvasRow + 1.
	ed.Update(tea.MouseClickMsg{X: c0 + 3, Y: r0 + 2 + 1 + 1, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: c0 + 3, Y: r0 + 2 + 1 + 1, Button: tea.MouseLeft})
	if ed.Camera().Zoom <= zCtxBefore {
		t.Fatalf("expected clicking 'Zoom In (+)' in canvas context menu to increase zoom (%.3f -> %.3f)", zCtxBefore, ed.Camera().Zoom)
	}
}

func TestInlineParamDrawerPreviewAndViewerTap(t *testing.T) {
	reg := nodegraph.NewRegistry()
	profile := nodegraph.BackendProfile{Name: "nebula", EdgeKinds: []string{"frame", "event"}}

	minZero := 0.0
	maxTwo := 2.0
	minInt := 1.0
	maxInt := 20.0
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("gpu/filter@1"),
		Description: "Cinema GPU filter.",
		Inlets:      []nodegraph.PortDef{{Name: "texture", Type: nodegraph.MustParseDataType("texture<rgba8>"), Required: true}},
		Outlets:     []nodegraph.PortDef{{Name: "texture", Type: nodegraph.MustParseDataType("texture<rgba8>")}},
		Params: []nodegraph.ParamDef{
			{Name: "preset", Type: nodegraph.ParamString, Default: "passthrough", Enum: []string{"passthrough", "cyberpunk", "noir"}},
			{Name: "intensity", Type: nodegraph.ParamNumber, Default: 1.0, Min: &minZero, Max: &maxTwo},
			{Name: "radius", Type: nodegraph.ParamInt, Default: int64(4), Min: &minInt, Max: &maxInt},
			{Name: "bypass", Type: nodegraph.ParamBool, Default: false},
		},
		Backends: []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:       nodegraph.MustParseTypeID("source/video@1"),
		Outlets:  []nodegraph.PortDef{{Name: "texture", Type: nodegraph.MustParseDataType("texture<rgba8>")}},
		Backends: []string{"nebula"},
	})
	reg.MustRegister(nodegraph.TypeDef{
		ID:       nodegraph.MustParseTypeID("surface/video@1"),
		Inlets:   []nodegraph.PortDef{{Name: "texture", Type: nodegraph.MustParseDataType("texture<rgba8>"), Required: true}},
		Backends: []string{"nebula"},
	})

	spec, err := nodegraph.ParseYAML([]byte(`version: 1
name: m12-inline-drawer-test
nodes:
  src:
    type: source/video@1
  grade:
    type: gpu/filter@1
    params:
      preset: passthrough
      intensity: 1.0
      radius: 4
      bypass: false
  out:
    type: surface/video@1
edges:
  - from: src.texture
    to: grade.texture
    kind: frame
  - from: grade.texture
    to: out.texture
    kind: frame
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	var watchedPreviews []string
	var watchedViewer string
	var commits int
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderVector,
		Width:          180,
		Height:         48,
		Commit: func(_ *nodegraph.Spec, _ nodegraph.SpecDiff) error {
			commits++
			return nil
		},
		OnWatchChange: func(previewNodes []string, viewerNode string) {
			watchedPreviews = slices.Clone(previewNodes)
			watchedViewer = viewerNode
		},
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	ed.SelectNode("grade")
	ed.SetZoom(1.0)
	_ = ed.FocusSelection()

	worldToCell := func(wx, wy float64) (int, int) {
		t.Helper()
		col, row, ok := ed.WorldToTerminalCell(wx, wy)
		if !ok {
			t.Fatalf("world (%0.1f, %0.1f) outside viewport", wx, wy)
		}
		return col, row
	}
	clickWorld := func(wx, wy float64) {
		t.Helper()
		cx, cy := worldToCell(wx, wy)
		ed.Update(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseLeft})
		ed.Update(tea.MouseReleaseMsg{X: cx, Y: cy, Button: tea.MouseLeft})
	}

	// 1. Toggle inline parameter drawer via 'w' key and verify card expands + builds 4 InlineParamRows.
	ed.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})
	if !ed.IsNodeParamsExpanded("grade") {
		t.Fatal("expected 'w' to expand inline parameter drawer on 'grade'")
	}
	nb := ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
	if !nb.ParamsOpen || nb.Bounds.W != ExpandedNodeWidth || len(nb.ParamRows) != 4 {
		t.Fatalf("expected ExpandedNodeWidth & 4 param rows, got open=%v W=%.1f rows=%d", nb.ParamsOpen, nb.Bounds.W, len(nb.ParamRows))
	}

	findRow := func(key string) InlineParamRow {
		t.Helper()
		box := ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
		for _, r := range box.ParamRows {
			if r.Key == key {
				return r
			}
		}
		t.Fatalf("param row %q not found", key)
		return InlineParamRow{}
	}

	// 2. Click enum next ('▸') on "preset" -> cycles passthrough -> cyberpunk!
	presetRow := findRow("preset")
	clickWorld(presetRow.IncRect.X+presetRow.IncRect.W*0.5, presetRow.IncRect.Y+presetRow.IncRect.H*0.5)
	if got := ed.CommittedSpec().Nodes["grade"].Params["preset"]; got != "cyberpunk" {
		t.Fatalf("expected inline enum click to set preset=cyberpunk, got %v", got)
	}

	// 3. Click bool toggle on "bypass" -> toggles false -> true!
	bypassRow := findRow("bypass")
	clickWorld(bypassRow.ControlRect.X+bypassRow.ControlRect.W*0.5, bypassRow.ControlRect.Y+bypassRow.ControlRect.H*0.5)
	if got := ed.CommittedSpec().Nodes["grade"].Params["bypass"]; got != true {
		t.Fatalf("expected inline bool click to set bypass=true, got %v", got)
	}

	// 4. Scrub horizontal slider on "intensity" across 4 motion steps -> commits live on every step,
	// yet Undo ('u') reverts the entire drag gesture in ONE undo step!
	intRow := findRow("intensity")
	x25, yMid := worldToCell(intRow.TrackRect.X+intRow.TrackRect.W*0.25, intRow.TrackRect.Y+intRow.TrackRect.H*0.5)
	x85, _ := worldToCell(intRow.TrackRect.X+intRow.TrackRect.W*0.85, intRow.TrackRect.Y+intRow.TrackRect.H*0.5)
	commitsBeforeDrag := commits
	ed.Update(tea.MouseClickMsg{X: x25, Y: yMid, Button: tea.MouseLeft})
	for step := 1; step <= 4; step++ {
		mx := x25 + ((x85-x25)*step)/4
		ed.Update(tea.MouseMotionMsg{X: mx, Y: yMid, Button: tea.MouseLeft})
	}
	ed.Update(tea.MouseReleaseMsg{X: x85, Y: yMid, Button: tea.MouseLeft})
	if commits <= commitsBeforeDrag+1 {
		t.Fatalf("expected live slider drag to commit multiple intermediate updates (before=%d after=%d)", commitsBeforeDrag, commits)
	}
	scrubbedVal, _ := ed.CommittedSpec().Nodes["grade"].Params["intensity"].(float64)
	if scrubbedVal < 1.45 || scrubbedVal > 1.95 {
		t.Fatalf("expected scrubbed intensity near 85%% of [0,2] (~1.7), got %v", scrubbedVal)
	}
	ed.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	revertedVal := inlineAnyToFloat(ed.CommittedSpec().Nodes["grade"].Params["intensity"])
	if math.Abs(revertedVal-1.0) > 1e-6 {
		t.Fatalf("expected single Undo after slider scrub to restore intensity=1.0, got %v", revertedVal)
	}

	// 5. Open on-card live preview ('o') and tap Viewer ('t') -> verify OnWatchChange fires and both Vector & Grid render live thumbnails!
	ed.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	ed.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if !slices.Equal(watchedPreviews, []string{"grade"}) || watchedViewer != "grade" {
		t.Fatalf("expected OnWatchChange(previews=[grade], viewer=grade), got previews=%v viewer=%q", watchedPreviews, watchedViewer)
	}

	// Supply a bright magenta 32x20 RGBA thumbnail for "grade".
	thumb := image.NewRGBA(image.Rect(0, 0, 32, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 32; x++ {
			thumb.SetRGBA(x, y, color.RGBA{R: 236, G: 38, B: 190, A: 255})
		}
	}
	_ = ed.SetNodePreview("grade", NodePreview{
		Image:   thumb,
		Summary: "32x20 texture",
		Outlet:  "texture",
		Active:  true,
	})

	nb = ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
	if !nb.PreviewOpen || !nb.ViewerActive || nb.Preview == nil || nb.Preview.Image == nil {
		t.Fatalf("expected grade NodeBox to have PreviewOpen & ViewerActive & Image, got %+v", nb)
	}

	// Verify RenderSceneVector blits the bright magenta thumbnail inside PreviewContentRect!
	vecImg := RenderSceneVector(ed.Scene(), ed.Camera(), 960, 640, DefaultTheme(), "PREVIEW TEST")
	midPreview := ed.Camera().WorldToScreen(
		nb.PreviewContentRect.X+nb.PreviewContentRect.W*0.5,
		nb.PreviewContentRect.Y+nb.PreviewContentRect.H*0.45,
	)
	gotPx := vecImg.RGBAAt(int(math.Round(midPreview.X)), int(math.Round(midPreview.Y)))
	if gotPx.R < 200 || gotPx.B < 150 || gotPx.G > 80 {
		t.Fatalf("expected magenta preview pixel at center of PreviewContentRect, got RGBA=%+v", gotPx)
	}

	// Verify RenderSceneGrid renders truecolor half-blocks ('▄') and header badges ('◉', '≡', '▣')!
	grid := RenderSceneGrid(ed.Scene(), ed.Camera(), 120, 42, DefaultTheme(), "GRID PREVIEW")
	gridStr := grid.Render(image.Rect(0, 0, 120, 42))
	for _, want := range []string{"▾ params", "▾ preview·texture", "▄", "◉"} {
		if !strings.Contains(gridStr, want) {
			t.Fatalf("expected RenderSceneGrid output to contain %q", want)
		}
	}

	// Verify text-line preview (for table/JSON/scalar outlets) also renders cleanly in both Vector & Grid!
	_ = ed.SetNodePreview("grade", NodePreview{
		Lines:   []string{"table (2 rows)", "id | score", "a  | 98.4"},
		Summary: "2 rows",
		Outlet:  "rows",
		Active:  true,
	})
	_ = RenderSceneVector(ed.Scene(), ed.Camera(), 960, 640, DefaultTheme(), "TEXT PREVIEW")
	gridText := RenderSceneGrid(ed.Scene(), ed.Camera(), 120, 42, DefaultTheme(), "GRID TEXT").Render(image.Rect(0, 0, 120, 42))
	if !strings.Contains(gridText, "id | score") {
		t.Fatalf("expected RenderSceneGrid text preview to contain 'id | score'")
	}
}

func pumpEditorCmds(ed *Model, cmd tea.Cmd, maxRounds int) []tea.Msg {
	var seen []tea.Msg
	queue := []tea.Cmd{cmd}
	for round := 0; round < maxRounds && len(queue) > 0; round++ {
		cur := queue
		queue = nil
		for _, c := range cur {
			if c == nil {
				continue
			}
			msg := c()
			if msg == nil {
				continue
			}
			if batch, ok := msg.(tea.BatchMsg); ok {
				queue = append(queue, batch...)
				continue
			}
			// Do not auto-fire 16ms debounce ticks immediately unless needed; feed all immediate results.
			seen = append(seen, msg)
			_, next := ed.Update(msg)
			if next != nil {
				queue = append(queue, next)
			}
		}
	}
	return seen
}

func TestEditorMouseHitAccuracyAcrossResizesAndZooms(t *testing.T) {
	reg, profile := newTestRegistry()
	minZero := 0.0
	maxTwo := 2.0
	reg.MustRegister(nodegraph.TypeDef{
		ID:          nodegraph.MustParseTypeID("transform/grade@1"),
		Description: "Color grade node with float slider.",
		Inlets:      []nodegraph.PortDef{{Name: "in", Type: nodegraph.TypeBytes, Required: true}},
		Outlets:     []nodegraph.PortDef{{Name: "out", Type: nodegraph.TypeDocument}},
		Params: []nodegraph.ParamDef{
			{Name: "gain", Type: nodegraph.ParamNumber, Default: 1.0, Min: &minZero, Max: &maxTwo},
		},
		Backends: []string{"nebula"},
	})

	spec, err := nodegraph.ParseYAML([]byte(`version: 1
name: zoom-resize-hit-test
nodes:
  src:
    type: source/http@1
    capabilities: [net:example.com]
    params:
      url: https://example.com/feed.json
    ui: {x: 40, y: 25}
  grade:
    type: transform/grade@1
    params:
      gain: 1.0
    ui: {x: 360, y: 25}
  view:
    type: surface/view@1
    ui: {x: 680, y: 25}
edges:
  - from: src.body
    to: grade.in
  - from: grade.out
    to: view.document
    kind: frame
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	sess := ntpicture.NewTerminalSession(ntpicture.TerminalOptions{
		Kitty:           ntpicture.KittyCapabilitySupported,
		CellPixelWidth:  18,
		CellPixelHeight: 40, // macOS Retina cell taller than HeaderHeight(30), CollapsedNodeHeight(36), and badges(16)
	})
	defer sess.Close()

	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderVector,
		PictureSession: sess,
		Width:          120,
		Height:         36,
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	pumpEditorCmds(ed, ed.Init(), 24)

	// 1. Vector canvas must use FitFill (never FitContain) so non-1:2 Retina font cells
	// and half-block glyph fallbacks never letterbox/pillarbox away from mouse cells.
	if gotFit := ed.Picture().Fit(); gotFit != ntpicture.FitFill {
		t.Fatalf("expected editor Picture Fit == FitFill, got %v", gotFit)
	}

	// Helper mapping world (wx, wy) to editor cell (col, row) in Vector mode.
	worldToVectorCell := func(wx, wy float64) (int, int) {
		canvasCols, _ := ed.splitColumns()
		bodyRows := ed.bodyRows()
		pxW, pxH := ed.canvasVectorPixelSize(canvasCols, bodyRows)
		sp := ed.Camera().WorldToScreen(wx, wy)
		col := int(math.Floor(sp.X / (float64(pxW) / float64(canvasCols))))
		row := int(math.Floor(sp.Y / (float64(pxH) / float64(bodyRows))))
		return col, row + 1 // +1 for header toolbar row
	}
	clickCell := func(col, row int) {
		_, c1 := ed.Update(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})
		_, c2 := ed.Update(tea.MouseReleaseMsg{X: col, Y: row, Button: tea.MouseLeft})
		pumpEditorCmds(ed, tea.Batch(c1, c2), 16)
	}

	// 2. WindowSizeMsg must emit CSI 16 t ("\x1b[16t") to re-query cell pixel metrics after Cmd++/Cmd+-.
	_, resizeCmd := ed.Update(tea.WindowSizeMsg{Width: 132, Height: 40})
	msgs := pumpEditorCmds(ed, resizeCmd, 16)
	sawCellSizeQuery := false
	for _, m := range msgs {
		if raw, ok := m.(tea.RawMsg); ok && strings.Contains(fmt.Sprint(raw.Msg), "\x1b[16t") {
			sawCellSizeQuery = true
			break
		}
	}
	if !sawCellSizeQuery {
		t.Fatalf("expected WindowSizeMsg in RenderVector mode to emit CSI 16 t cell-pixel query, saw %d msgs", len(msgs))
	}

	// 3. Ultrawide resize (> 297 Kitty placeholder limit) falls back cleanly to PictureGlyph
	// without geometry ErrorMsg desync, and shrinking back restores PictureKitty!
	_, wideCmd := ed.Update(tea.WindowSizeMsg{Width: 320, Height: 70})
	for _, m := range pumpEditorCmds(ed, wideCmd, 24) {
		if errMsg, ok := m.(ntpicture.ErrorMsg); ok && errMsg.Owner == ed.Picture() {
			t.Fatalf("unexpected ntpicture.ErrorMsg on 320x70 ultrawide resize: %v", errMsg.Err)
		}
	}
	if pCols, pRows := ed.Picture().Size(); pCols != 320 || pRows != 68 {
		t.Fatalf("ultrawide Picture.Size() = %dx%d, want 320x68", pCols, pRows)
	}
	if ed.Picture().Mode() != ntpicture.PictureGlyph {
		t.Fatalf("expected PictureGlyph fallback above 297 cols, got %v", ed.Picture().Mode())
	}

	_, shrinkCmd := ed.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	pumpEditorCmds(ed, shrinkCmd, 24)
	if ed.Picture().Mode() != ntpicture.PictureKitty {
		t.Fatalf("expected PictureKitty restored after shrinking to 120x36, got %v", ed.Picture().Mode())
	}

	// 4. Across extreme zoom levels (0.15x .. 3.5x), clicking a connected node's header or
	// body must ALWAYS select the node and NEVER hijack HitPort or unplug its incoming wire!
	for _, z := range []float64{0.15, 0.22, 0.35, 0.60, 1.0, 2.2, 3.5} {
		ed.SelectNode("grade")
		pumpEditorCmds(ed, ed.SetZoom(z), 16)
		ed.sel = Selection{}
		ed.rebuildScene()

		gBox := ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
		// Click header title interior (left-center of header bar, near connected grade.in inlet!).
		hdrCol, hdrRow := worldToVectorCell(gBox.Bounds.X+gBox.Bounds.W*0.38, gBox.Bounds.Y+min(gBox.Bounds.H*0.4, 14))
		clickCell(hdrCol, hdrRow)

		if ed.Selection().PrimaryNode() != "grade" {
			t.Fatalf("zoom=%.2f: clicking grade header at cell (%d,%d) failed to select grade (sel=%+v)",
				z, hdrCol, hdrRow, ed.Selection())
		}
		if len(ed.Spec().Edges) != 2 {
			t.Fatalf("zoom=%.2f: clicking grade header accidentally unplugged incoming wire! edges=%v",
				z, ed.Spec().Edges)
		}
	}

	// 5. Sub-cell Retina quantization test (Zoom=1.0, Pan=0, CellPixelSize=18x40):
	// Node "grade" is at Y=25 -> Header=[25,55], ParamsBadge=[32,48], Collapsed=[25,61].
	// Terminal row 0 midpoint is wy=20 (<25!) and row 1 midpoint is wy=60 (>48!).
	// Clicking the cell where ParamsBadge and CollapseBadge are painted MUST still trigger them!
	ed.cam = Camera{PanX: 0, PanY: 0, Zoom: 1.0}
	ed.userMovedCamera = true
	gBox := ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
	paramsCol, paramsRow := worldToVectorCell(
		gBox.ParamsBadgeRect.X+gBox.ParamsBadgeRect.W*0.5,
		gBox.ParamsBadgeRect.Y+gBox.ParamsBadgeRect.H*0.5,
	)
	clickCell(paramsCol, paramsRow)
	if !ed.IsNodeParamsExpanded("grade") {
		t.Fatalf("Retina sub-cell click at (%d,%d) failed to toggle grade inline params drawer", paramsCol, paramsRow)
	}

	// Scrub the inline "gain" slider even when its 18px track sits inside a 40px Retina cell!
	gBox = ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
	if len(gBox.ParamRows) == 0 {
		t.Fatal("expected inline ParamRows on grade")
	}
	track := gBox.ParamRows[0].TrackRect
	sCol0, sRow := worldToVectorCell(track.X+track.W*0.20, track.Y+track.H*0.5)
	sCol1, _ := worldToVectorCell(track.X+track.W*0.85, track.Y+track.H*0.5)
	ed.Update(tea.MouseClickMsg{X: sCol0, Y: sRow, Button: tea.MouseLeft})
	ed.Update(tea.MouseMotionMsg{X: sCol1, Y: sRow, Button: tea.MouseLeft})
	ed.Update(tea.MouseReleaseMsg{X: sCol1, Y: sRow, Button: tea.MouseLeft})
	gotGain := inlineAnyToFloat(ed.Spec().Nodes["grade"].Params["gain"])
	if gotGain < 1.35 {
		t.Fatalf("expected Retina sub-cell slider scrub to increase gain > 1.35, got %v", gotGain)
	}

	// Collapse "grade" (which has an outlet!) and verify clicking '▸' on the collapsed card
	// uncollapses it instead of getting blocked by the stacked outlet port!
	gBox = ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
	colCollapse, rowCollapse := worldToVectorCell(
		gBox.CollapseBadgeRect.X+gBox.CollapseBadgeRect.W*0.5,
		gBox.CollapseBadgeRect.Y+gBox.CollapseBadgeRect.H*0.5,
	)
	clickCell(colCollapse, rowCollapse)
	if !ed.Scene().Nodes[ed.Scene().NodeByID["grade"]].Collapsed {
		t.Fatal("expected clicking collapse glyph to collapse grade")
	}
	gCollapsed := ed.Scene().Nodes[ed.Scene().NodeByID["grade"]]
	colExpand, rowExpand := worldToVectorCell(
		gCollapsed.Bounds.X+gCollapsed.Bounds.W-12,
		gCollapsed.Bounds.Y+gCollapsed.Bounds.H*0.5,
	)
	clickCell(colExpand, rowExpand)
	if ed.Scene().Nodes[ed.Scene().NodeByID["grade"]].Collapsed {
		t.Fatal("regression: clicking '▸' on collapsed node with outlets failed to uncollapse (hijacked by stacked outlet port)")
	}

	// 6. Panning across the top header bar (outside canvas Y=0) must continue smoothly without jumping Pan to (0,0)!
	ed.cam = Camera{PanX: 120, PanY: 80, Zoom: 1.0}
	ed.userMovedCamera = true
	ed.Update(tea.MouseClickMsg{X: 30, Y: 15, Button: tea.MouseMiddle})
	ed.Update(tea.MouseMotionMsg{X: 32, Y: 0, Button: tea.MouseMiddle}) // Y=0 is header bar (!inCanvas)
	ed.Update(tea.MouseReleaseMsg{X: 32, Y: 0, Button: tea.MouseMiddle})
	camAfterPan := ed.Camera()
	// Moving X from 30 -> 32 (+2 cols * 18px = +36px) should shift PanX from 120 to ~84 (NEVER jump by 30*18=540!).
	if math.Abs(camAfterPan.PanX-84) > 25 {
		t.Fatalf("regression: panning cursor over header bar jumped PanX to %.1f (want ~84)", camAfterPan.PanX)
	}

	// 7. Extreme narrow resize (22x6) never soft-wraps header/footer or overflows terminal height.
	ed.Update(tea.WindowSizeMsg{Width: 22, Height: 6})
	narrowLines := strings.Split(ed.View(), "\n")
	if len(narrowLines) != 6 {
		t.Fatalf("22x6 narrow editor View() produced %d lines, want exact 6", len(narrowLines))
	}
	for i, ln := range narrowLines {
		if w := ansi.StringWidth(ln); w > 22 {
			t.Fatalf("22x6 narrow editor line %d width = %d > 22", i, w)
		}
	}
}

func TestEditorSubgraphPromoteDrillInEditAndUndo(t *testing.T) {
	reg, profile := newTestRegistry()
	spec := newSampleSpec(t)
	lib := nodegraph.NewLibrary()

	var rootCommits []*nodegraph.Spec
	var registeredComps []nodegraph.TypeID

	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Library:        lib,
		Mode:           RenderGrid,
		Width:          120,
		Height:         32,
		ShowInspector:  false,
		Commit: func(next *nodegraph.Spec, _ nodegraph.SpecDiff) error {
			rootCommits = append(rootCommits, next)
			return nil
		},
		OnRegisterComponent: func(comp *nodegraph.Spec) (nodegraph.TypeDef, error) {
			vOpts := nodegraph.ValidateOptions{Backend: &profile}
			vOpts.Backend.Namespaces = append(append([]string(nil), profile.Namespaces...), nodegraph.NamespaceGraph)
			def, err := nodegraph.SynthesizeTypeDef(comp, reg, vOpts)
			if err != nil {
				return nodegraph.TypeDef{}, err
			}
			if _, err := lib.Put(comp, "editor-test"); err != nil {
				return nodegraph.TypeDef{}, err
			}
			if err := reg.Upsert(def); err != nil {
				return nodegraph.TypeDef{}, err
			}
			registeredComps = append(registeredComps, def.ID)
			return def, nil
		},
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	// 1. Select group "pipeline" and press G to promote it to a reusable graph/* component.
	ed.SelectGroup("pipeline")
	ed.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})

	compID := nodegraph.MustParseTypeID("graph/core-pipeline@1")
	if len(registeredComps) != 1 || registeredComps[0] != compID {
		t.Fatalf("registeredComps = %v, want [%s]", registeredComps, compID)
	}
	if len(rootCommits) != 1 {
		t.Fatalf("rootCommits count = %d, want 1", len(rootCommits))
	}
	promotedRoot := ed.Spec()
	if len(promotedRoot.Nodes) != 2 {
		t.Fatalf("promoted root node count = %d, want 2 (pipeline, view)", len(promotedRoot.Nodes))
	}
	pipeNode, ok := promotedRoot.Nodes["pipeline"]
	if !ok || pipeNode.Type != compID {
		t.Fatalf("pipeline node = %+v (ok=%v), want type %s", pipeNode, ok, compID)
	}
	scRoot := ed.Scene()
	idxPipe, hasPipeBox := scRoot.NodeByID["pipeline"]
	if !hasPipeBox || !scRoot.Nodes[idxPipe].IsSubgraph || !strings.HasPrefix(scRoot.Nodes[idxPipe].Subtitle, "◈ ") {
		t.Fatalf("subgraph NodeBox = %+v, expected IsSubgraph=true and ◈ subtitle", scRoot.Nodes[idxPipe])
	}
	// Verify both Vector and Grid renderers draw subgraph cards cleanly.
	vecImg := RenderSceneVector(scRoot, ed.Camera(), 640, 360, DefaultTheme(), "TEST")
	if vecImg.Bounds().Dx() != 640 {
		t.Fatalf("unexpected vector image width %d", vecImg.Bounds().Dx())
	}
	gridOut := RenderSceneGrid(scRoot, ed.Camera(), 90, 26, DefaultTheme(), "TEST").Render(image.Rect(0, 0, 90, 26))
	if !strings.Contains(gridOut, "◈") {
		t.Fatalf("grid render missing ◈ subgraph marker:\n%s", gridOut)
	}

	// 2. Undo restores original primitive nodes and group; Redo re-applies promotion.
	if _, err := ed.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(ed.Spec().Nodes) != 3 || len(ed.Spec().Groups) != 1 {
		t.Fatalf("after Undo: nodes=%d groups=%d, want 3 nodes and 1 group", len(ed.Spec().Nodes), len(ed.Spec().Groups))
	}
	if _, err := ed.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if len(ed.Spec().Nodes) != 2 {
		t.Fatalf("after Redo: nodes=%d, want 2", len(ed.Spec().Nodes))
	}

	// 3. Select the graph/* instance node and press Enter to drill into the component.
	ed.SelectNode("pipeline")
	ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ed.Depth() != 1 || ed.ActiveComponent() != compID {
		t.Fatalf("after Enter: depth=%d active=%s, want depth=1 active=%s", ed.Depth(), ed.ActiveComponent(), compID)
	}
	crumbs := ed.Breadcrumbs()
	if len(crumbs) != 2 || crumbs[0] != "editor-test" || !strings.Contains(crumbs[1], "graph/core-pipeline@1") {
		t.Fatalf("Breadcrumbs = %v, want [editor-test, pipeline (graph/core-pipeline@1)]", crumbs)
	}
	viewHeader := strings.Split(ed.View(), "\n")[0]
	if !strings.Contains(viewHeader, "Back") {
		t.Fatalf("drilled-in header missing Back button: %q", viewHeader)
	}

	// Check boundary badge on internal "typeset" node (⇠ out:document).
	scInner := ed.Scene()
	tsIdx, ok := scInner.NodeByID["typeset"]
	if !ok || !slices.Contains(scInner.Nodes[tsIdx].BoundaryBadges, "⇠ out:document") {
		t.Fatalf("typeset BoundaryBadges = %v, want to contain '⇠ out:document'", scInner.Nodes[tsIdx].BoundaryBadges)
	}
	_ = RenderSceneVector(scInner, ed.Camera(), 640, 360, DefaultTheme(), "INNER")
	innerGrid := RenderSceneGrid(scInner, ed.Camera(), 90, 26, DefaultTheme(), "INNER").Render(image.Rect(0, 0, 90, 26))
	if !strings.Contains(innerGrid, "out:document") {
		t.Fatalf("inner grid render missing boundary badge out:document:\n%s", innerGrid)
	}

	// 4. Edit an internal node parameter while drilled in: updates Library without corrupting RootSpec.
	rootCommitCountBefore := len(rootCommits)
	if _, err := ed.ApplyEdits(nodegraph.SetParam("typeset", "limit", int64(42))); err != nil {
		t.Fatalf("inner ApplyEdits: %v", err)
	}
	if len(rootCommits) != rootCommitCountBefore {
		t.Fatalf("inner component edit unexpectedly invoked root Commit (%d -> %d)", rootCommitCountBefore, len(rootCommits))
	}
	updatedComp, ok := lib.LookupSpec(compID)
	if !ok {
		t.Fatalf("LookupSpec(%s) missing", compID)
	}
	if got := updatedComp.Nodes["typeset"].Params["limit"]; got != int64(42) {
		t.Fatalf("updatedComp typeset.limit = %v (%T), want int64(42)", got, got)
	}
	if _, hasPipe := ed.RootSpec().Nodes["pipeline"]; !hasPipe {
		t.Fatalf("RootSpec lost pipeline node while drilled in: %+v", ed.RootSpec().Nodes)
	}

	// 5. Press Backspace on canvas to navigate back to the root graph.
	ed.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if ed.Depth() != 0 || !ed.ActiveComponent().IsZero() {
		t.Fatalf("after Backspace: depth=%d active=%s, want depth=0", ed.Depth(), ed.ActiveComponent())
	}
	if ed.Selection().PrimaryNode() != "pipeline" {
		t.Fatalf("selection after NavigateUp = %q, want \"pipeline\"", ed.Selection().PrimaryNode())
	}
}

func TestDefaultThemeCoversStandardNamespaces(t *testing.T) {
	th := DefaultTheme()
	for _, ns := range []string{
		nodegraph.NamespaceSource,
		nodegraph.NamespaceTransform,
		nodegraph.NamespaceSurface,
		nodegraph.NamespaceWASM,
		nodegraph.NamespaceGPU,
		nodegraph.NamespaceGraph,
		nodegraph.NamespaceAgent,
	} {
		if _, ok := th.NamespaceColors[ns]; !ok {
			t.Errorf("DefaultTheme.NamespaceColors missing %q", ns)
		}
	}
}
