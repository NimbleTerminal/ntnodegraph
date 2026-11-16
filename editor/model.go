// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nimbleterminal.dev/ntpicture"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

const (
	maxUndoHistory           = 128
	inspectorMinCols         = 32
	minCanvasForSplit        = 42
	defaultViewportCol       = 96
	defaultViewportRow       = 28
	maxKittyPlaceholderCells = 297
	requestCellSizeCSI       = "\x1b[16t"
)

// CommitFunc is invoked whenever an editor mutation produces a complete,
// validated Spec ready for a host runtime (such as Nebula's live Engine.Reload).
// Returning a non-nil error rejects and rolls back the edit so the editor and
// host never drift out of sync.
type CommitFunc func(next *nodegraph.Spec, diff nodegraph.SpecDiff) error

// EditCommittedMsg is emitted as a tea.Cmd notification whenever the editor
// commits a spec edit.
type EditCommittedMsg struct {
	Spec       *nodegraph.Spec
	Diff       nodegraph.SpecDiff
	LiveSynced bool
}

// Options configures an editor.Model.
type Options struct {
	BackendProfile      nodegraph.BackendProfile
	Library             *nodegraph.Library
	OnRegisterComponent func(comp *nodegraph.Spec) (nodegraph.TypeDef, error)
	Mode                RenderMode
	Theme               *Theme
	Layout              LayoutOptions
	PictureSession      *ntpicture.TerminalSession
	Commit              CommitFunc
	OnPerfEvent         func(PerfEvent)
	OnWatchChange       func(previewNodes []string, viewerNode string)
	OnSelectionChange   func(sel Selection)
	ShowPerfHUD         bool
	Width               int
	Height              int
	ShowInspector       bool
}

type focusArea uint8

const (
	focusCanvas focusArea = iota
	focusPalette
	focusInspector
	focusQuickConnect
	focusHelp
)

type activeDragKind uint8

const (
	dragNone activeDragKind = iota
	dragPan
	dragZoom
	dragNodes
	dragGroup
	dragWire
	dragMarquee
	dragParamSlider
)

type dragState struct {
	kind                activeDragKind
	startWorld          Point
	curWorld            Point
	startScreenX        float64
	startScreenY        float64
	startPanX           float64
	startPanY           float64
	startZoom           float64
	moved               bool
	rightClickCandidate bool
	rightClickCol       int
	rightClickRow       int
	nodeOrigins         map[string]Point
	baseSelNodes        []string
	groupID             string
	groupOrigin         Point
	groupSize           Point
	baseSnapshot        *nodegraph.Spec
	wire                WireDragPreview
	detachedEdge        *nodegraph.EdgeSpec
	paramNodeID         string
	paramRow            InlineParamRow
	paramUndoPushed     bool
}

type uiButtonSpan struct {
	id       string
	startCol int
	endCol   int
}

type contextMenuAction struct {
	id    string
	label string
}

type contextMenuState struct {
	title     string
	anchorCol int
	anchorRow int
	worldPt   Point
	actions   []contextMenuAction
	hoverIdx  int
}

type vectorRefreshTickMsg struct{}

type navFrame struct {
	spec            *nodegraph.Spec
	committedSpec   *nodegraph.Spec
	undoStack       []*nodegraph.Spec
	redoStack       []*nodegraph.Spec
	cam             Camera
	userMovedCamera bool
	sel             Selection
	paramsExpanded  map[string]bool
	previewExpanded map[string]bool
	instanceID      string
	componentID     nodegraph.TypeID
}

// Model is the reusable Bubble Tea v2 ntnodegraph visual editor component.
type Model struct {
	spec                *nodegraph.Spec
	committedSpec       *nodegraph.Spec
	registry            *nodegraph.Registry
	library             *nodegraph.Library
	onRegisterComponent func(comp *nodegraph.Spec) (nodegraph.TypeDef, error)
	navStack            []navFrame
	activeComponent     nodegraph.TypeID
	profile             nodegraph.BackendProfile
	commitFn            CommitFunc
	theme               Theme
	layoutOpts          LayoutOptions
	mode                RenderMode
	cam                 Camera
	userMovedCamera     bool
	scene               *Scene

	cols, rows    int
	offsetX       int
	offsetY       int
	showInspector bool
	focus         focusArea
	spaceHeld     bool

	pic                 *ntpicture.Picture
	picAutoMode         bool
	kittyGridOverflow   bool
	kittyPrevAutoMode   bool
	lastCanvasCols      int
	lastCanvasRows      int
	vectorDirty         bool
	vectorTickScheduled bool

	onPerfEvent      func(PerfEvent)
	showPerfHUD      bool
	perf             PerfStats
	genSubmittedAt   map[uint64]time.Time
	lastPresentedGen uint64

	sel             Selection
	lastNotifiedSel Selection
	drag            dragState

	// Inline parameter drawers, on-card live previews, and viewer tap state
	paramsExpanded  map[string]bool
	previewExpanded map[string]bool
	nodePreviews    map[string]NodePreview
	viewerNodeID    string
	onWatchChange   func(previewNodes []string, viewerNode string)
	onSelChange     func(sel Selection)

	// Mouse hover, double-click, toolbar, and context-menu state
	hoverHit          HitResult
	hoverText         string
	headerButtons     []uiButtonSpan
	ctxMenu           *contextMenuState
	pendingClickCol   int
	pendingClickRow   int
	pendingPureClick  bool
	lastPureClickAt   time.Time
	lastPureClickCol  int
	lastPureClickRow  int
	pendingSpawnWorld *Point

	undoStack []*nodegraph.Spec
	redoStack []*nodegraph.Spec

	validationErr error
	lastErr       error
	statusMsg     string

	// Palette state
	paletteQuery      string
	paletteCursor     int
	paletteSpliceMode bool
	paletteLineItem   map[int]int

	// Inspector state
	inspectorRow      int
	editingField      bool
	fieldBuffer       string
	fieldTargetKey    string
	inspectorLineItem map[int]int

	// Quick-connect state
	qcCandidates []quickConnectItem
	qcCursor     int
	qcKindIndex  int
	qcLineItem   map[int]int
}

type quickConnectItem struct {
	From   nodegraph.PortRef
	To     nodegraph.PortRef
	OutT   nodegraph.DataType
	InT    nodegraph.DataType
	Coerce nodegraph.CoercionKind
}

// New constructs a visual editor Model for spec and reg.
func New(spec *nodegraph.Spec, reg *nodegraph.Registry, opts Options) *Model {
	if spec == nil {
		spec, _ = nodegraph.NewSpec("untitled")
	} else {
		spec = spec.Canonicalize()
	}
	if reg == nil {
		reg = nodegraph.NewRegistry()
	}
	theme := DefaultTheme()
	if opts.Theme != nil {
		theme = *opts.Theme
	}
	cols := opts.Width
	if cols <= 0 {
		cols = defaultViewportCol
	}
	rows := opts.Height
	if rows <= 0 {
		rows = defaultViewportRow
	}

	pic := ntpicture.New(ntpicture.Options{
		Session:     opts.PictureSession,
		AutoMode:    true,
		StableID:    true,
		RetireDelay: -1,
		Render: ntpicture.Config{
			Transient:   true,
			Fit:         ntpicture.FitFill,
			Background:  theme.CanvasBG,
			KittyFormat: ntpicture.KittyFormatZlib,
		},
	})

	lib := opts.Library
	if lib == nil {
		lib = nodegraph.NewLibrary()
	}

	m := &Model{
		spec:                spec,
		committedSpec:       spec.Clone(),
		registry:            reg,
		library:             lib,
		onRegisterComponent: opts.OnRegisterComponent,
		profile:             opts.BackendProfile,
		commitFn:            opts.Commit,
		onPerfEvent:         opts.OnPerfEvent,
		onWatchChange:       opts.OnWatchChange,
		onSelChange:         opts.OnSelectionChange,
		paramsExpanded:      make(map[string]bool),
		previewExpanded:     make(map[string]bool),
		nodePreviews:        make(map[string]NodePreview),
		showPerfHUD:         opts.ShowPerfHUD,
		genSubmittedAt:      make(map[uint64]time.Time, 16),
		theme:               theme,
		layoutOpts:          opts.Layout.normalized(),
		mode:                opts.Mode,
		cam:                 DefaultCamera(),
		cols:                cols,
		rows:                rows,
		showInspector:       opts.ShowInspector,
		pic:                 pic,
		picAutoMode:         true,
		vectorDirty:         true,
	}

	// Seed UI coordinates if none of the nodes have explicit coordinates yet.
	if len(m.spec.Nodes) > 0 && allNodesMissingUI(m.spec) {
		if laidOut, err := nodegraph.ApplyEdits(m.spec, AutoLayoutOps(m.spec)...); err == nil {
			m.spec = laidOut
			m.committedSpec = laidOut.Clone()
		}
	}

	if ids := m.spec.SortedNodeIDs(); len(ids) > 0 {
		m.sel = Selection{Nodes: []string{ids[0]}}
		m.lastNotifiedSel = cloneSelection(m.sel)
	}
	_, m.validationErr = nodegraph.Validate(m.spec, m.registry, m.validateOpts())
	m.rebuildScene()
	m.fitCameraInitial()
	m.syncPictureSize()
	return m
}

func (m *Model) validateOpts() nodegraph.ValidateOptions {
	if m.profile.Name == "" && m.profile.Compatibility == nil {
		return nodegraph.ValidateOptions{}
	}
	p := m.profile
	if len(p.Namespaces) > 0 && !slices.Contains(p.Namespaces, nodegraph.NamespaceGraph) {
		p.Namespaces = append(append([]string(nil), p.Namespaces...), nodegraph.NamespaceGraph)
	}
	return nodegraph.ValidateOptions{Backend: &p}
}

func allNodesMissingUI(spec *nodegraph.Spec) bool {
	for _, n := range spec.Nodes {
		if n.UI != nil {
			return false
		}
	}
	return true
}

// Init initializes terminal capability & cell-pixel probing for Vector mode.
func (m *Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.pic != nil {
		cmds = append(cmds, m.pic.Init())
	}
	if m.mode == RenderVector {
		cmds = append(cmds, m.refreshVectorPicture())
	}
	return tea.Batch(cmds...)
}

// Close releases any terminal image placements owned by the editor.
func (m *Model) Close() tea.Cmd {
	if m.pic == nil {
		return nil
	}
	return m.pic.Close()
}

// Spec returns a defensive copy of the current editor Spec (including draft edits).
// When drilled into a graph/* component (Depth() > 0), Spec returns that component's Spec;
// call RootSpec to read the top-level root graph Spec regardless of drill depth.
func (m *Model) Spec() *nodegraph.Spec {
	return m.spec.Clone()
}

// RootSpec returns a defensive copy of the top-level root graph Spec even when
// the user is drilled into a nested graph/* component.
func (m *Model) RootSpec() *nodegraph.Spec {
	if len(m.navStack) > 0 {
		return m.navStack[0].spec.Clone()
	}
	return m.spec.Clone()
}

// Library returns the active reusable graph/* component Library.
func (m *Model) Library() *nodegraph.Library {
	return m.library
}

// Depth returns the current subgraph drill-in depth (0 at the root graph).
func (m *Model) Depth() int {
	return len(m.navStack)
}

// ActiveComponent returns the TypeID of the currently drilled-in graph/* component,
// or a zero TypeID when editing at the root graph.
func (m *Model) ActiveComponent() nodegraph.TypeID {
	return m.activeComponent
}

// Breadcrumbs returns the human-readable navigation trail from root graph to the
// active canvas (e.g. ["main", "stage (graph/cinema-stage@1)"]).
func (m *Model) Breadcrumbs() []string {
	if len(m.navStack) == 0 {
		return []string{m.spec.Name}
	}
	crumbs := make([]string, 0, len(m.navStack)+1)
	crumbs = append(crumbs, m.navStack[0].spec.Name)
	for i := 0; i < len(m.navStack); i++ {
		compID := m.activeComponent
		if i+1 < len(m.navStack) {
			compID = m.navStack[i+1].componentID
		}
		inst := m.navStack[i].instanceID
		if inst != "" {
			crumbs = append(crumbs, fmt.Sprintf("%s (%s)", inst, compID))
		} else {
			crumbs = append(crumbs, compID.String())
		}
	}
	return crumbs
}

// IsDragging reports whether a mouse drag gesture (node, group, wire, marquee,
// pan, zoom, or inline card slider) is currently active on the canvas.
func (m *Model) IsDragging() bool {
	return m.drag.kind != dragNone
}

// CommittedSpec returns a defensive copy of the most recent Spec that passed
// full graph validation and host CommitFunc synchronization.
func (m *Model) CommittedSpec() *nodegraph.Spec {
	return m.committedSpec.Clone()
}

// LiveSynced reports whether the current canvas Spec has zero pending required
// inlets/params and matches CommittedSpec.
func (m *Model) LiveSynced() bool {
	return m.validationErr == nil
}

// ValidationError returns any current draft validation diagnostic (such as an
// unconnected required inlet on a newly added node).
func (m *Model) ValidationError() error {
	return m.validationErr
}

// LastError returns the most recent rejected edit or host commit error.
func (m *Model) LastError() error {
	return m.lastErr
}

// StatusMessage returns the latest human-readable status message.
func (m *Model) StatusMessage() string {
	return m.statusMsg
}

// Mode returns the active canvas RenderMode (RenderVector or RenderGrid).
func (m *Model) Mode() RenderMode {
	return m.mode
}

// Picture returns the underlying ntpicture.Picture used in RenderVector mode.
func (m *Model) Picture() *ntpicture.Picture {
	return m.pic
}

// Camera returns the current viewport Camera.
func (m *Model) Camera() Camera {
	return m.cam
}

// Scene returns the latest computed layout Scene.
func (m *Model) Scene() *Scene {
	return m.scene
}

// Registry returns the editor's active node type Registry.
func (m *Model) Registry() *nodegraph.Registry {
	return m.registry
}

// Selection returns a copy of the active Selection.
func (m *Model) Selection() Selection {
	out := Selection{
		Nodes: append([]string(nil), m.sel.Nodes...),
		Group: m.sel.Group,
	}
	if m.sel.Edge != nil {
		cp := *m.sel.Edge
		out.Edge = &cp
	}
	return out
}

// IsNodeParamsExpanded reports whether nodeID's inline parameter drawer is open.
func (m *Model) IsNodeParamsExpanded(nodeID string) bool {
	return m.paramsExpanded != nil && m.paramsExpanded[nodeID]
}

// SetNodeParamsExpanded opens or closes nodeID's inline parameter drawer on the canvas.
func (m *Model) SetNodeParamsExpanded(nodeID string, open bool) tea.Cmd {
	nspec, ok := m.spec.Nodes[nodeID]
	if !ok {
		return nil
	}
	def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, nodeID)
	if len(def.Params) == 0 {
		m.statusMsg = fmt.Sprintf("Node %q has no static parameters", nodeID)
		return nil
	}
	if m.paramsExpanded == nil {
		m.paramsExpanded = make(map[string]bool)
	}
	if open {
		m.paramsExpanded[nodeID] = true
		m.statusMsg = fmt.Sprintf("Expanded parameters on %q", nodeID)
	} else {
		delete(m.paramsExpanded, nodeID)
		m.statusMsg = fmt.Sprintf("Collapsed parameters on %q", nodeID)
	}
	m.rebuildScene()
	m.vectorDirty = true
	return m.refreshVectorIfReady()
}

// ToggleNodeParams toggles nodeID's inline parameter drawer.
func (m *Model) ToggleNodeParams(nodeID string) tea.Cmd {
	return m.SetNodeParamsExpanded(nodeID, !m.IsNodeParamsExpanded(nodeID))
}

