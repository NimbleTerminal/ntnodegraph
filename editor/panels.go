// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

type inspectorFieldKind uint8

const (
	inspLabel inspectorFieldKind = iota
	inspCollapsed
	inspCapabilities
	inspNodeDrillIn
	inspParam
	inspEdgeKind
	inspEdgeDelete
	inspGroupLabel
	inspGroupCollapsed
	inspGroupPromote
	inspGroupUngroup
)

type inspectorItem struct {
	kind     inspectorFieldKind
	key      string
	label    string
	value    string
	paramDef nodegraph.ParamDef
}

func (m *Model) handleKeyPress(ev tea.KeyPressMsg) tea.Cmd {
	if ev.Code == tea.KeySpace {
		m.spaceHeld = true
	}

	// 0. Cancel active wire-drag or floating context menu with Escape / navigate context menu with arrows+Enter!
	if m.drag.kind == dragWire && ev.Code == tea.KeyEscape {
		if m.drag.baseSnapshot != nil {
			m.spec = m.drag.baseSnapshot
		}
		m.drag = dragState{}
		m.rebuildScene()
		m.vectorDirty = true
		m.statusMsg = "Wire cancelled"
		return nil
	}
	if m.ctxMenu != nil {
		switch ev.Code {
		case tea.KeyEscape:
			m.ctxMenu = nil
			m.rebuildScene()
			m.vectorDirty = true
			return nil
		case tea.KeyUp:
			if len(m.ctxMenu.actions) > 0 {
				if m.ctxMenu.hoverIdx < 0 {
					m.ctxMenu.hoverIdx = 0
				} else {
					m.ctxMenu.hoverIdx = modPos(m.ctxMenu.hoverIdx-1, len(m.ctxMenu.actions))
				}
				m.rebuildScene()
				m.vectorDirty = true
			}
			return nil
		case tea.KeyDown:
			if len(m.ctxMenu.actions) > 0 {
				if m.ctxMenu.hoverIdx < 0 {
					m.ctxMenu.hoverIdx = 0
				} else {
					m.ctxMenu.hoverIdx = modPos(m.ctxMenu.hoverIdx+1, len(m.ctxMenu.actions))
				}
				m.rebuildScene()
				m.vectorDirty = true
			}
			return nil
		case tea.KeyEnter:
			if m.ctxMenu.hoverIdx >= 0 && m.ctxMenu.hoverIdx < len(m.ctxMenu.actions) {
				act := m.ctxMenu.actions[m.ctxMenu.hoverIdx]
				pt := m.ctxMenu.worldPt
				m.ctxMenu = nil
				m.rebuildScene()
				m.vectorDirty = true
				return m.executeContextAction(act.id, pt)
			}
			m.ctxMenu = nil
			m.rebuildScene()
			m.vectorDirty = true
			return nil
		}
	}

	// 1. Active inline text input inside the Inspector.
	if m.editingField {
		return m.handleFieldEditKey(ev)
	}

	// 2. Modal overlays: Palette, Quick-Connect, Help.
	switch m.focus {
	case focusPalette:
		return m.handlePaletteKey(ev)
	case focusQuickConnect:
		return m.handleQuickConnectKey(ev)
	case focusHelp:
		if ev.Code == tea.KeyEscape || ev.String() == "?" || ev.Code == tea.KeyEnter {
			m.focus = focusCanvas
		}
		return nil
	case focusInspector:
		if cmd, handled := m.handleInspectorKey(ev); handled {
			return cmd
		}
	}

	// 3. Canvas & global editor shortcuts.
	key := ev.String()
	txt := ev.Text

	// Normalize Kitty keyboard protocol shifted/numpad/ctrl zoom keys across all terminals.
	switch {
	case key == "+" || key == "=" || key == "shift+=" || key == "ctrl+=" || key == "ctrl++" || key == "ctrl+shift+=" || key == "kp+" || txt == "+" || txt == "=":
		return m.ZoomBy(1.20)
	case key == "-" || key == "_" || key == "shift+-" || key == "ctrl+-" || key == "ctrl+_" || key == "ctrl+shift+-" || key == "kp-" || txt == "-" || txt == "_":
		return m.ZoomBy(1.0 / 1.20)
	}

	switch key {
	case "?":
		m.focus = focusHelp
		return nil

	case "a", "/":
		m.focus = focusPalette
		m.paletteQuery = ""
		m.paletteCursor = 0
		m.paletteSpliceMode = false
		m.pendingSpawnWorld = nil
		return m.syncPictureSize()

	case "d":
		_, cmd, _ := m.DuplicateSelectedNodes()
		return cmd

	case "i":
		if !m.showInspector {
			m.showInspector = true
			m.focus = focusInspector
			m.inspectorRow = 0
			m.vectorDirty = true
			return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
		}
		if m.focus == focusInspector {
			m.focus = focusCanvas
		} else {
			m.focus = focusInspector
			m.inspectorRow = 0
		}
		return nil

	case "I":
		m.showInspector = !m.showInspector
		if !m.showInspector && m.focus == focusInspector {
			m.focus = focusCanvas
		}
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case "c":
		return m.openQuickConnect()

	case "v":
		return m.ToggleMode()

	case "g":
		return m.ToggleKittyMode()

	case "P":
		m.SetShowPerfHUD(!m.ShowPerfHUD())
		if m.ShowPerfHUD() {
			m.statusMsg = "Perf HUD enabled"
		} else {
			m.statusMsg = "Perf HUD hidden"
		}
		if m.mode == RenderVector {
			return m.refreshVectorPicture()
		}
		return nil

	case "G", "ctrl+g":
		if m.selectedPromotableGroupID() != "" {
			_, cmd, _ := m.PromoteSelectedGroupToSubgraph("")
			return cmd
		}
		_, cmd, _ := m.GroupSelectedNodes("")
		return cmd

	case "z":
		return m.toggleSelectedCollapse()

	case "w":
		return m.ToggleSelectedParams()

	case "W":
		return m.ToggleAllParams()

	case "o":
		return m.ToggleSelectedPreview()

	case "O":
		return m.ToggleAllPreviews()

	case "t":
		if id := m.sel.PrimaryNode(); id != "" {
			return m.ToggleViewerNode(id)
		}
		m.statusMsg = "Select a node with outlets to tap into Viewer (t)"
		return nil

	case "L":
		cmd, _ := m.AutoLayout()
		return cmd

	case "0", "ctrl+0":
		m.FitToView()
		if m.mode == RenderVector {
			return m.refreshVectorPicture()
		}
		return nil

	case "1", "ctrl+1":
		return m.SetZoom(1.0)

	case "f":
		return m.FocusSelection()

	case "h", "left":
		m.userMovedCamera = true
		m.cam.PanX -= 36.0 / m.cam.normalized().Zoom
		m.vectorDirty = true
		return nil
	case "l", "right":
		m.userMovedCamera = true
		m.cam.PanX += 36.0 / m.cam.normalized().Zoom
		m.vectorDirty = true
		return nil
	case "k", "up":
		m.userMovedCamera = true
		m.cam.PanY -= 36.0 / m.cam.normalized().Zoom
		m.vectorDirty = true
		return nil
	case "j", "down":
		m.userMovedCamera = true
		m.cam.PanY += 36.0 / m.cam.normalized().Zoom
		m.vectorDirty = true
		return nil

	case "H", "shift+left":
		return m.nudgeSelected(-20, 0)
	case "L_nudge", "shift+right":
		return m.nudgeSelected(20, 0)
	case "K", "shift+up":
		return m.nudgeSelected(0, -20)
	case "J", "shift+down":
		return m.nudgeSelected(0, 20)

	case "n", "]":
		m.cycleNodeSelection(1)
		return nil
	case "p", "[":
		m.cycleNodeSelection(-1)
		return nil
	case "e":
		m.cycleEdgeSelection()
		return nil

	case "x", "delete":
		cmd, _ := m.DeleteSelected()
		return cmd

	case "backspace":
		if m.Depth() > 0 {
			return m.NavigateUp()
		}
		cmd, _ := m.DeleteSelected()
		return cmd

	case "esc", "escape":
		if m.Depth() > 0 {
			return m.NavigateUp()
		}
		return nil

	case "u", "ctrl+z":
		cmd, _ := m.Undo()
		return cmd

	case "U", "ctrl+r", "ctrl+y":
		cmd, _ := m.Redo()
		return cmd

	case "enter":
		if m.focus == focusCanvas && len(m.sel.Nodes) == 1 && m.CanDrillIntoNode(m.sel.Nodes[0]) {
			cmd, _ := m.DrillIntoSubgraph(m.sel.Nodes[0])
			return cmd
		}
		if m.showInspector || len(m.sel.Nodes) > 0 || m.sel.Edge != nil || m.sel.Group != "" {
			if !m.showInspector {
				m.showInspector = true
			}
			m.focus = focusInspector
			m.inspectorRow = 0
			m.vectorDirty = true
			return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
		}
	}

	if ev.Code == tea.KeyEscape && m.Depth() > 0 {
		return m.NavigateUp()
	}

	return nil
}

