// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Command editor showcases the interactive Bubble Tea v2 ntnodegraph/editor
// canvas across Vector (rasterx + ntpicture Kitty/half-block) and Grid
// (ntcanvas.GraphemeGrid) rendering modes, inline parameter drawers, and
// simulated on-card RGBA/text previews.
package main

import (
	"flag"
	"fmt"
	"os"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"nimbleterminal.dev/ntnodegraph/editor"
	"nimbleterminal.dev/ntnodegraph/examples/internal/catalog"
	"nimbleterminal.dev/ntnodegraph/examples/internal/demohelp"
)

type editorDemo struct {
	env      *catalog.Environment
	ed       *editor.Model
	quitting bool
}

func newEditorDemo(initialMode editor.RenderMode) *editorDemo {
	env := catalog.New()
	spec := catalog.EditorSpec()

	ed := editor.New(spec, env.Registry, editor.Options{
		BackendProfile:      env.Backend,
		Library:             env.Library,
		OnRegisterComponent: env.RegisterComponent,
		Mode:                initialMode,
		Width:               104,
		Height:              30,
	})
	catalog.SeedPreviews(ed)
	ed.SetNodeParamsExpanded("coder", true)
	ed.SetNodePreviewExpanded("bloom", true)

	return &editorDemo{
		env: env,
		ed:  ed,
	}
}

func (m *editorDemo) ContextID() string { return "ntnodegraph-editor" }

func (m *editorDemo) Help() []key.Binding {
	if m.ed.WantCaptureKeyboard() {
		return []key.Binding{
			demohelp.Key("Confirm", "enter"),
			demohelp.Key("Cancel", "esc"),
		}
	}
	return []key.Binding{
		demohelp.Key("Vector/Grid", "v"),
		demohelp.Key("Params", "w"),
		demohelp.Key("Previews", "O"),
		demohelp.Key("Add node", "a"),
		demohelp.Key("Inspector", "i"),
		demohelp.Key("Layout", "L"),
		demohelp.Key("Fit", "0"),
		demohelp.Key("Undo", "u"),
		demohelp.Key("Help", "?"),
		demohelp.Key("Quit", "q"),
	}
}

func (m *editorDemo) Summary() string {
	spec := m.ed.Spec()
	return fmt.Sprintf("Editor: mode=%s nodes=%d edges=%d synced=%t",
		m.ed.Mode(), len(spec.Nodes), len(spec.Edges), m.ed.LiveSynced())
}

func (m *editorDemo) Init() tea.Cmd {
	return m.ed.Init()
}

func (m *editorDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		s := kp.String()
		if s == "ctrl+c" || (s == "q" && !m.ed.WantCaptureKeyboard()) {
			m.quitting = true
			return m, tea.Sequence(m.ed.Close(), tea.Quit)
		}
	}
	var cmd tea.Cmd
	m.ed, cmd = m.ed.Update(msg)
	if _, ok := msg.(editor.EditCommittedMsg); ok {
		catalog.SeedPreviews(m.ed)
	}
	return m, cmd
}

func (m *editorDemo) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	v := tea.NewView(m.ed.View())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.ReportFocus = true
	return v
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
	demohelp.Main(newEditorDemo(mode))
}