// ToggleSelectedParams toggles inline parameter drawers on the currently selected node(s).
func (m *Model) ToggleSelectedParams() tea.Cmd {
	if len(m.sel.Nodes) == 0 {
		m.statusMsg = "Select a node to toggle inline parameters (w)"
		return nil
	}
	allOpen := true
	eligible := 0
	for _, id := range m.sel.Nodes {
		nspec, ok := m.spec.Nodes[id]
		if !ok {
			continue
		}
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Params) > 0 {
			eligible++
			if !m.paramsExpanded[id] {
				allOpen = false
			}
		}
	}
	if eligible == 0 {
		m.statusMsg = "Selected node(s) have no static parameters"
		return nil
	}
	target := !allOpen
	for _, id := range m.sel.Nodes {
		nspec, ok := m.spec.Nodes[id]
		if !ok {
			continue
		}
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Params) == 0 {
			continue
		}
		if target {
			m.paramsExpanded[id] = true
		} else {
			delete(m.paramsExpanded, id)
		}
	}
	if target {
		m.statusMsg = fmt.Sprintf("Expanded inline parameters on %d node(s)", eligible)
	} else {
		m.statusMsg = fmt.Sprintf("Collapsed inline parameters on %d node(s)", eligible)
	}
	m.rebuildScene()
	m.vectorDirty = true
	return m.refreshVectorIfReady()
}

// ToggleAllParams expands or collapses inline parameter drawers across all nodes with parameters.
func (m *Model) ToggleAllParams() tea.Cmd {
	ids := m.spec.SortedNodeIDs()
	allOpen := true
	eligible := 0
	for _, id := range ids {
		nspec := m.spec.Nodes[id]
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Params) > 0 {
			eligible++
			if !m.paramsExpanded[id] {
				allOpen = false
			}
		}
	}
	if eligible == 0 {
		return nil
	}
	target := !allOpen
	for _, id := range ids {
		nspec := m.spec.Nodes[id]
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Params) == 0 {
			continue
		}
		if target {
			m.paramsExpanded[id] = true
		} else {
			delete(m.paramsExpanded, id)
		}
	}
	if target {
		m.statusMsg = fmt.Sprintf("Expanded inline parameters on all %d node(s)", eligible)
	} else {
		m.statusMsg = "Collapsed all inline parameter drawers"
	}
	m.rebuildScene()
	m.vectorDirty = true
	return m.refreshVectorIfReady()
}

// IsNodePreviewExpanded reports whether nodeID's on-card live output preview drawer is open.
func (m *Model) IsNodePreviewExpanded(nodeID string) bool {
	return m.previewExpanded != nil && m.previewExpanded[nodeID]
}

// PreviewExpandedNodes returns a sorted list of node IDs whose on-card live preview drawers are open.
func (m *Model) PreviewExpandedNodes() []string {
	if len(m.previewExpanded) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.previewExpanded))
	for id, open := range m.previewExpanded {
		if open {
			if _, exists := m.spec.Nodes[id]; exists {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// SetNodePreviewExpanded opens or closes nodeID's on-card live output preview drawer.
func (m *Model) SetNodePreviewExpanded(nodeID string, open bool) tea.Cmd {
	nspec, ok := m.spec.Nodes[nodeID]
	if !ok {
		return nil
	}
	def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, nodeID)
	if len(def.Outlets) == 0 {
		m.statusMsg = fmt.Sprintf("Node %q has no outlets to preview", nodeID)
		return nil
	}
	if m.previewExpanded == nil {
		m.previewExpanded = make(map[string]bool)
	}
	if open {
		m.previewExpanded[nodeID] = true
		m.statusMsg = fmt.Sprintf("Opened live preview on %q", nodeID)
	} else {
		delete(m.previewExpanded, nodeID)
		m.statusMsg = fmt.Sprintf("Closed live preview on %q", nodeID)
	}
	m.rebuildScene()
	m.vectorDirty = true
	m.notifyWatchChange()
	return m.refreshVectorIfReady()
}

// ToggleNodePreview toggles nodeID's on-card live output preview drawer.
func (m *Model) ToggleNodePreview(nodeID string) tea.Cmd {
	return m.SetNodePreviewExpanded(nodeID, !m.IsNodePreviewExpanded(nodeID))
}

// ToggleSelectedPreview toggles on-card live output previews on the currently selected node(s).
func (m *Model) ToggleSelectedPreview() tea.Cmd {
	if len(m.sel.Nodes) == 0 {
		m.statusMsg = "Select a node to toggle card preview (o)"
		return nil
	}
	allOpen := true
	eligible := 0
	for _, id := range m.sel.Nodes {
		nspec, ok := m.spec.Nodes[id]
		if !ok {
			continue
		}
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Outlets) > 0 {
			eligible++
			if !m.previewExpanded[id] {
				allOpen = false
			}
		}
	}
	if eligible == 0 {
		m.statusMsg = "Selected node(s) have no outlets to preview"
		return nil
	}
	target := !allOpen
	for _, id := range m.sel.Nodes {
		nspec, ok := m.spec.Nodes[id]
		if !ok {
			continue
		}
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Outlets) == 0 {
			continue
		}
		if target {
			m.previewExpanded[id] = true
		} else {
			delete(m.previewExpanded, id)
		}
	}
	if target {
		m.statusMsg = fmt.Sprintf("Opened live preview on %d node(s)", eligible)
	} else {
		m.statusMsg = fmt.Sprintf("Closed live preview on %d node(s)", eligible)
	}
	m.rebuildScene()
	m.vectorDirty = true
	m.notifyWatchChange()
	return m.refreshVectorIfReady()
}

// ToggleAllPreviews expands or collapses on-card live previews across all nodes with outlets.
func (m *Model) ToggleAllPreviews() tea.Cmd {
	ids := m.spec.SortedNodeIDs()
	allOpen := true
	eligible := 0
	for _, id := range ids {
		nspec := m.spec.Nodes[id]
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Outlets) > 0 {
			eligible++
			if !m.previewExpanded[id] {
				allOpen = false
			}
		}
	}
	if eligible == 0 {
		return nil
	}
	target := !allOpen
	for _, id := range ids {
		nspec := m.spec.Nodes[id]
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
		if len(def.Outlets) == 0 {
			continue
		}
		if target {
			m.previewExpanded[id] = true
		} else {
			delete(m.previewExpanded, id)
		}
	}
	if target {
		m.statusMsg = fmt.Sprintf("Opened live preview on all %d node(s)", eligible)
	} else {
		m.statusMsg = "Closed all on-card previews"
	}
	m.rebuildScene()
	m.vectorDirty = true
	m.notifyWatchChange()
	return m.refreshVectorIfReady()
}

// SetNodePreview updates the live host-supplied preview payload for nodeID.
func (m *Model) SetNodePreview(nodeID string, preview NodePreview) tea.Cmd {
	if _, ok := m.spec.Nodes[nodeID]; !ok {
		return nil
	}
	if m.nodePreviews == nil {
		m.nodePreviews = make(map[string]NodePreview)
	}
	cp := preview
	if len(preview.Lines) > 0 {
		cp.Lines = slices.Clone(preview.Lines)
	}
	m.nodePreviews[nodeID] = cp
	if !m.previewExpanded[nodeID] {
		return nil
	}
	m.rebuildScene()
	m.vectorDirty = true
	return m.refreshVectorIfReady()
}

// NodePreview returns any cached live preview payload for nodeID.
func (m *Model) NodePreview(nodeID string) (NodePreview, bool) {
	if m.nodePreviews == nil {
		return NodePreview{}, false
	}
	pv, ok := m.nodePreviews[nodeID]
	return pv, ok
}

// ClearNodePreviews drops all cached node preview payloads.
func (m *Model) ClearNodePreviews() {
	clear(m.nodePreviews)
	if len(m.previewExpanded) > 0 {
		m.rebuildScene()
		m.vectorDirty = true
	}
}

// ViewerNode returns the node ID currently tapped to the host's primary Viewer (if any).
func (m *Model) ViewerNode() string {
	return m.viewerNodeID
}

// SetViewerNode sets or clears (when nodeID == "") the node tapped to the host Viewer.
func (m *Model) SetViewerNode(nodeID string) tea.Cmd {
	if nodeID != "" {
		nspec, ok := m.spec.Nodes[nodeID]
		if !ok {
			return nil
		}
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, nodeID)
		if len(def.Outlets) == 0 {
			m.statusMsg = fmt.Sprintf("Node %q has no outlets to tap", nodeID)
			return nil
		}
	}
	if m.viewerNodeID == nodeID {
		return nil
	}
	m.viewerNodeID = nodeID
	if nodeID == "" {
		m.statusMsg = "Cleared Viewer outlet tap (showing final surface)"
	} else {
		m.statusMsg = fmt.Sprintf("Tapped %q outlet to Viewer", nodeID)
	}
	m.rebuildScene()
	m.vectorDirty = true
	m.notifyWatchChange()
	return m.refreshVectorIfReady()
}

// ToggleViewerNode toggles tapping nodeID into the host Viewer.
func (m *Model) ToggleViewerNode(nodeID string) tea.Cmd {
	if m.viewerNodeID == nodeID {
		return m.SetViewerNode("")
	}
	return m.SetViewerNode(nodeID)
}

func (m *Model) notifyWatchChange() {
	if m.onWatchChange != nil {
		m.onWatchChange(m.PreviewExpandedNodes(), m.viewerNodeID)
	}
}

func (m *Model) refreshVectorIfReady() tea.Cmd {
	if m.mode != RenderVector || m.pic == nil {
		return nil
	}
	if m.pic.Status().Busy && m.lastPresentedGen > 0 {
		return nil
	}
	return m.refreshVectorPicture()
}

// SetScreenOffset informs the editor of its top-left (col, row) origin inside a
// larger parent layout so mouse hit-testing aligns accurately.
func (m *Model) SetScreenOffset(col, row int) {
	m.offsetX = col
	m.offsetY = row
}

// SetSize resizes the editor viewport in terminal cells.
// If the user has not manually panned or zoomed the camera yet, SetSize also
// refits the initial camera framing to the newly reported terminal dimensions.
func (m *Model) SetSize(cols, rows int) tea.Cmd {
	cols = max(4, cols)
	rows = max(3, rows)
	if cols == m.cols && rows == m.rows {
		if m.mode == RenderVector && m.pic != nil && !m.pic.Visible() {
			m.vectorDirty = true
			return tea.Batch(m.syncPictureSize(), m.refreshVectorIfReady())
		}
		return nil
	}
	m.cols = cols
	m.rows = rows
	m.lastPureClickAt = time.Time{}
	if !m.userMovedCamera {
		m.fitCameraInitial()
	}
	m.vectorDirty = true
	cmd := m.syncPictureSize()
	if m.mode == RenderVector {
		return tea.Batch(cmd, m.refreshVectorIfReady())
	}
	return cmd
}

func (m *Model) bodyRows() int {
	return max(1, m.rows-2)
}

// SetMode switches between RenderVector and RenderGrid.
func (m *Model) SetMode(mode RenderMode) tea.Cmd {
	if m.mode == mode {
		return nil
	}
	m.mode = mode
	m.statusMsg = fmt.Sprintf("Canvas mode: %s", mode)
	if !m.userMovedCamera {
		m.fitCameraInitial()
	}
	if mode == RenderVector {
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorIfReady())
	}
	if m.pic != nil {
		return m.pic.SetVisible(false)
	}
	return nil
}

// ToggleMode toggles between RenderVector and RenderGrid.
func (m *Model) ToggleMode() tea.Cmd {
	if m.mode == RenderVector {
		return m.SetMode(RenderGrid)
	}
	return m.SetMode(RenderVector)
}

// ToggleKittyMode toggles ntpicture between Kitty graphics and half-block glyph
// rendering while in RenderVector mode.
func (m *Model) ToggleKittyMode() tea.Cmd {
	if m.pic == nil || m.mode != RenderVector {
		return nil
	}
	m.picAutoMode = false
	m.kittyGridOverflow = false
	cmd := m.pic.ToggleMode()
	szCmd := m.syncPictureSize()
	m.vectorDirty = true
	m.statusMsg = fmt.Sprintf("Vector backend: %v", m.pic.Mode())
	return tea.Batch(cmd, szCmd, m.refreshVectorIfReady())
}

// SetSpec replaces the editor's spec externally (e.g., on disk reload).
func (m *Model) SetSpec(next *nodegraph.Spec) tea.Cmd {
	if next == nil {
		return nil
	}
	m.spec = next.Canonicalize()
	_, m.validationErr = nodegraph.Validate(m.spec, m.registry, m.validateOpts())
	if m.validationErr == nil {
		m.committedSpec = m.spec.Clone()
	}
	m.sanitizeSelection()
	m.rebuildScene()
	m.vectorDirty = true
	if m.mode == RenderVector {
		return m.refreshVectorPicture()
	}
	return nil
}

func selectionsEqual(a, b Selection) bool {
	if a.Group != b.Group || !slices.Equal(a.Nodes, b.Nodes) {
		return false
	}
	if (a.Edge == nil) != (b.Edge == nil) {
		return false
	}
	if a.Edge != nil && *a.Edge != *b.Edge {
		return false
	}
	return true
}

func cloneSelection(s Selection) Selection {
	out := Selection{
		Nodes: append([]string(nil), s.Nodes...),
		Group: s.Group,
	}
	if s.Edge != nil {
		cp := *s.Edge
		out.Edge = &cp
	}
	return out
}

func (m *Model) maybeNotifySelectionChange() {
	if selectionsEqual(m.sel, m.lastNotifiedSel) {
		return
	}
	m.lastNotifiedSel = cloneSelection(m.sel)
	if m.onSelChange != nil {
		m.onSelChange(cloneSelection(m.sel))
	}
}

// SelectNode selects nodeID on the canvas.
func (m *Model) SelectNode(nodeID string) {
	if _, ok := m.spec.Nodes[nodeID]; !ok {
		return
	}
	m.sel = Selection{Nodes: []string{nodeID}}
	m.inspectorRow = 0
	m.editingField = false
	m.rebuildScene()
	m.vectorDirty = true
	m.maybeNotifySelectionChange()
}

// SelectEdge selects edge on the canvas.
func (m *Model) SelectEdge(edge nodegraph.EdgeSpec) {
	for _, existing := range m.spec.Edges {
		if existing == edge {
			cp := existing
			m.sel = Selection{Edge: &cp}
			m.inspectorRow = 0
			m.editingField = false
			m.rebuildScene()
			m.vectorDirty = true
			m.maybeNotifySelectionChange()
			return
		}
	}
}

// SelectGroup selects groupID on the canvas.
func (m *Model) SelectGroup(groupID string) {
	if _, ok := m.spec.Groups[groupID]; !ok {
		return
	}
	m.sel = Selection{Group: groupID}
	m.inspectorRow = 0
	m.editingField = false
	m.rebuildScene()
	m.vectorDirty = true
	m.maybeNotifySelectionChange()
}

// ApplyEdits applies declarative EditOps to the current editor spec, enforcing
// static compatibility/cycle/parameter invariants, recording undo history, and
// invoking Options.Commit when the resulting graph is complete.
func (m *Model) ApplyEdits(ops ...nodegraph.EditOp) (tea.Cmd, error) {
	return m.ApplyEditsWithUndo(true, ops...)
}

// ApplyEditsWithUndo applies declarative EditOps to the current editor spec,
// recording an undo snapshot only when recordUndo is true (allowing live
// slider scrubbing to coalesce into a single undo entry per drag gesture).
func (m *Model) ApplyEditsWithUndo(recordUndo bool, ops ...nodegraph.EditOp) (tea.Cmd, error) {
	candidate, err := nodegraph.ApplyEdits(m.spec, ops...)
	if err != nil {
		m.lastErr = err
		m.statusMsg = err.Error()
		return nil, err
	}
	return m.commitCandidate(candidate, recordUndo)
}

// ReplaceSpec transactionally commits next into the editor (validating, pushing
// the previous spec onto the undo stack, invoking Options.Commit, and refreshing
// the canvas), allowing external co-editors such as an attached MCP server to
// update the live canvas cleanly.
func (m *Model) ReplaceSpec(next *nodegraph.Spec) (tea.Cmd, error) {
	if next == nil {
		return nil, fmt.Errorf("%w: nil spec", nodegraph.ErrInvalidSpec)
	}
	return m.commitCandidate(next.Clone().Canonicalize(), true)
}