func (m *Model) nudgeSelected(dx, dy float64) tea.Cmd {
	if len(m.sel.Nodes) == 0 && m.sel.Group == "" {
		return nil
	}
	var ops []nodegraph.EditOp
	if m.sel.Group != "" {
		if g, ok := m.spec.Groups[m.sel.Group]; ok {
			for _, id := range g.Nodes {
				if idx, ok := m.scene.NodeByID[id]; ok {
					b := m.scene.Nodes[idx].Bounds
					ops = append(ops, nodegraph.MoveNode(id, b.X+dx, b.Y+dy))
				}
			}
			if g.UI != nil {
				gCopy := g
				uiCopy := *g.UI
				uiCopy.X += dx
				uiCopy.Y += dy
				gCopy.UI = &uiCopy
				ops = append(ops, nodegraph.PutGroup(m.sel.Group, gCopy))
			}
		}
	} else {
		for _, id := range m.sel.Nodes {
			if idx, ok := m.scene.NodeByID[id]; ok {
				b := m.scene.Nodes[idx].Bounds
				ops = append(ops, nodegraph.MoveNode(id, b.X+dx, b.Y+dy))
			}
		}
	}
	if len(ops) == 0 {
		return nil
	}
	cmd, _ := m.ApplyEdits(ops...)
	return cmd
}

func (m *Model) toggleSelectedCollapse() tea.Cmd {
	if m.sel.Group != "" {
		if g, ok := m.spec.Groups[m.sel.Group]; ok {
			g.Collapsed = !g.Collapsed
			cmd, _ := m.ApplyEdits(nodegraph.PutGroup(m.sel.Group, g))
			return cmd
		}
	}
	if id := m.sel.PrimaryNode(); id != "" {
		cur := false
		if n := m.spec.Nodes[id]; n.UI != nil {
			cur = n.UI.Collapsed
		}
		cmd, _ := m.ApplyEdits(nodegraph.SetNodeCollapsed(id, !cur))
		return cmd
	}
	return nil
}

func (m *Model) cycleNodeSelection(delta int) {
	ids := m.spec.SortedNodeIDs()
	if len(ids) == 0 {
		return
	}
	cur := m.sel.PrimaryNode()
	idx := slices.Index(ids, cur)
	if idx < 0 {
		m.SelectNode(ids[0])
		return
	}
	next := modPos(idx+delta, len(ids))
	m.SelectNode(ids[next])
}

func (m *Model) cycleEdgeSelection() {
	if len(m.spec.Edges) == 0 {
		return
	}
	edges := append([]nodegraph.EdgeSpec(nil), m.spec.Edges...)
	slices.SortFunc(edges, nodegraph.CompareEdgeSpec)
	if m.sel.Edge == nil {
		m.SelectEdge(edges[0])
		return
	}
	idx := slices.Index(edges, *m.sel.Edge)
	next := modPos(idx+1, len(edges))
	m.SelectEdge(edges[next])
}

// Palette modal

