// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"cmp"
	"fmt"
	"image"
	"math"
	"slices"
	"strconv"
	"strings"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

// World-space layout constants (1.0 zoom = 1 world unit per pixel in vector
// mode, and 10x20 world units per terminal cell in grid mode).
const (
	DefaultNodeWidth     = 216.0
	ExpandedNodeWidth    = 244.0
	HeaderHeight         = 30.0
	PortRowHeight        = 22.0
	SummaryRowHeight     = 20.0
	DrawerHeaderHeight   = 20.0
	InlineParamRowHeight = 22.0
	MaxInlineParamRows   = 8
	PreviewBoxHeight     = 100.0
	CardBottomPad        = 10.0
	CollapsedNodeHeight  = 36.0
	PortSocketRadius     = 6.0
	PortHitRadius        = 13.0
	WireHitThreshold     = 9.0
	GroupPadX            = 24.0
	GroupPadTop          = 36.0
	GroupPadBottom       = 20.0
	GroupHeaderHeight    = 26.0

	MinZoom = 0.15
	MaxZoom = 4.00

	GridCellWorldW = 10.0
	GridCellWorldH = 20.0

	DefaultColumnGap = 104.0
	DefaultRowGap    = 48.0
	DefaultOriginX   = 56.0
	DefaultOriginY   = 64.0
)

// LayoutDirection controls the primary dataflow axis for automatic graph arrangement.
type LayoutDirection uint8

const (
	// LayoutLeftToRight arranges topological stages in columns from left to right.
	LayoutLeftToRight LayoutDirection = iota
	// LayoutTopToBottom arranges topological stages in rows from top to bottom.
	LayoutTopToBottom
)

// LayoutOptions configures automatic topological node arrangement for AutoLayout
// and fallback positioning when NodeSpec.UI is omitted.
type LayoutOptions struct {
	Direction      LayoutDirection
	ColumnGap      float64 // Clear horizontal gap between columns (<= 0 uses DefaultColumnGap)
	RowGap         float64 // Clear vertical gap between sibling cards (<= 0 uses DefaultRowGap)
	OriginX        float64 // Left world margin (<= 0 uses DefaultOriginX)
	OriginY        float64 // Top world margin (<= 0 uses DefaultOriginY)
	DisableCenterY bool    // When false (default), shorter columns are vertically centered against the tallest column
}

// DefaultLayoutOptions returns the standard spacious Sugiyama/Kahn layout settings.
func DefaultLayoutOptions() LayoutOptions {
	return LayoutOptions{
		Direction: LayoutLeftToRight,
		ColumnGap: DefaultColumnGap,
		RowGap:    DefaultRowGap,
		OriginX:   DefaultOriginX,
		OriginY:   DefaultOriginY,
	}
}

func (o LayoutOptions) normalized() LayoutOptions {
	if o.ColumnGap <= 0 {
		o.ColumnGap = DefaultColumnGap
	}
	if o.RowGap <= 0 {
		o.RowGap = DefaultRowGap
	}
	if o.OriginX <= 0 {
		o.OriginX = DefaultOriginX
	}
	if o.OriginY <= 0 {
		o.OriginY = DefaultOriginY
	}
	return o
}

// Point is a 2D point in world or screen space.
type Point struct {
	X, Y float64
}

// Rect is an axis-aligned rectangle in world space.
type Rect struct {
	X, Y, W, H float64
}

// Contains reports whether (px, py) lies inside r.
func (r Rect) Contains(px, py float64) bool {
	return px >= r.X && px <= r.X+r.W && py >= r.Y && py <= r.Y+r.H
}

// Expand returns r padded outward by (dx, dy) on each side.
func (r Rect) Expand(dx, dy float64) Rect {
	return Rect{
		X: r.X - dx,
		Y: r.Y - dy,
		W: max(0, r.W+2*dx),
		H: max(0, r.H+2*dy),
	}
}

