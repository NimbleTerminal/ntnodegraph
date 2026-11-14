// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Command subgraphs showcases composable graph/* component libraries,
// Enter/Backspace breadcrumb component drill-in, G group-to-subgraph promotion,
// and live Flatten() expansion with fail-closed capability roll-up.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"nimbleterminal.dev/ntnodegraph"
	"nimbleterminal.dev/ntnodegraph/editor"
	"nimbleterminal.dev/ntnodegraph/examples/internal/catalog"
	"nimbleterminal.dev/ntnodegraph/examples/internal/demohelp"
)

type subgraphsDemo struct {
	env          *catalog.Environment
	ed           *editor.Model
	width        int
	height       int
	showFlatPane bool
	quitting     bool
}

func newSubgraphsDemo(initialMode editor.RenderMode) *subgraphsDemo {
	env := catalog.New()
	spec := catalog.SubgraphSpec()

	m := &subgraphsDemo{
		env:          env,
		width:        112,
		height:       30,
		showFlatPane: true,
	}
	leftW, _ := m.paneWidths()
	m.ed = editor.New(spec, env.Registry, editor.Options{
		BackendProfile:      env.Backend,
		Library:             env.Library,
		OnRegisterComponent: env.RegisterComponent,
		Mode:                initialMode,
		Width:               leftW,
		Height:              m.height,
	})
	catalog.SeedPreviews(m.ed)
	m.ed.SelectNode("synth")
	m.ed.SetNodePreviewExpanded("cinema", true)
	return m
}

func (m *subgraphsDemo) paneWidths() (leftW, rightW int) {
	if !m.showFlatPane || m.width < 84 {
		return max(20, m.width), 0
	}
	rightW = min(44, max(34, m.width/3))
	leftW = m.width - rightW - 1
	return leftW, rightW
}

func (m *subgraphsDemo) applyLayout() tea.Cmd {
	leftW, _ := m.paneWidths()
	m.ed.SetScreenOffset(0, 0)
	return m.ed.SetSize(leftW, m.height)
}

func (m *subgraphsDemo) ContextID() string { return "ntnodegraph-subgraphs" }

func (m *subgraphsDemo) Help() []key.Binding {
	if m.ed.WantCaptureKeyboard() {
		return []key.Binding{
			demohelp.Key("Confirm", "enter"),
			demohelp.Key("Cancel", "esc"),
		}
	}
	keys := []key.Binding{
		demohelp.Key("Drill in", "enter"),
	}
	if m.ed.Depth() > 0 {
		keys = append(keys, demohelp.Key("Back", "backspace"))
	}
	keys = append(keys,
		demohelp.Key("Promote group", "G"),
		demohelp.Key("Flatten pane", "y"),
		demohelp.Key("Vector/Grid", "v"),
		demohelp.Key("Params", "w"),
		demohelp.Key("Previews", "o"),
		demohelp.Key("Quit", "q"),
	)
	return keys
}

func (m *subgraphsDemo) Summary() string {
	root := m.ed.RootSpec()
	p := m.env.Backend
	flatNodes := 0
	if flat, _, err := ntnodegraph.Flatten(root, m.env.Registry, m.env.Library, ntnodegraph.FlattenOptions{Backend: &p}); err == nil {
		flatNodes = len(flat.Nodes)
	}
	return fmt.Sprintf("Subgraphs: depth=%d libs=%d flat_nodes=%d pane=%t mode=%s",
		m.ed.Depth(), len(m.env.Library.List()), flatNodes, m.showFlatPane, m.ed.Mode())
}

func (m *subgraphsDemo) Init() tea.Cmd {
	return m.ed.Init()
}

func (m *subgraphsDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch ev := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(24, ev.Width)
		m.height = max(8, ev.Height)
		return m, m.applyLayout()

	case tea.KeyPressMsg:
		s := ev.String()
		if s == "ctrl+c" || (s == "q" && !m.ed.WantCaptureKeyboard()) {
			m.quitting = true
			return m, tea.Sequence(m.ed.Close(), tea.Quit)
		}
		if s == "y" && !m.ed.WantCaptureKeyboard() {
			m.showFlatPane = !m.showFlatPane
			return m, m.applyLayout()
		}
	}

	var cmd tea.Cmd
	m.ed, cmd = m.ed.Update(msg)
	if _, ok := msg.(editor.EditCommittedMsg); ok {
		catalog.SeedPreviews(m.ed)
	}
	return m, cmd
}