func (m *Model) filteredPaletteTypes() []nodegraph.TypeDef {
	types := m.registry.ForBackend(m.profile.Name)
	q := strings.ToLower(strings.TrimSpace(m.paletteQuery))
	if q == "" {
		return types
	}
	var out []nodegraph.TypeDef
	for _, t := range types {
		if strings.Contains(strings.ToLower(t.ID.String()), q) ||
			strings.Contains(strings.ToLower(t.Description), q) {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) handlePaletteKey(ev tea.KeyPressMsg) tea.Cmd {
	items := m.filteredPaletteTypes()
	switch ev.Code {
	case tea.KeyEscape:
		m.focus = focusCanvas
		m.pendingSpawnWorld = nil
		m.paletteSpliceMode = false
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case tea.KeyTab:
		if m.sel.Edge != nil {
			m.paletteSpliceMode = !m.paletteSpliceMode
		}
		return nil

	case tea.KeyUp:
		if len(items) > 0 {
			m.paletteCursor = modPos(m.paletteCursor-1, len(items))
		}
		return nil

	case tea.KeyDown:
		if len(items) > 0 {
			m.paletteCursor = modPos(m.paletteCursor+1, len(items))
		}
		return nil

	case tea.KeyBackspace:
		if len(m.paletteQuery) > 0 {
			r := []rune(m.paletteQuery)
			m.paletteQuery = string(r[:len(r)-1])
			m.paletteCursor = 0
		}
		return nil

	case tea.KeyEnter:
		if len(items) == 0 {
			return nil
		}
		idx := min(len(items)-1, max(0, m.paletteCursor))
		return m.choosePaletteItem(items[idx], ev.Mod&tea.ModShift != 0 || m.paletteSpliceMode)
	}

	// Ctrl+S inside palette splices onto selected edge if one is active.
	if ev.String() == "ctrl+s" && len(items) > 0 && m.sel.Edge != nil {
		idx := min(len(items)-1, max(0, m.paletteCursor))
		chosen := items[idx]
		m.focus = focusCanvas
		_, cmd, _ := m.SpliceNodeOnSelectedEdge(chosen.ID)
		return tea.Batch(cmd, m.syncPictureSize(), m.refreshVectorPicture())
	}

	if ev.Text != "" {
		m.paletteQuery += ev.Text
		m.paletteCursor = 0
	} else if ev.Code == tea.KeySpace {
		m.paletteQuery += " "
		m.paletteCursor = 0
	}
	return nil
}

// Quick-connect modal

func (m *Model) availableEdgeKinds() []string {
	out := []string{""}
	for _, k := range m.profile.EdgeKinds {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func (m *Model) openQuickConnect() tea.Cmd {
	srcNode := m.sel.PrimaryNode()
	if srcNode == "" {
		if ids := m.spec.SortedNodeIDs(); len(ids) > 0 {
			srcNode = ids[0]
			m.SelectNode(srcNode)
		} else {
			return nil
		}
	}
	nspec := m.spec.Nodes[srcNode]
	srcDef, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, srcNode)
	if len(srcDef.Outlets) == 0 {
		m.statusMsg = fmt.Sprintf("Node %q has no outlets to connect", srcNode)
		return nil
	}

	var items []quickConnectItem
	for _, out := range srcDef.Outlets {
		for _, dstID := range m.spec.SortedNodeIDs() {
			if dstID == srcNode {
				continue
			}
			dstSpec := m.spec.Nodes[dstID]
			dstDef, _ := lookupTypeDef(m.registry, dstSpec.Type, m.spec, dstID)
			for _, in := range dstDef.Inlets {
				compat, coerce, err := nodegraph.CheckCompatibility(out.Type, in.Type)
				if err != nil || compat == nodegraph.CompatIncompatible {
					continue
				}
				from := nodegraph.PortRef{Node: srcNode, Port: out.Name}
				to := nodegraph.PortRef{Node: dstID, Port: in.Name}
				already := false
				for _, e := range m.spec.Edges {
					if e.From == from && e.To == to {
						already = true
						break
					}
				}
				if !already {
					items = append(items, quickConnectItem{
						From:   from,
						To:     to,
						OutT:   out.Type,
						InT:    in.Type,
						Coerce: coerce,
					})
				}
			}
		}
	}
	if len(items) == 0 {
		m.statusMsg = fmt.Sprintf("No unconnected compatible inlets for %q", srcNode)
		return nil
	}
	m.qcCandidates = items
	m.qcCursor = 0
	m.qcKindIndex = 0
	m.focus = focusQuickConnect
	return m.syncPictureSize()
}

func (m *Model) handleQuickConnectKey(ev tea.KeyPressMsg) tea.Cmd {
	switch ev.Code {
	case tea.KeyEscape:
		m.focus = focusCanvas
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
	case tea.KeyUp:
		if len(m.qcCandidates) > 0 {
			m.qcCursor = modPos(m.qcCursor-1, len(m.qcCandidates))
		}
		return nil
	case tea.KeyDown:
		if len(m.qcCandidates) > 0 {
			m.qcCursor = modPos(m.qcCursor+1, len(m.qcCandidates))
		}
		return nil
	case tea.KeyTab, tea.KeyRight, tea.KeyLeft:
		kinds := m.availableEdgeKinds()
		m.qcKindIndex = modPos(m.qcKindIndex+1, len(kinds))
		return nil
	case tea.KeyEnter:
		if len(m.qcCandidates) == 0 {
			return nil
		}
		cand := m.qcCandidates[min(len(m.qcCandidates)-1, max(0, m.qcCursor))]
		kinds := m.availableEdgeKinds()
		kind := kinds[min(len(kinds)-1, max(0, m.qcKindIndex))]
		m.focus = focusCanvas
		cmd, err := m.ApplyEdits(nodegraph.Connect(cand.From, cand.To, kind))
		if err == nil {
			m.SelectEdge(nodegraph.EdgeSpec{From: cand.From, To: cand.To, Kind: kind})
		}
		return tea.Batch(cmd, m.syncPictureSize(), m.refreshVectorPicture())
	}
	return nil
}

// Inspector handling

func (m *Model) buildInspectorItems() []inspectorItem {
	if m.sel.Edge != nil {
		e := *m.sel.Edge
		kindVal := e.Kind
		if kindVal == "" {
			kindVal = "(default)"
		}
		return []inspectorItem{
			{kind: inspEdgeKind, label: "Kind", value: kindVal},
			{kind: inspEdgeDelete, label: "Action", value: "[Disconnect Edge]"},
		}
	}
	if m.sel.Group != "" {
		g, ok := m.spec.Groups[m.sel.Group]
		if !ok {
			return nil
		}
		return []inspectorItem{
			{kind: inspGroupLabel, label: "Label", value: g.Label},
			{kind: inspGroupCollapsed, label: "Collapsed", value: fmt.Sprintf("%v", g.Collapsed)},
			{kind: inspGroupPromote, label: "Component", value: "[Promote to Subgraph (G)]"},
			{kind: inspGroupUngroup, label: "Action", value: "[Ungroup Nodes]"},
		}
	}
	id := m.sel.PrimaryNode()
	if id == "" {
		return nil
	}
	nspec, ok := m.spec.Nodes[id]
	if !ok {
		return nil
	}
	def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)

	var label string
	collapsed := false
	if nspec.UI != nil {
		label = nspec.UI.Label
		collapsed = nspec.UI.Collapsed
	}
	var capStrs []string
	for _, c := range nspec.Capabilities {
		capStrs = append(capStrs, c.String())
	}

	items := []inspectorItem{
		{kind: inspLabel, label: "Label", value: label},
		{kind: inspCollapsed, label: "Collapsed", value: fmt.Sprintf("%v", collapsed)},
		{kind: inspCapabilities, label: "Capabilities", value: strings.Join(capStrs, ", ")},
	}
	if m.CanDrillIntoNode(id) {
		items = append(items, inspectorItem{
			kind:  inspNodeDrillIn,
			label: "Subgraph",
			value: "[Drill Into Component (Enter)]",
		})
	}

	for _, p := range def.Params {
		raw, isSet := nspec.Params[p.Name]
		valStr := ""
		if isSet {
			valStr = formatParamValueForEdit(p.Type, raw)
		} else if p.Default != nil {
			valStr = formatParamValueForEdit(p.Type, p.Default) + " (default)"
		}
		items = append(items, inspectorItem{
			kind:     inspParam,
			key:      p.Name,
			label:    p.Name,
			value:    valStr,
			paramDef: p,
		})
	}
	return items
}

func formatParamValueForEdit(pt nodegraph.ParamType, v any) string {
	switch pt {
	case nodegraph.ParamStrings:
		switch xs := v.(type) {
		case []any:
			parts := make([]string, 0, len(xs))
			for _, elem := range xs {
				parts = append(parts, fmt.Sprintf("%v", elem))
			}
			return strings.Join(parts, ", ")
		case []string:
			return strings.Join(xs, ", ")
		}
	case nodegraph.ParamJSON:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", v)
}

func (m *Model) handleInspectorKey(ev tea.KeyPressMsg) (tea.Cmd, bool) {
	items := m.buildInspectorItems()
	switch ev.Code {
	case tea.KeyEscape:
		m.focus = focusCanvas
		return nil, true
	case tea.KeyUp:
		if len(items) > 0 {
			m.inspectorRow = modPos(m.inspectorRow-1, len(items))
		}
		return nil, true
	case tea.KeyDown:
		if len(items) > 0 {
			m.inspectorRow = modPos(m.inspectorRow+1, len(items))
		}
		return nil, true
	case tea.KeyEnter, tea.KeySpace:
		if len(items) == 0 {
			return nil, true
		}
		item := items[min(len(items)-1, max(0, m.inspectorRow))]
		return m.activateInspectorItem(item), true
	}
	return nil, false
}

func (m *Model) activateInspectorItem(item inspectorItem) tea.Cmd {
	switch item.kind {
	case inspCollapsed:
		id := m.sel.PrimaryNode()
		cur := false
		if n := m.spec.Nodes[id]; n.UI != nil {
			cur = n.UI.Collapsed
		}
		cmd, _ := m.ApplyEdits(nodegraph.SetNodeCollapsed(id, !cur))
		return cmd

	case inspLabel:
		id := m.sel.PrimaryNode()
		cur := ""
		if n := m.spec.Nodes[id]; n.UI != nil {
			cur = n.UI.Label
		}
		m.editingField = true
		m.fieldTargetKey = "__label__"
		m.fieldBuffer = cur
		return nil

	case inspCapabilities:
		id := m.sel.PrimaryNode()
		nspec := m.spec.Nodes[id]
		var parts []string
		for _, c := range nspec.Capabilities {
			parts = append(parts, c.String())
		}
		m.editingField = true
		m.fieldTargetKey = "__caps__"
		m.fieldBuffer = strings.Join(parts, ", ")
		return nil

	case inspNodeDrillIn:
		if id := m.sel.PrimaryNode(); id != "" {
			m.focus = focusCanvas
			cmd, _ := m.DrillIntoSubgraph(id)
			return cmd
		}
		return nil

	case inspParam:
		id := m.sel.PrimaryNode()
		nspec := m.spec.Nodes[id]
		// Direct toggle for booleans and cycle for enums!
		if item.paramDef.Type == nodegraph.ParamBool {
			cur := false
			if v, ok := nspec.Params[item.key].(bool); ok {
				cur = v
			} else if d, ok := item.paramDef.Default.(bool); ok {
				cur = d
			}
			cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, item.key, !cur))
			return cmd
		}
		if len(item.paramDef.Enum) > 0 {
			cur := ""
			if v, ok := nspec.Params[item.key].(string); ok {
				cur = v
			} else if d, ok := item.paramDef.Default.(string); ok {
				cur = d
			}
			idx := slices.Index(item.paramDef.Enum, cur)
			next := item.paramDef.Enum[modPos(idx+1, len(item.paramDef.Enum))]
			cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, item.key, next))
			return cmd
		}
		rawVal := ""
		if v, ok := nspec.Params[item.key]; ok {
			rawVal = formatParamValueForEdit(item.paramDef.Type, v)
		} else if item.paramDef.Default != nil {
			rawVal = formatParamValueForEdit(item.paramDef.Type, item.paramDef.Default)
		}
		m.editingField = true
		m.fieldTargetKey = item.key
		m.fieldBuffer = rawVal
		return nil

	case inspEdgeKind:
		if m.sel.Edge == nil {
			return nil
		}
		oldEdge := *m.sel.Edge
		kinds := m.availableEdgeKinds()
		idx := slices.Index(kinds, oldEdge.Kind)
		nextKind := kinds[modPos(idx+1, len(kinds))]
		cmd, err := m.ApplyEdits(
			nodegraph.Disconnect(oldEdge.From, oldEdge.To, oldEdge.Kind),
			nodegraph.Connect(oldEdge.From, oldEdge.To, nextKind),
		)
		if err == nil {
			m.SelectEdge(nodegraph.EdgeSpec{From: oldEdge.From, To: oldEdge.To, Kind: nextKind})
		}
		return cmd

	case inspEdgeDelete:
		cmd, _ := m.DeleteSelected()
		return cmd

	case inspGroupLabel:
		g := m.spec.Groups[m.sel.Group]
		m.editingField = true
		m.fieldTargetKey = "__group_label__"
		m.fieldBuffer = g.Label
		return nil

	case inspGroupCollapsed:
		g := m.spec.Groups[m.sel.Group]
		g.Collapsed = !g.Collapsed
		cmd, _ := m.ApplyEdits(nodegraph.PutGroup(m.sel.Group, g))
		return cmd

	case inspGroupPromote:
		m.focus = focusCanvas
		_, cmd, _ := m.PromoteSelectedGroupToSubgraph("")
		return cmd

	case inspGroupUngroup:
		cmd, _ := m.DeleteSelected()
		return cmd
	}
	return nil
}

