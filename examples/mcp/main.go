// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Command mcp showcases live co-editing between ntnodegraph/editor and the
// headless ntnodegraph/ngmcp JSON-RPC 2.0 server against a shared MemoryStore,
// displaying diff-stable canonical YAML alongside the interactive graph canvas.
package main

import (
	"context"
	"encoding/json"
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
	"nimbleterminal.dev/ntnodegraph/ngmcp"
)

type mcpStep struct {
	title   string
	request string
}

var scriptedMCPSteps = []mcpStep{
	{
		title: "edit: add surface/log@1 telemetry sink + wire filter.count",
		request: `{
			"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{
				"name":"edit",
				"arguments":{
					"target":"hn-editorial-desk",
					"auto_layout":true,
					"ops":[
						{"op":"add_node","id":"metrics","type":"surface/log@1","params":{"prefix":"story-count"}},
						{"op":"set_node_label","node":"metrics","label":"Story Counter"},
						{"op":"connect","from":"filter.count","to":"metrics.in"},
						{"op":"set_param","node":"filter","key":"keyword","value":"golang"}
					]
				}
			}
		}`,
	},
	{
		title: "component(extract): promote [filter, typeset] -> graph/story-curator@1",
		request: `{
			"jsonrpc":"2.0","id":2,"method":"tools/call",
			"params":{
				"name":"component",
				"arguments":{
					"action":"extract",
					"target":"hn-editorial-desk",
					"nodes":["filter","typeset"],
					"component_name":"story-curator",
					"instance_id":"curator",
					"expose_params":["filter.keyword","typeset.theme"],
					"replace_in_target":true
				}
			}
		}`,
	},
	{
		title: "edit: tune curator.keyword='webgpu' & curator.theme='terminal'",
		request: `{
			"jsonrpc":"2.0","id":3,"method":"tools/call",
			"params":{
				"name":"edit",
				"arguments":{
					"target":"hn-editorial-desk",
					"auto_layout":true,
					"ops":[
						{"op":"set_param","node":"curator","key":"keyword","value":"webgpu"},
						{"op":"set_param","node":"curator","key":"theme","value":"terminal"}
					]
				}
			}
		}`,
	},
	{
		title: "validate: audit port types, capabilities, and topological order",
		request: `{
			"jsonrpc":"2.0","id":4,"method":"tools/call",
			"params":{
				"name":"validate",
				"arguments":{"target":"hn-editorial-desk"}
			}
		}`,
	},
}

type mcpDemo struct {
	env        *catalog.Environment
	store      *ngmcp.MemoryStore
	server     *ngmcp.Server
	ed         *editor.Model
	width      int
	height     int
	layout     int // 0=split, 1=graph, 2=yaml
	stepIdx    int
	yamlScroll int
	mcpLog     []string
	quitting   bool
}

func newMCPDemo(initialMode editor.RenderMode) *mcpDemo {
	env := catalog.New()
	initial := catalog.MCPSpec()
	store := ngmcp.NewMemoryStore(initial.Name, map[string]*ntnodegraph.Spec{
		initial.Name: initial,
	})

	p := env.Backend
	srv := ngmcp.NewServer(ngmcp.Options{
		Registry: env.Registry,
		Library:  env.Library,
		Backend:  &p,
		Store:    store,
		AutoLayout: func(spec *ntnodegraph.Spec, reg *ntnodegraph.Registry, req ngmcp.LayoutRequest) ([]ntnodegraph.EditOp, error) {
			opts := editor.DefaultLayoutOptions()
			if req.Direction == "tb" {
				opts.Direction = editor.LayoutTopToBottom
			}
			if req.ColumnGap > 0 {
				opts.ColumnGap = req.ColumnGap
			}
			if req.RowGap > 0 {
				opts.RowGap = req.RowGap
			}
			return editor.AutoLayoutOpsWith(spec, reg, opts), nil
		},
		RegisterComponent: func(_ context.Context, comp *ntnodegraph.Spec, _ bool) (ntnodegraph.TypeDef, string, error) {
			def, err := env.RegisterComponent(comp)
			if err != nil {
				return ntnodegraph.TypeDef{}, "", err
			}
			return def, "memory", nil
		},
	})

	m := &mcpDemo{
		env:    env,
		store:  store,
		server: srv,
		width:  116,
		height: 30,
		mcpLog: []string{
			"Ready: press p (or click 'MCP Patch') to apply JSON-RPC 2.0 edits via ngmcp.",
		},
	}

	leftW, _ := m.paneWidths()
	m.ed = editor.New(initial, env.Registry, editor.Options{
		BackendProfile:      env.Backend,
		Library:             env.Library,
		OnRegisterComponent: env.RegisterComponent,
		Mode:                initialMode,
		Width:               max(24, leftW),
		Height:              m.height,
		Commit: func(next *ntnodegraph.Spec, diff ntnodegraph.SpecDiff) error {
			if _, err := store.Put(context.Background(), initial.Name, next, diff, ngmcp.PutOptions{Create: true}); err != nil {
				return err
			}
			m.appendLog(fmt.Sprintf("canvas -> store.Put(%s): +%dn -%dn ~%dp",
				initial.Name, len(diff.AddedNodes), len(diff.RemovedNodes), len(diff.ModifiedNodes)))
			return nil
		},
	})
	return m
}

