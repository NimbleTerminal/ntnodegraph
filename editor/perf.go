// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"fmt"
	"math"
	"time"
)

// PerfEvent records a single fine-grained UI/render timing event from the
// visual editor for profiling, automated regression tests, or host JSONL logs.
type PerfEvent struct {
	Kind              string    `json:"kind"` // "drag_move", "vector_submit", "picture_present"
	Timestamp         time.Time `json:"ts"`
	DragKind          string    `json:"drag_kind,omitempty"`
	Backend           string    `json:"backend,omitempty"`
	KittyFormat       string    `json:"kitty_format,omitempty"`
	KittyMedium       string    `json:"kitty_medium,omitempty"`
	PixelW            int       `json:"pixel_w,omitempty"`
	PixelH            int       `json:"pixel_h,omitempty"`
	Generation        uint64    `json:"generation,omitempty"`
	Presented         uint64    `json:"presented,omitempty"`
	PictureBusyBefore bool      `json:"picture_busy_before,omitempty"`
	SupersededFrames  uint64    `json:"superseded_frames,omitempty"`
	ApplyEditsUS      int64     `json:"apply_edits_us,omitempty"`
	SceneBuildUS      int64     `json:"scene_build_us,omitempty"`
	VectorRasterUS    int64     `json:"vector_raster_us,omitempty"`
	PresentLatencyUS  int64     `json:"present_latency_us,omitempty"`
}

// PerfStats summarizes cumulative and recent editor performance counters.
type PerfStats struct {
	DragMoves          uint64        `json:"drag_moves"`
	SceneRebuilds      uint64        `json:"scene_rebuilds"`
	VectorRasters      uint64        `json:"vector_rasters"`
	FramesSubmitted    uint64        `json:"frames_submitted"`
	FramesPresented    uint64        `json:"frames_presented"`
	FramesSuperseded   uint64        `json:"frames_superseded"`
	SubmittedWhileBusy uint64        `json:"submitted_while_busy"`
	LastPixelW         int           `json:"last_pixel_w"`
	LastPixelH         int           `json:"last_pixel_h"`
	LastApplyEdits     time.Duration `json:"last_apply_edits_ns"`
	MaxApplyEdits      time.Duration `json:"max_apply_edits_ns"`
	TotalApplyEdits    time.Duration `json:"total_apply_edits_ns"`
	LastSceneBuild     time.Duration `json:"last_scene_build_ns"`
	MaxSceneBuild      time.Duration `json:"max_scene_build_ns"`
	TotalSceneBuild    time.Duration `json:"total_scene_build_ns"`
	LastVectorRaster   time.Duration `json:"last_vector_raster_ns"`
	MaxVectorRaster    time.Duration `json:"max_vector_raster_ns"`
	TotalVectorRaster  time.Duration `json:"total_vector_raster_ns"`
	LastPresentLatency time.Duration `json:"last_present_latency_ns"`
	MaxPresentLatency  time.Duration `json:"max_present_latency_ns"`
	TotalPresent       time.Duration `json:"total_present_latency_ns"`
}

func (k activeDragKind) String() string {
	switch k {
	case dragPan:
		return "pan"
	case dragZoom:
		return "zoom"
	case dragNodes:
		return "nodes"
	case dragGroup:
		return "group"
	case dragWire:
		return "wire"
	case dragMarquee:
		return "marquee"
	case dragParamSlider:
		return "param_slider"
	default:
		return "none"
	}
}

// PerfStats returns a snapshot of the editor's cumulative and recent frame
// telemetry counters.
func (m *Model) PerfStats() PerfStats {
	return m.perf
}

// ResetPerfStats zeroes cumulative editor performance counters while keeping
// any in-flight generation timestamps intact.
func (m *Model) ResetPerfStats() {
	m.perf = PerfStats{}
}

// SetShowPerfHUD enables or disables the live frame-timing readout in the
// canvas HUD badge.
func (m *Model) SetShowPerfHUD(show bool) {
	if m.showPerfHUD == show {
		return
	}
	m.showPerfHUD = show
	m.vectorDirty = true
}

// ShowPerfHUD reports whether the live frame-timing HUD readout is enabled.
func (m *Model) ShowPerfHUD() bool {
	return m.showPerfHUD
}

// WorldToTerminalCell maps a world-space coordinate (wx, wy) to the global
// terminal cell (col, row) accounting for the editor's screen offset, 1-row
// header bar, and active Vector/Grid camera transform.
func (m *Model) WorldToTerminalCell(wx, wy float64) (col, row int, ok bool) {
	canvasCols, _ := m.splitColumns()
	bodyRows := max(4, m.rows-2)
	if canvasCols <= 0 || bodyRows <= 0 {
		return 0, 0, false
	}
	var localCol, localRow int
	if m.mode == RenderGrid {
		localCol, localRow = m.cam.WorldToGrid(wx, wy)
	} else {
		pxW, pxH := m.canvasVectorPixelSize(canvasCols, bodyRows)
		sp := m.cam.WorldToScreen(wx, wy)
		localCol = int(math.Floor(sp.X * float64(canvasCols) / float64(pxW)))
		localRow = int(math.Floor(sp.Y * float64(bodyRows) / float64(pxH)))
	}
	if localCol < 0 || localCol >= canvasCols || localRow < 0 || localRow >= bodyRows {
		return 0, 0, false
	}
	return m.offsetX + localCol, m.offsetY + 1 + localRow, true
}

func (m *Model) emitPerfEvent(ev PerfEvent) {
	if m.onPerfEvent != nil {
		m.onPerfEvent(ev)
	}
}

func (m *Model) formatPerfHUDSuffix() string {
	if !m.showPerfHUD {
		return ""
	}
	vecMS := float64(m.perf.LastVectorRaster.Microseconds()) / 1000.0
	prepMS := float64(m.perf.LastPresentLatency.Microseconds()) / 1000.0
	scUS := m.perf.LastSceneBuild.Microseconds()
	return fmt.Sprintf("  │  sc=%dµs vec=%.1fms prep=%.1fms pres=%d/%d drop=%d",
		scUS,
		vecMS,
		prepMS,
		m.perf.FramesPresented,
		m.perf.FramesSubmitted,
		m.perf.FramesSuperseded,
	)
}