func (m *Model) handleFieldEditKey(ev tea.KeyPressMsg) tea.Cmd {
	switch ev.Code {
	case tea.KeyEscape:
		m.editingField = false
		return nil

	case tea.KeyBackspace:
		if len(m.fieldBuffer) > 0 {
			r := []rune(m.fieldBuffer)
			m.fieldBuffer = string(r[:len(r)-1])
		}
		return nil

	case tea.KeyEnter:
		target := m.fieldTargetKey
		raw := m.fieldBuffer
		m.editingField = false

		switch target {
		case "__label__":
			if id := m.sel.PrimaryNode(); id != "" {
				cmd, _ := m.ApplyEdits(nodegraph.SetNodeLabel(id, strings.TrimSpace(raw)))
				return cmd
			}
		case "__caps__":
			if id := m.sel.PrimaryNode(); id != "" {
				var caps []nodegraph.Capability
				for _, part := range strings.Split(raw, ",") {
					s := strings.TrimSpace(part)
					if s == "" {
						continue
					}
					c, err := nodegraph.ParseCapability(s)
					if err != nil {
						m.lastErr = err
						m.statusMsg = err.Error()
						return nil
					}
					caps = append(caps, c)
				}
				cmd, _ := m.ApplyEdits(nodegraph.SetCapabilities(id, caps...))
				return cmd
			}
		case "__group_label__":
			if m.sel.Group != "" {
				g := m.spec.Groups[m.sel.Group]
				g.Label = strings.TrimSpace(raw)
				cmd, _ := m.ApplyEdits(nodegraph.PutGroup(m.sel.Group, g))
				return cmd
			}
		default:
			id := m.sel.PrimaryNode()
			if id == "" {
				return nil
			}
			nspec := m.spec.Nodes[id]
			def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, id)
			pdef, ok := def.Param(target)
			if !ok {
				return nil
			}
			val, deleteKey, err := parseParamInput(pdef, raw)
			if err != nil {
				m.lastErr = err
				m.statusMsg = err.Error()
				return nil
			}
			if deleteKey {
				cmd, _ := m.ApplyEdits(nodegraph.DeleteParam(id, target))
				return cmd
			}
			cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, target, val))
			return cmd
		}
		return nil
	}

	if ev.Text != "" {
		m.fieldBuffer += ev.Text
	} else if ev.Code == tea.KeySpace {
		m.fieldBuffer += " "
	}
	return nil
}

// Chrome & Side Panel Views

func (m *Model) renderHeaderBar(width int) string {
	if width <= 0 {
		m.headerButtons = m.headerButtons[:0]
		return ""
	}
	syncBadge := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#052E16")).
		Background(lipgloss.Color("#34D399")).
		Padding(0, 1).
		Render("LIVE SYNC")
	if !m.LiveSynced() {
		syncBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#451A03")).
			Background(lipgloss.Color("#FBBF24")).
			Padding(0, 1).
			Render("DRAFT")
	}

	titleText := truncateASCII(m.spec.Name, 15)
	if m.Depth() > 0 {
		crumbs := m.Breadcrumbs()
		titleText = truncateASCII(crumbs[0]+" ▸ "+m.spec.Name, 22)
	}
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#F8FAFC")).
		Render(fmt.Sprintf(" %s ", titleText))

	type btnSpec struct {
		id    string
		label string
	}
	modeLabel := "Vec"
	if m.mode == RenderGrid {
		modeLabel = "Grid"
	}
	groupLabel := "Group"
	if m.selectedPromotableGroupID() != "" {
		groupLabel = "◈Promote"
	}
	zoomLabel := fmt.Sprintf("%d%%", int(math.Round(m.cam.normalized().Zoom*100)))
	buttons := make([]btnSpec, 0, 18)
	if m.Depth() > 0 {
		buttons = append(buttons, btnSpec{id: "nav_up", label: "◂ Back"})
	}
	buttons = append(buttons,
		btnSpec{id: "add", label: "+Add"},
		btnSpec{id: "connect", label: "Wire"},
		btnSpec{id: "zoom_out", label: "−"},
		btnSpec{id: "zoom_reset", label: zoomLabel},
		btnSpec{id: "zoom_in", label: "+"},
		btnSpec{id: "fit", label: "Fit"},
		btnSpec{id: "layout", label: "Layout"},
		btnSpec{id: "mode", label: modeLabel},
		btnSpec{id: "inspect", label: "Inspect"},
		btnSpec{id: "params", label: "Params"},
		btnSpec{id: "preview", label: "Preview"},
		btnSpec{id: "tap", label: "Tap"},
		btnSpec{id: "group", label: groupLabel},
		btnSpec{id: "delete", label: "Del"},
		btnSpec{id: "undo", label: "Undo"},
		btnSpec{id: "redo", label: "Redo"},
		btnSpec{id: "help", label: "?"},
	)

	curCol := ansi.StringWidth(syncBadge) + ansi.StringWidth(title)
	m.headerButtons = m.headerButtons[:0]
	parts := []string{syncBadge, title}

	for _, b := range buttons {
		text := " " + b.label + " "
		pillW := ansi.StringWidth(text)
		if curCol+pillW+1 > width {
			break
		}
		bg := "#1E293B"
		fg := "#CBD5E1"
		bold := false
		selID := m.sel.PrimaryNode()
		if b.id == "nav_up" {
			bg = "#0D9488"
			fg = "#F8FAFC"
			bold = true
		} else if (b.id == "inspect" && m.showInspector) ||
			(b.id == "params" && selID != "" && m.IsNodeParamsExpanded(selID)) ||
			(b.id == "preview" && selID != "" && m.IsNodePreviewExpanded(selID)) ||
			(b.id == "tap" && selID != "" && m.ViewerNode() == selID) ||
			(b.id == "group" && m.selectedPromotableGroupID() != "") ||
			(b.id == "add" && m.focus == focusPalette) ||
			(b.id == "connect" && m.focus == focusQuickConnect) ||
			(b.id == "help" && m.focus == focusHelp) {
			bg = "#334155"
			fg = "#38BDF8"
			bold = true
		}
		pill := lipgloss.NewStyle().
			Bold(bold).
			Foreground(lipgloss.Color(fg)).
			Background(lipgloss.Color(bg)).
			Render(text)
		spacer := lipgloss.NewStyle().Background(lipgloss.Color("#0F172A")).Render(" ")
		parts = append(parts, spacer, pill)
		curCol += 1
		m.headerButtons = append(m.headerButtons, uiButtonSpan{
			id:       b.id,
			startCol: curCol,
			endCol:   curCol + pillW,
		})
		curCol += pillW
	}

	row := lipgloss.JoinHorizontal(lipgloss.Center, parts...)
	if ansi.StringWidth(row) > width {
		row = ansi.Truncate(row, width, "")
	}
	rendered := lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Background(lipgloss.Color("#0F172A")).
		Render(row)
	if idx := strings.IndexByte(rendered, '\n'); idx >= 0 {
		rendered = rendered[:idx]
	}
	return rendered
}

