// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"math"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newBenchSpecAndRegistry(tb testing.TB) *Model {
	tb.Helper()
	tDummy := &testing.T{}
	spec, reg, profile := newFiveStageBranchingSpec(tDummy)
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderVector,
		Width:          138, // half of a 276-col wide Studio split-pane -> ~96 canvas cols
		Height:         47,  // 45 body rows
		ShowInspector:  true,
		ShowPerfHUD:    true,
	})
	return ed
}

func TestEditorPerfTelemetryAndWorldToCell(t *testing.T) {
	spec, reg, profile := newFiveStageBranchingSpec(t)
	var events []PerfEvent
	ed := New(spec, reg, Options{
		BackendProfile: profile,
		Mode:           RenderVector,
		Width:          138,
		Height:         47,
		ShowInspector:  true,
		ShowPerfHUD:    true,
		OnPerfEvent: func(ev PerfEvent) {
			events = append(events, ev)
		},
	})
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()

	sc := ed.Scene()
	filterBox := sc.Nodes[sc.NodeByID["filter"]].Bounds
	col, row, ok := ed.WorldToTerminalCell(filterBox.X+filterBox.W*0.5, filterBox.Y+14)
	if !ok {
		t.Fatalf("expected filter header center to map to visible terminal cell")
	}

	ed.ResetPerfStats()
	events = nil

	ed.Update(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})
	for step := 1; step <= 6; step++ {
		_, cmd := ed.Update(tea.MouseMotionMsg{X: col + step*2, Y: row + step, Button: tea.MouseLeft})
		if step == 1 && cmd != nil {
			// Fire coalesced 60 Hz refresh tick so vector submission is recorded.
			ed.Update(vectorRefreshTickMsg{})
		}
	}
	ed.Update(tea.MouseReleaseMsg{X: col + 12, Y: row + 6, Button: tea.MouseLeft})

	st := ed.PerfStats()
	if st.DragMoves == 0 || st.VectorRasters == 0 || st.FramesSubmitted == 0 {
		t.Fatalf("expected non-zero perf counters after drag, got %+v", st)
	}
	if len(events) == 0 {
		t.Fatalf("expected OnPerfEvent callbacks during drag")
	}
}

func BenchmarkEditorNodeDragStepOnly(b *testing.B) {
	ed := newBenchSpecAndRegistry(b)
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()
	if cmd := ed.Picture().SetTerminal(true, 18, 38); cmd != nil {
		_ = cmd()
	}
	ed.FitToView()

	sc := ed.Scene()
	nb := sc.Nodes[sc.NodeByID["filter"]]
	col, row, _ := ed.WorldToTerminalCell(nb.Bounds.X+nb.Bounds.W*0.5, nb.Bounds.Y+14)
	ed.Update(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dx := int(math.Round(math.Sin(float64(i)*0.15) * 18))
		dy := int(math.Round(math.Cos(float64(i)*0.15) * 8))
		ed.Update(tea.MouseMotionMsg{X: col + dx, Y: row + dy, Button: tea.MouseLeft})
	}
}

func BenchmarkEditorNodeDragVectorRaster(b *testing.B) {
	cases := []struct {
		name         string
		cellW, cellH int
	}{
		{name: "Standard_10x20", cellW: 10, cellH: 20},
		{name: "Retina_18x38", cellW: 18, cellH: 38},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			ed := newBenchSpecAndRegistry(b)
			defer func() {
				if cmd := ed.Close(); cmd != nil {
					_ = cmd()
				}
			}()
			if cmd := ed.Picture().SetTerminal(true, tc.cellW, tc.cellH); cmd != nil {
				_ = cmd()
			}
			ed.FitToView()

			sc := ed.Scene()
			nb := sc.Nodes[sc.NodeByID["filter"]]
			col, row, _ := ed.WorldToTerminalCell(nb.Bounds.X+nb.Bounds.W*0.5, nb.Bounds.Y+14)
			ed.Update(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})

			canvasCols, _ := ed.splitColumns()
			bodyRows := max(4, ed.rows-2)
			pxW, pxH := ed.canvasVectorPixelSize(canvasCols, bodyRows)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dx := int(math.Round(math.Sin(float64(i)*0.15) * 18))
				dy := int(math.Round(math.Cos(float64(i)*0.15) * 8))
				ed.Update(tea.MouseMotionMsg{X: col + dx, Y: row + dy, Button: tea.MouseLeft})
				_ = RenderSceneVector(ed.Scene(), ed.Camera(), pxW, pxH, ed.theme, "BENCH")
			}
		})
	}
}

func BenchmarkEditorNodeDragFullKittyPipeline(b *testing.B) {
	ed := newBenchSpecAndRegistry(b)
	defer func() {
		if cmd := ed.Close(); cmd != nil {
			_ = cmd()
		}
	}()
	// Configure explicit Retina Kitty session (18x38 px/cell).
	if cmd := ed.Picture().SetTerminal(true, 18, 38); cmd != nil {
		_ = cmd()
	}
	ed.FitToView()
	// Drain initial sync.
	if initCmd := ed.refreshVectorPicture(); initCmd != nil {
		_ = initCmd()
	}

	sc := ed.Scene()
	nb := sc.Nodes[sc.NodeByID["filter"]]
	col, row, _ := ed.WorldToTerminalCell(nb.Bounds.X+nb.Bounds.W*0.5, nb.Bounds.Y+14)
	ed.Update(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})

	// Helper recursively executes synchronous Bubble Tea Cmd batches (excluding time.Sleep ticks)
	// so we measure the full RenderSceneVector + ntpicture Kitty PNG preparation cost per frame!
	var drainSync func(cmd tea.Cmd)
	drainSync = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				drainSync(sub)
			}
			return
		}
		// Do not block on retirement grace timers.
		if _, isTick := msg.(vectorRefreshTickMsg); isTick {
			return
		}
		_, next := ed.Update(msg)
		if next != nil {
			// Only drain immediate picturePrepared -> commit steps, not 50ms retire timers.
			switch msg.(type) {
			default:
				// Check if pic is still busy preparing; if not busy, frame committed!
				if ed.Picture().Status().Busy {
					drainSync(next)
				}
			}
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dx := int(math.Round(math.Sin(float64(i)*0.15) * 18))
		dy := int(math.Round(math.Cos(float64(i)*0.15) * 8))
		ed.Update(tea.MouseMotionMsg{X: col + dx, Y: row + dy, Button: tea.MouseLeft})
		cmd := ed.refreshVectorPicture()
		drainSync(cmd)
	}
}