// ReplaceRootSpec transactionally replaces the root graph Spec (returning to
// the root graph first if the user was drilled into a graph/* component).
func (m *Model) ReplaceRootSpec(next *nodegraph.Spec) (tea.Cmd, error) {
	if next == nil {
		return nil, fmt.Errorf("%w: nil spec", nodegraph.ErrInvalidSpec)
	}
	navCmd := m.NavigateToRoot()
	cmd, err := m.commitCandidate(next.Clone().Canonicalize(), true)
	if err != nil {
		return navCmd, err
	}
	return tea.Batch(navCmd, cmd), nil
}

func (m *Model) registerComponentSpec(comp *nodegraph.Spec) (nodegraph.TypeDef, error) {
	if comp == nil {
		return nodegraph.TypeDef{}, fmt.Errorf("%w: nil component spec", nodegraph.ErrInvalidSpec)
	}
	canon := comp.Canonicalize()
	if m.onRegisterComponent != nil {
		def, err := m.onRegisterComponent(canon)
		if err != nil {
			return nodegraph.TypeDef{}, err
		}
		_ = m.registry.Upsert(def)
		return def, nil
	}
	if m.library == nil {
		m.library = nodegraph.NewLibrary()
	}
	def, err := nodegraph.SynthesizeTypeDef(canon, m.registry, m.validateOpts())
	if err != nil {
		return nodegraph.TypeDef{}, err
	}
	if _, err := m.library.Put(canon, "editor"); err != nil {
		return nodegraph.TypeDef{}, err
	}
	if err := m.registry.Upsert(def); err != nil {
		return nodegraph.TypeDef{}, err
	}
	return def, nil
}

func (m *Model) commitCandidate(candidate *nodegraph.Spec, recordUndo bool) (tea.Cmd, error) {
	if !m.activeComponent.IsZero() && candidate != nil {
		candidate = candidate.Clone()
		candidate.Name = m.activeComponent.Name
		candidate.ComponentVersion = m.activeComponent.Major
		candidate = candidate.Canonicalize()
	}

	_, valErr := nodegraph.Validate(candidate, m.registry, m.validateOpts())
	if valErr != nil && !isPermissibleDraftError(valErr) {
		m.lastErr = valErr
		m.statusMsg = firstLine(valErr.Error())
		return nil, valErr
	}

	var diff nodegraph.SpecDiff
	if valErr == nil {
		diff = nodegraph.Diff(m.committedSpec, candidate)
		if !m.activeComponent.IsZero() {
			if _, regErr := m.registerComponentSpec(candidate.Clone()); regErr != nil {
				m.lastErr = regErr
				m.statusMsg = "Component rejected: " + firstLine(regErr.Error())
				return nil, regErr
			}
		} else if m.commitFn != nil {
			if err := m.commitFn(candidate.Clone(), diff); err != nil {
				m.lastErr = err
				m.statusMsg = "Host rejected edit: " + firstLine(err.Error())
				return nil, err
			}
		}
	} else {
		diff = nodegraph.Diff(m.spec, candidate)
	}

	if recordUndo {
		m.pushUndo(m.spec)
		m.redoStack = nil
	}

	m.spec = candidate
	m.validationErr = valErr
	m.lastErr = nil
	if valErr == nil {
		m.committedSpec = candidate.Clone()
		m.statusMsg = summarizeDiff(diff)
	} else {
		m.statusMsg = "Draft: " + firstLine(valErr.Error())
	}

	m.sanitizeSelection()
	m.rebuildScene()
	m.vectorDirty = true

	notifySpec := m.spec.Clone()
	synced := valErr == nil
	notifyCmd := func() tea.Msg {
		return EditCommittedMsg{
			Spec:       notifySpec,
			Diff:       diff,
			LiveSynced: synced,
		}
	}
	if m.mode == RenderVector {
		return tea.Batch(notifyCmd, m.refreshVectorPicture()), nil
	}
	return notifyCmd, nil
}

func isPermissibleDraftError(err error) bool {
	var verr *nodegraph.ValidationError
	if !errors.As(err, &verr) || len(verr.Issues) == 0 {
		return false
	}
	for _, iss := range verr.Issues {
		switch iss.Code {
		case nodegraph.IssueMissingRequiredInlet:
			// Allowed while wiring a newly added or disconnected node.
		default:
			return false
		}
	}
	return true
}

// Undo reverts the previous spec edit and syncs with the host if valid.
func (m *Model) Undo() (tea.Cmd, error) {
	if len(m.undoStack) == 0 {
		m.statusMsg = "Nothing to undo"
		return nil, nil
	}
	prev := m.undoStack[len(m.undoStack)-1]
	currentSnap := m.spec.Clone()
	m.undoStack = m.undoStack[:len(m.undoStack)-1]
	cmd, err := m.commitCandidate(prev.Clone(), false)
	if err != nil {
		m.undoStack = append(m.undoStack, prev)
		return nil, err
	}
	m.redoStack = append(m.redoStack, currentSnap)
	m.statusMsg = "Undid edit"
	return cmd, nil
}

// Redo re-applies the most recently undone spec edit.
func (m *Model) Redo() (tea.Cmd, error) {
	if len(m.redoStack) == 0 {
		m.statusMsg = "Nothing to redo"
		return nil, nil
	}
	next := m.redoStack[len(m.redoStack)-1]
	currentSnap := m.spec.Clone()
	m.redoStack = m.redoStack[:len(m.redoStack)-1]
	cmd, err := m.commitCandidate(next.Clone(), false)
	if err != nil {
		m.redoStack = append(m.redoStack, next)
		return nil, err
	}
	m.pushUndo(currentSnap)
	m.statusMsg = "Redid edit"
	return cmd, nil
}

// LayoutOptions returns the active automatic graph arrangement parameters.
func (m *Model) LayoutOptions() LayoutOptions {
	return m.layoutOpts
}

// SetLayoutOptions updates the automatic graph arrangement parameters used by
// AutoLayout and fallback positioning for nodes without explicit UI coordinates.
func (m *Model) SetLayoutOptions(opts LayoutOptions) {
	m.layoutOpts = opts.normalized()
	m.rebuildScene()
	m.vectorDirty = true
}

// AutoLayout arranges all nodes using the configured LayoutOptions (defaulting
// to Sugiyama/Kahn left-to-right columns with card-height-aware spacing and
// port-aware barycentric wire crossing reduction).
func (m *Model) AutoLayout() (tea.Cmd, error) {
	ops := AutoLayoutOpsWithScene(m.spec, m.registry, m.currentSceneOptions())
	if len(ops) == 0 {
		return nil, nil
	}
	cmd, err := m.ApplyEdits(ops...)
	if err != nil {
		return nil, err
	}
	m.FitToView()
	if m.mode == RenderVector {
		return tea.Batch(cmd, m.refreshVectorIfReady()), nil
	}
	return cmd, nil
}

// FitToView frames all nodes and groups inside the current canvas viewport.
func (m *Model) FitToView() {
	m.userMovedCamera = false
	m.lastPureClickAt = time.Time{}
	m.fitCameraInitial()
	m.vectorDirty = true
}

func (m *Model) viewportCenterScreen() (sx, sy float64) {
	canvasCols, _ := m.splitColumns()
	if canvasCols <= 0 {
		canvasCols = m.cols
	}
	bodyRows := m.bodyRows()
	if m.mode == RenderGrid {
		return float64(max(1, canvasCols)) * GridCellWorldW * 0.5, float64(max(1, bodyRows)) * GridCellWorldH * 0.5
	}
	pxW, pxH := m.canvasVectorPixelSize(canvasCols, bodyRows)
	return float64(pxW) * 0.5, float64(pxH) * 0.5
}

// ZoomBy multiplies the camera zoom around the center of the active canvas
// viewport and schedules a canvas refresh.
func (m *Model) ZoomBy(factor float64) tea.Cmd {
	m.userMovedCamera = true
	m.lastPureClickAt = time.Time{}
	cx, cy := m.viewportCenterScreen()
	m.cam.ZoomAt(factor, cx, cy)
	m.vectorDirty = true
	if m.mode == RenderVector {
		return m.refreshVectorIfReady()
	}
	return nil
}

// SetZoom sets an exact camera zoom level (clamped to [MinZoom, MaxZoom]),
// centering on the primary selected node/group when one is selected, or keeping
// the current viewport center anchored otherwise.
func (m *Model) SetZoom(targetZoom float64) tea.Cmd {
	m.userMovedCamera = true
	m.lastPureClickAt = time.Time{}
	z := min(MaxZoom, max(MinZoom, targetZoom))
	anchorWorld := m.viewportCenterWorld()
	if m.scene != nil {
		if id := m.sel.PrimaryNode(); id != "" {
			if idx, ok := m.scene.NodeByID[id]; ok {
				b := m.scene.Nodes[idx].Bounds
				anchorWorld = Point{X: b.X + b.W*0.5, Y: b.Y + b.H*0.5}
			}
		} else if m.sel.Group != "" {
			for _, gb := range m.scene.Groups {
				if gb.ID == m.sel.Group {
					anchorWorld = Point{X: gb.Bounds.X + gb.Bounds.W*0.5, Y: gb.Bounds.Y + gb.Bounds.H*0.5}
					break
				}
			}
		}
	}
	cx, cy := m.viewportCenterScreen()
	m.cam.Zoom = z
	m.cam.PanX = anchorWorld.X - cx/z
	m.cam.PanY = anchorWorld.Y - cy/z
	m.vectorDirty = true
	if m.mode == RenderVector {
		return m.refreshVectorIfReady()
	}
	return nil
}

// FocusSelection frames the currently selected node(s) or group snugly inside
// the canvas viewport (or fits the entire graph if nothing is selected).
func (m *Model) FocusSelection() tea.Cmd {
	if m.scene == nil {
		m.FitToView()
		if m.mode == RenderVector {
			return m.refreshVectorIfReady()
		}
		return nil
	}
	var bounds Rect
	if len(m.sel.Nodes) > 0 {
		for _, id := range m.sel.Nodes {
			if idx, ok := m.scene.NodeByID[id]; ok {
				bounds = bounds.Union(m.scene.Nodes[idx].Bounds)
			}
		}
	} else if m.sel.Group != "" {
		for _, gb := range m.scene.Groups {
			if gb.ID == m.sel.Group {
				bounds = gb.Bounds
				break
			}
		}
	}
	if bounds.W <= 0 || bounds.H <= 0 {
		m.FitToView()
		if m.mode == RenderVector {
			return m.refreshVectorIfReady()
		}
		return nil
	}
	m.userMovedCamera = true
	m.lastPureClickAt = time.Time{}
	canvasCols, _ := m.splitColumns()
	if canvasCols <= 0 {
		canvasCols = m.cols
	}
	bodyRows := m.bodyRows()
	pxW, pxH := m.canvasEffectiveSize(canvasCols, bodyRows)
	m.cam.FitBounds(bounds.Expand(32, 24), pxW, pxH)
	if m.cam.Zoom > 1.35 {
		m.cam.Zoom = 1.35
		cx, cy := m.viewportCenterScreen()
		centerX := bounds.X + bounds.W*0.5
		centerY := bounds.Y + bounds.H*0.5
		m.cam.PanX = centerX - cx/m.cam.Zoom
		m.cam.PanY = centerY - cy/m.cam.Zoom
	}
	m.vectorDirty = true
	if m.mode == RenderVector {
		return m.refreshVectorIfReady()
	}
	return nil
}

// AddNodeFromType instantiates a node of typeID at the center of the current
// viewport, generating a deterministic unique ID and default static parameters.
func (m *Model) AddNodeFromType(typeID nodegraph.TypeID) (string, tea.Cmd, error) {
	center := m.viewportCenterWorld()
	return m.AddNodeFromTypeAt(typeID, center.X-DefaultNodeWidth*0.5, center.Y-50)
}

// AddNodeFromTypeAt instantiates a node of typeID at explicit world coordinates (x, y).
func (m *Model) AddNodeFromTypeAt(typeID nodegraph.TypeID, x, y float64) (string, tea.Cmd, error) {
	def, ok := m.registry.Lookup(typeID)
	if !ok {
		err := fmt.Errorf("%w: %s", nodegraph.ErrUnknownNodeType, typeID)
		m.lastErr = err
		return "", nil, err
	}
	newID := uniqueNodeID(m.spec, typeID.Name)
	nspec := buildDefaultNodeSpec(def, x, y)
	cmd, err := m.ApplyEdits(nodegraph.AddNode(newID, nspec))
	if err != nil {
		return "", nil, err
	}
	m.SelectNode(newID)
	m.statusMsg = fmt.Sprintf("Added node %q (%s)", newID, typeID)
	if m.mode == RenderVector {
		cmd = tea.Batch(cmd, m.refreshVectorPicture())
	}
	return newID, cmd, nil
}

// DuplicateSelectedNodes clones the currently selected node(s) offset by (+36, +36)
// world units, preserving their configured parameters and capabilities.
func (m *Model) DuplicateSelectedNodes() ([]string, tea.Cmd, error) {
	if len(m.sel.Nodes) == 0 {
		return nil, nil, nil
	}
	var (
		ops    []nodegraph.EditOp
		newIDs []string
		used   = make(map[string]bool, len(m.spec.Nodes)+len(m.sel.Nodes))
	)
	for id := range m.spec.Nodes {
		used[id] = true
	}
	srcIDs := append([]string(nil), m.sel.Nodes...)
	slices.Sort(srcIDs)
	for _, srcID := range srcIDs {
		orig, ok := m.spec.Nodes[srcID]
		if !ok {
			continue
		}
		cand := srcID + "_copy"
		for n := 2; used[cand]; n++ {
			cand = fmt.Sprintf("%s_copy_%d", srcID, n)
		}
		used[cand] = true
		cp := nodegraph.NodeSpec{
			Type:         orig.Type,
			Capabilities: append([]nodegraph.Capability(nil), orig.Capabilities...),
		}
		if len(orig.Params) > 0 {
			cp.Params = make(map[string]any, len(orig.Params))
			for k, v := range orig.Params {
				cp.Params[k] = v
			}
		}
		x, y := 80.0, 80.0
		var label string
		collapsed := false
		if orig.UI != nil {
			x, y = orig.UI.X+36, orig.UI.Y+36
			label = orig.UI.Label
			collapsed = orig.UI.Collapsed
		} else if idx, hasBox := m.scene.NodeByID[srcID]; hasBox {
			x = m.scene.Nodes[idx].Bounds.X + 36
			y = m.scene.Nodes[idx].Bounds.Y + 36
		}
		cp.UI = &nodegraph.NodeUI{
			X:         math.Round(x),
			Y:         math.Round(y),
			Label:     label,
			Collapsed: collapsed,
		}
		ops = append(ops, nodegraph.AddNode(cand, cp))
		newIDs = append(newIDs, cand)
	}
	if len(ops) == 0 {
		return nil, nil, nil
	}
	cmd, err := m.ApplyEdits(ops...)
	if err != nil {
		return nil, nil, err
	}
	m.sel = Selection{Nodes: newIDs}
	m.rebuildScene()
	m.vectorDirty = true
	m.statusMsg = fmt.Sprintf("Duplicated %d node(s)", len(newIDs))
	if m.mode == RenderVector {
		cmd = tea.Batch(cmd, m.refreshVectorPicture())
	}
	return newIDs, cmd, nil
}