func (m *Model) renderFooterBar(width int) string {
	if width <= 0 {
		return ""
	}
	msg := m.statusMsg
	fg := "#94A3B8"
	if m.lastErr != nil {
		msg = "⚠ " + firstLine(m.lastErr.Error())
		fg = "#FCA5A5"
	} else if m.hoverText != "" {
		msg = "⌖ " + m.hoverText
		fg = "#7DD3FC"
		if m.validationErr != nil {
			msg += "  ·  ● " + firstLine(m.validationErr.Error())
		}
	} else if m.validationErr != nil {
		msg = "● " + firstLine(m.validationErr.Error())
		fg = "#FCD34D"
	} else if msg == "" {
		msg = fmt.Sprintf("Ready · %d nodes · %d edges · %d groups · right-click or double-click canvas", len(m.spec.Nodes), len(m.spec.Edges), len(m.spec.Groups))
	}

	rendered := lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Foreground(lipgloss.Color(fg)).
		Background(lipgloss.Color("#0F172A")).
		Padding(0, 1).
		Render(truncateASCII(msg, max(1, width-2)))
	if idx := strings.IndexByte(rendered, '\n'); idx >= 0 {
		rendered = rendered[:idx]
	}
	if ansi.StringWidth(rendered) > width {
		rendered = ansi.Truncate(rendered, width, "")
	}
	return rendered
}

func (m *Model) renderSidePanel(cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	var lines []string
	var headerTitle string
	headerColor := color.NRGBA{R: 30, G: 41, B: 59, A: 255}

	switch m.focus {
	case focusPalette:
		headerTitle = "NODE PALETTE"
		headerColor = rgb(67, 56, 202)
		lines = m.renderPaletteLines(max(1, cols-2))
	case focusQuickConnect:
		headerTitle = "QUICK CONNECT"
		headerColor = rgb(14, 116, 144)
		lines = m.renderQuickConnectLines(max(1, cols-2))
	case focusHelp:
		headerTitle = "EDITOR SHORTCUTS"
		headerColor = rgb(51, 65, 85)
		lines = m.renderHelpLines(max(1, cols-2))
	default:
		headerTitle = "INSPECTOR"
		if m.focus == focusInspector {
			headerColor = rgb(2, 132, 199)
		}
		lines = m.renderInspectorLines(max(1, cols-2))
	}

	innerHdrW := max(1, cols-2)
	closeBtn := "[×]"
	var hdrLine string
	if innerHdrW <= ansi.StringWidth(closeBtn) {
		hdrLine = ansi.Truncate(closeBtn, innerHdrW, "")
	} else {
		maxTitle := max(1, innerHdrW-ansi.StringWidth(closeBtn)-1)
		titleTrim := truncateASCII(headerTitle, maxTitle)
		pad := max(1, innerHdrW-ansi.StringWidth(titleTrim)-ansi.StringWidth(closeBtn))
		hdrLine = ansi.Truncate(titleTrim+strings.Repeat(" ", pad)+closeBtn, innerHdrW, "")
	}

	hdrStyle := lipgloss.NewStyle().
		Width(cols).
		MaxWidth(cols).
		Bold(true).
		Foreground(lipgloss.Color("#F8FAFC")).
		Background(lipgloss.Color(hexColor(headerColor))).
		Padding(0, 1)

	bodyStyle := lipgloss.NewStyle().
		Width(cols).
		MaxWidth(cols).
		Foreground(lipgloss.Color("#CBD5E1")).
		Background(lipgloss.Color("#111827")).
		Padding(0, 1)

	out := make([]string, 0, rows)
	firstRow := firstLine(hdrStyle.Render(hdrLine))
	if ansi.StringWidth(firstRow) > cols {
		firstRow = ansi.Truncate(firstRow, cols, "")
	}
	out = append(out, firstRow)
	for i := 0; i < rows-1; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		rendered := firstLine(bodyStyle.Render(padOrTruncate(line, max(1, cols-2))))
		if ansi.StringWidth(rendered) > cols {
			rendered = ansi.Truncate(rendered, cols, "")
		}
		out = append(out, rendered)
	}
	return strings.Join(out, "\n")
}

func (m *Model) renderPaletteLines(w int) []string {
	m.paletteLineItem = make(map[int]int)
	items := m.filteredPaletteTypes()
	modeLine := "Click or Enter=Add · Esc=Close"
	if m.sel.Edge != nil {
		if m.paletteSpliceMode {
			modeLine = "Mode: [SPLICE INTO WIRE] (click to toggle)"
		} else {
			modeLine = "Mode: [ADD NODE] (click for SPLICE)"
		}
	}
	lines := []string{
		fmt.Sprintf("Search: %s▌", m.paletteQuery),
		truncateASCII(modeLine, w),
		strings.Repeat("─", max(4, w)),
	}
	if len(items) == 0 {
		lines = append(lines, "(no matching node types)")
		return lines
	}
	cur := min(len(items)-1, max(0, m.paletteCursor))
	for i, def := range items {
		prefix := "  "
		if i == cur {
			prefix = "▸ "
		}
		m.paletteLineItem[len(lines)] = i
		lines = append(lines, truncateASCII(prefix+def.ID.String(), w))
		if i == cur && def.Description != "" {
			m.paletteLineItem[len(lines)] = i
			lines = append(lines, "    "+truncateASCII(def.Description, max(4, w-4)))
		}
	}
	return lines
}

func (m *Model) renderQuickConnectLines(w int) []string {
	m.qcLineItem = make(map[int]int)
	kinds := m.availableEdgeKinds()
	kLabel := kinds[min(len(kinds)-1, max(0, m.qcKindIndex))]
	if kLabel == "" {
		kLabel = "default"
	}
	lines := []string{
		fmt.Sprintf("Edge Kind [Click/Tab]: %s ↻", kLabel),
		"Click or Enter=Connect · Esc=Cancel",
		strings.Repeat("─", max(4, w)),
	}
	cur := min(len(m.qcCandidates)-1, max(0, m.qcCursor))
	for i, c := range m.qcCandidates {
		prefix := "  "
		if i == cur {
			prefix = "▸ "
		}
		m.qcLineItem[len(lines)] = i
		row := fmt.Sprintf("%s%s -> %s (%s->%s)", prefix, c.From.Port, c.To, c.OutT, c.InT)
		lines = append(lines, truncateASCII(row, w))
	}
	return lines
}

func (m *Model) renderHelpLines(w int) []string {
	raw := []string{
		"Mouse:",
		"  Wheel / Ctrl+Whl: Zoom canvas at cursor",
		"  Alt + Drag      : Scrub-zoom in / out",
		"  Toolbar − % +   : Click zoom / 100% / Fit",
		"  Space/Right Drag: Pan canvas",
		"  Drag Node/Group : Move layout",
		"  Drag Socket     : Wire / Rewire inlet",
		"  Shift + Drag    : Box multi-select",
		"  Double-Click    : Add / Inspect / Splice",
		"  Card Badges/Rows: Sliders / Preview / Tap",
		"  Right-Click     : Context menu",
		"",
		"Keyboard:",
		"  + / -    Zoom in / out",
		"  1 / 0    Zoom 100% / Fit all",
		"  f        Focus selected node/group",
		"  hjkl/Arr Pan canvas",
		"  a or /   Open Node Palette",
		"  c        Quick-Connect ports",
		"  d        Duplicate selected node",
		"  w / W    Toggle inline Params (node/all)",
		"  o / O    Toggle card Preview (node/all)",
		"  t        Tap node outlet to Viewer",
		"  i / I    Focus / Toggle Inspector",
		"  n / p    Next / Prev node",
		"  e        Cycle edges",
		"  Enter/⌫  Drill into / Exit graph/*",
		"  x / Del  Delete selected",
		"  G (x2)   Group -> Promote Component",
		"  z        Collapse node/group",
		"  v / g    Vector↔Grid / Kitty↔Glyph",
		"  L        Auto-layout graph",
		"  u / U    Undo / Redo",
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, truncateASCII(r, w))
	}
	return out
}