func (m *mcpDemo) appendLog(line string) {
	m.mcpLog = append(m.mcpLog, line)
	if len(m.mcpLog) > 6 {
		m.mcpLog = m.mcpLog[len(m.mcpLog)-6:]
	}
}

func (m *mcpDemo) paneWidths() (leftW, rightW int) {
	switch m.layout {
	case 1:
		return max(24, m.width), 0
	case 2:
		return 0, max(24, m.width)
	default:
		if m.width < 84 {
			return max(24, m.width), 0
		}
		rightW = min(48, max(36, (m.width*2)/5))
		leftW = m.width - rightW - 1
		return leftW, rightW
	}
}

func (m *mcpDemo) applyLayout() tea.Cmd {
	leftW, _ := m.paneWidths()
	if leftW <= 0 {
		if m.ed.Picture() != nil {
			return m.ed.Picture().SetVisible(false)
		}
		return nil
	}
	m.ed.SetScreenOffset(0, 0)
	return m.ed.SetSize(leftW, m.height)
}

func (m *mcpDemo) runNextMCPStep() tea.Cmd {
	if m.stepIdx >= len(scriptedMCPSteps) {
		// Reset back to initial spec so the user can replay endlessly.
		initial := catalog.MCPSpec()
		_, _ = m.store.Put(context.Background(), initial.Name, initial, ntnodegraph.SpecDiff{}, ngmcp.PutOptions{Create: true})
		m.stepIdx = 0
		m.yamlScroll = 0
		m.appendLog("reset -> restored initial hn-editorial-desk spec")
		return tea.Batch(m.ed.SetSpec(initial), func() tea.Msg {
			m.ed.FitToView()
			return nil
		})
	}

	step := scriptedMCPSteps[m.stepIdx]
	m.stepIdx++

	respBytes, hasReply := m.server.HandleJSON(context.Background(), []byte(step.request))
	if !hasReply {
		m.appendLog("MCP: notification processed")
		return nil
	}

	var env struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respBytes, &env)
	if env.Error != nil {
		m.appendLog("RPC error: " + env.Error.Message)
		return nil
	}
	status := "OK"
	if env.Result.IsError {
		status = "TOOL_ERR"
	}
	m.appendLog(fmt.Sprintf("[%s] #%d %s", status, m.stepIdx, step.title))

	updated, getErr := m.store.Get(context.Background(), "hn-editorial-desk")
	if getErr != nil {
		return nil
	}
	cmd := m.ed.SetSpec(updated)
	m.ed.FitToView()
	return cmd
}

func (m *mcpDemo) ContextID() string { return "ntnodegraph-mcp" }