// SpliceNodeOnSelectedEdge inserts a new node of typeID directly into the
// currently selected edge when typeID has a compatible inlet and outlet.
func (m *Model) SpliceNodeOnSelectedEdge(typeID nodegraph.TypeID) (string, tea.Cmd, error) {
	if m.sel.Edge == nil {
		return m.AddNodeFromType(typeID)
	}
	edge := *m.sel.Edge
	def, ok := m.registry.Lookup(typeID)
	if !ok {
		return "", nil, fmt.Errorf("%w: %s", nodegraph.ErrUnknownNodeType, typeID)
	}
	srcAnchor, okSrc := m.scene.findPort(edge.From.Node, edge.From.Port, true)
	dstAnchor, okDst := m.scene.findPort(edge.To.Node, edge.To.Port, false)
	if !okSrc || !okDst {
		return m.AddNodeFromType(typeID)
	}

	inPort := ""
	for _, in := range def.Inlets {
		if c, _, err := m.profile.CheckCompatibility(srcAnchor.Type, in.Type); err == nil && c != nodegraph.CompatIncompatible {
			inPort = in.Name
			break
		}
	}
	outPort := ""
	for _, out := range def.Outlets {
		if c, _, err := m.profile.CheckCompatibility(out.Type, dstAnchor.Type); err == nil && c != nodegraph.CompatIncompatible {
			outPort = out.Name
			break
		}
	}
	if inPort == "" || outPort == "" {
		err := fmt.Errorf("%w: %s has no compatible ports to splice into %s -> %s", nodegraph.ErrTypeMismatch, typeID, edge.From, edge.To)
		m.lastErr = err
		m.statusMsg = err.Error()
		return "", nil, err
	}

	newID := uniqueNodeID(m.spec, typeID.Name)
	midX := (srcAnchor.Center.X+dstAnchor.Center.X)*0.5 - DefaultNodeWidth*0.5
	midY := (srcAnchor.Center.Y+dstAnchor.Center.Y)*0.5 - 48
	nspec := buildDefaultNodeSpec(def, midX, midY)

	cmd, err := m.ApplyEdits(
		nodegraph.AddNode(newID, nspec),
		nodegraph.Disconnect(edge.From, edge.To, edge.Kind),
		nodegraph.Connect(edge.From, nodegraph.PortRef{Node: newID, Port: inPort}, ""),
		nodegraph.Connect(nodegraph.PortRef{Node: newID, Port: outPort}, edge.To, edge.Kind),
	)
	if err != nil {
		return "", nil, err
	}
	m.SelectNode(newID)
	m.statusMsg = fmt.Sprintf("Spliced %q into %s -> %s", newID, edge.From, edge.To)
	if m.mode == RenderVector {
		cmd = tea.Batch(cmd, m.refreshVectorPicture())
	}
	return newID, cmd, nil
}

// GroupSelectedNodes wraps the currently selected nodes in a visual GroupSpec.
func (m *Model) GroupSelectedNodes(label string) (string, tea.Cmd, error) {
	if len(m.sel.Nodes) == 0 {
		return "", nil, nil
	}
	gid := uniqueGroupID(m.spec, "group")
	if label == "" {
		label = "Group " + strings.TrimPrefix(gid, "group_")
		if gid == "group" {
			label = "Group 1"
		}
	}
	members := append([]string(nil), m.sel.Nodes...)
	slices.Sort(members)
	cmd, err := m.ApplyEdits(nodegraph.PutGroup(gid, nodegraph.GroupSpec{
		Label: label,
		Nodes: members,
	}))
	if err != nil {
		return "", nil, err
	}
	m.SelectGroup(gid)
	m.statusMsg = fmt.Sprintf("Created group %q (%d nodes)", label, len(members))
	if m.mode == RenderVector {
		cmd = tea.Batch(cmd, m.refreshVectorPicture())
	}
	return gid, cmd, nil
}

// CanDrillIntoNode reports whether nodeID is a graph/* subgraph instance whose
// component definition is available in the editor's Library.
func (m *Model) CanDrillIntoNode(nodeID string) bool {
	if m.library == nil || m.spec == nil {
		return false
	}
	nspec, ok := m.spec.Nodes[nodeID]
	if !ok || nspec.Type.Namespace != nodegraph.NamespaceGraph {
		return false
	}
	_, ok = m.library.LookupSpec(nspec.Type)
	return ok
}

// DrillIntoSelectedSubgraph drills into the currently selected graph/* node's
// component definition, pushing the parent canvas onto the breadcrumb stack.
func (m *Model) DrillIntoSelectedSubgraph() (tea.Cmd, error) {
	id := m.sel.PrimaryNode()
	if id == "" {
		err := fmt.Errorf("%w: no graph/* node selected", nodegraph.ErrUnknownNode)
		m.lastErr = err
		return nil, err
	}
	return m.DrillIntoSubgraph(id)
}

// DrillIntoSubgraph opens nodeID's graph/* component Spec on the canvas and
// pushes the current canvas state onto the breadcrumb navigation stack.
func (m *Model) DrillIntoSubgraph(nodeID string) (tea.Cmd, error) {
	nspec, ok := m.spec.Nodes[nodeID]
	if !ok {
		err := fmt.Errorf("%w: %q", nodegraph.ErrUnknownNode, nodeID)
		m.lastErr = err
		return nil, err
	}
	if nspec.Type.Namespace != nodegraph.NamespaceGraph {
		err := fmt.Errorf("%w: node %q (%s) is a primitive node, not a graph/* subgraph", nodegraph.ErrInvalidSpec, nodeID, nspec.Type)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nil, err
	}
	if len(m.navStack) >= nodegraph.DefaultMaxSubgraphDepth {
		err := fmt.Errorf("%w: max depth %d", nodegraph.ErrSubgraphDepthExceeded, nodegraph.DefaultMaxSubgraphDepth)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nil, err
	}
	if nspec.Type == m.activeComponent {
		err := fmt.Errorf("%w: %s", nodegraph.ErrSubgraphCycle, nspec.Type)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nil, err
	}
	for _, fr := range m.navStack {
		if !fr.componentID.IsZero() && fr.componentID == nspec.Type {
			err := fmt.Errorf("%w: %s", nodegraph.ErrSubgraphCycle, nspec.Type)
			m.lastErr = err
			m.statusMsg = err.Error()
			return nil, err
		}
	}
	if m.library == nil {
		err := fmt.Errorf("%w: %s (no component library configured)", nodegraph.ErrUnknownNodeType, nspec.Type)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nil, err
	}
	compSpec, ok := m.library.LookupSpec(nspec.Type)
	if !ok {
		err := fmt.Errorf("%w: %s", nodegraph.ErrUnknownNodeType, nspec.Type)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nil, err
	}
	compSpec = compSpec.Canonicalize()
	if len(compSpec.Nodes) > 0 && allNodesMissingUI(compSpec) {
		if laidOut, err := nodegraph.ApplyEdits(compSpec, AutoLayoutOpsWith(compSpec, m.registry, m.layoutOpts)...); err == nil {
			compSpec = laidOut
		}
	}

	m.navStack = append(m.navStack, navFrame{
		spec:            m.spec.Clone(),
		committedSpec:   m.committedSpec.Clone(),
		undoStack:       append([]*nodegraph.Spec(nil), m.undoStack...),
		redoStack:       append([]*nodegraph.Spec(nil), m.redoStack...),
		cam:             m.cam,
		userMovedCamera: m.userMovedCamera,
		sel:             cloneSelection(m.sel),
		paramsExpanded:  mapsCloneBool(m.paramsExpanded),
		previewExpanded: mapsCloneBool(m.previewExpanded),
		instanceID:      nodeID,
		componentID:     m.activeComponent,
	})

	m.activeComponent = nspec.Type
	m.spec = compSpec
	m.committedSpec = compSpec.Clone()
	m.undoStack = nil
	m.redoStack = nil
	m.paramsExpanded = make(map[string]bool)
	m.previewExpanded = make(map[string]bool)
	m.nodePreviews = make(map[string]NodePreview)
	m.viewerNodeID = ""
	m.editingField = false
	m.ctxMenu = nil
	m.lastErr = nil

	if ids := m.spec.SortedNodeIDs(); len(ids) > 0 {
		m.sel = Selection{Nodes: []string{ids[0]}}
	} else {
		m.sel = Selection{}
	}
	_, m.validationErr = nodegraph.Validate(m.spec, m.registry, m.validateOpts())
	m.rebuildScene()
	m.FitToView()
	m.notifyWatchChange()
	m.statusMsg = fmt.Sprintf("Drilled into %s (%s) — Backspace/Esc or ◂ Back to return", nodeID, nspec.Type)
	if m.mode == RenderVector {
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture()), nil
	}
	return nil, nil
}

// NavigateUp pops one level from the subgraph breadcrumb stack and restores the
// parent canvas state. Returns nil when already at the root graph.
func (m *Model) NavigateUp() tea.Cmd {
	if len(m.navStack) == 0 {
		return nil
	}
	top := m.navStack[len(m.navStack)-1]
	m.navStack = m.navStack[:len(m.navStack)-1]

	m.activeComponent = top.componentID
	m.spec = top.spec
	m.committedSpec = top.committedSpec
	m.undoStack = top.undoStack
	m.redoStack = top.redoStack
	m.cam = top.cam
	m.userMovedCamera = top.userMovedCamera
	m.sel = top.sel
	if top.instanceID != "" {
		if _, ok := m.spec.Nodes[top.instanceID]; ok {
			m.sel = Selection{Nodes: []string{top.instanceID}}
		}
	}
	m.paramsExpanded = top.paramsExpanded
	if m.paramsExpanded == nil {
		m.paramsExpanded = make(map[string]bool)
	}
	m.previewExpanded = top.previewExpanded
	if m.previewExpanded == nil {
		m.previewExpanded = make(map[string]bool)
	}
	m.nodePreviews = make(map[string]NodePreview)
	m.viewerNodeID = ""
	m.editingField = false
	m.ctxMenu = nil
	m.lastErr = nil

	_, m.validationErr = nodegraph.Validate(m.spec, m.registry, m.validateOpts())
	if m.validationErr == nil {
		m.committedSpec = m.spec.Clone()
	}
	m.sanitizeSelection()
	m.rebuildScene()
	m.vectorDirty = true
	m.notifyWatchChange()
	m.statusMsg = fmt.Sprintf("Returned to %s", m.spec.Name)
	if m.mode == RenderVector {
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
	}
	return nil
}

// NavigateToRoot pops all subgraph breadcrumb frames back to the root graph.
func (m *Model) NavigateToRoot() tea.Cmd {
	var cmd tea.Cmd
	for len(m.navStack) > 0 {
		cmd = m.NavigateUp()
	}
	return cmd
}

func (m *Model) selectedPromotableGroupID() string {
	if m.sel.Group != "" {
		if _, ok := m.spec.Groups[m.sel.Group]; ok {
			return m.sel.Group
		}
	}
	if len(m.sel.Nodes) == 0 {
		return ""
	}
	selSorted := append([]string(nil), m.sel.Nodes...)
	slices.Sort(selSorted)
	selSorted = slices.Compact(selSorted)
	for _, gid := range m.spec.SortedGroupIDs() {
		g := m.spec.Groups[gid]
		mem := append([]string(nil), g.Nodes...)
		slices.Sort(mem)
		mem = slices.Compact(mem)
		if slices.Equal(selSorted, mem) {
			return gid
		}
	}
	return ""
}

// PromoteSelectedGroupToSubgraph promotes the currently selected group (or the
// group whose exact member set is selected) into a reusable graph/* component.
func (m *Model) PromoteSelectedGroupToSubgraph(componentName string) (nodegraph.TypeDef, tea.Cmd, error) {
	gid := m.selectedPromotableGroupID()
	if gid == "" {
		err := fmt.Errorf("%w: select a group to promote", nodegraph.ErrUnknownGroup)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nodegraph.TypeDef{}, nil, err
	}
	return m.PromoteGroupToSubgraph(gid, componentName)
}

// PromoteGroupToSubgraph promotes groupID into a reusable graph/* component Spec,
// registers it in the editor's Library + Registry (and OnRegisterComponent), and
// commits the updated parent Spec with a single replacement graph/* instance node.
func (m *Model) PromoteGroupToSubgraph(groupID, componentName string) (nodegraph.TypeDef, tea.Cmd, error) {
	group, ok := m.spec.Groups[groupID]
	if !ok {
		err := fmt.Errorf("%w: %q", nodegraph.ErrUnknownGroup, groupID)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nodegraph.TypeDef{}, nil, err
	}
	compName := strings.TrimSpace(componentName)
	if compName == "" {
		base := group.Label
		if base == "" {
			base = groupID
		}
		compName = m.uniqueComponentName(base)
	}

	var backendPtr *nodegraph.BackendProfile
	if m.profile.Name != "" || m.profile.Compatibility != nil {
		p := m.profile
		backendPtr = &p
	}
	beforeNodes := make(map[string]bool, len(m.spec.Nodes))
	for id := range m.spec.Nodes {
		beforeNodes[id] = true
	}

	comp, nextParent, _, err := nodegraph.PromoteGroupToSubgraph(m.spec, groupID, m.registry, nodegraph.ExtractOptions{
		ComponentName: compName,
		Backend:       backendPtr,
	})
	if err != nil {
		m.lastErr = err
		m.statusMsg = firstLine(err.Error())
		return nodegraph.TypeDef{}, nil, err
	}

	def, err := m.registerComponentSpec(comp)
	if err != nil {
		m.lastErr = err
		m.statusMsg = firstLine(err.Error())
		return nodegraph.TypeDef{}, nil, err
	}

	cmd, err := m.commitCandidate(nextParent, true)
	if err != nil {
		return nodegraph.TypeDef{}, nil, err
	}

	instID := ""
	for _, nid := range m.spec.SortedNodeIDs() {
		if !beforeNodes[nid] && m.spec.Nodes[nid].Type == def.ID {
			instID = nid
			break
		}
	}
	if instID != "" {
		m.SelectNode(instID)
	}
	m.statusMsg = fmt.Sprintf("Promoted group %q -> %s (Enter to drill in)", groupID, def.ID)
	if m.mode == RenderVector {
		cmd = tea.Batch(cmd, m.refreshVectorPicture())
	}
	return def, cmd, nil
}

// ExtractSelectedNodesToSubgraph extracts the currently selected nodes into a
// reusable graph/* component Spec, registers it, and replaces them with a single
// component instance node.
func (m *Model) ExtractSelectedNodesToSubgraph(componentName string) (nodegraph.TypeDef, tea.Cmd, error) {
	if len(m.sel.Nodes) == 0 {
		err := fmt.Errorf("%w: select one or more nodes to extract", nodegraph.ErrInvalidSpec)
		m.lastErr = err
		m.statusMsg = err.Error()
		return nodegraph.TypeDef{}, nil, err
	}
	compName := strings.TrimSpace(componentName)
	if compName == "" {
		compName = m.uniqueComponentName(m.sel.Nodes[0] + "-sub")
	}
	var backendPtr *nodegraph.BackendProfile
	if m.profile.Name != "" || m.profile.Compatibility != nil {
		p := m.profile
		backendPtr = &p
	}
	beforeNodes := make(map[string]bool, len(m.spec.Nodes))
	for id := range m.spec.Nodes {
		beforeNodes[id] = true
	}

	comp, nextParent, _, err := nodegraph.ExtractSubgraph(m.spec, m.sel.Nodes, m.registry, nodegraph.ExtractOptions{
		ComponentName: compName,
		Backend:       backendPtr,
	})
	if err != nil {
		m.lastErr = err
		m.statusMsg = firstLine(err.Error())
		return nodegraph.TypeDef{}, nil, err
	}

	def, err := m.registerComponentSpec(comp)
	if err != nil {
		m.lastErr = err
		m.statusMsg = firstLine(err.Error())
		return nodegraph.TypeDef{}, nil, err
	}

	cmd, err := m.commitCandidate(nextParent, true)
	if err != nil {
		return nodegraph.TypeDef{}, nil, err
	}

	instID := ""
	for _, nid := range m.spec.SortedNodeIDs() {
		if !beforeNodes[nid] && m.spec.Nodes[nid].Type == def.ID {
			instID = nid
			break
		}
	}
	if instID != "" {
		m.SelectNode(instID)
	}
	m.statusMsg = fmt.Sprintf("Extracted %s (Enter to drill in)", def.ID)
	if m.mode == RenderVector {
		cmd = tea.Batch(cmd, m.refreshVectorPicture())
	}
	return def, cmd, nil
}