func (m *Model) renderInspectorLines(w int) []string {
	m.inspectorLineItem = make(map[int]int)
	var lines []string
	switch {
	case m.sel.Edge != nil:
		e := *m.sel.Edge
		lines = append(lines,
			fmt.Sprintf("Edge: %s -> %s", e.From, e.To),
			strings.Repeat("─", max(4, w)),
		)
	case m.sel.Group != "":
		g := m.spec.Groups[m.sel.Group]
		lines = append(lines,
			fmt.Sprintf("Group: %s (%d nodes)", m.sel.Group, len(g.Nodes)),
			strings.Repeat("─", max(4, w)),
		)
	case len(m.sel.Nodes) > 0:
		id := m.sel.PrimaryNode()
		nspec := m.spec.Nodes[id]
		lines = append(lines,
			fmt.Sprintf("Node: %s", id),
			fmt.Sprintf("Type: %s", nspec.Type),
			strings.Repeat("─", max(4, w)),
		)
	default:
		return []string{
			"Nothing selected.",
			"",
			"• Click a node, wire, or group",
			"• Shift+Drag canvas to box-select",
			"• Double-click canvas to add node",
			"• Right-click anywhere for menu",
		}
	}

	items := m.buildInspectorItems()
	cur := min(len(items)-1, max(0, m.inspectorRow))
	for i, it := range items {
		prefix := "  "
		if m.focus == focusInspector && i == cur {
			prefix = "▸ "
		}
		val := it.value
		editingThis := m.editingField && i == cur
		if editingThis {
			val = m.fieldBuffer + "▌"
		}
		m.inspectorLineItem[len(lines)] = i

		isNumeric := it.kind == inspParam && (it.paramDef.Type == nodegraph.ParamInt || it.paramDef.Type == nodegraph.ParamNumber)
		if isNumeric && !editingThis && w >= 18 {
			stepper := "[-][+]"
			leftMax := max(4, w-len(stepper)-1)
			left := padOrTruncate(fmt.Sprintf("%s%s: %s", prefix, it.label, val), leftMax)
			lines = append(lines, left+" "+stepper)
		} else {
			lines = append(lines, truncateASCII(fmt.Sprintf("%s%s: %s", prefix, it.label, val), w))
		}
	}

	lines = append(lines, "", "Click row to edit/toggle · [-][+] step")
	return lines
}

func (m *Model) choosePaletteItem(chosen nodegraph.TypeDef, splice bool) tea.Cmd {
	spawnPt := m.pendingSpawnWorld
	m.pendingSpawnWorld = nil
	m.paletteSpliceMode = false
	m.focus = focusCanvas

	var cmd tea.Cmd
	switch {
	case splice && m.sel.Edge != nil:
		_, cmd, _ = m.SpliceNodeOnSelectedEdge(chosen.ID)
	case spawnPt != nil:
		_, cmd, _ = m.AddNodeFromTypeAt(chosen.ID, spawnPt.X-DefaultNodeWidth*0.5, spawnPt.Y-40)
	default:
		_, cmd, _ = m.AddNodeFromType(chosen.ID)
	}
	return tea.Batch(cmd, m.syncPictureSize(), m.refreshVectorPicture())
}

func (m *Model) executeHeaderButton(id string) tea.Cmd {
	m.editingField = false
	switch id {
	case "nav_up":
		return m.NavigateUp()

	case "add":
		if m.focus == focusPalette {
			m.focus = focusCanvas
			m.pendingSpawnWorld = nil
			m.paletteSpliceMode = false
		} else {
			m.focus = focusPalette
			m.paletteQuery = ""
			m.paletteCursor = 0
			m.paletteSpliceMode = m.sel.Edge != nil
			m.pendingSpawnWorld = nil
		}
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case "connect":
		if m.focus == focusQuickConnect {
			m.focus = focusCanvas
			m.vectorDirty = true
			return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
		}
		return m.openQuickConnect()

	case "mode":
		return m.ToggleMode()

	case "zoom_in":
		return m.ZoomBy(1.22)

	case "zoom_out":
		return m.ZoomBy(1.0 / 1.22)

	case "zoom_reset":
		if math.Abs(m.cam.normalized().Zoom-1.0) < 0.03 {
			m.FitToView()
			if m.mode == RenderVector {
				return m.refreshVectorPicture()
			}
			return nil
		}
		return m.SetZoom(1.0)

	case "fit":
		m.FitToView()
		if m.mode == RenderVector {
			return m.refreshVectorPicture()
		}
		return nil

	case "layout":
		cmd, _ := m.AutoLayout()
		return cmd

	case "group":
		if m.selectedPromotableGroupID() != "" {
			_, cmd, _ := m.PromoteSelectedGroupToSubgraph("")
			return cmd
		}
		_, cmd, _ := m.GroupSelectedNodes("")
		return cmd

	case "delete":
		cmd, _ := m.DeleteSelected()
		return cmd

	case "undo":
		cmd, _ := m.Undo()
		return cmd

	case "redo":
		cmd, _ := m.Redo()
		return cmd

	case "inspect":
		m.showInspector = !m.showInspector
		if !m.showInspector && m.focus == focusInspector {
			m.focus = focusCanvas
		} else if m.showInspector {
			m.focus = focusInspector
		}
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case "params":
		return m.ToggleSelectedParams()

	case "preview":
		return m.ToggleSelectedPreview()

	case "tap":
		if selID := m.sel.PrimaryNode(); selID != "" {
			return m.ToggleViewerNode(selID)
		}
		return nil

	case "help":
		if m.focus == focusHelp {
			m.focus = focusCanvas
		} else {
			m.focus = focusHelp
		}
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
	}
	return nil
}