// Union returns the smallest bounding rectangle enclosing both r and other.
func (r Rect) Union(other Rect) Rect {
	if r.W <= 0 || r.H <= 0 {
		return other
	}
	if other.W <= 0 || other.H <= 0 {
		return r
	}
	x0 := min(r.X, other.X)
	y0 := min(r.Y, other.Y)
	x1 := max(r.X+r.W, other.X+other.W)
	y1 := max(r.Y+r.H, other.Y+other.H)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// Intersects reports whether r and other overlap.
func (r Rect) Intersects(other Rect) bool {
	return r.X <= other.X+other.W &&
		r.X+r.W >= other.X &&
		r.Y <= other.Y+other.H &&
		r.Y+r.H >= other.Y
}

// NormalizedRect constructs an axis-aligned Rect spanning arbitrary corner points a and b.
func NormalizedRect(a, b Point) Rect {
	x0 := min(a.X, b.X)
	y0 := min(a.Y, b.Y)
	return Rect{
		X: x0,
		Y: y0,
		W: math.Abs(b.X - a.X),
		H: math.Abs(b.Y - a.Y),
	}
}

// Camera maps between unbounded world coordinates and viewport pixel/cell
// coordinates.
type Camera struct {
	PanX float64
	PanY float64
	Zoom float64
}

// DefaultCamera returns a 1.0x camera anchored near top-left with a small margin.
func DefaultCamera() Camera {
	return Camera{PanX: -24, PanY: -24, Zoom: 1.0}
}

func (c Camera) normalized() Camera {
	out := c
	if math.IsNaN(out.PanX) || math.IsInf(out.PanX, 0) {
		out.PanX = 0
	}
	if math.IsNaN(out.PanY) || math.IsInf(out.PanY, 0) {
		out.PanY = 0
	}
	if math.IsNaN(out.Zoom) || math.IsInf(out.Zoom, 0) || out.Zoom <= 0 {
		out.Zoom = 1.0
	}
	out.Zoom = clampFloat(out.Zoom, MinZoom, MaxZoom)
	return out
}

// WorldToScreen converts a world coordinate to viewport pixel coordinates.
func (c Camera) WorldToScreen(wx, wy float64) Point {
	nc := c.normalized()
	return Point{
		X: (wx - nc.PanX) * nc.Zoom,
		Y: (wy - nc.PanY) * nc.Zoom,
	}
}

// ScreenToWorld converts viewport pixel coordinates to world coordinates.
func (c Camera) ScreenToWorld(sx, sy float64) Point {
	nc := c.normalized()
	return Point{
		X: nc.PanX + sx/nc.Zoom,
		Y: nc.PanY + sy/nc.Zoom,
	}
}

// CellToWorld converts terminal cell coordinates (with fractional sub-cell center)
// into world coordinates.
func (c Camera) CellToWorld(col, row float64, cellPxW, cellPxH int) Point {
	pw := float64(max(1, cellPxW))
	ph := float64(max(1, cellPxH))
	return c.ScreenToWorld(col*pw, row*ph)
}

// WorldToGrid converts a world coordinate into grid cell space.
func (c Camera) WorldToGrid(wx, wy float64) (col, row int) {
	nc := c.normalized()
	gx := ((wx - nc.PanX) * nc.Zoom) / GridCellWorldW
	gy := ((wy - nc.PanY) * nc.Zoom) / GridCellWorldH
	return int(math.Round(gx)), int(math.Round(gy))
}

// GridToWorld converts grid cell coordinates back to world coordinates.
func (c Camera) GridToWorld(col, row float64) Point {
	nc := c.normalized()
	return Point{
		X: nc.PanX + (col*GridCellWorldW)/nc.Zoom,
		Y: nc.PanY + (row*GridCellWorldH)/nc.Zoom,
	}
}

// ZoomAt adjusts Zoom by factor while keeping the world point under (screenX, screenY)
// stationary.
func (c *Camera) ZoomAt(factor, screenX, screenY float64) {
	nc := c.normalized()
	before := nc.ScreenToWorld(screenX, screenY)
	nc.Zoom = clampFloat(nc.Zoom*factor, MinZoom, MaxZoom)
	nc.PanX = before.X - screenX/nc.Zoom
	nc.PanY = before.Y - screenY/nc.Zoom
	*c = nc
}

// FitBounds frames worldBounds inside a pixel viewport of (viewPxW, viewPxH) with
// comfortable margin.
func (c *Camera) FitBounds(worldBounds Rect, viewPxW, viewPxH int) {
	if worldBounds.W <= 0 || worldBounds.H <= 0 || viewPxW <= 0 || viewPxH <= 0 {
		*c = DefaultCamera()
		return
	}
	padded := worldBounds.Expand(48, 48)
	zx := float64(viewPxW) / padded.W
	zy := float64(viewPxH) / padded.H
	zoom := clampFloat(min(zx, zy), MinZoom, 1.25)
	panX := padded.X - (float64(viewPxW)/zoom-padded.W)*0.5
	panY := padded.Y - (float64(viewPxH)/zoom-padded.H)*0.5
	*c = Camera{PanX: panX, PanY: panY, Zoom: zoom}
}

// Selection describes the currently selected graph element(s).
type Selection struct {
	Nodes []string
	Edge  *nodegraph.EdgeSpec
	Group string
}

// PrimaryNode returns the first selected node ID, if any.
func (s Selection) PrimaryNode() string {
	if len(s.Nodes) == 0 {
		return ""
	}
	return s.Nodes[0]
}

// HasNode reports whether id is currently selected.
func (s Selection) HasNode(id string) bool {
	return slices.Contains(s.Nodes, id)
}

// PortAnchor describes a rendered inlet or outlet socket in world coordinates.
type PortAnchor struct {
	NodeID      string
	Name        string
	Type        nodegraph.DataType
	Required    bool
	IsOutlet    bool
	Connected   bool
	Hovered     bool
	CompatState PortCompatState
	Center      Point
}

// PortCompatState indicates how a port relates to an active wire-drag gesture.
type PortCompatState uint8

const (
	PortCompatNeutral PortCompatState = iota
	PortCompatValid
	PortCompatInvalid
)

// NodePreview holds a host-supplied live output preview for a single node card
// (an RGBA thumbnail for frame/texture outlets and/or formatted text lines for
// table, document, JSON, or scalar outlets).
type NodePreview struct {
	Image   *image.RGBA
	Lines   []string
	Summary string
	Outlet  string
	Active  bool
}

// InlineParamKind classifies the interactive control rendered inside a node's
// collapsible inline parameter drawer.
type InlineParamKind uint8

const (
	InlineParamText InlineParamKind = iota
	InlineParamEnum
	InlineParamBool
	InlineParamFloat
	InlineParamInt
)

// InlineParamAction identifies which sub-control of an InlineParamRow was hit.
type InlineParamAction uint8

const (
	ParamActionNone InlineParamAction = iota
	ParamActionToggleBool
	ParamActionEnumPrev
	ParamActionEnumNext
	ParamActionStepDec
	ParamActionStepInc
	ParamActionScrubSlider
	ParamActionEditInspector
)

// InlineParamRow describes a single parameter row inside a node card's
// collapsible inline parameter drawer.
type InlineParamRow struct {
	Key          string
	Label        string
	DisplayValue string
	Kind         InlineParamKind
	Fraction     float64
	HasRange     bool
	Min          float64
	Max          float64
	BoolVal      bool
	EnumValues   []string
	ParamDef     nodegraph.ParamDef
	Bounds       Rect
	ControlRect  Rect
	TrackRect    Rect
	DecRect      Rect
	IncRect      Rect
}

// SceneOptions configures layout, expanded inline drawers, and live previews
// when building a Scene.
type SceneOptions struct {
	Layout          LayoutOptions
	ParamsExpanded  map[string]bool
	PreviewExpanded map[string]bool
	NodePreviews    map[string]NodePreview
	ViewerNodeID    string
}

// NodeBox is the computed visual geometry of a single node or collapsed group proxy.
type NodeBox struct {
	ID             string
	Title          string
	Subtitle       string
	Namespace      string
	IsSubgraph     bool
	Bounds         Rect
	Collapsed      bool
	Selected       bool
	Hovered        bool
	GroupHidden    bool
	CapBadges      []string
	BoundaryBadges []string
	ParamSummary   []string
	Inlets         []PortAnchor
	Outlets        []PortAnchor

	HasParams        bool
	ParamsOpen       bool
	ParamRows        []InlineParamRow
	ParamsHeaderRect Rect

	HasOutlets         bool
	PreviewOpen        bool
	Preview            *NodePreview
	PreviewHeaderRect  Rect
	PreviewContentRect Rect

	ViewerActive      bool
	ViewerBadgeRect   Rect
	ParamsBadgeRect   Rect
	PreviewBadgeRect  Rect
	CollapseBadgeRect Rect
}

// GroupBox is the computed visual geometry of an editor UI group.
type GroupBox struct {
	ID        string
	Label     string
	Bounds    Rect
	Collapsed bool
	Selected  bool
	Hovered   bool
	Members   []string
}

// WirePath is the computed Bezier and orthogonal geometry of an edge.
type WirePath struct {
	Edge      nodegraph.EdgeSpec
	FromPos   Point
	Ctrl1     Point
	Ctrl2     Point
	ToPos     Point
	Waypoints []Point
	DataType  nodegraph.DataType
	Compat    nodegraph.Compatibility
	Coercion  nodegraph.CoercionKind
	Selected  bool
	Hovered   bool
}

// WireDragPreview describes an in-progress interactive port connection gesture.
type WireDragPreview struct {
	Active     bool
	FromOutlet bool
	Anchor     nodegraph.PortRef
	AnchorType nodegraph.DataType
	AnchorPos  Point
	CursorPos  Point
	SnapRadius float64
	SnapTarget *PortAnchor
	Kind       string
}

// ContextMenuOverlay describes a floating right-click context menu anchored in
// canvas cell coordinates (AnchorCol, AnchorRow).
type ContextMenuOverlay struct {
	Title      string
	AnchorCol  int
	AnchorRow  int
	CanvasCols int
	CanvasRows int
	Items      []string
	HoverIdx   int
}

// CellBounds returns the clamped inclusive cell rectangle (c0, r0, c1, r1) for
// rendering and hit-testing the context menu inside a (cols x rows) canvas.
func (cm ContextMenuOverlay) CellBounds(cols, rows int) (c0, r0, c1, r1 int) {
	maxLen := len([]rune(cm.Title)) + 4
	for _, it := range cm.Items {
		if l := len([]rune(it)) + 6; l > maxLen {
			maxLen = l
		}
	}
	menuW := min(max(18, maxLen), max(10, cols-2))
	menuH := len(cm.Items) + 3 // top border/title + items + bottom border
	c0 = min(max(0, cm.AnchorCol), max(0, cols-menuW))
	r0 = min(max(0, cm.AnchorRow), max(0, rows-menuH))
	c1 = min(cols-1, c0+menuW-1)
	r1 = min(rows-1, r0+menuH-1)
	return c0, r0, c1, r1
}

// ItemAt returns the menu item index at canvas cell (col, row), and whether the
// coordinate landed anywhere inside the context menu box.
func (cm ContextMenuOverlay) ItemAt(col, row, cols, rows int) (itemIdx int, inside bool) {
	c0, r0, c1, r1 := cm.CellBounds(cols, rows)
	if col < c0 || col > c1 || row < r0 || row > r1 {
		return -1, false
	}
	idx := row - (r0 + 2)
	if idx >= 0 && idx < len(cm.Items) {
		return idx, true
	}
	return -1, true
}

// Scene is a deterministic, immutable layout snapshot of a Spec ready for both
// vector and cell-grid rendering as well as mouse hit-testing.
type Scene struct {
	Groups      []GroupBox
	Nodes       []NodeBox
	NodeByID    map[string]int
	Wires       []WirePath
	DragPreview *WireDragPreview
	Marquee     *Rect
	ContextMenu *ContextMenuOverlay
	WorldBounds Rect
}

// ApplyHover marks the hovered element in sc from hit.
func (sc *Scene) ApplyHover(hit HitResult) {
	if sc == nil {
		return
	}
	switch hit.Kind {
	case HitPort:
		if idx, ok := sc.NodeByID[hit.Port.NodeID]; ok {
			nb := &sc.Nodes[idx]
			if hit.Port.IsOutlet {
				for i := range nb.Outlets {
					if nb.Outlets[i].Name == hit.Port.Name {
						nb.Outlets[i].Hovered = true
					}
				}
			} else {
				for i := range nb.Inlets {
					if nb.Inlets[i].Name == hit.Port.Name {
						nb.Inlets[i].Hovered = true
					}
				}
			}
		}
	case HitNode:
		if idx, ok := sc.NodeByID[hit.NodeID]; ok {
			sc.Nodes[idx].Hovered = true
		}
	case HitWire:
		for i := range sc.Wires {
			if sc.Wires[i].Edge == hit.Edge {
				sc.Wires[i].Hovered = true
			}
		}
	case HitGroup:
		for i := range sc.Groups {
			if sc.Groups[i].ID == hit.GroupID {
				sc.Groups[i].Hovered = true
			}
		}
	}
}

// BuildScene computes a deterministic Scene for spec against reg using DefaultLayoutOptions.
func BuildScene(
	spec *nodegraph.Spec,
	reg *nodegraph.Registry,
	sel Selection,
	drag *WireDragPreview,
) *Scene {
	return BuildSceneWithLayout(spec, reg, sel, drag, DefaultLayoutOptions())
}

// BuildSceneWithLayout computes a deterministic Scene using explicit fallback LayoutOptions.
func BuildSceneWithLayout(
	spec *nodegraph.Spec,
	reg *nodegraph.Registry,
	sel Selection,
	drag *WireDragPreview,
	layoutOpts LayoutOptions,
) *Scene {
	return BuildSceneWithOptions(spec, reg, sel, drag, SceneOptions{Layout: layoutOpts})
}

// BuildSceneWithOptions computes a deterministic Scene including any expanded
// inline parameter drawers, live output preview drawers, and viewer tap badges.
func BuildSceneWithOptions(
	spec *nodegraph.Spec,
	reg *nodegraph.Registry,
	sel Selection,
	drag *WireDragPreview,
	sceneOpts SceneOptions,
) *Scene {
	sc := &Scene{
		NodeByID: make(map[string]int),
	}
	if spec == nil {
		return sc
	}

	// Track which nodes belong to collapsed groups.
	collapsedGroupByNode := make(map[string]string)
	groupIDs := spec.SortedGroupIDs()
	for _, gid := range groupIDs {
		g := spec.Groups[gid]
		if g.Collapsed {
			for _, member := range g.Nodes {
				if _, ok := collapsedGroupByNode[member]; !ok {
					collapsedGroupByNode[member] = gid
				}
			}
		}
	}

	// Count connected ports so sockets render filled vs hollow (including internal
	// ports fed by exposed component boundary inlets/outlets).
	boundaryBadgesByNode, boundaryFedInlets, boundarySourcedOutlets := computeNodeBoundaryBadges(spec)
	inletConnected := make(map[nodegraph.PortRef]bool, len(spec.Edges)+len(boundaryFedInlets))
	outletConnected := make(map[nodegraph.PortRef]bool, len(spec.Edges)+len(boundarySourcedOutlets))
	for _, e := range spec.Edges {
		outletConnected[e.From] = true
		inletConnected[e.To] = true
	}
	for ref := range boundaryFedInlets {
		inletConnected[ref] = true
	}
	for ref := range boundarySourcedOutlets {
		outletConnected[ref] = true
	}

	// Deterministic fallback coordinates when UI is nil.
	fallbackPos := computeLayeredPositionsWithScene(spec, reg, sceneOpts)

	nodeIDs := spec.SortedNodeIDs()
	// Render selected nodes last so they sit cleanly on top of overlapping cards.
	orderedIDs := make([]string, 0, len(nodeIDs))
	var selectedOrdered []string
	for _, id := range nodeIDs {
		if sel.HasNode(id) {
			selectedOrdered = append(selectedOrdered, id)
		} else {
			orderedIDs = append(orderedIDs, id)
		}
	}
	orderedIDs = append(orderedIDs, selectedOrdered...)

	for _, id := range orderedIDs {
		nspec := spec.Nodes[id]
		def, hasDef := lookupTypeDef(reg, nspec.Type, spec, id)

		pos := fallbackPos[id]
		var label string
		collapsed := false
		if nspec.UI != nil {
			pos = Point{X: sanitizeCoord(nspec.UI.X), Y: sanitizeCoord(nspec.UI.Y)}
			label = nspec.UI.Label
			collapsed = nspec.UI.Collapsed
		}

		title := id
		if label != "" {
			title = fmt.Sprintf("%s (%s)", label, id)
		}
		isSubgraph := nspec.Type.Namespace == nodegraph.NamespaceGraph
		subtitle := nspec.Type.String()
		if isSubgraph {
			subtitle = "◈ " + subtitle
		}
		if !hasDef {
			subtitle += " [unregistered]"
		}

		var capBadges []string
		for _, c := range nspec.Capabilities {
			capBadges = append(capBadges, c.String())
		}
		slices.Sort(capBadges)
		boundaryBadges := boundaryBadgesByNode[id]

		paramSummary := formatParamSummaries(nspec, def)
		hasParams := len(def.Params) > 0
		hasOutlets := len(def.Outlets) > 0
		paramsOpen := !collapsed && hasParams && sceneOpts.ParamsExpanded != nil && sceneOpts.ParamsExpanded[id]
		previewOpen := !collapsed && hasOutlets && sceneOpts.PreviewExpanded != nil && sceneOpts.PreviewExpanded[id]
		viewerActive := sceneOpts.ViewerNodeID != "" && sceneOpts.ViewerNodeID == id

		cardW := DefaultNodeWidth
		if paramsOpen || previewOpen {
			cardW = ExpandedNodeWidth
		}

		portRows := max(len(def.Inlets), len(def.Outlets))
		var (
			paramRows          []InlineParamRow
			paramsHeaderRect   Rect
			previewHeaderRect  Rect
			previewContentRect Rect
			previewPtr         *NodePreview
		)

		if sceneOpts.NodePreviews != nil {
			if pv, ok := sceneOpts.NodePreviews[id]; ok {
				cp := pv
				if len(pv.Lines) > 0 {
					cp.Lines = slices.Clone(pv.Lines)
				}
				previewPtr = &cp
			}
		}

		cardH := CollapsedNodeHeight
		if !collapsed {
			curY := pos.Y + HeaderHeight + float64(max(1, portRows))*PortRowHeight
			if len(capBadges) > 0 {
				curY += SummaryRowHeight
			}
			if len(boundaryBadges) > 0 {
				curY += SummaryRowHeight
			}
			if paramsOpen {
				paramsHeaderRect = Rect{X: pos.X + 6, Y: curY + 2, W: cardW - 12, H: DrawerHeaderHeight - 2}
				curY += DrawerHeaderHeight
				paramRows = buildInlineParamRows(nspec, def, pos.X, curY, cardW)
				curY += float64(len(paramRows)) * InlineParamRowHeight
			} else {
				curY += float64(min(2, len(paramSummary))) * SummaryRowHeight
			}
			if previewOpen {
				previewHeaderRect = Rect{X: pos.X + 6, Y: curY + 2, W: cardW - 12, H: DrawerHeaderHeight - 2}
				curY += DrawerHeaderHeight
				previewContentRect = Rect{X: pos.X + 8, Y: curY + 2, W: cardW - 16, H: PreviewBoxHeight - 4}
				curY += PreviewBoxHeight
			}
			cardH = (curY - pos.Y) + CardBottomPad
		}

		// Compute right-aligned header badge rects (leaving [pos.X .. pos.X+138] open for title & dragging).
		badgeRight := pos.X + cardW - 4
		collapseRect := Rect{X: badgeRight - 16, Y: pos.Y + 4, W: 16, H: HeaderHeight - 8}
		badgeRight -= 18
		var previewBadgeRect, paramsBadgeRect, viewerBadgeRect Rect
		if !collapsed {
			if hasOutlets {
				previewBadgeRect = Rect{X: badgeRight - 16, Y: pos.Y + 4, W: 16, H: HeaderHeight - 8}
				badgeRight -= 18
			}
			if hasParams {
				paramsBadgeRect = Rect{X: badgeRight - 16, Y: pos.Y + 4, W: 16, H: HeaderHeight - 8}
				badgeRight -= 18
			}
			if hasOutlets {
				viewerBadgeRect = Rect{X: badgeRight - 16, Y: pos.Y + 4, W: 16, H: HeaderHeight - 8}
			}
		}

		nb := NodeBox{
			ID:                 id,
			Title:              title,
			Subtitle:           subtitle,
			Namespace:          nspec.Type.Namespace,
			IsSubgraph:         isSubgraph,
			Bounds:             Rect{X: pos.X, Y: pos.Y, W: cardW, H: cardH},
			Collapsed:          collapsed,
			Selected:           sel.HasNode(id),
			GroupHidden:        collapsedGroupByNode[id] != "",
			CapBadges:          capBadges,
			BoundaryBadges:     boundaryBadges,
			ParamSummary:       paramSummary,
			HasParams:          hasParams,
			ParamsOpen:         paramsOpen,
			ParamRows:          paramRows,
			ParamsHeaderRect:   paramsHeaderRect,
			HasOutlets:         hasOutlets,
			PreviewOpen:        previewOpen,
			Preview:            previewPtr,
			PreviewHeaderRect:  previewHeaderRect,
			PreviewContentRect: previewContentRect,
			ViewerActive:       viewerActive,
			ViewerBadgeRect:    viewerBadgeRect,
			ParamsBadgeRect:    paramsBadgeRect,
			PreviewBadgeRect:   previewBadgeRect,
			CollapseBadgeRect:  collapseRect,
		}

		for i, in := range def.Inlets {
			py := pos.Y + HeaderHeight*0.5
			if !collapsed {
				py = pos.Y + HeaderHeight + (float64(i)+0.5)*PortRowHeight
			}
			ref := nodegraph.PortRef{Node: id, Port: in.Name}
			nb.Inlets = append(nb.Inlets, PortAnchor{
				NodeID:      id,
				Name:        in.Name,
				Type:        in.Type,
				Required:    in.Required,
				IsOutlet:    false,
				Connected:   inletConnected[ref],
				CompatState: evalDragCompat(drag, id, in.Type, false),
				Center:      Point{X: pos.X, Y: py},
			})
		}

		for i, out := range def.Outlets {
			py := pos.Y + HeaderHeight*0.5
			if !collapsed {
				py = pos.Y + HeaderHeight + (float64(i)+0.5)*PortRowHeight
			}
			ref := nodegraph.PortRef{Node: id, Port: out.Name}
			nb.Outlets = append(nb.Outlets, PortAnchor{
				NodeID:      id,
				Name:        out.Name,
				Type:        out.Type,
				Required:    out.Required,
				IsOutlet:    true,
				Connected:   outletConnected[ref],
				CompatState: evalDragCompat(drag, id, out.Type, true),
				Center:      Point{X: pos.X + cardW, Y: py},
			})
		}

		sc.NodeByID[id] = len(sc.Nodes)
		sc.Nodes = append(sc.Nodes, nb)
		if !nb.GroupHidden {
			sc.WorldBounds = sc.WorldBounds.Union(nb.Bounds)
		}
	}

	// Build GroupBoxes in deterministic group ID order.
	collapsedGroupBounds := make(map[string]Rect)
	for _, gid := range groupIDs {
		g := spec.Groups[gid]
		label := g.Label
		if label == "" {
			label = gid
		}
		var memberBounds Rect
		validMembers := make([]string, 0, len(g.Nodes))
		for _, mid := range g.Nodes {
			if idx, ok := sc.NodeByID[mid]; ok {
				validMembers = append(validMembers, mid)
				memberBounds = memberBounds.Union(sc.Nodes[idx].Bounds)
			}
		}
		slices.Sort(validMembers)

		var bounds Rect
		switch {
		case len(validMembers) > 0:
			bounds = Rect{
				X: memberBounds.X - GroupPadX,
				Y: memberBounds.Y - GroupPadTop,
				W: memberBounds.W + 2*GroupPadX,
				H: memberBounds.H + GroupPadTop + GroupPadBottom,
			}
			if g.UI != nil && g.UI.Width > 0 && g.UI.Height > 0 {
				explicit := Rect{
					X: sanitizeCoord(g.UI.X),
					Y: sanitizeCoord(g.UI.Y),
					W: sanitizeCoord(g.UI.Width),
					H: sanitizeCoord(g.UI.Height),
				}
				bounds = bounds.Union(explicit)
			}
		case g.UI != nil:
			w := max(180.0, sanitizeCoord(g.UI.Width))
			h := max(90.0, sanitizeCoord(g.UI.Height))
			bounds = Rect{X: sanitizeCoord(g.UI.X), Y: sanitizeCoord(g.UI.Y), W: w, H: h}
		default:
			bounds = Rect{X: 40, Y: 40, W: 220, H: 100}
		}

		if g.Collapsed {
			bounds.W = max(190.0, min(bounds.W, 250.0))
			bounds.H = GroupHeaderHeight + 18.0
			collapsedGroupBounds[gid] = bounds
		}

		gb := GroupBox{
			ID:        gid,
			Label:     label,
			Bounds:    bounds,
			Collapsed: g.Collapsed,
			Selected:  sel.Group == gid,
			Members:   validMembers,
		}
		sc.Groups = append(sc.Groups, gb)
		sc.WorldBounds = sc.WorldBounds.Union(bounds)
	}

	// Re-pin ports of nodes hidden inside a collapsed group onto the group's left/right edges
	// so external wires continue routing cleanly!
	for i := range sc.Nodes {
		nb := &sc.Nodes[i]
		gid := collapsedGroupByNode[nb.ID]
		if gid == "" {
			continue
		}
		gb, ok := collapsedGroupBounds[gid]
		if !ok {
			continue
		}
		midY := gb.Y + gb.H*0.5
		for j := range nb.Inlets {
			nb.Inlets[j].Center = Point{X: gb.X, Y: midY}
		}
		for j := range nb.Outlets {
			nb.Outlets[j].Center = Point{X: gb.X + gb.W, Y: midY}
		}
	}

	// Build deterministic sorted WirePaths.
	sortedEdges := append([]nodegraph.EdgeSpec(nil), spec.Edges...)
	slices.SortFunc(sortedEdges, nodegraph.CompareEdgeSpec)
	for _, edge := range sortedEdges {
		// Skip completely internal edges inside the same collapsed group.
		gFrom := collapsedGroupByNode[edge.From.Node]
		gTo := collapsedGroupByNode[edge.To.Node]
		if gFrom != "" && gFrom == gTo {
			continue
		}

		fromAnchor, okFrom := sc.findPort(edge.From.Node, edge.From.Port, true)
		toAnchor, okTo := sc.findPort(edge.To.Node, edge.To.Port, false)
		if !okFrom || !okTo {
			continue
		}

		compat, coerce, _ := nodegraph.CheckCompatibility(fromAnchor.Type, toAnchor.Type)
		c1, c2 := bezierControls(fromAnchor.Center, toAnchor.Center)
		isSelected := sel.Edge != nil && *sel.Edge == edge

		sc.Wires = append(sc.Wires, WirePath{
			Edge:      edge,
			FromPos:   fromAnchor.Center,
			Ctrl1:     c1,
			Ctrl2:     c2,
			ToPos:     toAnchor.Center,
			Waypoints: orthogonalWaypoints(fromAnchor.Center, toAnchor.Center),
			DataType:  fromAnchor.Type,
			Compat:    compat,
			Coercion:  coerce,
			Selected:  isSelected,
		})
	}

	if drag != nil && drag.Active {
		cp := *drag
		if anchor, ok := sc.findPort(cp.Anchor.Node, cp.Anchor.Port, cp.FromOutlet); ok {
			cp.AnchorPos = anchor.Center
			cp.AnchorType = anchor.Type
		}
		// Snap to nearby compatible port if within PortHitRadius*1.4.
		if target := sc.findSnapPort(&cp); target != nil {
			cp.SnapTarget = target
			cp.CursorPos = target.Center
		}
		sc.DragPreview = &cp
	}

	return sc
}

func (sc *Scene) findPort(nodeID, portName string, isOutlet bool) (PortAnchor, bool) {
	idx, ok := sc.NodeByID[nodeID]
	if !ok {
		return PortAnchor{}, false
	}
	nb := sc.Nodes[idx]
	ports := nb.Inlets
	if isOutlet {
		ports = nb.Outlets
	}
	for _, p := range ports {
		if p.Name == portName {
			return p, true
		}
	}
	return PortAnchor{}, false
}

func (sc *Scene) findSnapPort(drag *WireDragPreview) *PortAnchor {
	wantOutlet := !drag.FromOutlet
	maxDist := max(PortHitRadius*1.6, drag.SnapRadius)
	unconnectedSlack := 0.0
	if !wantOutlet {
		// When terminal cell quantization spans multiple stacked inlets on a card,
		// prefer an open compatible inlet over an already-wired sibling inside the
		// same snap neighborhood unless the connected inlet is the only one in range
		// or much closer.
		unconnectedSlack = max(PortRowHeight*0.85, maxDist*0.45)
	}
	bestScore := math.MaxFloat64
	var best *PortAnchor
	for i := len(sc.Nodes) - 1; i >= 0; i-- {
		nb := &sc.Nodes[i]
		if nb.GroupHidden || nb.ID == drag.Anchor.Node {
			continue
		}
		ports := nb.Inlets
		if wantOutlet {
			ports = nb.Outlets
		}
		for j := range ports {
			p := &ports[j]
			if p.CompatState != PortCompatValid {
				continue
			}
			d := math.Hypot(p.Center.X-drag.CursorPos.X, p.Center.Y-drag.CursorPos.Y)
			if d > maxDist {
				continue
			}
			score := d
			if !wantOutlet && p.Connected {
				score += unconnectedSlack
			}
			if score < bestScore {
				bestScore = score
				cp := *p
				best = &cp
			}
		}
	}
	return best
}

// HitKind classifies what lies under a world-space cursor coordinate.
type HitKind uint8

const (
	HitNone HitKind = iota
	HitPort
	HitNode
	HitWire
	HitGroup
)

// HitResult describes the topmost interactive element at a world-space point.
type HitResult struct {
	Kind           HitKind
	NodeID         string
	Port           PortAnchor
	Edge           nodegraph.EdgeSpec
	GroupID        string
	InHeader       bool
	ToggleCollapse bool
	ToggleParams   bool
	TogglePreview  bool
	ToggleViewer   bool
	ParamKey       string
	ParamAction    InlineParamAction
	ParamRow       *InlineParamRow
}

// HitTest finds the topmost interactive element at (wx, wy) in world space.
func (sc *Scene) HitTest(wx, wy float64) HitResult {
	return sc.HitTestScaled(wx, wy, PortHitRadius, WireHitThreshold)
}

// HitTestScaled finds the topmost interactive element at (wx, wy) using custom
// world-space radii (for example, expanded to match terminal cell size in Grid
// mode or zoomed-out viewports).
func (sc *Scene) HitTestScaled(wx, wy, portRadius, wireRadius float64) HitResult {
	if sc == nil {
		return HitResult{}
	}
	if portRadius <= 0 {
		portRadius = PortHitRadius
	}
	if wireRadius <= 0 {
		wireRadius = WireHitThreshold
	}

	// 1. Port sockets take highest priority for crisp wire dragging.
	bestPortDist := portRadius
	var hitPort *PortAnchor
	for i := len(sc.Nodes) - 1; i >= 0; i-- {
		nb := &sc.Nodes[i]
		if nb.GroupHidden {
			continue
		}
		for j := range nb.Outlets {
			p := &nb.Outlets[j]
			if d := math.Hypot(wx-p.Center.X, wy-p.Center.Y); d <= bestPortDist {
				bestPortDist = d
				cp := *p
				hitPort = &cp
			}
		}
		for j := range nb.Inlets {
			p := &nb.Inlets[j]
			if d := math.Hypot(wx-p.Center.X, wy-p.Center.Y); d <= bestPortDist {
				bestPortDist = d
				cp := *p
				hitPort = &cp
			}
		}
	}
	if hitPort != nil {
		return HitResult{
			Kind:   HitPort,
			NodeID: hitPort.NodeID,
			Port:   *hitPort,
		}
	}

	// 2. Node cards (reverse draw order = top-to-bottom).
	for i := len(sc.Nodes) - 1; i >= 0; i-- {
		nb := sc.Nodes[i]
		if nb.GroupHidden {
			continue
		}
		if nb.Bounds.Contains(wx, wy) {
			inHeader := wy <= nb.Bounds.Y+HeaderHeight
			if inHeader {
				toggleCollapse := wx >= nb.Bounds.X+nb.Bounds.W-22 || (nb.CollapseBadgeRect.W > 0 && nb.CollapseBadgeRect.Contains(wx, wy))
				togglePreview := !toggleCollapse && nb.PreviewBadgeRect.W > 0 && nb.PreviewBadgeRect.Contains(wx, wy)
				toggleParams := !toggleCollapse && !togglePreview && nb.ParamsBadgeRect.W > 0 && nb.ParamsBadgeRect.Contains(wx, wy)
				toggleViewer := !toggleCollapse && !togglePreview && !toggleParams && nb.ViewerBadgeRect.W > 0 && nb.ViewerBadgeRect.Contains(wx, wy)
				return HitResult{
					Kind:           HitNode,
					NodeID:         nb.ID,
					InHeader:       true,
					ToggleCollapse: toggleCollapse,
					ToggleParams:   toggleParams,
					TogglePreview:  togglePreview,
					ToggleViewer:   toggleViewer,
				}
			}
			if !nb.Collapsed {
				if nb.ParamsOpen && nb.ParamsHeaderRect.W > 0 && nb.ParamsHeaderRect.Contains(wx, wy) {
					return HitResult{
						Kind:         HitNode,
						NodeID:       nb.ID,
						ToggleParams: true,
					}
				}
				if nb.PreviewOpen && nb.PreviewHeaderRect.W > 0 && nb.PreviewHeaderRect.Contains(wx, wy) {
					return HitResult{
						Kind:          HitNode,
						NodeID:        nb.ID,
						TogglePreview: true,
					}
				}
				if nb.ParamsOpen {
					for rIdx := range nb.ParamRows {
						row := nb.ParamRows[rIdx]
						if !row.ControlRect.Contains(wx, wy) {
							continue
						}
						act := classifyInlineParamHit(row, wx, wy)
						if act != ParamActionNone {
							cp := row
							return HitResult{
								Kind:        HitNode,
								NodeID:      nb.ID,
								ParamKey:    row.Key,
								ParamAction: act,
								ParamRow:    &cp,
							}
						}
					}
				}
			}
			return HitResult{
				Kind:     HitNode,
				NodeID:   nb.ID,
				InHeader: false,
			}
		}
	}

	// 3. Wires (analytic distance to sampled cubic Bezier).
	bestWireDist := wireRadius
	var hitWire *nodegraph.EdgeSpec
	for i := len(sc.Wires) - 1; i >= 0; i-- {
		w := sc.Wires[i]
		if d := distanceToBezier(Point{X: wx, Y: wy}, w.FromPos, w.Ctrl1, w.Ctrl2, w.ToPos); d <= bestWireDist {
			bestWireDist = d
			cp := w.Edge
			hitWire = &cp
		}
	}
	if hitWire != nil {
		return HitResult{
			Kind: HitWire,
			Edge: *hitWire,
		}
	}

	// 4. Group headers (or collapsed group pills).
	hdrHitH := max(GroupHeaderHeight, wireRadius*2)
	for i := len(sc.Groups) - 1; i >= 0; i-- {
		gb := sc.Groups[i]
		headerRect := Rect{X: gb.Bounds.X, Y: gb.Bounds.Y, W: gb.Bounds.W, H: hdrHitH}
		if gb.Collapsed {
			headerRect = gb.Bounds
		}
		if headerRect.Contains(wx, wy) {
			toggle := wx >= gb.Bounds.X+gb.Bounds.W-28
			return HitResult{
				Kind:           HitGroup,
				GroupID:        gb.ID,
				InHeader:       true,
				ToggleCollapse: toggle,
			}
		}
	}

	return HitResult{Kind: HitNone}
}

// HitTestViewport resolves the topmost interactive element at (wx, wy) while
// accounting for camera zoom (matching the vector renderer's LOD visibility and
// painted header height) and terminal cell quantization (cellWorldW x cellWorldH
// around the clicked cell center).
func (sc *Scene) HitTestViewport(wx, wy, zoom, cellWorldW, cellWorldH float64) HitResult {
	if sc == nil {
		return HitResult{}
	}
	if zoom <= 0 || math.IsNaN(zoom) || math.IsInf(zoom, 0) {
		zoom = 1.0
	}
	cellW := max(0.0, cellWorldW)
	cellH := max(0.0, cellWorldH)
	if math.IsNaN(cellW) || math.IsInf(cellW, 0) {
		cellW = 0
	}
	if math.IsNaN(cellH) || math.IsInf(cellH, 0) {
		cellH = 0
	}
	cellRect := Rect{
		X: wx - cellW*0.5,
		Y: wy - cellH*0.5,
		W: cellW,
		H: cellH,
	}

	// Pass 1a: Tight directional port socket check across visible nodes (picking
	// closest socket center, tie-breaking by topmost draw order), unless an upper
	// node's visible header badge or inline control explicitly contains (wx, wy).
	bestPortDist := math.MaxFloat64
	var bestPort *PortAnchor
	for i := len(sc.Nodes) - 1; i >= 0; i-- {
		nb := &sc.Nodes[i]
		if nb.GroupHidden {
			continue
		}
		if nodeControlContainsPoint(nb, wx, wy, zoom) {
			break
		}
		if port, d, ok := portHitOnNode(nb, wx, wy, zoom, cellW, cellH); ok && d < bestPortDist-1e-6 {
			bestPortDist = d
			cp := port
			bestPort = &cp
		}
	}
	if bestPort != nil {
		return HitResult{
			Kind:   HitPort,
			NodeID: bestPort.NodeID,
			Port:   *bestPort,
		}
	}

	// Pass 1b: Top-to-bottom exact z-order check for node cards containing (wx, wy).
	for i := len(sc.Nodes) - 1; i >= 0; i-- {
		nb := &sc.Nodes[i]
		if nb.GroupHidden {
			continue
		}
		if nb.Bounds.Contains(wx, wy) {
			return resolveNodeHit(*nb, wx, wy, zoom, cellRect)
		}
	}

	// Pass 2: Sub-cell quantization recovery when terminal cell midpoint (wx, wy)
	// falls outside a sub-cell card (e.g. collapsed card on Retina or zoomed-out
	// card smaller than one terminal cell) that is painted inside cellRect.
	if cellRect.W > 0 && cellRect.H > 0 {
		bestNodeIdx := -1
		bestNodeDist := math.MaxFloat64
		for i := len(sc.Nodes) - 1; i >= 0; i-- {
			nb := &sc.Nodes[i]
			if nb.GroupHidden || !cardQuantizedCellHit(wx, wy, cellRect, nb.Bounds) {
				continue
			}
			d := pointToRectDist(wx, wy, nb.Bounds)
			if d < bestNodeDist {
				bestNodeDist = d
				bestNodeIdx = i
			}
		}
		if bestNodeIdx >= 0 {
			nb := sc.Nodes[bestNodeIdx]
			cx := clampFloat(wx, nb.Bounds.X+0.5, nb.Bounds.X+max(0.5, nb.Bounds.W-0.5))
			cy := clampFloat(wy, nb.Bounds.Y+0.5, nb.Bounds.Y+max(0.5, nb.Bounds.H-0.5))
			return resolveNodeHit(nb, cx, cy, zoom, cellRect)
		}
	}

	// Pass 3: Wires (analytic distance to sampled cubic Bezier, bounded in world space).
	wireR := max(WireHitThreshold, min(30.0, max(WireHitThreshold/zoom, math.Hypot(cellW, cellH)*0.48)))
	bestWireDist := wireR
	var hitWire *nodegraph.EdgeSpec
	for i := len(sc.Wires) - 1; i >= 0; i-- {
		w := sc.Wires[i]
		if d := distanceToBezier(Point{X: wx, Y: wy}, w.FromPos, w.Ctrl1, w.Ctrl2, w.ToPos); d <= bestWireDist {
			bestWireDist = d
			cp := w.Edge
			hitWire = &cp
		}
	}
	if hitWire != nil {
		return HitResult{
			Kind: HitWire,
			Edge: *hitWire,
		}
	}

	// Pass 4: Group headers (or collapsed group pills), exact first then sub-cell Y overlap.
	hdrHitH := max(GroupHeaderHeight, min(56.0, max(16.0/zoom, cellH*0.55)))
	for i := len(sc.Groups) - 1; i >= 0; i-- {
		gb := sc.Groups[i]
		headerRect := Rect{X: gb.Bounds.X, Y: gb.Bounds.Y, W: gb.Bounds.W, H: hdrHitH}
		if gb.Collapsed {
			headerRect = gb.Bounds
		}
		if headerRect.Contains(wx, wy) {
			toggle := gb.Bounds.W*zoom >= 42 && wx >= gb.Bounds.X+gb.Bounds.W-28
			return HitResult{
				Kind:           HitGroup,
				GroupID:        gb.ID,
				InHeader:       true,
				ToggleCollapse: toggle,
			}
		}
	}
	if cellRect.W > 0 && cellRect.H > 0 {
		bestGroupIdx := -1
		bestGroupDist := math.MaxFloat64
		for i := len(sc.Groups) - 1; i >= 0; i-- {
			gb := sc.Groups[i]
			headerRect := Rect{X: gb.Bounds.X, Y: gb.Bounds.Y, W: gb.Bounds.W, H: hdrHitH}
			if gb.Collapsed {
				headerRect = gb.Bounds
			}
			if !cardQuantizedCellHit(wx, wy, cellRect, headerRect) {
				continue
			}
			if d := pointToRectDist(wx, wy, headerRect); d < bestGroupDist {
				bestGroupDist = d
				bestGroupIdx = i
			}
		}
		if bestGroupIdx >= 0 {
			gb := sc.Groups[bestGroupIdx]
			toggle := gb.Bounds.W*zoom >= 42 && wx >= gb.Bounds.X+gb.Bounds.W-28
			return HitResult{
				Kind:           HitGroup,
				GroupID:        gb.ID,
				InHeader:       true,
				ToggleCollapse: toggle,
			}
		}
	}

	return HitResult{Kind: HitNone}
}

func nodeControlContainsPoint(nb *NodeBox, wx, wy, zoom float64) bool {
	if !nb.Bounds.Contains(wx, wy) {
		return false
	}
	if nb.Bounds.W*zoom >= 42 {
		collapseRect := nb.CollapseBadgeRect
		if collapseRect.W <= 0 {
			collapseRect = Rect{
				X: nb.Bounds.X + nb.Bounds.W - 22,
				Y: nb.Bounds.Y,
				W: 22,
				H: min(nb.Bounds.H, HeaderHeight),
			}
		}
		if collapseRect.Contains(wx, wy) {
			return true
		}
	}
	if !nb.Collapsed && zoom >= 0.48 && nb.Bounds.W*zoom >= 84 {
		for _, br := range []Rect{nb.PreviewBadgeRect, nb.ParamsBadgeRect, nb.ViewerBadgeRect} {
			if br.W > 0 && br.Contains(wx, wy) {
				return true
			}
		}
	}
	if !nb.Collapsed && zoom >= 0.42 && nb.ParamsOpen {
		for rIdx := range nb.ParamRows {
			if nb.ParamRows[rIdx].ControlRect.Contains(wx, wy) {
				return true
			}
		}
	}
	return false
}

func portHitOnNode(nb *NodeBox, wx, wy, zoom, cellW, cellH float64) (PortAnchor, float64, bool) {
	if nodeControlContainsPoint(nb, wx, wy, zoom) {
		return PortAnchor{}, 0, false
	}

	// Keep vertical port reach bounded near the card so large zoomed-out cells
	// above or below a card never steal clicks from neighboring rows.
	if wy < nb.Bounds.Y-max(PortHitRadius, cellH*0.52) || wy > nb.Bounds.Y+nb.Bounds.H+max(PortHitRadius, cellH*0.52) {
		return PortAnchor{}, 0, false
	}
	// On expanded cards tall enough to separate the header band from port rows,
	// clicks inside the upper header bar belong to the node header, never a port.
	if !nb.Collapsed && (cellH <= 0 || nb.Bounds.H >= cellH*1.45) {
		if wy < nb.Bounds.Y+HeaderHeight-max(3.0, cellH*0.18) {
			return PortAnchor{}, 0, false
		}
	}

	// Outside the card border there is no body competition, so allow comfortable reach.
	// Inside the card border:
	// - Below zoom 0.30 (overview mode where port labels are hidden) or when the entire
	//   card is narrower than ~2.8 terminal cells, keep inward reach at the border so
	//   cells centered inside the card always select/drag the node rather than detaching wires.
	// - Otherwise allow up to half a terminal cell inward while always preserving at least
	//   max(cellW*1.2, 56% of card width) in the card center for node selection/dragging.
	outerRX := max(PortHitRadius, cellW*0.60, min(36.0, 9.0/zoom))
	var innerRXIn, innerRXOut float64
	switch {
	case zoom < 0.30 || (cellW > 0 && nb.Bounds.W < cellW*2.8):
		innerRXIn = min(6.0, nb.Bounds.W*0.04)
		innerRXOut = min(5.0, nb.Bounds.W*0.04)
	case nb.Collapsed:
		// On collapsed cards, outlets sit at (X+W, Y+15) right next to the '▸' expand
		// glyph at [X+W-22, X+W-4]; keep inward outlet reach on the border so '▸' works.
		innerRXIn = min(max(6.0, cellW*0.52), nb.Bounds.W*0.18)
		innerRXOut = min(5.0, nb.Bounds.W*0.08)
	default:
		maxInner := max(6.0, (nb.Bounds.W-max(cellW*1.2, nb.Bounds.W*0.56))*0.5)
		innerRXIn = min(maxInner, max(PortHitRadius, cellW*0.52, min(18.0, 6.5/zoom)))
		innerRXOut = innerRXIn
	}
	portRY := max(PortHitRadius, cellH*0.55, min(PortRowHeight*0.80, 9.0/zoom))
	maxDiag := math.Hypot(outerRX, portRY)

	bestDist := maxDiag
	var hit *PortAnchor

	for j := range nb.Outlets {
		p := &nb.Outlets[j]
		dxIn := p.Center.X - wx
		dxOut := wx - p.Center.X
		if dxIn > innerRXOut || dxOut > outerRX || math.Abs(wy-p.Center.Y) > portRY {
			continue
		}
		if d := math.Hypot(wx-p.Center.X, wy-p.Center.Y); d <= bestDist {
			bestDist = d
			cp := *p
			hit = &cp
		}
	}
	for j := range nb.Inlets {
		p := &nb.Inlets[j]
		dxIn := wx - p.Center.X
		dxOut := p.Center.X - wx
		if dxIn > innerRXIn || dxOut > outerRX || math.Abs(wy-p.Center.Y) > portRY {
			continue
		}
		if d := math.Hypot(wx-p.Center.X, wy-p.Center.Y); d <= bestDist {
			bestDist = d
			cp := *p
			hit = &cp
		}
	}
	if hit == nil {
		return PortAnchor{}, 0, false
	}
	return *hit, bestDist, true
}

func resolveNodeHit(nb NodeBox, wx, wy, zoom float64, cellRect Rect) HitResult {
	effectiveHdrH := min(nb.Bounds.H, max(HeaderHeight, 13.0/zoom))
	inHeader := wy <= nb.Bounds.Y+effectiveHdrH

	// 1. Header badges & collapse toggle (gated by the same LOD visibility thresholds as render_vector.go).
	showCollapse := nb.Bounds.W*zoom >= 42
	showBadges := !nb.Collapsed && zoom >= 0.48 && nb.Bounds.W*zoom >= 84
	type badgeCandidate struct {
		kind string
		rect Rect
	}
	var candidates []badgeCandidate
	if showCollapse {
		collapseRect := nb.CollapseBadgeRect
		if collapseRect.W <= 0 {
			collapseRect = Rect{
				X: nb.Bounds.X + nb.Bounds.W - 22,
				Y: nb.Bounds.Y,
				W: 22,
				H: min(nb.Bounds.H, HeaderHeight),
			}
		}
		candidates = append(candidates, badgeCandidate{kind: "collapse", rect: collapseRect})
	}
	if showBadges {
		if nb.PreviewBadgeRect.W > 0 {
			candidates = append(candidates, badgeCandidate{kind: "preview", rect: nb.PreviewBadgeRect})
		}
		if nb.ParamsBadgeRect.W > 0 {
			candidates = append(candidates, badgeCandidate{kind: "params", rect: nb.ParamsBadgeRect})
		}
		if nb.ViewerBadgeRect.W > 0 {
			candidates = append(candidates, badgeCandidate{kind: "viewer", rect: nb.ViewerBadgeRect})
		}
	}

	bestBadgeKind := ""
	bestBadgeDist := math.MaxFloat64
	bestBadgeCenterDist := math.MaxFloat64
	for _, c := range candidates {
		exact := c.rect.Contains(wx, wy)
		// At low zoom (<0.45) require an exact hit on Collapse so clicking small overview cards selects/drags cleanly.
		if !exact && (zoom < 0.45 || !subControlOverlapsCell(cellRect, c.rect)) {
			continue
		}
		d := pointToRectDist(wx, wy, c.rect)
		cd := math.Hypot(wx-(c.rect.X+c.rect.W*0.5), wy-(c.rect.Y+c.rect.H*0.5))
		if d < bestBadgeDist-1e-6 || (math.Abs(d-bestBadgeDist) <= 1e-6 && cd < bestBadgeCenterDist) {
			bestBadgeDist = d
			bestBadgeCenterDist = cd
			bestBadgeKind = c.kind
		}
	}

	// 2. Inline parameter & preview drawer controls (drawn when !nb.Collapsed && zoom >= 0.42).
	if !nb.Collapsed && zoom >= 0.42 {
		bestRowIdx := -1
		bestRowDist := bestBadgeDist
		for rIdx := range nb.ParamRows {
			row := nb.ParamRows[rIdx]
			if !nb.ParamsOpen {
				break
			}
			if !row.ControlRect.Contains(wx, wy) && !subControlOverlapsCell(cellRect, row.ControlRect) {
				continue
			}
			if d := pointToRectDist(wx, wy, row.ControlRect); d < bestRowDist-1e-6 {
				bestRowDist = d
				bestRowIdx = rIdx
			}
		}
		if bestRowIdx >= 0 {
			row := nb.ParamRows[bestRowIdx]
			cx := clampFloat(wx, row.ControlRect.X+0.5, row.ControlRect.X+max(0.5, row.ControlRect.W-0.5))
			cy := clampFloat(wy, row.ControlRect.Y+0.5, row.ControlRect.Y+max(0.5, row.ControlRect.H-0.5))
			act := classifyInlineParamHit(row, cx, cy)
			if act != ParamActionNone {
				cp := row
				return HitResult{
					Kind:        HitNode,
					NodeID:      nb.ID,
					ParamKey:    row.Key,
					ParamAction: act,
					ParamRow:    &cp,
				}
			}
		}

		if bestBadgeKind == "" {
			if nb.ParamsOpen && nb.ParamsHeaderRect.W > 0 && (nb.ParamsHeaderRect.Contains(wx, wy) || subControlOverlapsCell(cellRect, nb.ParamsHeaderRect)) {
				return HitResult{
					Kind:         HitNode,
					NodeID:       nb.ID,
					ToggleParams: true,
				}
			}
			if nb.PreviewOpen && nb.PreviewHeaderRect.W > 0 && (nb.PreviewHeaderRect.Contains(wx, wy) || subControlOverlapsCell(cellRect, nb.PreviewHeaderRect)) {
				return HitResult{
					Kind:          HitNode,
					NodeID:        nb.ID,
					TogglePreview: true,
				}
			}
		}
	}

	if bestBadgeKind != "" {
		return HitResult{
			Kind:           HitNode,
			NodeID:         nb.ID,
			InHeader:       true,
			ToggleCollapse: bestBadgeKind == "collapse",
			TogglePreview:  bestBadgeKind == "preview",
			ToggleParams:   bestBadgeKind == "params",
			ToggleViewer:   bestBadgeKind == "viewer",
		}
	}

	return HitResult{
		Kind:     HitNode,
		NodeID:   nb.ID,
		InHeader: inHeader,
	}
}

func cardQuantizedCellHit(wx, wy float64, cell, target Rect) bool {
	if !rectOverlapsCell(cell, target) {
		return false
	}
	// On any axis where target spans >= 1.15 terminal cells, at least one cell
	// midpoint is guaranteed to fall inside target, so require the cursor midpoint
	// on that axis to stay inside target (preventing large cards/groups from
	// stealing empty-canvas marquee clicks next to their borders). When target is
	// smaller than ~1 cell on an axis (e.g. collapsed card on Retina or zoomed-out
	// overview card, or a sub-cell header badge sticking near the top edge), cell
	// overlap recovers the hit cleanly.
	hitX := (wx >= target.X && wx <= target.X+target.W) || target.W < cell.W*1.15
	hitY := (wy >= target.Y && wy <= target.Y+target.H) || target.H < cell.H*1.15 ||
		(wy < target.Y && target.Y-wy <= min(cell.H*0.5, HeaderHeight*0.6))
	return hitX && hitY
}

func rectOverlapsCell(cell, target Rect) bool {
	if cell.W <= 0 || cell.H <= 0 || target.W <= 0 || target.H <= 0 {
		return false
	}
	return cell.X < target.X+target.W &&
		cell.X+cell.W > target.X &&
		cell.Y < target.Y+target.H &&
		cell.Y+cell.H > target.Y
}

func subControlOverlapsCell(cell, ctrl Rect) bool {
	if !rectOverlapsCell(cell, ctrl) {
		return false
	}
	ovW := min(cell.X+cell.W, ctrl.X+ctrl.W) - max(cell.X, ctrl.X)
	ovH := min(cell.Y+cell.H, ctrl.Y+ctrl.H) - max(cell.Y, ctrl.Y)
	return ovW >= min(ctrl.W*0.25, cell.W*0.25) && ovH >= min(ctrl.H*0.25, cell.H*0.25)
}

func pointToRectDist(px, py float64, r Rect) float64 {
	dx := max(0.0, max(r.X-px, px-(r.X+r.W)))
	dy := max(0.0, max(r.Y-py, py-(r.Y+r.H)))
	return math.Hypot(dx, dy)
}

// AutoLayoutOps computes deterministic left-to-right topological column
// MoveNode edits for all nodes in spec using DefaultLayoutOptions.
func AutoLayoutOps(spec *nodegraph.Spec) []nodegraph.EditOp {
	return AutoLayoutOpsWith(spec, nil, DefaultLayoutOptions())
}

// AutoLayoutOpsWith computes deterministic Sugiyama/Kahn layered MoveNode edits
// using card-height-aware spacing, port-aware barycentric crossing reduction,
// and configurable LayoutOptions.
func AutoLayoutOpsWith(spec *nodegraph.Spec, reg *nodegraph.Registry, opts LayoutOptions) []nodegraph.EditOp {
	return AutoLayoutOpsWithScene(spec, reg, SceneOptions{Layout: opts})
}

// AutoLayoutOpsWithScene computes deterministic Sugiyama/Kahn layered MoveNode
// edits accounting for any expanded inline parameter or preview drawers.
func AutoLayoutOpsWithScene(spec *nodegraph.Spec, reg *nodegraph.Registry, sceneOpts SceneOptions) []nodegraph.EditOp {
	if spec == nil || len(spec.Nodes) == 0 {
		return nil
	}
	positions := computeLayeredPositionsWithScene(spec, reg, sceneOpts)
	ids := spec.SortedNodeIDs()
	ops := make([]nodegraph.EditOp, 0, len(ids))
	for _, id := range ids {
		pt := positions[id]
		ops = append(ops, nodegraph.MoveNode(id, pt.X, pt.Y))
	}
	return ops
}

func estimateNodeCardDims(spec *nodegraph.Spec, reg *nodegraph.Registry, id string, sceneOpts SceneOptions) (float64, float64) {
	nspec, ok := spec.Nodes[id]
	if !ok {
		return DefaultNodeWidth, 118.0
	}
	if nspec.UI != nil && nspec.UI.Collapsed {
		return DefaultNodeWidth, CollapsedNodeHeight
	}
	def, _ := lookupTypeDef(reg, nspec.Type, spec, id)
	paramsOpen := len(def.Params) > 0 && sceneOpts.ParamsExpanded != nil && sceneOpts.ParamsExpanded[id]
	previewOpen := len(def.Outlets) > 0 && sceneOpts.PreviewExpanded != nil && sceneOpts.PreviewExpanded[id]

	cardW := DefaultNodeWidth
	if paramsOpen || previewOpen {
		cardW = ExpandedNodeWidth
	}

	portRows := max(1, max(len(def.Inlets), len(def.Outlets)))
	h := HeaderHeight + float64(portRows)*PortRowHeight
	if len(nspec.Capabilities) > 0 {
		h += SummaryRowHeight
	}
	if nodeHasBoundaryBadges(spec, id) {
		h += SummaryRowHeight
	}
	if paramsOpen {
		h += DrawerHeaderHeight + float64(min(MaxInlineParamRows, len(def.Params)))*InlineParamRowHeight
	} else {
		h += float64(min(2, len(formatParamSummaries(nspec, def)))) * SummaryRowHeight
	}
	if previewOpen {
		h += DrawerHeaderHeight + PreviewBoxHeight
	}
	return cardW, h + CardBottomPad
}

func computeNodeBoundaryBadges(spec *nodegraph.Spec) (map[string][]string, map[nodegraph.PortRef]bool, map[nodegraph.PortRef]bool) {
	if spec == nil || (len(spec.Inlets) == 0 && len(spec.Outlets) == 0 && len(spec.Params) == 0) {
		return nil, nil, nil
	}
	badges := make(map[string][]string)
	fedInlets := make(map[nodegraph.PortRef]bool)
	sourcedOutlets := make(map[nodegraph.PortRef]bool)

	inlets := slices.Clone(spec.Inlets)
	slices.SortFunc(inlets, func(a, b nodegraph.ExposedPortSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, in := range inlets {
		for _, ref := range in.ResolvedTargets() {
			fedInlets[ref] = true
			lbl := "⇢ in:" + in.Name
			if !slices.Contains(badges[ref.Node], lbl) {
				badges[ref.Node] = append(badges[ref.Node], lbl)
			}
		}
	}

	outlets := slices.Clone(spec.Outlets)
	slices.SortFunc(outlets, func(a, b nodegraph.ExposedPortSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, out := range outlets {
		if out.Target.Node != "" {
			sourcedOutlets[out.Target] = true
			lbl := "⇠ out:" + out.Name
			if !slices.Contains(badges[out.Target.Node], lbl) {
				badges[out.Target.Node] = append(badges[out.Target.Node], lbl)
			}
		}
	}

	params := slices.Clone(spec.Params)
	slices.SortFunc(params, func(a, b nodegraph.ExposedParamSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, pm := range params {
		for _, ref := range pm.ResolvedTargets() {
			lbl := "⇢ param:" + pm.Name
			if !slices.Contains(badges[ref.Node], lbl) {
				badges[ref.Node] = append(badges[ref.Node], lbl)
			}
		}
	}

	return badges, fedInlets, sourcedOutlets
}

func nodeHasBoundaryBadges(spec *nodegraph.Spec, nodeID string) bool {
	if spec == nil {
		return false
	}
	for _, in := range spec.Inlets {
		for _, ref := range in.ResolvedTargets() {
			if ref.Node == nodeID {
				return true
			}
		}
	}
	for _, out := range spec.Outlets {
		if out.Target.Node == nodeID {
			return true
		}
	}
	for _, pm := range spec.Params {
		for _, ref := range pm.ResolvedTargets() {
			if ref.Node == nodeID {
				return true
			}
		}
	}
	return false
}

func outletPortIndex(spec *nodegraph.Spec, reg *nodegraph.Registry, nodeID, portName string) int {
	nspec, ok := spec.Nodes[nodeID]
	if !ok {
		return 0
	}
	def, _ := lookupTypeDef(reg, nspec.Type, spec, nodeID)
	for i, out := range def.Outlets {
		if out.Name == portName {
			return i
		}
	}
	return 0
}

func computeLayeredPositionsWithScene(spec *nodegraph.Spec, reg *nodegraph.Registry, sceneOpts SceneOptions) map[string]Point {
	opts := sceneOpts.Layout.normalized()
	ids := spec.SortedNodeIDs()
	out := make(map[string]Point, len(ids))
	if len(ids) == 0 {
		return out
	}

	// Pass 1: Cycle-safe Kahn longest-path rank assignment (ignoring feedback
	// kind=="frame" back-edges first so feedback loops don't collapse ranks).
	inDeg := make(map[string]int, len(ids))
	adj := make(map[string][]string, len(ids))
	for _, id := range ids {
		inDeg[id] = 0
	}
	addEdges := func(includeFrame bool) {
		clear(adj)
		for _, id := range ids {
			inDeg[id] = 0
		}
		for _, e := range spec.Edges {
			if e.From.Node == e.To.Node {
				continue
			}
			if !includeFrame && e.Kind == "frame" {
				continue
			}
			if _, ok1 := inDeg[e.From.Node]; !ok1 {
				continue
			}
			if _, ok2 := inDeg[e.To.Node]; !ok2 {
				continue
			}
			adj[e.From.Node] = append(adj[e.From.Node], e.To.Node)
			inDeg[e.To.Node]++
		}
		for id := range adj {
			slices.Sort(adj[id])
		}
	}
	addEdges(true)

	runKahn := func() (map[string]int, map[string]bool) {
		layer := make(map[string]int, len(ids))
		var queue []string
		for _, id := range ids {
			if inDeg[id] == 0 {
				queue = append(queue, id)
			}
		}
		visited := make(map[string]bool, len(ids))
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			visited[u] = true
			for _, v := range adj[u] {
				if layer[u]+1 > layer[v] {
					layer[v] = layer[u] + 1
				}
				inDeg[v]--
				if inDeg[v] == 0 {
					queue = append(queue, v)
					slices.Sort(queue)
				}
			}
		}
		return layer, visited
	}

	layer, visited := runKahn()
	if len(visited) < len(ids) {
		// Retry ignoring kind=="frame" cycle-breaking edges so feedback pipelines rank cleanly.
		addEdges(false)
		layer, visited = runKahn()
		for _, id := range ids {
			if !visited[id] {
				layer[id] = 0
			}
		}
	}

	columns := make(map[int][]string)
	maxLayer := 0
	for _, id := range ids {
		l := layer[id]
		columns[l] = append(columns[l], id)
		if l > maxLayer {
			maxLayer = l
		}
	}

	// Pass 2: Deterministic port-aware barycentric crossing reduction within each rank.
	rankOrder := make(map[string]float64, len(ids))
	slices.Sort(columns[0])
	for i, id := range columns[0] {
		rankOrder[id] = float64(i)
	}
	for col := 1; col <= maxLayer; col++ {
		members := columns[col]
		bary := make(map[string]float64, len(members))
		for idx, id := range members {
			var sum float64
			var count int
			for _, e := range spec.Edges {
				if e.To.Node == id && layer[e.From.Node] < col {
					pIdx := outletPortIndex(spec, reg, e.From.Node, e.From.Port)
					sum += rankOrder[e.From.Node] + float64(pIdx)*0.18
					count++
				}
			}
			if count > 0 {
				bary[id] = sum / float64(count)
			} else {
				bary[id] = float64(idx) + 1000.0
			}
		}
		slices.SortFunc(members, func(a, b string) int {
			if d := cmp.Compare(bary[a], bary[b]); d != 0 {
				return d
			}
			return cmp.Compare(a, b)
		})
		for i, id := range members {
			rankOrder[id] = float64(i)
		}
	}

	// Pass 3: Card-height-aware stacking and rank centering.
	if opts.Direction == LayoutTopToBottom {
		rowPitchX := DefaultNodeWidth + opts.ColumnGap*0.65
		for _, id := range ids {
			if w, _ := estimateNodeCardDims(spec, reg, id, sceneOpts); w+opts.ColumnGap*0.65 > rowPitchX {
				rowPitchX = w + opts.ColumnGap*0.65
			}
		}
		maxSpanW := 0.0
		spanByLayer := make([]float64, maxLayer+1)
		for r := 0; r <= maxLayer; r++ {
			n := len(columns[r])
			if n > 0 {
				span := float64(n)*DefaultNodeWidth + float64(n-1)*(opts.ColumnGap*0.65)
				spanByLayer[r] = span
				if span > maxSpanW {
					maxSpanW = span
				}
			}
		}
		curY := opts.OriginY
		for r := 0; r <= maxLayer; r++ {
			members := columns[r]
			offsetX := opts.OriginX
			if !opts.DisableCenterY && maxSpanW > spanByLayer[r] {
				offsetX += math.Round((maxSpanW - spanByLayer[r]) * 0.5)
			}
			tallest := 0.0
			for i, id := range members {
				out[id] = Point{
					X: math.Round(offsetX + float64(i)*rowPitchX),
					Y: math.Round(curY),
				}
				if _, h := estimateNodeCardDims(spec, reg, id, sceneOpts); h > tallest {
					tallest = h
				}
			}
			curY += tallest + opts.RowGap
		}
		return out
	}

	colWidths := make([]float64, maxLayer+1)
	colHeights := make([]float64, maxLayer+1)
	maxColH := 0.0
	for col := 0; col <= maxLayer; col++ {
		members := columns[col]
		var totalH float64
		colW := DefaultNodeWidth
		for i, id := range members {
			if i > 0 {
				totalH += opts.RowGap
			}
			w, h := estimateNodeCardDims(spec, reg, id, sceneOpts)
			if w > colW {
				colW = w
			}
			totalH += h
		}
		colWidths[col] = colW
		colHeights[col] = totalH
		if totalH > maxColH {
			maxColH = totalH
		}
	}

	colX := opts.OriginX
	for col := 0; col <= maxLayer; col++ {
		members := columns[col]
		curY := opts.OriginY
		if !opts.DisableCenterY && maxColH > colHeights[col] {
			curY += math.Round((maxColH - colHeights[col]) * 0.5)
		}
		x := math.Round(colX)
		for _, id := range members {
			out[id] = Point{X: x, Y: math.Round(curY)}
			_, h := estimateNodeCardDims(spec, reg, id, sceneOpts)
			curY += h + opts.RowGap
		}
		colX += colWidths[col] + opts.ColumnGap
	}
	return out
}

func buildInlineParamRows(nspec nodegraph.NodeSpec, def nodegraph.TypeDef, cardX, startY, cardW float64) []InlineParamRow {
	limit := min(MaxInlineParamRows, len(def.Params))
	if limit == 0 {
		return nil
	}
	rows := make([]InlineParamRow, 0, limit)
	for i := range limit {
		pdef := def.Params[i]
		ry := startY + float64(i)*InlineParamRowHeight
		rowRect := Rect{X: cardX + 6, Y: ry + 1, W: cardW - 12, H: InlineParamRowHeight - 2}
		ctrlX := cardX + 94
		ctrlW := max(48.0, (cardX+cardW-8)-ctrlX)
		ctrlRect := Rect{X: ctrlX, Y: ry + 2, W: ctrlW, H: InlineParamRowHeight - 4}

		raw, has := nspec.Params[pdef.Name]
		if !has {
			raw = pdef.Default
		}

		row := InlineParamRow{
			Key:         pdef.Name,
			Label:       pdef.Name,
			ParamDef:    pdef,
			Bounds:      rowRect,
			ControlRect: ctrlRect,
		}

		switch {
		case len(pdef.Enum) > 0:
			row.Kind = InlineParamEnum
			row.EnumValues = slices.Clone(pdef.Enum)
			s, _ := raw.(string)
			if s == "" && len(pdef.Enum) > 0 {
				s = pdef.Enum[0]
			}
			row.DisplayValue = s
			btnW := min(16.0, ctrlW*0.22)
			row.DecRect = Rect{X: ctrlRect.X, Y: ctrlRect.Y, W: btnW, H: ctrlRect.H}
			row.IncRect = Rect{X: ctrlRect.X + ctrlRect.W - btnW, Y: ctrlRect.Y, W: btnW, H: ctrlRect.H}
			row.TrackRect = Rect{X: ctrlRect.X + btnW, Y: ctrlRect.Y, W: max(4, ctrlRect.W-2*btnW), H: ctrlRect.H}

		case pdef.Type == nodegraph.ParamBool:
			row.Kind = InlineParamBool
			b, _ := raw.(bool)
			row.BoolVal = b
			if b {
				row.DisplayValue = "ON"
				row.Fraction = 1.0
			} else {
				row.DisplayValue = "OFF"
				row.Fraction = 0.0
			}
			row.TrackRect = ctrlRect

		case pdef.Type == nodegraph.ParamInt:
			row.Kind = InlineParamInt
			cur := inlineAnyToInt(raw)
			lo, hi := resolveInlineNumericSpan(pdef, float64(cur), true)
			row.HasRange = hi > lo
			row.Min = lo
			row.Max = hi
			if hi > lo {
				row.Fraction = clampFloat((float64(cur)-lo)/(hi-lo), 0, 1)
			}
			row.DisplayValue = strconv.FormatInt(cur, 10)
			btnW := min(15.0, ctrlW*0.20)
			row.DecRect = Rect{X: ctrlRect.X, Y: ctrlRect.Y, W: btnW, H: ctrlRect.H}
			row.IncRect = Rect{X: ctrlRect.X + ctrlRect.W - btnW, Y: ctrlRect.Y, W: btnW, H: ctrlRect.H}
			row.TrackRect = Rect{X: ctrlRect.X + btnW + 2, Y: ctrlRect.Y, W: max(8, ctrlRect.W-2*btnW-4), H: ctrlRect.H}

		case pdef.Type == nodegraph.ParamNumber:
			row.Kind = InlineParamFloat
			cur := inlineAnyToFloat(raw)
			lo, hi := resolveInlineNumericSpan(pdef, cur, false)
			row.HasRange = hi > lo
			row.Min = lo
			row.Max = hi
			if hi > lo {
				row.Fraction = clampFloat((cur-lo)/(hi-lo), 0, 1)
			}
			row.DisplayValue = formatCompactFloat(cur)
			btnW := min(15.0, ctrlW*0.20)
			row.DecRect = Rect{X: ctrlRect.X, Y: ctrlRect.Y, W: btnW, H: ctrlRect.H}
			row.IncRect = Rect{X: ctrlRect.X + ctrlRect.W - btnW, Y: ctrlRect.Y, W: btnW, H: ctrlRect.H}
			row.TrackRect = Rect{X: ctrlRect.X + btnW + 2, Y: ctrlRect.Y, W: max(8, ctrlRect.W-2*btnW-4), H: ctrlRect.H}

		default:
			row.Kind = InlineParamText
			s := ""
			if raw != nil {
				s = fmt.Sprintf("%v", raw)
			}
			s = strings.ReplaceAll(s, "\n", " ")
			if runes := []rune(s); len(runes) > 16 {
				s = string(runes[:14]) + "..."
			}
			row.DisplayValue = s
			row.TrackRect = ctrlRect
		}
		rows = append(rows, row)
	}
	return rows
}

func classifyInlineParamHit(row InlineParamRow, wx, wy float64) InlineParamAction {
	switch row.Kind {
	case InlineParamBool:
		return ParamActionToggleBool
	case InlineParamEnum:
		if row.DecRect.Contains(wx, wy) {
			return ParamActionEnumPrev
		}
		return ParamActionEnumNext
	case InlineParamFloat, InlineParamInt:
		if row.DecRect.Contains(wx, wy) {
			return ParamActionStepDec
		}
		if row.IncRect.Contains(wx, wy) {
			return ParamActionStepInc
		}
		if row.TrackRect.Contains(wx, wy) {
			return ParamActionScrubSlider
		}
		return ParamActionEditInspector
	default:
		return ParamActionEditInspector
	}
}

// ResolveNumericSpan derives finite slider [lo, hi] bounds for pdef around cur
// when pdef.Min or pdef.Max is omitted.
func ResolveNumericSpan(pdef nodegraph.ParamDef, cur float64, isInt bool) (float64, float64) {
	return resolveInlineNumericSpan(pdef, cur, isInt)
}

func resolveInlineNumericSpan(pdef nodegraph.ParamDef, cur float64, isInt bool) (float64, float64) {
	if pdef.Min != nil && pdef.Max != nil && *pdef.Max > *pdef.Min {
		return *pdef.Min, *pdef.Max
	}
	if pdef.Min != nil {
		lo := *pdef.Min
		ref := math.Max(math.Abs(cur), math.Abs(inlineAnyToFloat(pdef.Default)))
		if isInt {
			hi := math.Max(lo+100, math.Ceil(ref*2.0))
			return lo, hi
		}
		if ref <= 2.0 {
			return lo, math.Max(lo+2.0, 2.0)
		}
		return lo, math.Max(lo+10.0, ref*2.0)
	}
	if pdef.Max != nil {
		hi := *pdef.Max
		if isInt {
			return math.Min(0, hi-100), hi
		}
		return math.Min(0, hi-2.0), hi
	}
	if isInt {
		span := math.Max(50, math.Abs(cur)*2)
		return math.Min(0, cur-span), math.Max(100, cur+span)
	}
	if math.Abs(cur) <= 2.0 {
		return 0.0, 2.0
	}
	return cur - math.Abs(cur), cur + math.Abs(cur)
}

func inlineAnyToFloat(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	default:
		return 0
	}
}

func inlineAnyToInt(raw any) int64 {
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(math.Round(v))
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func formatCompactFloat(v float64) string {
	if math.Abs(v) >= 100 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'g', 4, 64)
}

func lookupTypeDef(reg *nodegraph.Registry, tid nodegraph.TypeID, spec *nodegraph.Spec, nodeID string) (nodegraph.TypeDef, bool) {
	if reg != nil {
		if def, ok := reg.Lookup(tid); ok {
			return def, true
		}
	}
	// Synthesize ports from incident edges if a node type is unregistered so the
	// editor can still display and repair broken specs gracefully.
	synth := nodegraph.TypeDef{ID: tid}
	seenIn := make(map[string]bool)
	seenOut := make(map[string]bool)
	for _, e := range spec.Edges {
		if e.To.Node == nodeID && !seenIn[e.To.Port] {
			seenIn[e.To.Port] = true
			synth.Inlets = append(synth.Inlets, nodegraph.PortDef{Name: e.To.Port, Type: nodegraph.TypeAny})
		}
		if e.From.Node == nodeID && !seenOut[e.From.Port] {
			seenOut[e.From.Port] = true
			synth.Outlets = append(synth.Outlets, nodegraph.PortDef{Name: e.From.Port, Type: nodegraph.TypeAny})
		}
	}
	slices.SortFunc(synth.Inlets, func(a, b nodegraph.PortDef) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(synth.Outlets, func(a, b nodegraph.PortDef) int { return cmp.Compare(a.Name, b.Name) })
	return synth, false
}

func formatParamSummaries(nspec nodegraph.NodeSpec, def nodegraph.TypeDef) []string {
	if len(nspec.Params) == 0 && len(def.Params) == 0 {
		return nil
	}
	keys := make([]string, 0, len(nspec.Params))
	for k := range nspec.Params {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var out []string
	for _, k := range keys {
		val := fmt.Sprintf("%v", nspec.Params[k])
		if len(val) > 20 {
			val = val[:17] + "..."
		}
		out = append(out, fmt.Sprintf("%s=%s", k, val))
	}
	if len(out) == 0 {
		for _, p := range def.Params {
			if p.Default != nil {
				val := fmt.Sprintf("%v", p.Default)
				if len(val) > 18 {
					val = val[:15] + "..."
				}
				out = append(out, fmt.Sprintf("%s=%s", p.Name, val))
				if len(out) >= 2 {
					break
				}
			}
		}
	}
	return out
}

func evalDragCompat(drag *WireDragPreview, nodeID string, portType nodegraph.DataType, isOutlet bool) PortCompatState {
	if drag == nil || !drag.Active {
		return PortCompatNeutral
	}
	if nodeID == drag.Anchor.Node || isOutlet == drag.FromOutlet {
		return PortCompatInvalid
	}
	var outType, inType nodegraph.DataType
	if drag.FromOutlet {
		outType, inType = drag.AnchorType, portType
	} else {
		outType, inType = portType, drag.AnchorType
	}
	compat, _, err := nodegraph.CheckCompatibility(outType, inType)
	if err != nil || compat == nodegraph.CompatIncompatible {
		return PortCompatInvalid
	}
	return PortCompatValid
}

func bezierControls(from, to Point) (Point, Point) {
	dx := to.X - from.X
	dy := to.Y - from.Y
	tangent := max(52.0, math.Abs(dx)*0.48+math.Abs(dy)*0.12)
	return Point{X: from.X + tangent, Y: from.Y}, Point{X: to.X - tangent, Y: to.Y}
}

func orthogonalWaypoints(from, to Point) []Point {
	if to.X >= from.X+20 {
		midX := (from.X + to.X) * 0.5
		return []Point{
			from,
			{X: midX, Y: from.Y},
			{X: midX, Y: to.Y},
			to,
		}
	}
	detourY := max(from.Y, to.Y) + 48
	return []Point{
		from,
		{X: from.X + 24, Y: from.Y},
		{X: from.X + 24, Y: detourY},
		{X: to.X - 24, Y: detourY},
		{X: to.X - 24, Y: to.Y},
		to,
	}
}

func evalCubicBezier(p0, c1, c2, p3 Point, t float64) Point {
	u := 1 - t
	tt := t * t
	uu := u * u
	uuu := uu * u
	ttt := tt * t
	return Point{
		X: uuu*p0.X + 3*uu*t*c1.X + 3*u*tt*c2.X + ttt*p3.X,
		Y: uuu*p0.Y + 3*uu*t*c1.Y + 3*u*tt*c2.Y + ttt*p3.Y,
	}
}

func distanceToBezier(pt, p0, c1, c2, p3 Point) float64 {
	const steps = 28
	best := math.MaxFloat64
	prev := p0
	for i := 1; i <= steps; i++ {
		cur := evalCubicBezier(p0, c1, c2, p3, float64(i)/steps)
		if d := distanceToSegment(pt, prev, cur); d < best {
			best = d
		}
		prev = cur
	}
	return best
}

func distanceToSegment(p, a, b Point) float64 {
	vx := b.X - a.X
	vy := b.Y - a.Y
	wx := p.X - a.X
	wy := p.Y - a.Y
	lenSq := vx*vx + vy*vy
	if lenSq <= 1e-9 {
		return math.Hypot(wx, wy)
	}
	t := clampFloat((wx*vx+wy*vy)/lenSq, 0, 1)
	projX := a.X + t*vx
	projY := a.Y + t*vy
	return math.Hypot(p.X-projX, p.Y-projY)
}

func sanitizeCoord(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return clampFloat(v, -1e6, 1e6)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