func (m *mcpDemo) Help() []key.Binding {
	if m.ed.WantCaptureKeyboard() {
		return []key.Binding{
			demohelp.Key("Confirm", "enter"),
			demohelp.Key("Cancel", "esc"),
		}
	}
	patchLabel := "MCP Patch"
	if m.stepIdx >= len(scriptedMCPSteps) {
		patchLabel = "Reset Demo"
	}
	return []key.Binding{
		demohelp.Key(patchLabel, "p"),
		demohelp.Key("Split/Full", "F"),
		demohelp.Key("Vector/Grid", "v"),
		demohelp.Key("Scroll YAML", "]"),
		demohelp.Key("Add node", "a"),
		demohelp.Key("Inspector", "i"),
		demohelp.Key("Quit", "q"),
	}
}

func (m *mcpDemo) Summary() string {
	spec := m.ed.RootSpec()
	return fmt.Sprintf("MCP: step=%d nodes=%d edges=%d layout=%d mode=%s",
		m.stepIdx, len(spec.Nodes), len(spec.Edges), m.layout, m.ed.Mode())
}

func (m *mcpDemo) Init() tea.Cmd {
	return m.ed.Init()
}

func (m *mcpDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if !m.ed.WantCaptureKeyboard() {
			switch s {
			case "p":
				return m, m.runNextMCPStep()
			case "F":
				m.layout = (m.layout + 1) % 3
				return m, m.applyLayout()
			case "]":
				m.yamlScroll += 5
				return m, nil
			case "[":
				m.yamlScroll = max(0, m.yamlScroll-5)
				return m, nil
			}
		}
	}

	if m.layout == 2 {
		return m, nil
	}
	var cmd tea.Cmd
	m.ed, cmd = m.ed.Update(msg)
	return m, cmd
}

func (m *mcpDemo) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	leftW, rightW := m.paneWidths()
	var out string
	switch {
	case rightW <= 0:
		out = m.ed.View()
	case leftW <= 0:
		out = m.renderRightPane(rightW, m.height)
	default:
		left := m.ed.View()
		right := m.renderRightPane(rightW, m.height)
		sep := strings.Repeat("│\n", max(0, m.height-1)) + "│"
		sepStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#2d3848")).Render(sep)
		out = lipgloss.JoinHorizontal(lipgloss.Top, left, sepStyled, right)
	}
	v := tea.NewView(out)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.ReportFocus = true
	return v
}

func (m *mcpDemo) renderRightPane(w, h int) string {
	hdrStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787"))
	subStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#58a6ff"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b9bb4"))
	yamlKeyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#93c5fd"))

	var lines []string
	lines = append(lines, hdrStyle.Render(fmt.Sprintf("NGMCP JSON-RPC 2.0 (%d/%d · press p)", m.stepIdx, len(scriptedMCPSteps))))
	for _, entry := range m.mcpLog {
		lines = append(lines, " "+dimStyle.Render(entry))
	}
	lines = append(lines, strings.Repeat("─", max(4, w-2)))

	spec := m.ed.RootSpec()
	yamlBytes, err := ntnodegraph.MarshalYAML(spec)
	var yamlLines []string
	if err != nil {
		yamlLines = []string{"marshal error: " + err.Error()}
	} else {
		yamlLines = strings.Split(strings.TrimRight(string(yamlBytes), "\n"), "\n")
	}

	maxScroll := max(0, len(yamlLines)-max(4, h-len(lines)-2))
	if m.yamlScroll > maxScroll {
		m.yamlScroll = maxScroll
	}
	lines = append(lines, subStyle.Render(fmt.Sprintf("CANONICAL YAML (%d lines · [/] scroll)", len(yamlLines))))

	for i := m.yamlScroll; i < len(yamlLines); i++ {
		raw := yamlLines[i]
		if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.HasPrefix(strings.TrimSpace(raw), "-") {
			raw = yamlKeyStyle.Render(raw[:colon+1]) + raw[colon+1:]
		}
		lines = append(lines, raw)
	}

	out := make([]string, h)
	for i := range h {
		line := ""
		if i < len(lines) {
			line = ansi.Truncate(lines[i], max(1, w-1), "…")
		}
		out[i] = " " + line
		if pad := w - ansi.StringWidth(out[i]); pad > 0 {
			out[i] += strings.Repeat(" ", pad)
		}
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
	demohelp.Main(newMCPDemo(mode))
}