func (m *Model) uniqueComponentName(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	var b strings.Builder
	prevSep := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			prevSep = false
			continue
		}
		if b.Len() > 0 && !prevSep {
			b.WriteByte('-')
			prevSep = true
		}
	}
	base := strings.Trim(b.String(), "-_")
	if base == "" {
		base = "component"
	}
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-_")
	}
	cand := base
	for n := 2; n < 10000; n++ {
		tid := nodegraph.TypeID{Namespace: nodegraph.NamespaceGraph, Name: cand, Major: 1}
		_, inReg := m.registry.Lookup(tid)
		inLib := false
		if m.library != nil {
			_, inLib = m.library.Get(tid)
		}
		if !inReg && !inLib {
			return cand
		}
		cand = fmt.Sprintf("%s-%d", base, n)
	}
	return fmt.Sprintf("%s-x", base)
}

func mapsCloneBool(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return make(map[string]bool)
	}
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// DeleteSelected removes the currently selected node(s), edge, or group.
func (m *Model) DeleteSelected() (tea.Cmd, error) {
	switch {
	case m.sel.Edge != nil:
		e := *m.sel.Edge
		m.sel.Edge = nil
		return m.ApplyEdits(nodegraph.Disconnect(e.From, e.To, e.Kind))
	case m.sel.Group != "":
		gid := m.sel.Group
		m.sel.Group = ""
		return m.ApplyEdits(nodegraph.RemoveGroup(gid))
	case len(m.sel.Nodes) > 0:
		ops := make([]nodegraph.EditOp, 0, len(m.sel.Nodes))
		for _, id := range m.sel.Nodes {
			ops = append(ops, nodegraph.RemoveNode(id))
		}
		m.sel.Nodes = nil
		return m.ApplyEdits(ops...)
	default:
		return nil, nil
	}
}

// WantCaptureKeyboard reports whether an active modal or text field inside the
// editor currently consumes printable keys so parent hosts don't trigger global
// single-letter shortcuts.
func (m *Model) WantCaptureKeyboard() bool {
	return m.focus == focusPalette || m.focus == focusQuickConnect || m.focus == focusHelp || m.editingField || m.ctxMenu != nil
}