func (m *Model) handleSidePanelClick(ev tea.Mouse, panelCol, panelRow, panelCols int) tea.Cmd {
	if ev.Button != tea.MouseLeft {
		return nil
	}
	// Panel header row (panelRow == 0): check [×] button at right edge.
	if panelRow == 0 {
		if panelCol >= max(0, panelCols-5) {
			m.editingField = false
			if m.focus == focusPalette || m.focus == focusQuickConnect || m.focus == focusHelp {
				m.focus = focusCanvas
				m.pendingSpawnWorld = nil
				m.paletteSpliceMode = false
			} else {
				m.showInspector = false
				m.focus = focusCanvas
			}
			m.vectorDirty = true
			return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
		}
		return nil
	}

	lineIdx := panelRow - 1
	innerW := max(1, panelCols-2)
	contentCol := panelCol - 1 // account for 1-col left padding

	switch m.focus {
	case focusPalette:
		_ = m.renderPaletteLines(innerW)
		if lineIdx == 1 && m.sel.Edge != nil {
			m.paletteSpliceMode = !m.paletteSpliceMode
			return nil
		}
		if itemIdx, ok := m.paletteLineItem[lineIdx]; ok {
			items := m.filteredPaletteTypes()
			if itemIdx >= 0 && itemIdx < len(items) {
				m.paletteCursor = itemIdx
				return m.choosePaletteItem(items[itemIdx], ev.Mod&tea.ModShift != 0 || m.paletteSpliceMode)
			}
		}
		return nil

	case focusQuickConnect:
		_ = m.renderQuickConnectLines(innerW)
		if lineIdx == 0 {
			kinds := m.availableEdgeKinds()
			m.qcKindIndex = modPos(m.qcKindIndex+1, len(kinds))
			return nil
		}
		if itemIdx, ok := m.qcLineItem[lineIdx]; ok && itemIdx >= 0 && itemIdx < len(m.qcCandidates) {
			m.qcCursor = itemIdx
			cand := m.qcCandidates[itemIdx]
			kinds := m.availableEdgeKinds()
			kind := kinds[min(len(kinds)-1, max(0, m.qcKindIndex))]
			m.focus = focusCanvas
			cmd, err := m.ApplyEdits(nodegraph.Connect(cand.From, cand.To, kind))
			if err == nil {
				m.SelectEdge(nodegraph.EdgeSpec{From: cand.From, To: cand.To, Kind: kind})
			}
			return tea.Batch(cmd, m.syncPictureSize(), m.refreshVectorPicture())
		}
		return nil

	case focusHelp:
		m.focus = focusCanvas
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	default:
		m.focus = focusInspector
		_ = m.renderInspectorLines(innerW)
		itemIdx, ok := m.inspectorLineItem[lineIdx]
		if !ok {
			m.editingField = false
			return nil
		}
		items := m.buildInspectorItems()
		if itemIdx < 0 || itemIdx >= len(items) {
			return nil
		}
		// If clicking a different row while inline editing, cancel previous edit first.
		if m.editingField && m.inspectorRow != itemIdx {
			m.editingField = false
		}
		m.inspectorRow = itemIdx
		it := items[itemIdx]

		// Check [-][+] numeric stepper buttons at right edge of row!
		isNumeric := it.kind == inspParam && (it.paramDef.Type == nodegraph.ParamInt || it.paramDef.Type == nodegraph.ParamNumber)
		if isNumeric && !m.editingField && innerW >= 18 && contentCol >= innerW-6 {
			if contentCol < innerW-3 {
				return m.stepNumericParam(it, -1)
			}
			return m.stepNumericParam(it, +1)
		}
		return m.activateInspectorItem(it)
	}
}

func (m *Model) stepNumericParam(item inspectorItem, dir int) tea.Cmd {
	return m.stepNodeNumericParam(m.sel.PrimaryNode(), item.paramDef, dir)
}

func (m *Model) stepNodeNumericParam(id string, paramDef nodegraph.ParamDef, dir int) tea.Cmd {
	if id == "" {
		return nil
	}
	nspec, ok := m.spec.Nodes[id]
	if !ok {
		return nil
	}
	raw, has := nspec.Params[paramDef.Name]
	if !has {
		raw = paramDef.Default
	}

	switch paramDef.Type {
	case nodegraph.ParamInt:
		cur := inlineAnyToInt(raw)
		next := cur + int64(dir)
		if paramDef.Min != nil && float64(next) < *paramDef.Min {
			next = int64(math.Ceil(*paramDef.Min))
		}
		if paramDef.Max != nil && float64(next) > *paramDef.Max {
			next = int64(math.Floor(*paramDef.Max))
		}
		cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, paramDef.Name, next))
		return cmd

	case nodegraph.ParamNumber:
		cur := inlineAnyToFloat(raw)
		step := 1.0
		if paramDef.Min != nil && paramDef.Max != nil && *paramDef.Max > *paramDef.Min {
			span := *paramDef.Max - *paramDef.Min
			switch {
			case span <= 2.5:
				step = 0.05
			case span <= 15.0:
				step = 0.25
			case span <= 50.0:
				step = 1.0
			default:
				step = math.Max(1.0, math.Round(span/20.0))
			}
		} else if math.Abs(cur) < 2.0 && cur != math.Trunc(cur) {
			step = 0.1
		}
		next := math.Round((cur+float64(dir)*step)*1000) / 1000
		if paramDef.Min != nil && next < *paramDef.Min {
			next = *paramDef.Min
		}
		if paramDef.Max != nil && next > *paramDef.Max {
			next = *paramDef.Max
		}
		cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, paramDef.Name, next))
		return cmd
	}
	return nil
}

func (m *Model) handleInlineParamClick(hit HitResult, worldPt Point) tea.Cmd {
	if hit.ParamRow == nil {
		return nil
	}
	row := *hit.ParamRow
	id := hit.NodeID
	nspec, ok := m.spec.Nodes[id]
	if !ok {
		return nil
	}

	switch hit.ParamAction {
	case ParamActionToggleBool:
		cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, row.Key, !row.BoolVal))
		return cmd

	case ParamActionEnumPrev, ParamActionEnumNext:
		if len(row.EnumValues) == 0 {
			return nil
		}
		dir := 1
		if hit.ParamAction == ParamActionEnumPrev {
			dir = -1
		}
		cur := row.DisplayValue
		idx := slices.Index(row.EnumValues, cur)
		if idx < 0 {
			idx = 0
		}
		nextVal := row.EnumValues[modPos(idx+dir, len(row.EnumValues))]
		cmd, _ := m.ApplyEdits(nodegraph.SetParam(id, row.Key, nextVal))
		return cmd

	case ParamActionStepDec:
		return m.stepNodeNumericParam(id, row.ParamDef, -1)

	case ParamActionStepInc:
		return m.stepNodeNumericParam(id, row.ParamDef, +1)

	case ParamActionScrubSlider:
		m.drag = dragState{
			kind:         dragParamSlider,
			startWorld:   worldPt,
			baseSnapshot: m.spec.Clone(),
			paramNodeID:  id,
			paramRow:     row,
		}
		return m.scrubInlineSliderAt(worldPt.X)

	case ParamActionEditInspector:
		m.SelectNode(id)
		m.showInspector = true
		m.focus = focusInspector
		items := m.buildInspectorItems()
		for i, it := range items {
			if it.kind == inspParam && it.key == row.Key {
				m.inspectorRow = i
				break
			}
		}
		rawVal := ""
		if v, exists := nspec.Params[row.Key]; exists {
			rawVal = formatParamValueForEdit(row.ParamDef.Type, v)
		} else if row.ParamDef.Default != nil {
			rawVal = formatParamValueForEdit(row.ParamDef.Type, row.ParamDef.Default)
		}
		m.editingField = true
		m.fieldTargetKey = row.Key
		m.fieldBuffer = rawVal
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
	}
	return nil
}

func (m *Model) scrubInlineSliderAt(worldX float64) tea.Cmd {
	id := m.drag.paramNodeID
	row := m.drag.paramRow
	nspec, ok := m.spec.Nodes[id]
	if !ok || row.TrackRect.W <= 0 || !row.HasRange {
		return nil
	}
	frac := clampFloat((worldX-row.TrackRect.X)/row.TrackRect.W, 0, 1)
	rawVal := row.Min + frac*(row.Max-row.Min)

	rawCur, has := nspec.Params[row.Key]
	if !has {
		rawCur = row.ParamDef.Default
	}

	var nextVal any
	switch row.Kind {
	case InlineParamInt:
		v := int64(math.Round(rawVal))
		if row.ParamDef.Min != nil && float64(v) < *row.ParamDef.Min {
			v = int64(math.Ceil(*row.ParamDef.Min))
		}
		if row.ParamDef.Max != nil && float64(v) > *row.ParamDef.Max {
			v = int64(math.Floor(*row.ParamDef.Max))
		}
		if inlineAnyToInt(rawCur) == v {
			return nil
		}
		nextVal = v

	case InlineParamFloat:
		span := row.Max - row.Min
		quant := 100.0
		if span > 40 {
			quant = 10.0
		} else if span <= 1.0 {
			quant = 1000.0
		}
		v := math.Round(rawVal*quant) / quant
		if row.ParamDef.Min != nil && v < *row.ParamDef.Min {
			v = *row.ParamDef.Min
		}
		if row.ParamDef.Max != nil && v > *row.ParamDef.Max {
			v = *row.ParamDef.Max
		}
		if math.Abs(inlineAnyToFloat(rawCur)-v) < 1e-6 {
			return nil
		}
		nextVal = v

	default:
		return nil
	}

	tEdit := time.Now()
	candidate, err := nodegraph.ApplyEdits(m.spec, nodegraph.SetParam(id, row.Key, nextVal))
	editDur := time.Since(tEdit)
	if err != nil {
		return nil
	}

	m.perf.DragMoves++
	m.perf.LastApplyEdits = editDur
	if editDur > m.perf.MaxApplyEdits {
		m.perf.MaxApplyEdits = editDur
	}
	m.perf.TotalApplyEdits += editDur

	recordUndo := !m.drag.paramUndoPushed
	cmd, commitErr := m.commitCandidate(candidate, recordUndo)
	if commitErr == nil && recordUndo {
		m.drag.paramUndoPushed = true
	}
	m.emitPerfEvent(PerfEvent{
		Kind:         "drag_move",
		Timestamp:    time.Now(),
		DragKind:     m.drag.kind.String(),
		ApplyEditsUS: editDur.Microseconds(),
		SceneBuildUS: m.perf.LastSceneBuild.Microseconds(),
	})
	return cmd
}