func (m *subgraphsDemo) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	leftW, rightW := m.paneWidths()
	left := m.ed.View()
	var out string
	if rightW <= 0 {
		out = left
	} else {
		right := m.renderFlattenPane(rightW, m.height)
		sep := strings.Repeat("│\n", max(0, m.height-1)) + "│"
		sepStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#2d3848")).Render(sep)
		_ = leftW
		out = lipgloss.JoinHorizontal(lipgloss.Top, left, sepStyled, right)
	}
	v := tea.NewView(out)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.ReportFocus = true
	return v
}

func (m *subgraphsDemo) renderFlattenPane(w, h int) string {
	hdrStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b9bb4"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#58a6ff"))
	capStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))

	var lines []string
	lines = append(lines, hdrStyle.Render("FLATTENED GRAPH & LIBRARY (y)"))
	crumbs := strings.Join(m.ed.Breadcrumbs(), " › ")
	lines = append(lines, dimStyle.Render("Trail: "+crumbs))
	lines = append(lines, "")

	comps := m.env.Library.List()
	lines = append(lines, fmt.Sprintf("Library (%d components):", len(comps)))
	for _, entry := range comps {
		lines = append(lines, fmt.Sprintf(" • %s (%dn, %de)",
			idStyle.Render(entry.ID.String()),
			len(entry.Spec.Nodes),
			len(entry.Spec.Edges),
		))
	}
	lines = append(lines, "")

	root := m.ed.RootSpec()
	p := m.env.Backend
	flat, report, err := ntnodegraph.Flatten(root, m.env.Registry, m.env.Library, ntnodegraph.FlattenOptions{Backend: &p})
	if err != nil {
		lines = append(lines, "Flatten status: draft / pending")
		lines = append(lines, dimStyle.Render(err.Error()))
	} else {
		lines = append(lines, fmt.Sprintf("Flatten(%s): %d -> %d concrete nodes",
			root.Name, len(root.Nodes), len(flat.Nodes)))
		lines = append(lines, dimStyle.Render(fmt.Sprintf("Expanded %d graph/* instance(s), %d edges",
			report.ExpandedInstances, len(flat.Edges))))
		lines = append(lines, "")
		for _, nid := range flat.SortedNodeIDs() {
			ns := flat.Nodes[nid]
			caps := ""
			if len(ns.Capabilities) > 0 {
				rawCaps := make([]string, len(ns.Capabilities))
				for i, c := range ns.Capabilities {
					rawCaps[i] = c.String()
				}
				caps = " [" + capStyle.Render(strings.Join(rawCaps, ",")) + "]"
			}
			lines = append(lines, fmt.Sprintf(" %s %s%s",
				idStyle.Render(nid),
				dimStyle.Render(ns.Type.String()),
				caps,
			))
		}
	}

	lines = append(lines, "")
	lines = append(lines, dimStyle.Render("Tips: Select synth/cinema + Enter to drill"))
	lines = append(lines, dimStyle.Render("Click group header + G to promote group"))

	out := make([]string, h)
	for i := range h {
		line := ""
		if i < len(lines) {
			line = ansi.Truncate(lines[i], max(1, w-1), "…")
		}
		pad := max(0, w-ansi.StringWidth(line))
		out[i] = " " + ansi.Truncate(line, max(0, w-1), "…")
		if padNow := w - ansi.StringWidth(out[i]); padNow > 0 {
			out[i] += strings.Repeat(" ", padNow)
		}
		_ = pad
	}
	return strings.Join(out, "\n")
}

func main() {
	modeFlag := flag.String("mode", "vector", "Initial canvas mode: vector or grid")
	flag.Parse()

	mode := editor.RenderVector
	if *modeFlag == "grid" {
		mode = editor.RenderGrid
	} else if *modeFlag != "vector" {
		fmt.Fprintf(os.Stderr, "unknown -mode %q (expected vector or grid)\n", *modeFlag)
		os.Exit(2)
	}
	demohelp.Main(newSubgraphsDemo(mode))
}
