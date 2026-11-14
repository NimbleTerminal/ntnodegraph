// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Package editor implements the reusable ntnodegraph visual node-and-wire
// editor for Bubble Tea v2 on NimbleTerminal vector and cell-grid surfaces.
//
// The editor owns layout, camera pan/zoom, hit-testing, selection, interactive
// port wiring, grouping, registry-driven node creation, and typed parameter
// editing. It never executes a graph; instead it validates and emits declarative
// nodegraph.EditOp batches so any host (such as a live Nebula studio or a file
// authoring tool) can apply them transactionally.
package editor

import (
	"image/color"

	"nimbleterminal.dev/ntcanvas"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

// RenderMode selects how the node-and-wire canvas is drawn inside the viewport.
type RenderMode uint8

const (
	// RenderVector rasterizes anti-aliased cubic Bezier wires, rounded node
	// cards, and embedded JetBrains Mono typography via rasterx into an
	// ntpicture.Picture (full-resolution Kitty graphics where supported, with
	// automatic truecolor half-block glyph fallback).
	RenderVector RenderMode = iota
	// RenderGrid renders the same scene into an ntcanvas.GraphemeGrid using
	// crisp Unicode box-drawing node cards and orthogonal wires.
	RenderGrid
)

// String returns the lowercase name of the render mode.
func (m RenderMode) String() string {
	switch m {
	case RenderGrid:
		return "grid"
	default:
		return "vector"
	}
}

// Theme defines the color palette used by both the pixel vector renderer and
// the Unicode cell-grid renderer.
type Theme struct {
	CanvasBG      color.NRGBA
	GridMinor     color.NRGBA
	GridMajor     color.NRGBA
	CardBG        color.NRGBA
	CardBGSelect  color.NRGBA
	CardBorder    color.NRGBA
	CardShadow    color.NRGBA
	SelectGlow    color.NRGBA
	TextPrimary   color.NRGBA
	TextSecondary color.NRGBA
	TextMuted     color.NRGBA
	BadgeBG       color.NRGBA
	GroupFill     color.NRGBA
	GroupBorder   color.NRGBA
	GroupHeader   color.NRGBA
	CompatOK      color.NRGBA
	CompatBad     color.NRGBA
	WireCasing    color.NRGBA

	NamespaceColors map[string]color.NRGBA
	TypeColors      map[nodegraph.BaseType]color.NRGBA
}

// DefaultTheme returns the default dark-slate NimbleTerminal Studio palette.
func DefaultTheme() Theme {
	return Theme{
		CanvasBG:      rgb(13, 17, 23),
		GridMinor:     rgb(23, 29, 39),
		GridMajor:     rgb(34, 43, 58),
		CardBG:        rgb(22, 27, 36),
		CardBGSelect:  rgb(28, 36, 50),
		CardBorder:    rgb(56, 68, 89),
		CardShadow:    rgba(0, 0, 0, 120),
		SelectGlow:    rgb(56, 189, 248),
		TextPrimary:   rgb(236, 242, 250),
		TextSecondary: rgb(168, 181, 201),
		TextMuted:     rgb(108, 122, 145),
		BadgeBG:       rgb(33, 41, 56),
		GroupFill:     rgba(56, 189, 248, 22),
		GroupBorder:   rgba(96, 165, 250, 130),
		GroupHeader:   rgba(30, 58, 95, 190),
		CompatOK:      rgb(34, 197, 94),
		CompatBad:     rgb(239, 68, 68),
		WireCasing:    rgba(8, 11, 16, 210),
		NamespaceColors: map[string]color.NRGBA{
			nodegraph.NamespaceSource:    rgb(14, 116, 144),
			nodegraph.NamespaceTransform: rgb(79, 70, 229),
			nodegraph.NamespaceSurface:   rgb(194, 65, 12),
			nodegraph.NamespaceWASM:      rgb(126, 34, 206),
			nodegraph.NamespaceGPU:       rgb(190, 24, 93),
			nodegraph.NamespaceGraph:     rgb(13, 148, 136),
			nodegraph.NamespaceAgent:     rgb(217, 119, 6),
		},
		TypeColors: map[nodegraph.BaseType]color.NRGBA{
			nodegraph.BaseBytes:    rgb(56, 189, 248),
			nodegraph.BaseString:   rgb(163, 230, 53),
			nodegraph.BaseNumber:   rgb(96, 165, 250),
			nodegraph.BaseBool:     rgb(251, 113, 133),
			nodegraph.BaseJSON:     rgb(251, 191, 36),
			nodegraph.BaseTable:    rgb(45, 212, 191),
			nodegraph.BaseDocument: rgb(52, 211, 153),
			nodegraph.BaseFrame:    rgb(244, 114, 182),
			nodegraph.BaseTexture:  rgb(192, 132, 252),
			nodegraph.BaseEvent:    rgb(250, 204, 21),
			nodegraph.BaseAny:      rgb(148, 163, 184),
		},
	}
}

// NamespaceColor returns the header color for a node type namespace.
func (t Theme) NamespaceColor(ns string) color.NRGBA {
	if c, ok := t.NamespaceColors[ns]; ok {
		return c
	}
	return rgb(51, 65, 85)
}

// TypeColor returns the accent color for a port or wire DataType.
func (t Theme) TypeColor(dt nodegraph.DataType) color.NRGBA {
	if c, ok := t.TypeColors[dt.Base]; ok {
		return c
	}
	return rgb(148, 163, 184)
}

func rgb(r, g, b uint8) color.NRGBA {
	return color.NRGBA{R: r, G: g, B: b, A: 255}
}

func rgba(r, g, b, a uint8) color.NRGBA {
	return color.NRGBA{R: r, G: g, B: b, A: a}
}

func toCanvasColor(c color.NRGBA) ntcanvas.Color {
	return ntcanvas.RGBColor(c.R, c.G, c.B)
}