func (m *Model) openContextMenuAt(canvasCol, canvasRow int, worldPt Point) {
	hit := m.hitTestAt(worldPt)
	switch hit.Kind {
	case HitNode:
		if !m.sel.HasNode(hit.NodeID) {
			m.SelectNode(hit.NodeID)
		}
		nspec := m.spec.Nodes[hit.NodeID]
		def, _ := lookupTypeDef(m.registry, nspec.Type, m.spec, hit.NodeID)
		var actions []contextMenuAction
		if m.CanDrillIntoNode(hit.NodeID) {
			actions = append(actions, contextMenuAction{id: "drill_in", label: "Drill Into Subgraph (Enter)"})
		}
		actions = append(actions, contextMenuAction{id: "inspect", label: "Inspect Parameters"})
		if len(def.Params) > 0 {
			actions = append(actions, contextMenuAction{id: "toggle_params", label: "Toggle Inline Params (w)"})
		}
		if len(def.Outlets) > 0 {
			actions = append(actions,
				contextMenuAction{id: "toggle_preview", label: "Toggle Card Preview (o)"},
				contextMenuAction{id: "toggle_tap", label: "Tap Outlet to Viewer (t)"},
			)
		}
		actions = append(actions,
			contextMenuAction{id: "connect", label: "Quick-Connect Outlet..."},
			contextMenuAction{id: "duplicate", label: "Duplicate Node"},
			contextMenuAction{id: "collapse", label: "Collapse / Expand"},
		)
		if len(m.sel.Nodes) >= 1 {
			actions = append(actions,
				contextMenuAction{id: "group", label: "Group Selected"},
				contextMenuAction{id: "extract_subgraph", label: "Extract Selection to Component"},
			)
		}
		actions = append(actions, contextMenuAction{id: "delete", label: "Delete Node"})
		m.ctxMenu = &contextMenuState{
			title:     "Node · " + hit.NodeID,
			anchorCol: canvasCol,
			anchorRow: canvasRow,
			worldPt:   worldPt,
			actions:   actions,
			hoverIdx:  0,
		}

	case HitWire:
		m.SelectEdge(hit.Edge)
		m.ctxMenu = &contextMenuState{
			title:     fmt.Sprintf("Wire · %s -> %s", hit.Edge.From, hit.Edge.To),
			anchorCol: canvasCol,
			anchorRow: canvasRow,
			worldPt:   worldPt,
			actions: []contextMenuAction{
				{id: "splice", label: "Splice Node Into Wire..."},
				{id: "edge_kind", label: "Cycle Delivery Kind"},
				{id: "delete", label: "Disconnect Wire"},
			},
			hoverIdx: 0,
		}

	case HitGroup:
		m.SelectGroup(hit.GroupID)
		m.ctxMenu = &contextMenuState{
			title:     "Group · " + hit.GroupID,
			anchorCol: canvasCol,
			anchorRow: canvasRow,
			worldPt:   worldPt,
			actions: []contextMenuAction{
				{id: "promote_group", label: "Promote Group to Component (G)"},
				{id: "rename_group", label: "Rename Group"},
				{id: "collapse", label: "Collapse / Expand"},
				{id: "delete", label: "Ungroup Nodes"},
			},
			hoverIdx: 0,
		}

	default:
		var canvasActions []contextMenuAction
		if m.Depth() > 0 {
			canvasActions = append(canvasActions, contextMenuAction{id: "nav_up", label: "Return to Parent Graph (⌫)"})
		}
		canvasActions = append(canvasActions,
			contextMenuAction{id: "add_here", label: "Add Node Here..."},
			contextMenuAction{id: "zoom_in", label: "Zoom In (+)"},
			contextMenuAction{id: "zoom_out", label: "Zoom Out (-)"},
			contextMenuAction{id: "zoom_100", label: "Zoom to 100% (1)"},
			contextMenuAction{id: "fit", label: "Fit Camera to Graph (0)"},
			contextMenuAction{id: "layout", label: "Auto-Layout Graph (L)"},
			contextMenuAction{id: "mode", label: "Toggle Vector / Grid (v)"},
		)
		m.ctxMenu = &contextMenuState{
			title:     "Canvas",
			anchorCol: canvasCol,
			anchorRow: canvasRow,
			worldPt:   worldPt,
			actions:   canvasActions,
			hoverIdx:  0,
		}
	}
	m.rebuildScene()
	m.vectorDirty = true
}

func (m *Model) executeContextAction(actionID string, worldPt Point) tea.Cmd {
	switch actionID {
	case "nav_up":
		return m.NavigateUp()

	case "drill_in":
		cmd, _ := m.DrillIntoSelectedSubgraph()
		return cmd

	case "promote_group":
		_, cmd, _ := m.PromoteSelectedGroupToSubgraph("")
		return cmd

	case "extract_subgraph":
		_, cmd, _ := m.ExtractSelectedNodesToSubgraph("")
		return cmd

	case "add_here":
		pt := worldPt
		m.pendingSpawnWorld = &pt
		m.paletteSpliceMode = false
		m.focus = focusPalette
		m.paletteQuery = ""
		m.paletteCursor = 0
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case "inspect":
		m.showInspector = true
		m.focus = focusInspector
		m.inspectorRow = 0
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case "toggle_params":
		return m.ToggleSelectedParams()

	case "toggle_preview":
		return m.ToggleSelectedPreview()

	case "toggle_tap":
		if selID := m.sel.PrimaryNode(); selID != "" {
			return m.ToggleViewerNode(selID)
		}
		return nil

	case "connect":
		return m.openQuickConnect()

	case "duplicate":
		_, cmd, _ := m.DuplicateSelectedNodes()
		return cmd

	case "collapse":
		return m.toggleSelectedCollapse()

	case "group":
		if m.selectedPromotableGroupID() != "" {
			_, cmd, _ := m.PromoteSelectedGroupToSubgraph("")
			return cmd
		}
		_, cmd, _ := m.GroupSelectedNodes("")
		return cmd

	case "splice":
		m.focus = focusPalette
		m.paletteQuery = ""
		m.paletteCursor = 0
		m.paletteSpliceMode = true
		m.pendingSpawnWorld = nil
		m.vectorDirty = true
		return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())

	case "edge_kind":
		return m.activateInspectorItem(inspectorItem{kind: inspEdgeKind})

	case "rename_group":
		if m.sel.Group != "" {
			g := m.spec.Groups[m.sel.Group]
			m.showInspector = true
			m.focus = focusInspector
			m.inspectorRow = 0
			m.editingField = true
			m.fieldTargetKey = "__group_label__"
			m.fieldBuffer = g.Label
			m.vectorDirty = true
			return tea.Batch(m.syncPictureSize(), m.refreshVectorPicture())
		}

	case "delete":
		cmd, _ := m.DeleteSelected()
		return cmd

	case "layout":
		cmd, _ := m.AutoLayout()
		return cmd

	case "fit":
		m.FitToView()
		if m.mode == RenderVector {
			return m.refreshVectorPicture()
		}
		return nil

	case "zoom_in":
		return m.ZoomBy(1.25)

	case "zoom_out":
		return m.ZoomBy(1.0 / 1.25)

	case "zoom_100":
		return m.SetZoom(1.0)

	case "mode":
		return m.ToggleMode()
	}
	return nil
}

func hexColor(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}