// Update processes Bubble Tea v2 messages (keyboard, mouse, window size, and
// ntpicture async preparation/kitty events).
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Always forward messages to ntpicture so Kitty probing, cell pixel reports,
	// and prepared frames complete cleanly.
	if m.pic != nil {
		cwBefore, chBefore := m.pic.CellPixelSize()
		kittyBefore := m.pic.KittySupported()
		if cmd := m.pic.Update(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cwAfter, chAfter := m.pic.CellPixelSize()
		cellMetricsChanged := cwAfter != cwBefore || chAfter != chBefore
		if cellMetricsChanged && !m.userMovedCamera {
			m.fitCameraInitial()
		}
		if (cellMetricsChanged || m.pic.KittySupported() != kittyBefore) && m.mode == RenderVector {
			m.vectorDirty = true
		}
	}

	coalesceVector := false
	switch ev := msg.(type) {
	case ntpicture.PresentedMsg:
		if m.pic != nil && ev.Owner == m.pic {
			now := time.Now()
			m.perf.FramesPresented++
			var superseded uint64
			if ev.Generation > m.lastPresentedGen+1 {
				superseded = ev.Generation - m.lastPresentedGen - 1
				m.perf.FramesSuperseded += superseded
			}
			m.lastPresentedGen = ev.Generation
			var lat time.Duration
			if tSub, ok := m.genSubmittedAt[ev.Generation]; ok {
				lat = now.Sub(tSub)
				m.perf.LastPresentLatency = lat
				if lat > m.perf.MaxPresentLatency {
					m.perf.MaxPresentLatency = lat
				}
				m.perf.TotalPresent += lat
			}
			for g := range m.genSubmittedAt {
				if g <= ev.Generation {
					delete(m.genSubmittedAt, g)
				}
			}
			st := m.pic.Status()
			backend := "glyph"
			if m.pic.Mode() == ntpicture.PictureKitty {
				backend = "kitty"
			}
			m.emitPerfEvent(PerfEvent{
				Kind:             "picture_present",
				Timestamp:        now,
				DragKind:         m.drag.kind.String(),
				Backend:          backend,
				KittyFormat:      fmt.Sprint(st.Format),
				KittyMedium:      fmt.Sprint(st.Medium),
				PixelW:           m.perf.LastPixelW,
				PixelH:           m.perf.LastPixelH,
				Generation:       ev.Generation,
				Presented:        st.Presented,
				SupersededFrames: superseded,
				PresentLatencyUS: lat.Microseconds(),
			})
		}

	case ntpicture.ErrorMsg:
		if m.pic != nil && ev.Owner == m.pic {
			if m.pic.Mode() == ntpicture.PictureKitty {
				m.kittyGridOverflow = true
				m.kittyPrevAutoMode = m.picAutoMode
				cmds = append(cmds, m.pic.SetMode(ntpicture.PictureGlyph), m.syncPictureSize())
				m.vectorDirty = true
			}
		}

	case vectorRefreshTickMsg:
		m.vectorTickScheduled = false
		if m.mode == RenderVector && m.vectorDirty {
			if m.pic != nil && m.pic.Status().Busy && m.lastPresentedGen > 0 {
				// Leave m.vectorDirty set; ntpicture.PresentedMsg will flush the
				// latest frame as soon as the in-flight frame commits!
				return m, tea.Batch(cmds...)
			}
			cmds = append(cmds, m.refreshVectorPicture())
		}
		return m, tea.Batch(cmds...)

	case tea.WindowSizeMsg:
		if m.drag.kind == dragPan || m.drag.kind == dragZoom {
			m.drag = dragState{}
		}
		cmds = append(cmds, m.SetSize(ev.Width, ev.Height))
		if m.mode == RenderVector && m.pic != nil {
			cmds = append(cmds, tea.Raw(requestCellSizeCSI))
		}

	case tea.KeyPressMsg:
		if cmd := m.handleKeyPress(ev); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case tea.KeyReleaseMsg:
		if ev.Code == tea.KeySpace {
			m.spaceHeld = false
		}

	case tea.MouseClickMsg:
		if cmd := m.handleMouseClick(ev.Mouse()); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case tea.MouseMotionMsg:
		coalesceVector = true
		if cmd := m.handleMouseMotion(ev.Mouse()); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case tea.MouseReleaseMsg:
		if cmd := m.handleMouseRelease(ev.Mouse()); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case tea.MouseWheelMsg:
		coalesceVector = true
		if cmd := m.handleMouseWheel(ev.Mouse()); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if m.mode == RenderVector && m.pic != nil {
		canvasCols, _ := m.splitColumns()
		bodyRows := m.bodyRows()
		wantVis := canvasCols > 0 && bodyRows > 0
		if canvasCols != m.lastCanvasCols || bodyRows != m.lastCanvasRows || wantVis != m.pic.Visible() {
			if cmd := m.syncPictureSize(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.vectorDirty = true
		}
	}

	if m.mode == RenderVector && m.vectorDirty {
		if coalesceVector || m.vectorTickScheduled {
			if cmd := m.scheduleVectorRefresh(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else if m.pic != nil && m.pic.Status().Busy && m.lastPresentedGen > 0 {
			// Backpressure: never cancel an in-flight ntpicture job mid-encode;
			// PresentedMsg / ErrorMsg will immediately flush the newest state.
		} else {
			cmds = append(cmds, m.refreshVectorPicture())
		}
	}

	m.maybeNotifySelectionChange()
	return m, tea.Batch(cmds...)
}

// View renders the complete editor surface (canvas + optional docked inspector
// or active modal overlay + status footer) to an exact (cols x rows) string.
func (m *Model) View() string {
	bodyRows := m.bodyRows()
	canvasCols, inspectorCols := m.splitColumns()

	header := m.renderHeaderBar(m.cols)
	canvasStr := m.renderCanvasArea(canvasCols, bodyRows)

	var body string
	if inspectorCols > 0 {
		inspStr := m.renderSidePanel(inspectorCols, bodyRows)
		if canvasCols > 0 {
			body = lipgloss.JoinHorizontal(lipgloss.Top, canvasStr, inspStr)
		} else {
			body = inspStr
		}
	} else {
		body = canvasStr
	}

	footer := m.renderFooterBar(m.cols)
	return header + "\n" + body + "\n" + footer
}

func (m *Model) splitColumns() (canvasCols, inspectorCols int) {
	needSide := m.showInspector || m.focus == focusPalette || m.focus == focusInspector || m.focus == focusQuickConnect || m.focus == focusHelp
	if !needSide || m.cols < minCanvasForSplit+inspectorMinCols {
		if m.focus == focusPalette || m.focus == focusQuickConnect || m.focus == focusHelp {
			// Narrow terminal: dedicate view to active overlay.
			return 0, m.cols
		}
		return m.cols, 0
	}
	inspW := min(42, max(inspectorMinCols, m.cols/3))
	return m.cols - inspW, inspW
}

func (m *Model) syncPictureSize() tea.Cmd {
	if m.pic == nil {
		return nil
	}
	canvasCols, _ := m.splitColumns()
	bodyRows := m.bodyRows()
	if canvasCols <= 0 || bodyRows <= 0 {
		m.lastCanvasCols = 0
		m.lastCanvasRows = 0
		return m.pic.SetVisible(false)
	}
	var cmds []tea.Cmd
	oversizedForKitty := canvasCols > maxKittyPlaceholderCells || bodyRows > maxKittyPlaceholderCells
	if oversizedForKitty && m.pic.Mode() == ntpicture.PictureKitty {
		m.kittyGridOverflow = true
		m.kittyPrevAutoMode = m.picAutoMode
		cmds = append(cmds, m.pic.SetMode(ntpicture.PictureGlyph))
	}
	m.lastCanvasCols = canvasCols
	m.lastCanvasRows = bodyRows
	cmds = append(cmds, m.pic.SetVisible(m.mode == RenderVector), m.pic.SetSize(canvasCols, bodyRows))
	if !oversizedForKitty && m.kittyGridOverflow {
		m.kittyGridOverflow = false
		if m.kittyPrevAutoMode {
			cmds = append(cmds, m.pic.SetMode(ntpicture.PictureAuto))
		} else {
			cmds = append(cmds, m.pic.SetMode(ntpicture.PictureKitty))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) scheduleVectorRefresh() tea.Cmd {
	if m.pic == nil || m.mode != RenderVector || !m.vectorDirty || m.vectorTickScheduled {
		return nil
	}
	m.vectorTickScheduled = true
	return tea.Tick(16*time.Millisecond, func(time.Time) tea.Msg {
		return vectorRefreshTickMsg{}
	})
}

func (m *Model) canvasVectorPixelSize(canvasCols, bodyRows int) (pxW, pxH int) {
	cw, ch := 10, 20
	if m.pic != nil {
		if pw, ph := m.pic.CellPixelSize(); pw > 0 && ph > 0 {
			cw, ch = pw, ph
		}
	}
	rawW := max(64, max(1, canvasCols)*cw)
	rawH := max(64, max(1, bodyRows)*ch)
	if maxEdge := max(rawW, rawH); maxEdge > MaxVectorEdgePx {
		scale := float64(MaxVectorEdgePx) / float64(maxEdge)
		rawW = max(64, int(math.Round(float64(rawW)*scale)))
		rawH = max(64, int(math.Round(float64(rawH)*scale)))
	}
	return rawW, rawH
}

func (m *Model) canvasEffectiveSize(canvasCols, bodyRows int) (int, int) {
	if m.mode == RenderGrid {
		return max(64, max(1, canvasCols)*int(GridCellWorldW)), max(64, max(1, bodyRows)*int(GridCellWorldH))
	}
	return m.canvasVectorPixelSize(canvasCols, bodyRows)
}

func (m *Model) refreshVectorPicture() tea.Cmd {
	m.vectorDirty = false
	if m.pic == nil || m.mode != RenderVector {
		return nil
	}
	canvasCols, _ := m.splitColumns()
	bodyRows := m.bodyRows()
	if canvasCols <= 0 || bodyRows <= 0 {
		return m.pic.SetVisible(false)
	}
	var szCmd tea.Cmd
	pCols, pRows := m.pic.Size()
	if canvasCols != m.lastCanvasCols || bodyRows != m.lastCanvasRows || pCols != canvasCols || pRows != bodyRows || !m.pic.Visible() {
		szCmd = m.syncPictureSize()
	}

	pxW, pxH := m.canvasVectorPixelSize(canvasCols, bodyRows)

	backendTag := "GLYPH"
	backendLower := "glyph"
	if m.pic.Mode() == ntpicture.PictureKitty {
		backendTag = "KITTY"
		backendLower = "kitty"
	}
	hud := fmt.Sprintf("VECTOR·%s  %d%%  (%d,%d)  %dn %de%s",
		backendTag,
		int(math.Round(m.cam.Zoom*100)),
		int(math.Round(m.cam.PanX)),
		int(math.Round(m.cam.PanY)),
		len(m.spec.Nodes),
		len(m.spec.Edges),
		m.formatPerfHUDSuffix(),
	)

	preStatus := m.pic.Status()
	busyBefore := preStatus.Busy

	tRaster := time.Now()
	img := RenderSceneVector(m.scene, m.cam, pxW, pxH, m.theme, hud)
	rasterDur := time.Since(tRaster)

	m.perf.VectorRasters++
	m.perf.LastPixelW = pxW
	m.perf.LastPixelH = pxH
	m.perf.LastVectorRaster = rasterDur
	if rasterDur > m.perf.MaxVectorRaster {
		m.perf.MaxVectorRaster = rasterDur
	}
	m.perf.TotalVectorRaster += rasterDur

	tSub := time.Now()
	imgCmd := m.pic.SetImageImmutable(img)
	postStatus := m.pic.Status()
	m.perf.FramesSubmitted++
	if busyBefore {
		m.perf.SubmittedWhileBusy++
	}
	if m.genSubmittedAt == nil {
		m.genSubmittedAt = make(map[uint64]time.Time, 16)
	}
	if len(m.genSubmittedAt) > 64 {
		for g := range m.genSubmittedAt {
			if g+32 < postStatus.Generation {
				delete(m.genSubmittedAt, g)
			}
		}
	}
	m.genSubmittedAt[postStatus.Generation] = tSub

	m.emitPerfEvent(PerfEvent{
		Kind:              "vector_submit",
		Timestamp:         tSub,
		DragKind:          m.drag.kind.String(),
		Backend:           backendLower,
		KittyFormat:       fmt.Sprint(postStatus.Format),
		KittyMedium:       fmt.Sprint(postStatus.Medium),
		PixelW:            pxW,
		PixelH:            pxH,
		Generation:        postStatus.Generation,
		Presented:         postStatus.Presented,
		PictureBusyBefore: busyBefore,
		VectorRasterUS:    rasterDur.Microseconds(),
	})

	return tea.Batch(szCmd, imgCmd)
}

func (m *Model) renderCanvasArea(cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	if m.mode == RenderVector && m.pic != nil {
		pCols, pRows := m.pic.Size()
		if pCols == cols && pRows == rows {
			m.pic.ResetViewport()
			if view := m.pic.View(); view != "" {
				return padBlockExact(view, cols, rows)
			}
		} else if pCols >= cols && pRows >= rows {
			m.pic.SetViewport(ntpicture.Viewport{Width: cols, Height: rows})
			if view := m.pic.View(); view != "" {
				return padBlockExact(view, cols, rows)
			}
		}
	}
	hud := fmt.Sprintf("GRID  %d%%  (%d,%d)  %dn %de",
		int(math.Round(m.cam.Zoom*100)),
		int(math.Round(m.cam.PanX)),
		int(math.Round(m.cam.PanY)),
		len(m.spec.Nodes),
		len(m.spec.Edges),
	)
	grid := RenderSceneGrid(m.scene, m.cam, cols, rows, m.theme, hud)
	return grid.Render(image.Rect(0, 0, cols, rows))
}

func (m *Model) currentSceneOptions() SceneOptions {
	return SceneOptions{
		Backend:         &m.profile,
		Layout:          m.layoutOpts,
		ParamsExpanded:  m.paramsExpanded,
		PreviewExpanded: m.previewExpanded,
		NodePreviews:    m.nodePreviews,
		ViewerNodeID:    m.viewerNodeID,
	}
}

func (m *Model) rebuildScene() {
	t0 := time.Now()
	var preview *WireDragPreview
	if m.drag.kind == dragWire && m.drag.wire.Active {
		preview = &m.drag.wire
	}
	m.scene = BuildSceneWithOptions(m.spec, m.registry, m.sel, preview, m.currentSceneOptions())
	if m.drag.kind == dragMarquee {
		r := NormalizedRect(m.drag.startWorld, m.drag.curWorld)
		m.scene.Marquee = &r
	} else if m.hoverHit.Kind != HitNone {
		m.scene.ApplyHover(m.hoverHit)
	}
	if m.ctxMenu != nil && len(m.ctxMenu.actions) > 0 {
		canvasCols, _ := m.splitColumns()
		bodyRows := m.bodyRows()
		items := make([]string, len(m.ctxMenu.actions))
		for i, a := range m.ctxMenu.actions {
			items[i] = a.label
		}
		m.scene.ContextMenu = &ContextMenuOverlay{
			Title:      m.ctxMenu.title,
			AnchorCol:  m.ctxMenu.anchorCol,
			AnchorRow:  m.ctxMenu.anchorRow,
			CanvasCols: max(20, canvasCols),
			CanvasRows: bodyRows,
			Items:      items,
			HoverIdx:   m.ctxMenu.hoverIdx,
		}
	}
	dur := time.Since(t0)
	m.perf.SceneRebuilds++
	m.perf.LastSceneBuild = dur
	if dur > m.perf.MaxSceneBuild {
		m.perf.MaxSceneBuild = dur
	}
	m.perf.TotalSceneBuild += dur

	if !sameSelection(m.lastNotifiedSel, m.sel) {
		m.lastNotifiedSel = m.Selection()
		if m.onSelChange != nil {
			m.onSelChange(m.Selection())
		}
	}
}

func sameSelection(a, b Selection) bool {
	if a.Group != b.Group || !slices.Equal(a.Nodes, b.Nodes) {
		return false
	}
	if (a.Edge == nil) != (b.Edge == nil) {
		return false
	}
	if a.Edge != nil && *a.Edge != *b.Edge {
		return false
	}
	return true
}

func (m *Model) fitCameraInitial() {
	canvasCols, _ := m.splitColumns()
	if canvasCols <= 0 {
		canvasCols = m.cols
	}
	bodyRows := m.bodyRows()
	if m.scene != nil && m.scene.WorldBounds.W > 0 {
		pxW, pxH := m.canvasEffectiveSize(canvasCols, bodyRows)
		m.cam.FitBounds(m.scene.WorldBounds, pxW, pxH)
	} else {
		m.cam = DefaultCamera()
	}
}

func (m *Model) viewportCenterWorld() Point {
	canvasCols, _ := m.splitColumns()
	if canvasCols <= 0 {
		canvasCols = m.cols
	}
	bodyRows := m.bodyRows()
	if m.mode == RenderGrid {
		return m.cam.GridToWorld(float64(canvasCols)*0.5, float64(bodyRows)*0.5)
	}
	pxW, pxH := m.canvasVectorPixelSize(canvasCols, bodyRows)
	return m.cam.ScreenToWorld(float64(pxW)*0.5, float64(pxH)*0.5)
}

func (m *Model) pushUndo(snap *nodegraph.Spec) {
	if snap == nil {
		return
	}
	m.undoStack = append(m.undoStack, snap.Clone())
	if len(m.undoStack) > maxUndoHistory {
		m.undoStack = m.undoStack[len(m.undoStack)-maxUndoHistory:]
	}
}

func (m *Model) sanitizeSelection() {
	kept := m.sel.Nodes[:0]
	for _, id := range m.sel.Nodes {
		if _, ok := m.spec.Nodes[id]; ok {
			kept = append(kept, id)
		}
	}
	m.sel.Nodes = kept
	if m.sel.Edge != nil {
		found := false
		for _, e := range m.spec.Edges {
			if e == *m.sel.Edge {
				found = true
				break
			}
		}
		if !found {
			m.sel.Edge = nil
		}
	}
	if m.sel.Group != "" {
		if _, ok := m.spec.Groups[m.sel.Group]; !ok {
			m.sel.Group = ""
		}
	}
	watchChanged := false
	for id := range m.paramsExpanded {
		if _, ok := m.spec.Nodes[id]; !ok {
			delete(m.paramsExpanded, id)
		}
	}
	for id := range m.previewExpanded {
		if _, ok := m.spec.Nodes[id]; !ok {
			delete(m.previewExpanded, id)
			watchChanged = true
		}
	}
	for id := range m.nodePreviews {
		if _, ok := m.spec.Nodes[id]; !ok {
			delete(m.nodePreviews, id)
		}
	}
	if m.viewerNodeID != "" {
		if _, ok := m.spec.Nodes[m.viewerNodeID]; !ok {
			m.viewerNodeID = ""
			watchChanged = true
		}
	}
	if watchChanged {
		m.notifyWatchChange()
	}
}

// Mouse handling

func (m *Model) mouseToCanvasScreenUnclamped(ev tea.Mouse) (worldPt, screenPt Point, inCanvas bool) {
	localCol := ev.X - m.offsetX
	localRow := ev.Y - m.offsetY - 1 // subtract 1-row header bar
	canvasCols, _ := m.splitColumns()
	bodyRows := m.bodyRows()
	inCanvas = localCol >= 0 && localCol < canvasCols && localRow >= 0 && localRow < bodyRows
	effCols := max(1, canvasCols)
	effRows := max(1, bodyRows)
	if m.mode == RenderGrid {
		screenPt = Point{X: float64(localCol) * GridCellWorldW, Y: float64(localRow) * GridCellWorldH}
		worldPt = m.cam.GridToWorld(float64(localCol), float64(localRow))
		return worldPt, screenPt, inCanvas
	}
	pxW, pxH := m.canvasVectorPixelSize(effCols, effRows)
	sx := (float64(localCol) + 0.5) * (float64(pxW) / float64(effCols))
	sy := (float64(localRow) + 0.5) * (float64(pxH) / float64(effRows))
	return m.cam.ScreenToWorld(sx, sy), Point{X: sx, Y: sy}, inCanvas
}

func (m *Model) mouseToCanvasWorld(ev tea.Mouse) (Point, Point, bool) {
	worldPt, screenPt, inCanvas := m.mouseToCanvasScreenUnclamped(ev)
	if !inCanvas {
		return Point{}, Point{}, false
	}
	return worldPt, screenPt, true
}

func (m *Model) canvasCellWorldSize() (cellWorldW, cellWorldH float64) {
	z := m.cam.normalized().Zoom
	if m.mode == RenderGrid {
		return GridCellWorldW / z, GridCellWorldH / z
	}
	canvasCols, _ := m.splitColumns()
	bodyRows := m.bodyRows()
	effCols := max(1, canvasCols)
	effRows := max(1, bodyRows)
	pxW, pxH := m.canvasVectorPixelSize(effCols, effRows)
	return (float64(pxW) / float64(effCols)) / z, (float64(pxH) / float64(effRows)) / z
}

func (m *Model) wireSnapRadius() float64 {
	cw, ch := m.canvasCellWorldSize()
	return max(PortHitRadius*1.6, min(48.0, math.Hypot(cw, ch)*0.65))
}

func (m *Model) hitTestAt(worldPt Point) HitResult {
	z := m.cam.normalized().Zoom
	cellW, cellH := m.canvasCellWorldSize()
	return m.scene.HitTestViewport(worldPt.X, worldPt.Y, z, cellW, cellH)
}

func (m *Model) handleMouseClick(ev tea.Mouse) tea.Cmd {
	localCol := ev.X - m.offsetX
	localRow := ev.Y - m.offsetY
	m.hoverHit = HitResult{}
	m.hoverText = ""

	// 1. Header toolbar button clicks (row 0).
	if localRow == 0 && localCol >= 0 && localCol < m.cols && ev.Button == tea.MouseLeft {
		m.ctxMenu = nil
		if len(m.headerButtons) == 0 {
			_ = m.renderHeaderBar(m.cols)
		}
		for _, btn := range m.headerButtons {
			if localCol >= btn.startCol && localCol < btn.endCol {
				return m.executeHeaderButton(btn.id)
			}
		}
		return nil
	}

	canvasCols, inspCols := m.splitColumns()
	bodyRows := m.bodyRows()

	// 2. Side-panel clicks (Inspector, Palette, Quick-Connect, Help).
	if inspCols > 0 && localCol >= canvasCols && localCol < m.cols && localRow >= 1 && localRow <= bodyRows {
		m.ctxMenu = nil
		return m.handleSidePanelClick(ev, localCol-canvasCols, localRow-1, inspCols)
	}

	worldPt, screenPt, inCanvas := m.mouseToCanvasWorld(ev)
	if !inCanvas {
		return nil
	}

	// 3. Floating context menu hit-test (if open).
	if m.ctxMenu != nil {
		canvasRow := localRow - 1
		cm := ContextMenuOverlay{
			Title:     m.ctxMenu.title,
			AnchorCol: m.ctxMenu.anchorCol,
			AnchorRow: m.ctxMenu.anchorRow,
			Items:     make([]string, len(m.ctxMenu.actions)),
		}
		for i, a := range m.ctxMenu.actions {
			cm.Items[i] = a.label
		}
		itemIdx, inside := cm.ItemAt(localCol, canvasRow, max(20, canvasCols), bodyRows)
		if inside && ev.Button == tea.MouseLeft {
			if itemIdx >= 0 && itemIdx < len(m.ctxMenu.actions) {
				act := m.ctxMenu.actions[itemIdx]
				pt := m.ctxMenu.worldPt
				m.ctxMenu = nil
				m.rebuildScene()
				m.vectorDirty = true
				return m.executeContextAction(act.id, pt)
			}
			return nil
		}
		// Click outside context menu dismisses it cleanly.
		m.ctxMenu = nil
		m.rebuildScene()
		m.vectorDirty = true
		if ev.Button != tea.MouseRight {
			return nil
		}
	}

	// Dismiss modal side overlays if the user clicks back onto the canvas.
	var modalCloseCmd tea.Cmd
	if m.focus == focusPalette || m.focus == focusQuickConnect || m.focus == focusHelp {
		m.focus = focusCanvas
		m.pendingSpawnWorld = nil
		m.paletteSpliceMode = false
		modalCloseCmd = m.syncPictureSize()
	} else {
		m.focus = focusCanvas
	}
	m.editingField = false

	// 4a. Alt/Option + Left-drag smoothly scrub-zooms around the clicked world point!
	if ev.Button == tea.MouseLeft && ev.Mod&tea.ModAlt != 0 {
		m.pendingPureClick = false
		m.drag = dragState{
			kind:         dragZoom,
			startWorld:   worldPt,
			startScreenX: screenPt.X,
			startScreenY: screenPt.Y,
			startZoom:    m.cam.normalized().Zoom,
		}
		m.statusMsg = "Drag up/right to zoom in · down/left to zoom out"
		return modalCloseCmd
	}

	// 4b. Middle mouse or Space+left mouse pans immediately; Right mouse pans on drag or opens Context Menu on stationary click!
	if ev.Button == tea.MouseMiddle || (ev.Button == tea.MouseLeft && m.spaceHeld) {
		m.pendingPureClick = false
		m.drag = dragState{
			kind:         dragPan,
			startScreenX: screenPt.X,
			startScreenY: screenPt.Y,
			startPanX:    m.cam.PanX,
			startPanY:    m.cam.PanY,
		}
		return modalCloseCmd
	}
	if ev.Button == tea.MouseRight {
		m.pendingPureClick = false
		m.drag = dragState{
			kind:                dragPan,
			rightClickCandidate: true,
			rightClickCol:       localCol,
			rightClickRow:       localRow - 1,
			startWorld:          worldPt,
			startScreenX:        screenPt.X,
			startScreenY:        screenPt.Y,
			startPanX:           m.cam.PanX,
			startPanY:           m.cam.PanY,
		}
		return modalCloseCmd
	}
	if ev.Button != tea.MouseLeft {
		return modalCloseCmd
	}

	// Check if this left-click completes a stationary double-click!
	now := time.Now()
	isDoubleClick := !m.lastPureClickAt.IsZero() &&
		now.Sub(m.lastPureClickAt) <= 420*time.Millisecond &&
		absInt(localCol-m.lastPureClickCol) <= 1 &&
		absInt((localRow-1)-m.lastPureClickRow) <= 1

	hit := m.hitTestAt(worldPt)
	isInlineCtrl := hit.Kind == HitNode && (hit.ToggleCollapse || hit.ToggleParams || hit.TogglePreview || hit.ToggleViewer || hit.ParamAction != ParamActionNone)
	if isDoubleClick && !isInlineCtrl {
		m.lastPureClickAt = time.Time{}
		m.pendingPureClick = false
		return tea.Batch(modalCloseCmd, m.handleCanvasDoubleClick(hit, worldPt))
	}

	m.pendingPureClick = true
	m.pendingClickCol = localCol
	m.pendingClickRow = localRow - 1

	switch hit.Kind {
	case HitPort:
		m.pendingPureClick = false
		// If dragging from an already-connected INLET (without Shift), unplug its incoming wire so the user can rewire or drop to disconnect!
		if !hit.Port.IsOutlet && ev.Mod&tea.ModShift == 0 {
			targetRef := nodegraph.PortRef{Node: hit.Port.NodeID, Port: hit.Port.Name}
			for i := len(m.spec.Edges) - 1; i >= 0; i-- {
				e := m.spec.Edges[i]
				if e.To == targetRef {
					if srcAnchor, ok := m.scene.findPort(e.From.Node, e.From.Port, true); ok {
						snap := m.spec.Clone()
						detached := e
						if previewSpec, err := nodegraph.ApplyEdits(snap, nodegraph.Disconnect(e.From, e.To, e.Kind)); err == nil {
							m.spec = previewSpec
							m.drag = dragState{
								kind:         dragWire,
								baseSnapshot: snap,
								detachedEdge: &detached,
								wire: WireDragPreview{
									Active:     true,
									FromOutlet: true,
									Anchor:     e.From,
									AnchorType: srcAnchor.Type,
									AnchorPos:  srcAnchor.Center,
									CursorPos:  worldPt,
									SnapRadius: m.wireSnapRadius(),
									Kind:       e.Kind,
								},
							}
							m.rebuildScene()
							m.vectorDirty = true
							m.statusMsg = fmt.Sprintf("Rewiring %s -> ... (drop on canvas to disconnect)", e.From)
							return modalCloseCmd
						}
					}
					break
				}
			}
		}

		m.drag = dragState{
			kind: dragWire,
			wire: WireDragPreview{
				Active:     true,
				FromOutlet: hit.Port.IsOutlet,
				Anchor:     nodegraph.PortRef{Node: hit.Port.NodeID, Port: hit.Port.Name},
				AnchorType: hit.Port.Type,
				AnchorPos:  hit.Port.Center,
				CursorPos:  worldPt,
				SnapRadius: m.wireSnapRadius(),
			},
		}
		m.rebuildScene()
		m.vectorDirty = true
		m.statusMsg = fmt.Sprintf("Drag wire from %s.%s (%s)...", hit.Port.NodeID, hit.Port.Name, hit.Port.Type)
		return modalCloseCmd

	case HitNode:
		if hit.ToggleCollapse {
			m.pendingPureClick = false
			cur := false
			if n := m.spec.Nodes[hit.NodeID]; n.UI != nil {
				cur = n.UI.Collapsed
			}
			cmd, _ := m.ApplyEdits(nodegraph.SetNodeCollapsed(hit.NodeID, !cur))
			return tea.Batch(modalCloseCmd, cmd)
		}
		if hit.ToggleParams {
			m.pendingPureClick = false
			if !m.sel.HasNode(hit.NodeID) {
				m.sel = Selection{Nodes: []string{hit.NodeID}}
			}
			return tea.Batch(modalCloseCmd, m.ToggleNodeParams(hit.NodeID))
		}
		if hit.TogglePreview {
			m.pendingPureClick = false
			if !m.sel.HasNode(hit.NodeID) {
				m.sel = Selection{Nodes: []string{hit.NodeID}}
			}
			return tea.Batch(modalCloseCmd, m.ToggleNodePreview(hit.NodeID))
		}
		if hit.ToggleViewer {
			m.pendingPureClick = false
			if !m.sel.HasNode(hit.NodeID) {
				m.sel = Selection{Nodes: []string{hit.NodeID}}
			}
			return tea.Batch(modalCloseCmd, m.ToggleViewerNode(hit.NodeID))
		}
		if hit.ParamAction != ParamActionNone && hit.ParamRow != nil {
			m.pendingPureClick = false
			if !m.sel.HasNode(hit.NodeID) {
				m.sel = Selection{Nodes: []string{hit.NodeID}}
			}
			return tea.Batch(modalCloseCmd, m.handleInlineParamClick(hit, worldPt))
		}
		if ev.Mod&tea.ModShift != 0 {
			if m.sel.HasNode(hit.NodeID) {
				m.sel.Nodes = slices.DeleteFunc(m.sel.Nodes, func(id string) bool { return id == hit.NodeID })
			} else {
				m.sel.Nodes = append(m.sel.Nodes, hit.NodeID)
			}
			m.sel.Edge = nil
			m.sel.Group = ""
		} else if !m.sel.HasNode(hit.NodeID) {
			m.sel = Selection{Nodes: []string{hit.NodeID}}
		}
		origins := make(map[string]Point, len(m.sel.Nodes))
		for _, id := range m.sel.Nodes {
			if idx, ok := m.scene.NodeByID[id]; ok {
				b := m.scene.Nodes[idx].Bounds
				origins[id] = Point{X: b.X, Y: b.Y}
			}
		}
		m.drag = dragState{
			kind:         dragNodes,
			startWorld:   worldPt,
			nodeOrigins:  origins,
			baseSnapshot: m.spec.Clone(),
		}
		m.inspectorRow = 0
		m.rebuildScene()
		m.vectorDirty = true
		return modalCloseCmd

	case HitWire:
		cp := hit.Edge
		m.sel = Selection{Edge: &cp}
		m.inspectorRow = 0
		m.rebuildScene()
		m.vectorDirty = true
		m.statusMsg = fmt.Sprintf("Selected edge %s -> %s (double-click to splice, right-click for menu)", cp.From, cp.To)
		return modalCloseCmd

	case HitGroup:
		g, ok := m.spec.Groups[hit.GroupID]
		if !ok {
			return modalCloseCmd
		}
		if hit.ToggleCollapse {
			m.pendingPureClick = false
			g.Collapsed = !g.Collapsed
			cmd, _ := m.ApplyEdits(nodegraph.PutGroup(hit.GroupID, g))
			return tea.Batch(modalCloseCmd, cmd)
		}
		m.sel = Selection{Group: hit.GroupID}
		origins := make(map[string]Point, len(g.Nodes))
		for _, id := range g.Nodes {
			if idx, ok := m.scene.NodeByID[id]; ok {
				b := m.scene.Nodes[idx].Bounds
				origins[id] = Point{X: b.X, Y: b.Y}
			}
		}
		var gOrigin, gSize Point
		for _, gb := range m.scene.Groups {
			if gb.ID == hit.GroupID {
				gOrigin = Point{X: gb.Bounds.X, Y: gb.Bounds.Y}
				gSize = Point{X: gb.Bounds.W, Y: gb.Bounds.H}
				break
			}
		}
		m.drag = dragState{
			kind:         dragGroup,
			startWorld:   worldPt,
			groupID:      hit.GroupID,
			groupOrigin:  gOrigin,
			groupSize:    gSize,
			nodeOrigins:  origins,
			baseSnapshot: m.spec.Clone(),
		}
		m.inspectorRow = 0
		m.rebuildScene()
		m.vectorDirty = true
		return modalCloseCmd

	default:
		// Shift + Drag on empty canvas performs marquee box multi-selection!
		if ev.Mod&tea.ModShift != 0 {
			m.pendingPureClick = false
			m.drag = dragState{
				kind:         dragMarquee,
				startWorld:   worldPt,
				curWorld:     worldPt,
				baseSelNodes: append([]string(nil), m.sel.Nodes...),
			}
			m.rebuildScene()
			m.vectorDirty = true
			m.statusMsg = "Box select: drag across nodes..."
			return modalCloseCmd
		}
		// Clicked empty canvas: clear selection and allow background pan drag!
		m.sel = Selection{}
		m.drag = dragState{
			kind:         dragPan,
			startScreenX: screenPt.X,
			startScreenY: screenPt.Y,
			startPanX:    m.cam.PanX,
			startPanY:    m.cam.PanY,
		}
		m.rebuildScene()
		m.vectorDirty = true
		return modalCloseCmd
	}
}

func (m *Model) handleCanvasDoubleClick(hit HitResult, worldPt Point) tea.Cmd {
	switch hit.Kind {
	case HitNode:
		m.SelectNode(hit.NodeID)
		if m.CanDrillIntoNode(hit.NodeID) {
			cmd, _ := m.DrillIntoSubgraph(hit.NodeID)
			return cmd
		}
		m.showInspector = true
		m.focus = focusInspector
		items := m.buildInspectorItems()
		// Jump straight to first static parameter if the node has one.
		m.inspectorRow = 0
		for i, it := range items {
			if it.kind == inspParam {
				m.inspectorRow = i
				break
			}
		}
		m.statusMsg = fmt.Sprintf("Inspecting node %q", hit.NodeID)
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case HitWire:
		m.SelectEdge(hit.Edge)
		m.focus = focusPalette
		m.paletteQuery = ""
		m.paletteCursor = 0
		m.paletteSpliceMode = true
		m.pendingSpawnWorld = nil
		m.statusMsg = fmt.Sprintf("Splice node into %s -> %s", hit.Edge.From, hit.Edge.To)
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case HitGroup:
		m.SelectGroup(hit.GroupID)
		g := m.spec.Groups[hit.GroupID]
		m.showInspector = true
		m.focus = focusInspector
		m.inspectorRow = 0
		m.editingField = true
		m.fieldTargetKey = "__group_label__"
		m.fieldBuffer = g.Label
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	default:
		// Double-click empty canvas -> open Palette to spawn a node right here!
		pt := worldPt
		m.pendingSpawnWorld = &pt
		m.paletteSpliceMode = false
		m.focus = focusPalette
		m.paletteQuery = ""
		m.paletteCursor = 0
		m.statusMsg = fmt.Sprintf("Add node at (%d, %d)", int(math.Round(pt.X)), int(math.Round(pt.Y)))
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
	}
}

func (m *Model) handleMouseMotion(ev tea.Mouse) tea.Cmd {
	localCol := ev.X - m.offsetX
	localRow := ev.Y - m.offsetY

	// Passive hover tracking when no button drag is active (only sent if an
	// embedding host explicitly enables AllMotion; Studio uses CellMotion so idle
	// mouse movement produces zero terminal traffic or row cursor hijacking).
	if m.drag.kind == dragNone {
		canvasCols, _ := m.splitColumns()
		bodyRows := m.bodyRows()

		// Context-menu hover tracking.
		if m.ctxMenu != nil && localCol >= 0 && localCol < canvasCols && localRow >= 1 && localRow <= bodyRows {
			cm := ContextMenuOverlay{
				Title:     m.ctxMenu.title,
				AnchorCol: m.ctxMenu.anchorCol,
				AnchorRow: m.ctxMenu.anchorRow,
				Items:     make([]string, len(m.ctxMenu.actions)),
			}
			for i, a := range m.ctxMenu.actions {
				cm.Items[i] = a.label
			}
			idx, _ := cm.ItemAt(localCol, localRow-1, max(20, canvasCols), bodyRows)
			if idx != m.ctxMenu.hoverIdx {
				m.ctxMenu.hoverIdx = idx
				m.rebuildScene()
				m.vectorDirty = true
			}
			return nil
		}

		// Canvas element hover highlighting (ports, nodes, wires, groups).
		worldPt, _, inCanvas := m.mouseToCanvasWorld(ev)
		var nextHit HitResult
		var nextText string
		if inCanvas {
			nextHit = m.hitTestAt(worldPt)
			switch nextHit.Kind {
			case HitPort:
				dir := "inlet"
				if nextHit.Port.IsOutlet {
					dir = "outlet"
				}
				req := ""
				if nextHit.Port.Required {
					req = " · required"
				}
				nextText = fmt.Sprintf("Port %s.%s (%s · %s%s) — drag to wire", nextHit.Port.NodeID, nextHit.Port.Name, dir, nextHit.Port.Type, req)
			case HitWire:
				kind := nextHit.Edge.Kind
				if kind == "" {
					kind = "signal"
				}
				nextText = fmt.Sprintf("Wire %s -> %s [%s] — double-click to splice, right-click for menu", nextHit.Edge.From, nextHit.Edge.To, kind)
			case HitNode:
				switch {
				case nextHit.ToggleViewer:
					nextText = fmt.Sprintf("Tap %s outlet into Studio Viewer (t)", nextHit.NodeID)
				case nextHit.ToggleParams:
					nextText = fmt.Sprintf("Toggle inline parameter drawer on %s (w)", nextHit.NodeID)
				case nextHit.TogglePreview:
					nextText = fmt.Sprintf("Toggle live output preview on %s (o)", nextHit.NodeID)
				case nextHit.ParamKey != "":
					nextText = fmt.Sprintf("Param %s.%s — click or drag slider to adjust", nextHit.NodeID, nextHit.ParamKey)
				default:
					if ns, ok := m.spec.Nodes[nextHit.NodeID]; ok {
						if m.CanDrillIntoNode(nextHit.NodeID) {
							nextText = fmt.Sprintf("Subgraph %s (%s) — Enter or double-click to drill in, drag to move, right-click for menu", nextHit.NodeID, ns.Type)
						} else {
							nextText = fmt.Sprintf("Node %s (%s) — drag to move, double-click to inspect, right-click for menu", nextHit.NodeID, ns.Type)
						}
					}
				}
			case HitGroup:
				if g, ok := m.spec.Groups[nextHit.GroupID]; ok {
					nextText = fmt.Sprintf("Group %q (%d nodes) — drag header to move all, double-click to rename", g.Label, len(g.Nodes))
				}
			}
		}
		if !sameHoverTarget(m.hoverHit, nextHit) || m.hoverText != nextText {
			m.hoverHit = nextHit
			m.hoverText = nextText
			m.rebuildScene()
			m.vectorDirty = true
		}
		return nil
	}

	worldPt, screenPt, inCanvas := m.mouseToCanvasScreenUnclamped(ev)
	if !inCanvas && m.drag.kind != dragPan && m.drag.kind != dragZoom && m.drag.kind != dragMarquee && m.drag.kind != dragParamSlider {
		return nil
	}

	switch m.drag.kind {
	case dragParamSlider:
		m.drag.moved = true
		return m.scrubInlineSliderAt(worldPt.X)

	case dragZoom:
		// Dragging up (-dy) or right (+dx) zooms in smoothly around startScreen/startWorld.
		delta := (m.drag.startScreenY - screenPt.Y) + (screenPt.X-m.drag.startScreenX)*0.65
		m.drag.moved = true
		m.userMovedCamera = true
		nextZoom := min(MaxZoom, max(MinZoom, m.drag.startZoom*math.Pow(1.0055, delta)))
		m.cam.Zoom = nextZoom
		m.cam.PanX = m.drag.startWorld.X - m.drag.startScreenX/nextZoom
		m.cam.PanY = m.drag.startWorld.Y - m.drag.startScreenY/nextZoom
		m.perf.DragMoves++
		m.emitPerfEvent(PerfEvent{
			Kind:      "drag_move",
			Timestamp: time.Now(),
			DragKind:  m.drag.kind.String(),
		})
		m.vectorDirty = true

	case dragPan:
		distPx := math.Hypot(screenPt.X-m.drag.startScreenX, screenPt.Y-m.drag.startScreenY)
		if distPx >= 5.0 {
			m.drag.moved = true
			m.drag.rightClickCandidate = false
		}
		if m.drag.rightClickCandidate {
			return nil
		}
		m.userMovedCamera = true
		dx := (screenPt.X - m.drag.startScreenX) / m.cam.normalized().Zoom
		dy := (screenPt.Y - m.drag.startScreenY) / m.cam.normalized().Zoom
		m.cam.PanX = m.drag.startPanX - dx
		m.cam.PanY = m.drag.startPanY - dy
		m.perf.DragMoves++
		m.emitPerfEvent(PerfEvent{
			Kind:      "drag_move",
			Timestamp: time.Now(),
			DragKind:  m.drag.kind.String(),
		})
		m.vectorDirty = true

	case dragMarquee:
		if !inCanvas {
			return nil
		}
		m.drag.moved = true
		m.drag.curWorld = worldPt
		box := NormalizedRect(m.drag.startWorld, worldPt)
		selected := append([]string(nil), m.drag.baseSelNodes...)
		for _, nb := range m.scene.Nodes {
			if nb.GroupHidden {
				continue
			}
			if box.Intersects(nb.Bounds) && !slices.Contains(selected, nb.ID) {
				selected = append(selected, nb.ID)
			}
		}
		slices.Sort(selected)
		m.sel = Selection{Nodes: selected}
		m.rebuildScene()
		m.perf.DragMoves++
		m.emitPerfEvent(PerfEvent{
			Kind:         "drag_move",
			Timestamp:    time.Now(),
			DragKind:     m.drag.kind.String(),
			SceneBuildUS: m.perf.LastSceneBuild.Microseconds(),
		})
		m.vectorDirty = true

	case dragNodes:
		dx := math.Round(worldPt.X - m.drag.startWorld.X)
		dy := math.Round(worldPt.Y - m.drag.startWorld.Y)
		if math.Abs(dx) < 1 && math.Abs(dy) < 1 && !m.drag.moved {
			return nil
		}
		m.drag.moved = true
		var ops []nodegraph.EditOp
		ids := make([]string, 0, len(m.drag.nodeOrigins))
		for id := range m.drag.nodeOrigins {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			orig := m.drag.nodeOrigins[id]
			ops = append(ops, nodegraph.MoveNode(id, orig.X+dx, orig.Y+dy))
		}
		tEdit := time.Now()
		preview, err := nodegraph.ApplyEdits(m.drag.baseSnapshot, ops...)
		editDur := time.Since(tEdit)
		if err == nil {
			m.perf.DragMoves++
			m.perf.LastApplyEdits = editDur
			if editDur > m.perf.MaxApplyEdits {
				m.perf.MaxApplyEdits = editDur
			}
			m.perf.TotalApplyEdits += editDur
			m.spec = preview
			m.rebuildScene()
			m.emitPerfEvent(PerfEvent{
				Kind:         "drag_move",
				Timestamp:    time.Now(),
				DragKind:     m.drag.kind.String(),
				ApplyEditsUS: editDur.Microseconds(),
				SceneBuildUS: m.perf.LastSceneBuild.Microseconds(),
			})
			m.vectorDirty = true
		}

	case dragGroup:
		dx := math.Round(worldPt.X - m.drag.startWorld.X)
		dy := math.Round(worldPt.Y - m.drag.startWorld.Y)
		if math.Abs(dx) < 1 && math.Abs(dy) < 1 && !m.drag.moved {
			return nil
		}
		m.drag.moved = true
		var ops []nodegraph.EditOp
		ids := make([]string, 0, len(m.drag.nodeOrigins))
		for id := range m.drag.nodeOrigins {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			orig := m.drag.nodeOrigins[id]
			ops = append(ops, nodegraph.MoveNode(id, orig.X+dx, orig.Y+dy))
		}
		if g, ok := m.drag.baseSnapshot.Groups[m.drag.groupID]; ok && g.UI != nil {
			gCopy := g
			uiCopy := *g.UI
			uiCopy.X = m.drag.groupOrigin.X + dx
			uiCopy.Y = m.drag.groupOrigin.Y + dy
			gCopy.UI = &uiCopy
			ops = append(ops, nodegraph.PutGroup(m.drag.groupID, gCopy))
		}
		tEdit := time.Now()
		preview, err := nodegraph.ApplyEdits(m.drag.baseSnapshot, ops...)
		editDur := time.Since(tEdit)
		if err == nil {
			m.perf.DragMoves++
			m.perf.LastApplyEdits = editDur
			if editDur > m.perf.MaxApplyEdits {
				m.perf.MaxApplyEdits = editDur
			}
			m.perf.TotalApplyEdits += editDur
			m.spec = preview
			m.rebuildScene()
			m.emitPerfEvent(PerfEvent{
				Kind:         "drag_move",
				Timestamp:    time.Now(),
				DragKind:     m.drag.kind.String(),
				ApplyEditsUS: editDur.Microseconds(),
				SceneBuildUS: m.perf.LastSceneBuild.Microseconds(),
			})
			m.vectorDirty = true
		}

	case dragWire:
		m.drag.moved = true
		m.drag.wire.CursorPos = worldPt
		m.drag.wire.SnapRadius = m.wireSnapRadius()
		m.rebuildScene()
		m.perf.DragMoves++
		m.emitPerfEvent(PerfEvent{
			Kind:         "drag_move",
			Timestamp:    time.Now(),
			DragKind:     m.drag.kind.String(),
			SceneBuildUS: m.perf.LastSceneBuild.Microseconds(),
		})
		if m.scene.DragPreview != nil && m.scene.DragPreview.SnapTarget != nil {
			t := m.scene.DragPreview.SnapTarget
			m.hoverText = fmt.Sprintf("Drop to connect -> %s.%s (%s · compatible)", t.NodeID, t.Name, t.Type)
		} else {
			m.hoverText = ""
		}
		m.vectorDirty = true
	}
	return nil
}

func (m *Model) handleMouseRelease(ev tea.Mouse) tea.Cmd {
	wasPure := m.pendingPureClick && !m.drag.moved
	m.pendingPureClick = false
	if wasPure && ev.Button == tea.MouseLeft {
		m.lastPureClickAt = time.Now()
		m.lastPureClickCol = m.pendingClickCol
		m.lastPureClickRow = m.pendingClickRow
	}

	if m.drag.kind == dragNone {
		return nil
	}
	defer func() {
		m.drag = dragState{}
		m.hoverHit = HitResult{}
		m.hoverText = ""
		m.rebuildScene()
		m.vectorDirty = true
	}()

	switch m.drag.kind {
	case dragParamSlider:
		return nil

	case dragPan:
		if m.drag.rightClickCandidate && !m.drag.moved {
			m.openContextMenuAt(m.drag.rightClickCol, m.drag.rightClickRow, m.drag.startWorld)
		}
		return nil

	case dragMarquee:
		m.statusMsg = fmt.Sprintf("Selected %d node(s)", len(m.sel.Nodes))
		return nil

	case dragNodes, dragGroup:
		if !m.drag.moved || m.drag.baseSnapshot == nil {
			return nil
		}
		finalSpec := m.spec
		m.spec = m.drag.baseSnapshot
		cmd, _ := m.commitCandidate(finalSpec, true)
		return cmd

	case dragWire:
		worldPt, _, inCanvas := m.mouseToCanvasWorld(ev)
		if inCanvas {
			m.drag.wire.CursorPos = worldPt
			m.drag.wire.SnapRadius = m.wireSnapRadius()
			m.rebuildScene()
		}
		var target *PortAnchor
		if m.scene.DragPreview != nil && m.scene.DragPreview.SnapTarget != nil {
			target = m.scene.DragPreview.SnapTarget
		} else if inCanvas {
			hit := m.hitTestAt(worldPt)
			if hit.Kind == HitPort && hit.Port.IsOutlet != m.drag.wire.FromOutlet && hit.Port.NodeID != m.drag.wire.Anchor.Node {
				cp := hit.Port
				target = &cp
			}
		}

		// Handle detached-inlet rewire / unplug gesture!
		if m.drag.detachedEdge != nil && m.drag.baseSnapshot != nil {
			detached := *m.drag.detachedEdge
			m.spec = m.drag.baseSnapshot
			m.drag = dragState{}
			if target == nil {
				cmd, _ := m.ApplyEdits(nodegraph.Disconnect(detached.From, detached.To, detached.Kind))
				m.statusMsg = fmt.Sprintf("Disconnected %s -> %s", detached.From, detached.To)
				return cmd
			}
			newTo := nodegraph.PortRef{Node: target.NodeID, Port: target.Name}
			if newTo == detached.To {
				m.statusMsg = "Wire unchanged"
				return nil
			}
			cmd, err := m.ApplyEdits(
				nodegraph.Disconnect(detached.From, detached.To, detached.Kind),
				nodegraph.Connect(detached.From, newTo, detached.Kind),
			)
			if err == nil {
				m.SelectEdge(nodegraph.EdgeSpec{From: detached.From, To: newTo, Kind: detached.Kind})
			}
			return cmd
		}

		if target == nil {
			m.statusMsg = "Wire cancelled"
			return nil
		}
		var from, to nodegraph.PortRef
		if m.drag.wire.FromOutlet {
			from = m.drag.wire.Anchor
			to = nodegraph.PortRef{Node: target.NodeID, Port: target.Name}
		} else {
			from = nodegraph.PortRef{Node: target.NodeID, Port: target.Name}
			to = m.drag.wire.Anchor
		}
		kind := m.drag.wire.Kind
		m.drag = dragState{}
		cmd, err := m.ApplyEdits(nodegraph.Connect(from, to, kind))
		if err == nil {
			m.SelectEdge(nodegraph.EdgeSpec{From: from, To: to, Kind: kind})
		}
		return cmd
	}
	return nil
}

func (m *Model) handleMouseWheel(ev tea.Mouse) tea.Cmd {
	localCol := ev.X - m.offsetX
	localRow := ev.Y - m.offsetY
	canvasCols, inspCols := m.splitColumns()
	bodyRows := m.bodyRows()
	forceZoomMod := ev.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) != 0

	// Scroll side panel lists when wheeling over the docked panel (unless Ctrl/Alt/Cmd is held to zoom!).
	if !forceZoomMod && inspCols > 0 && localCol >= canvasCols && localCol < m.cols && localRow >= 1 && localRow <= bodyRows {
		delta := 0
		switch ev.Button {
		case tea.MouseWheelUp:
			delta = -1
		case tea.MouseWheelDown:
			delta = 1
		}
		if delta != 0 {
			switch m.focus {
			case focusPalette:
				if items := m.filteredPaletteTypes(); len(items) > 0 {
					m.paletteCursor = modPos(m.paletteCursor+delta, len(items))
				}
			case focusQuickConnect:
				if len(m.qcCandidates) > 0 {
					m.qcCursor = modPos(m.qcCursor+delta, len(m.qcCandidates))
				}
			default:
				m.focus = focusInspector
				if items := m.buildInspectorItems(); len(items) > 0 {
					m.inspectorRow = modPos(m.inspectorRow+delta, len(items))
				}
			}
		}
		return nil
	}

	_, screenPt, inCanvas := m.mouseToCanvasWorld(ev)
	if !inCanvas {
		if forceZoomMod && localCol >= 0 && localCol < m.cols && localRow >= 0 && localRow < m.rows {
			cx, cy := m.viewportCenterScreen()
			screenPt = Point{X: cx, Y: cy}
		} else {
			return nil
		}
	}
	// Shift+Wheel pans vertically; Ctrl/Alt/Cmd+Wheel or plain Wheel zooms smoothly!
	m.lastPureClickAt = time.Time{}
	if ev.Mod&tea.ModShift != 0 && !forceZoomMod {
		switch ev.Button {
		case tea.MouseWheelUp:
			m.userMovedCamera = true
			m.cam.PanY -= 32 / m.cam.normalized().Zoom
			m.vectorDirty = true
		case tea.MouseWheelDown:
			m.userMovedCamera = true
			m.cam.PanY += 32 / m.cam.normalized().Zoom
			m.vectorDirty = true
		}
		return nil
	}
	switch ev.Button {
	case tea.MouseWheelUp:
		m.userMovedCamera = true
		m.cam.ZoomAt(1.16, screenPt.X, screenPt.Y)
		m.vectorDirty = true
	case tea.MouseWheelDown:
		m.userMovedCamera = true
		m.cam.ZoomAt(1.0/1.16, screenPt.X, screenPt.Y)
		m.vectorDirty = true
	case tea.MouseWheelLeft:
		m.userMovedCamera = true
		m.cam.PanX -= 32 / m.cam.normalized().Zoom
		m.vectorDirty = true
	case tea.MouseWheelRight:
		m.userMovedCamera = true
		m.cam.PanX += 32 / m.cam.normalized().Zoom
		m.vectorDirty = true
	}
	return nil
}

func sameHoverTarget(a, b HitResult) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case HitPort:
		return a.Port.NodeID == b.Port.NodeID && a.Port.Name == b.Port.Name && a.Port.IsOutlet == b.Port.IsOutlet
	case HitNode:
		return a.NodeID == b.NodeID &&
			a.ToggleCollapse == b.ToggleCollapse &&
			a.ToggleParams == b.ToggleParams &&
			a.TogglePreview == b.TogglePreview &&
			a.ToggleViewer == b.ToggleViewer &&
			a.ParamKey == b.ParamKey &&
			a.ParamAction == b.ParamAction
	case HitWire:
		return a.Edge == b.Edge
	case HitGroup:
		return a.GroupID == b.GroupID
	default:
		return true
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Helpers

func uniqueNodeID(spec *nodegraph.Spec, rawBase string) string {
	var b strings.Builder
	for i := 0; i < len(rawBase); i++ {
		c := rawBase[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b.WriteByte(c)
		} else if c == '.' {
			b.WriteByte('_')
		}
	}
	base := strings.Trim(b.String(), "_-")
	if base == "" || !nodegraph.IsValidID(base) {
		base = "node"
	}
	if _, exists := spec.Nodes[base]; !exists {
		return base
	}
	for i := 2; i < 10000; i++ {
		cand := fmt.Sprintf("%s_%d", base, i)
		if _, exists := spec.Nodes[cand]; !exists {
			return cand
		}
	}
	return fmt.Sprintf("node_%d", time.Now().UnixNano()%100000)
}

func uniqueGroupID(spec *nodegraph.Spec, base string) string {
	if _, exists := spec.Groups[base]; !exists {
		return base
	}
	for i := 2; i < 10000; i++ {
		cand := fmt.Sprintf("%s_%d", base, i)
		if _, exists := spec.Groups[cand]; !exists {
			return cand
		}
	}
	return "group_x"
}

func buildDefaultNodeSpec(def nodegraph.TypeDef, x, y float64) nodegraph.NodeSpec {
	params := make(map[string]any)
	for _, p := range def.Params {
		if p.Default != nil {
			params[p.Name] = p.Default
		} else if p.Required {
			switch p.Type {
			case nodegraph.ParamString:
				if p.Name == "url" {
					params[p.Name] = "https://example.com"
				} else if len(p.Enum) > 0 {
					params[p.Name] = p.Enum[0]
				} else {
					params[p.Name] = "value"
				}
			case nodegraph.ParamDuration:
				params[p.Name] = "1s"
			case nodegraph.ParamInt:
				params[p.Name] = int64(1)
			case nodegraph.ParamNumber:
				params[p.Name] = 1.0
			case nodegraph.ParamBool:
				params[p.Name] = false
			}
		}
	}
	if len(params) == 0 {
		params = nil
	}

	var caps []nodegraph.Capability
	for _, rule := range def.Capabilities {
		if rule.HostFromURLParam != "" {
			if rawURL, ok := params[rule.HostFromURLParam].(string); ok && rawURL != "" {
				if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
					if c, err := nodegraph.ParseCapability(rule.Kind + ":" + u.Hostname()); err == nil {
						caps = append(caps, c)
					}
				}
			}
		} else if !rule.RequireScope {
			if c, err := nodegraph.ParseCapability(rule.Kind); err == nil {
				caps = append(caps, c)
			}
		} else if rule.Scope != "" {
			if c, err := nodegraph.ParseCapability(rule.Kind + ":" + rule.Scope); err == nil {
				caps = append(caps, c)
			}
		}
	}

	return nodegraph.NodeSpec{
		Type:         def.ID,
		Capabilities: caps,
		Params:       params,
		UI: &nodegraph.NodeUI{
			X: math.Round(x),
			Y: math.Round(y),
		},
	}
}

// ParseParamInput parses a user-entered string according to def.Type, returning
// deleteParam=true when raw is blank for an optional parameter (reverting to default).
func ParseParamInput(def nodegraph.ParamDef, raw string) (any, bool, error) {
	return parseParamInput(def, raw)
}

func parseParamInput(def nodegraph.ParamDef, raw string) (any, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" && !def.Required {
		return nil, true, nil // delete param -> revert to default
	}
	switch def.Type {
	case nodegraph.ParamString:
		return raw, false, nil
	case nodegraph.ParamDuration:
		if _, err := time.ParseDuration(trimmed); err != nil {
			return nil, false, fmt.Errorf("%w: invalid duration %q", nodegraph.ErrInvalidParam, trimmed)
		}
		return trimmed, false, nil
	case nodegraph.ParamBool:
		b, err := strconv.ParseBool(trimmed)
		if err != nil {
			return nil, false, fmt.Errorf("%w: expected true or false", nodegraph.ErrInvalidParam)
		}
		return b, false, nil
	case nodegraph.ParamInt:
		v, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("%w: expected integer", nodegraph.ErrInvalidParam)
		}
		return v, false, nil
	case nodegraph.ParamNumber:
		v, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false, fmt.Errorf("%w: expected finite number", nodegraph.ErrInvalidParam)
		}
		return v, false, nil
	case nodegraph.ParamStrings:
		parts := strings.Split(trimmed, ",")
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			s := strings.TrimSpace(p)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, false, nil
	case nodegraph.ParamJSON:
		var val any
		dec := json.NewDecoder(strings.NewReader(trimmed))
		if err := dec.Decode(&val); err != nil {
			return nil, false, fmt.Errorf("%w: invalid JSON: %v", nodegraph.ErrInvalidParam, err)
		}
		return val, false, nil
	default:
		return raw, false, nil
	}
}

func summarizeDiff(d nodegraph.SpecDiff) string {
	if d.IsEmpty() {
		return "No changes"
	}
	if !d.HasRuntimeChanges() {
		return "Layout/UI updated (0 node restarts)"
	}
	var parts []string
	if len(d.AddedNodes) > 0 {
		parts = append(parts, fmt.Sprintf("+%d node", len(d.AddedNodes)))
	}
	if len(d.RemovedNodes) > 0 {
		parts = append(parts, fmt.Sprintf("-%d node", len(d.RemovedNodes)))
	}
	if len(d.ModifiedNodes) > 0 {
		parts = append(parts, fmt.Sprintf("~%d node", len(d.ModifiedNodes)))
	}
	if len(d.AddedEdges) > 0 {
		parts = append(parts, fmt.Sprintf("+%d edge", len(d.AddedEdges)))
	}
	if len(d.RemovedEdges) > 0 {
		parts = append(parts, fmt.Sprintf("-%d edge", len(d.RemovedEdges)))
	}
	if len(parts) == 0 {
		return "Spec updated"
	}
	return "Applied: " + strings.Join(parts, ", ")
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func padBlockExact(content string, cols, rows int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, strings.Repeat(" ", cols))
	}
	return strings.Join(lines, "\n")
}
