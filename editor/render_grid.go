// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"nimbleterminal.dev/ntcanvas"
)

// RenderSceneGrid renders sc through cam into an ntcanvas.GraphemeGrid of
// dimensions (cols, rows) using Unicode box-drawing cards, colored sockets, and
// orthogonal wires.
func RenderSceneGrid(sc *Scene, cam Camera, cols, rows int, theme Theme, hudLabel string) *ntcanvas.GraphemeGrid {
	w := max(1, cols)
	h := max(1, rows)
	cam = cam.normalized()

	grid := ntcanvas.NewGraphemeGrid(w, h)
	bgStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(theme.GridMinor),
		Background: toCanvasColor(theme.CanvasBG),
	}
	grid.Fill(" ", ntcanvas.CellStyle{Background: toCanvasColor(theme.CanvasBG)})

	// Subtle grid dots every 4x2 cells anchored to world origin.
	originCol, originRow := cam.WorldToGrid(0, 0)
	for r := 0; r < h; r++ {
		if modPos(r-originRow, 3) != 0 {
			continue
		}
		for c := 0; c < w; c++ {
			if modPos(c-originCol, 6) == 0 {
				grid.SetTextWithStyle(image.Pt(c, r), "·", bgStyle)
			}
		}
	}

	if sc == nil {
		return grid
	}

	// 1. Groups.
	for _, gb := range sc.Groups {
		drawGridGroup(grid, gb, cam, theme)
	}

	// 2. Orthogonal wires.
	for _, wp := range sc.Wires {
		col := theme.TypeColor(wp.DataType)
		if wp.Selected {
			col = theme.SelectGlow
		} else if wp.Hovered {
			col = rgba(186, 230, 253, 255)
		}
		style := ntcanvas.CellStyle{
			Foreground: toCanvasColor(col),
			Background: toCanvasColor(theme.CanvasBG),
		}
		if wp.Selected || wp.Hovered {
			style.Attributes = ntcanvas.Bold
		}
		drawGridOrthogonalWire(grid, wp.Waypoints, cam, style, wp.Edge.Kind == "frame")
	}

	// 3. Interactive wire-drag preview.
	if sc.DragPreview != nil && sc.DragPreview.Active {
		var from, to Point
		if sc.DragPreview.FromOutlet {
			from = sc.DragPreview.AnchorPos
			to = sc.DragPreview.CursorPos
		} else {
			from = sc.DragPreview.CursorPos
			to = sc.DragPreview.AnchorPos
		}
		col := theme.TypeColor(sc.DragPreview.AnchorType)
		if sc.DragPreview.SnapTarget != nil {
			col = theme.CompatOK
		}
		style := ntcanvas.CellStyle{
			Foreground: toCanvasColor(col),
			Background: toCanvasColor(theme.CanvasBG),
			Attributes: ntcanvas.Bold,
		}
		drawGridOrthogonalWire(grid, orthogonalWaypoints(from, to), cam, style, true)
	}

	// 4. Node cards.
	for _, nb := range sc.Nodes {
		if nb.GroupHidden {
			continue
		}
		drawGridNode(grid, nb, cam, theme)
	}

	// 5. Marquee selection box.
	if sc.Marquee != nil && (sc.Marquee.W > 1 || sc.Marquee.H > 1) {
		mc0, mr0 := cam.WorldToGrid(sc.Marquee.X, sc.Marquee.Y)
		mc1, mr1 := cam.WorldToGrid(sc.Marquee.X+sc.Marquee.W, sc.Marquee.Y+sc.Marquee.H)
		if mc1 > mc0 && mr1 > mr0 {
			mStyle := ntcanvas.CellStyle{
				Foreground: toCanvasColor(theme.SelectGlow),
				Background: toCanvasColor(theme.CanvasBG),
				Attributes: ntcanvas.Bold,
			}
			drawGridBox(grid, mc0, mr0, mc1, mr1, false, mStyle, mStyle, false)
		}
	}

	// 6. HUD status pill in bottom-left row.
	if hudLabel != "" && h >= 4 && w >= 20 {
		text := " " + truncateASCII(hudLabel, max(8, w-4)) + " "
		hudStyle := ntcanvas.CellStyle{
			Foreground: toCanvasColor(theme.TextSecondary),
			Background: toCanvasColor(theme.BadgeBG),
		}
		grid.SetTextWithStyle(image.Pt(1, h-1), text, hudStyle)
	}

	// 7. Floating right-click context menu overlay.
	if sc.ContextMenu != nil && len(sc.ContextMenu.Items) > 0 {
		drawGridContextMenu(grid, *sc.ContextMenu, w, h, theme)
	}

	return grid
}

func drawGridGroup(grid *ntcanvas.GraphemeGrid, gb GroupBox, cam Camera, theme Theme) {
	c0, r0 := cam.WorldToGrid(gb.Bounds.X, gb.Bounds.Y)
	c1, r1 := cam.WorldToGrid(gb.Bounds.X+gb.Bounds.W, gb.Bounds.Y+gb.Bounds.H)
	if c1-c0 < 4 {
		c1 = c0 + 4
	}
	if r1-r0 < 2 {
		r1 = r0 + 2
	}
	borderCol := theme.GroupBorder
	if gb.Selected {
		borderCol = theme.SelectGlow
	}
	borderStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(borderCol),
		Background: toCanvasColor(theme.CanvasBG),
	}
	if gb.Selected {
		borderStyle.Attributes = ntcanvas.Bold
	}
	drawGridBox(grid, c0, r0, c1, r1, false, borderStyle, ntcanvas.CellStyle{Background: toCanvasColor(theme.CanvasBG)}, false)

	glyph := "▾"
	if gb.Collapsed {
		glyph = "▸"
	}
	title := fmt.Sprintf(" %s %s (%d) ", glyph, gb.Label, len(gb.Members))
	title = truncateASCII(title, max(2, c1-c0-1))
	if r0 >= 0 && r0 < grid.Height() && c0+1 < grid.Width() {
		grid.SetTextWithStyle(image.Pt(max(0, c0+1), r0), title, ntcanvas.CellStyle{
			Foreground: toCanvasColor(theme.TextPrimary),
			Background: toCanvasColor(theme.GroupHeader),
			Attributes: ntcanvas.Bold,
		})
	}
}

func drawGridNode(grid *ntcanvas.GraphemeGrid, nb NodeBox, cam Camera, theme Theme) {
	c0, r0 := cam.WorldToGrid(nb.Bounds.X, nb.Bounds.Y)
	c1, r1 := cam.WorldToGrid(nb.Bounds.X+nb.Bounds.W, nb.Bounds.Y+nb.Bounds.H)
	minRows := 2
	if !nb.Collapsed {
		minRows = max(3, max(len(nb.Inlets), len(nb.Outlets))+2)
	}
	if c1-c0 < 12 {
		c1 = c0 + 12
	}
	if r1-r0 < minRows {
		r1 = r0 + minRows
	}

	bBounds := grid.Bounds()
	if c1 < bBounds.Min.X || r1 < bBounds.Min.Y || c0 >= bBounds.Max.X || r0 >= bBounds.Max.Y {
		return
	}

	bodyBG := theme.CardBG
	borderCol := theme.CardBorder
	if nb.Selected {
		bodyBG = theme.CardBGSelect
		borderCol = theme.SelectGlow
	} else if nb.Hovered {
		borderCol = rgba(148, 163, 184, 220)
	}
	borderStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(borderCol),
		Background: toCanvasColor(bodyBG),
	}
	if nb.Selected {
		borderStyle.Attributes = ntcanvas.Bold
	}
	fillStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(theme.TextSecondary),
		Background: toCanvasColor(bodyBG),
	}
	drawGridBox(grid, c0, r0, c1, r1, nb.Selected, borderStyle, fillStyle, true)

	// Header bar on row r0+1 (or r0 if tiny).
	hdrRow := r0
	if r1-r0 >= 2 {
		hdrRow = r0 + 1
	}
	innerW := max(1, c1-c0-1)
	if hdrRow >= 0 && hdrRow < grid.Height() {
		nsCol := theme.NamespaceColor(nb.Namespace)
		hdrStyle := ntcanvas.CellStyle{
			Foreground: toCanvasColor(theme.TextPrimary),
			Background: toCanvasColor(nsCol),
			Attributes: ntcanvas.Bold,
		}
		collapseGlyph := "▾"
		if nb.Collapsed {
			collapseGlyph = "▸"
		}
		badgeReserve := 2
		if !nb.Collapsed && innerW >= 16 {
			if nb.ViewerActive {
				badgeReserve += 2
			}
			if nb.HasParams {
				badgeReserve += 2
			}
			if nb.HasOutlets {
				badgeReserve += 2
			}
		}
		titlePrefix := " "
		if nb.IsSubgraph {
			titlePrefix = " ◈ "
		}
		hdrText := padOrTruncate(titlePrefix+nb.Title, max(1, innerW-badgeReserve)) + strings.Repeat(" ", min(innerW, badgeReserve))
		setClippedRowText(grid, c0+1, c1-1, hdrRow, hdrText, hdrStyle)

		// Right-aligned header badges.
		bCol := c1 - 1
		setCellIfInBounds(grid, bCol, hdrRow, collapseGlyph, hdrStyle)
		bCol -= 2
		if !nb.Collapsed && innerW >= 16 {
			if nb.HasOutlets && bCol > c0+2 {
				fg := theme.TextSecondary
				if nb.PreviewOpen {
					fg = rgba(16, 185, 129, 255)
				}
				setCellIfInBounds(grid, bCol, hdrRow, "▣", ntcanvas.CellStyle{
					Foreground: toCanvasColor(fg),
					Background: toCanvasColor(nsCol),
					Attributes: ntcanvas.Bold,
				})
				bCol -= 2
			}
			if nb.HasParams && bCol > c0+2 {
				fg := theme.TextSecondary
				if nb.ParamsOpen {
					fg = theme.SelectGlow
				}
				setCellIfInBounds(grid, bCol, hdrRow, "≡", ntcanvas.CellStyle{
					Foreground: toCanvasColor(fg),
					Background: toCanvasColor(nsCol),
					Attributes: ntcanvas.Bold,
				})
				bCol -= 2
			}
			if nb.ViewerActive && bCol > c0+2 {
				setCellIfInBounds(grid, bCol, hdrRow, "◉", ntcanvas.CellStyle{
					Foreground: toCanvasColor(rgba(245, 158, 11, 255)),
					Background: toCanvasColor(nsCol),
					Attributes: ntcanvas.Bold,
				})
			}
		}
	}

	if !nb.Collapsed {
		maxPortRow := hdrRow
		for _, in := range nb.Inlets {
			_, pr := cam.WorldToGrid(in.Center.X, in.Center.Y)
			pr = min(r1-1, max(hdrRow+1, pr))
			if pr > maxPortRow {
				maxPortRow = pr
			}
			sock := "○"
			if in.Connected {
				sock = "●"
			}
			sockCol := theme.TypeColor(in.Type)
			if in.CompatState == PortCompatValid {
				sockCol = theme.CompatOK
				sock = "◉"
			} else if in.Hovered {
				sockCol = theme.SelectGlow
				sock = "◉"
			}
			setCellIfInBounds(grid, c0, pr, sock, ntcanvas.CellStyle{
				Foreground: toCanvasColor(sockCol),
				Background: toCanvasColor(bodyBG),
				Attributes: ntcanvas.Bold,
			})
			lbl := in.Name
			if in.Required {
				lbl += "*"
			}
			setClippedRowText(grid, c0+2, (c0+c1)/2, pr, lbl, fillStyle)
		}
		for _, out := range nb.Outlets {
			_, pr := cam.WorldToGrid(out.Center.X, out.Center.Y)
			pr = min(r1-1, max(hdrRow+1, pr))
			if pr > maxPortRow {
				maxPortRow = pr
			}
			sock := "○"
			if out.Connected {
				sock = "●"
			}
			sockCol := theme.TypeColor(out.Type)
			if out.CompatState == PortCompatValid {
				sockCol = theme.CompatOK
				sock = "◉"
			} else if out.Hovered {
				sockCol = theme.SelectGlow
				sock = "◉"
			}
			setCellIfInBounds(grid, c1, pr, sock, ntcanvas.CellStyle{
				Foreground: toCanvasColor(sockCol),
				Background: toCanvasColor(bodyBG),
				Attributes: ntcanvas.Bold,
			})
			lbl := truncateASCII(out.Name, max(1, (c1-c0)/2-2))
			startCol := max(c0+2, c1-1-len(lbl))
			setClippedRowText(grid, startCol, c1-1, pr, lbl, fillStyle)
		}

		drawerStartRow := maxPortRow + 1
		if len(nb.BoundaryBadges) > 0 && drawerStartRow <= r1-1 {
			badgeLine := padOrTruncate(" "+strings.Join(nb.BoundaryBadges, " "), max(1, c1-c0-1))
			setClippedRowText(grid, c0+1, c1-1, drawerStartRow, badgeLine, ntcanvas.CellStyle{
				Foreground: toCanvasColor(rgba(45, 212, 191, 255)),
				Background: toCanvasColor(theme.BadgeBG),
				Attributes: ntcanvas.Bold,
			})
			drawerStartRow++
		}
		if nb.ParamsOpen {
			drawGridParamsDrawer(grid, nb, c0, c1, drawerStartRow, r1-1, cam, bodyBG, theme)
		}
		if nb.PreviewOpen {
			drawGridPreviewDrawer(grid, nb, c0, c1, drawerStartRow, r1-1, cam, bodyBG, theme)
		}
	}
}

func drawGridParamsDrawer(grid *ntcanvas.GraphemeGrid, nb NodeBox, c0, c1, minRow, maxRow int, cam Camera, bodyBG color.NRGBA, theme Theme) {
	if maxRow < minRow || c1-c0 < 8 {
		return
	}
	innerW := c1 - c0 - 1
	if nb.ParamsHeaderRect.W > 0 {
		_, hr := cam.WorldToGrid(nb.ParamsHeaderRect.X, nb.ParamsHeaderRect.Y+nb.ParamsHeaderRect.H*0.5)
		if hr >= minRow && hr <= maxRow {
			hdr := padOrTruncate(fmt.Sprintf(" ▾ params (%d)", len(nb.ParamRows)), innerW)
			setClippedRowText(grid, c0+1, c1-1, hr, hdr, ntcanvas.CellStyle{
				Foreground: toCanvasColor(theme.SelectGlow),
				Background: toCanvasColor(theme.BadgeBG),
				Attributes: ntcanvas.Bold,
			})
		}
	}
	for _, row := range nb.ParamRows {
		_, rr := cam.WorldToGrid(row.Bounds.X, row.Bounds.Y+row.Bounds.H*0.5)
		if rr < minRow || rr > maxRow {
			continue
		}
		ctrlStr := formatGridInlineControl(row)
		ctrlRunes := []rune(ctrlStr)
		lblAvail := max(2, innerW-len(ctrlRunes)-2)
		lbl := padOrTruncate(" "+row.Label, lblAvail)
		setClippedRowText(grid, c0+1, c1-1, rr, lbl, ntcanvas.CellStyle{
			Foreground: toCanvasColor(theme.TextSecondary),
			Background: toCanvasColor(bodyBG),
		})
		ctrlStart := max(c0+2, c1-len(ctrlRunes))
		fg := theme.TextPrimary
		switch row.Kind {
		case InlineParamBool:
			if row.BoolVal {
				fg = rgba(16, 185, 129, 255)
			} else {
				fg = theme.TextMuted
			}
		case InlineParamEnum:
			fg = theme.SelectGlow
		}
		setClippedRowText(grid, ctrlStart, c1-1, rr, ctrlStr, ntcanvas.CellStyle{
			Foreground: toCanvasColor(fg),
			Background: toCanvasColor(bodyBG),
			Attributes: ntcanvas.Bold,
		})
	}
}

func formatGridInlineControl(row InlineParamRow) string {
	switch row.Kind {
	case InlineParamBool:
		if row.BoolVal {
			return "[ON] "
		}
		return "[OFF]"
	case InlineParamEnum:
		return "◂" + truncateASCII(row.DisplayValue, 8) + "▸"
	case InlineParamFloat, InlineParamInt:
		const barW = 4
		filled := min(barW, max(0, int(math.Round(clampFloat(row.Fraction, 0, 1)*barW))))
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)
		return fmt.Sprintf("-%s+ %s", bar, truncateASCII(row.DisplayValue, 5))
	default:
		return "[" + truncateASCII(row.DisplayValue, 8) + "]"
	}
}

func drawGridPreviewDrawer(grid *ntcanvas.GraphemeGrid, nb NodeBox, c0, c1, minRow, maxRow int, cam Camera, bodyBG color.NRGBA, theme Theme) {
	if maxRow < minRow || c1-c0 < 8 {
		return
	}
	innerW := c1 - c0 - 1
	if nb.PreviewHeaderRect.W > 0 {
		_, hr := cam.WorldToGrid(nb.PreviewHeaderRect.X, nb.PreviewHeaderRect.Y+nb.PreviewHeaderRect.H*0.5)
		if hr >= minRow && hr <= maxRow {
			title := " ▾ preview"
			if nb.Preview != nil && nb.Preview.Outlet != "" {
				title += "·" + nb.Preview.Outlet
			}
			setClippedRowText(grid, c0+1, c1-1, hr, padOrTruncate(title, innerW), ntcanvas.CellStyle{
				Foreground: toCanvasColor(rgba(110, 231, 183, 255)),
				Background: toCanvasColor(theme.BadgeBG),
				Attributes: ntcanvas.Bold,
			})
		}
	}
	if nb.PreviewContentRect.W <= 0 || nb.PreviewContentRect.H <= 0 {
		return
	}
	_, pr0 := cam.WorldToGrid(nb.PreviewContentRect.X, nb.PreviewContentRect.Y)
	_, pr1 := cam.WorldToGrid(nb.PreviewContentRect.X, nb.PreviewContentRect.Y+nb.PreviewContentRect.H)
	pr0 = max(minRow, pr0)
	pr1 = min(maxRow, pr1)
	if pr1 < pr0 {
		return
	}
	pc0 := c0 + 2
	pc1 := c1 - 2
	if pc1 < pc0 {
		return
	}

	switch {
	case nb.Preview != nil && nb.Preview.Image != nil && !nb.Preview.Image.Bounds().Empty():
		src := nb.Preview.Image
		sb := src.Bounds()
		sw, sh := sb.Dx(), sb.Dy()
		colsAvail := pc1 - pc0 + 1
		subRowsAvail := (pr1 - pr0 + 1) * 2
		for r := pr0; r <= pr1; r++ {
			subY0 := (r - pr0) * 2
			subY1 := subY0 + 1
			sy0 := sb.Min.Y + min(sh-1, max(0, (subY0*sh)/max(1, subRowsAvail)))
			sy1 := sb.Min.Y + min(sh-1, max(0, (subY1*sh)/max(1, subRowsAvail)))
			for c := pc0; c <= pc1; c++ {
				sx := sb.Min.X + min(sw-1, max(0, ((c-pc0)*sw)/max(1, colsAvail)))
				top := src.RGBAAt(sx, sy0)
				bot := src.RGBAAt(sx, sy1)
				setCellIfInBounds(grid, c, r, "▄", ntcanvas.CellStyle{
					Background: toCanvasColor(rgba(top.R, top.G, top.B, 255)),
					Foreground: toCanvasColor(rgba(bot.R, bot.G, bot.B, 255)),
				})
			}
		}

	case nb.Preview != nil && len(nb.Preview.Lines) > 0:
		for i := 0; i <= pr1-pr0 && i < len(nb.Preview.Lines); i++ {
			col := theme.TextSecondary
			if i == 0 {
				col = theme.TextPrimary
			}
			setClippedRowText(grid, pc0, pc1, pr0+i, padOrTruncate(nb.Preview.Lines[i], pc1-pc0+1), ntcanvas.CellStyle{
				Foreground: toCanvasColor(col),
				Background: toCanvasColor(theme.CanvasBG),
			})
		}

	default:
		msg := "(waiting...)"
		if nb.Preview != nil && nb.Preview.Summary != "" {
			msg = nb.Preview.Summary
		}
		setClippedRowText(grid, pc0, pc1, pr0, padOrTruncate(msg, pc1-pc0+1), ntcanvas.CellStyle{
			Foreground: toCanvasColor(theme.TextMuted),
			Background: toCanvasColor(bodyBG),
		})
	}
}

func drawGridBox(grid *ntcanvas.GraphemeGrid, c0, r0, c1, r1 int, heavy bool, borderStyle, fillStyle ntcanvas.CellStyle, solidFill bool) {
	tl, tr, bl, br, horz, vert := "╭", "╮", "╰", "╯", "─", "│"
	if heavy {
		tl, tr, bl, br, horz, vert = "┏", "┓", "┗", "┛", "━", "┃"
	}

	if solidFill && c1 > c0+1 && r1 > r0+1 {
		rect := image.Rect(c0+1, r0+1, c1, r1).Intersect(grid.Bounds())
		if !rect.Empty() {
			grid.FillRect(rect, " ", fillStyle)
		}
	}

	for c := c0 + 1; c < c1; c++ {
		setCellIfInBounds(grid, c, r0, horz, borderStyle)
		setCellIfInBounds(grid, c, r1, horz, borderStyle)
	}
	for r := r0 + 1; r < r1; r++ {
		setCellIfInBounds(grid, c0, r, vert, borderStyle)
		setCellIfInBounds(grid, c1, r, vert, borderStyle)
	}
	setCellIfInBounds(grid, c0, r0, tl, borderStyle)
	setCellIfInBounds(grid, c1, r0, tr, borderStyle)
	setCellIfInBounds(grid, c0, r1, bl, borderStyle)
	setCellIfInBounds(grid, c1, r1, br, borderStyle)
}

func drawGridOrthogonalWire(grid *ntcanvas.GraphemeGrid, pts []Point, cam Camera, style ntcanvas.CellStyle, dashed bool) {
	if len(pts) < 2 {
		return
	}
	gpts := make([]image.Point, 0, len(pts))
	for _, p := range pts {
		c, r := cam.WorldToGrid(p.X, p.Y)
		gp := image.Pt(c, r)
		if len(gpts) == 0 || gpts[len(gpts)-1] != gp {
			gpts = append(gpts, gp)
		}
	}
	if len(gpts) == 1 {
		setCellIfInBounds(grid, gpts[0].X, gpts[0].Y, "▸", style)
		return
	}

	horzGlyph := "─"
	vertGlyph := "│"
	if dashed {
		horzGlyph = "╌"
		vertGlyph = "╎"
	}

	for i := 0; i < len(gpts)-1; i++ {
		a, b := gpts[i], gpts[i+1]
		switch {
		case a.Y == b.Y:
			lo, hi := min(a.X, b.X), max(a.X, b.X)
			for x := lo; x <= hi; x++ {
				setCellIfInBounds(grid, x, a.Y, horzGlyph, style)
			}
		case a.X == b.X:
			lo, hi := min(a.Y, b.Y), max(a.Y, b.Y)
			for y := lo; y <= hi; y++ {
				setCellIfInBounds(grid, a.X, y, vertGlyph, style)
			}
		default:
			// Fallback diagonal segment via ntcanvas.RasterLineAA.
			ntcanvas.RasterLineAA(
				grid.Bounds(),
				ntcanvas.Float64Point{X: float64(a.X), Y: float64(a.Y)},
				ntcanvas.Float64Point{X: float64(b.X), Y: float64(b.Y)},
				func(pt ntcanvas.Point, cov float64) {
					if cov >= 0.35 {
						setCellIfInBounds(grid, pt.X, pt.Y, "•", style)
					}
				},
			)
		}
	}

	// Smooth 90-degree corner glyphs at intermediate bends.
	for i := 1; i < len(gpts)-1; i++ {
		prev, cur, next := gpts[i-1], gpts[i], gpts[i+1]
		if c := cornerGlyph(prev, cur, next); c != "" {
			setCellIfInBounds(grid, cur.X, cur.Y, c, style)
		}
	}

	end := gpts[len(gpts)-1]
	setCellIfInBounds(grid, end.X, end.Y, "▸", style)
}

func cornerGlyph(prev, cur, next image.Point) string {
	dx1, dy1 := signInt(cur.X-prev.X), signInt(cur.Y-prev.Y)
	dx2, dy2 := signInt(next.X-cur.X), signInt(next.Y-cur.Y)
	switch {
	case (dx1 > 0 && dy2 > 0) || (dy1 < 0 && dx2 < 0):
		return "╮"
	case (dx1 > 0 && dy2 < 0) || (dy1 > 0 && dx2 < 0):
		return "╯"
	case (dy1 > 0 && dx2 > 0) || (dx1 < 0 && dy2 < 0):
		return "╰"
	case (dy1 < 0 && dx2 > 0) || (dx1 < 0 && dy2 > 0):
		return "╭"
	default:
		return ""
	}
}

func setCellIfInBounds(grid *ntcanvas.GraphemeGrid, c, r int, text string, style ntcanvas.CellStyle) {
	if c >= 0 && c < grid.Width() && r >= 0 && r < grid.Height() {
		grid.SetTextWithStyle(image.Pt(c, r), text, style)
	}
}

func setClippedRowText(grid *ntcanvas.GraphemeGrid, minCol, maxCol, row int, text string, style ntcanvas.CellStyle) {
	if row < 0 || row >= grid.Height() || maxCol < minCol || text == "" {
		return
	}
	start := max(0, minCol)
	end := min(grid.Width()-1, maxCol)
	if end < start {
		return
	}
	skip := start - minCol
	runes := []rune(text)
	if skip >= len(runes) {
		return
	}
	avail := end - start + 1
	slice := runes[skip:min(len(runes), skip+avail)]
	grid.SetTextWithStyle(image.Pt(start, row), string(slice), style)
}

func truncateASCII(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxCols {
		return s
	}
	if maxCols <= 1 {
		return ansi.Truncate(s, maxCols, "")
	}
	return ansi.Truncate(s, maxCols, "…")
}

func padOrTruncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	t := truncateASCII(s, width)
	w := ansi.StringWidth(t)
	if w < width {
		return t + strings.Repeat(" ", width-w)
	}
	return t
}

func modPos(a, m int) int {
	res := a % m
	if res < 0 {
		res += m
	}
	return res
}

func signInt(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	default:
		return 0
	}
}

func roundCell(v float64) int {
	return int(math.Round(v))
}

var _ = roundCell

func drawGridContextMenu(grid *ntcanvas.GraphemeGrid, cm ContextMenuOverlay, cols, rows int, theme Theme) {
	c0, r0, c1, r1 := cm.CellBounds(cols, rows)
	borderStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(theme.SelectGlow),
		Background: toCanvasColor(theme.CardBGSelect),
		Attributes: ntcanvas.Bold,
	}
	fillStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(theme.TextSecondary),
		Background: toCanvasColor(theme.CardBGSelect),
	}
	drawGridBox(grid, c0, r0, c1, r1, false, borderStyle, fillStyle, true)

	innerW := max(1, c1-c0-1)
	hdrStyle := ntcanvas.CellStyle{
		Foreground: toCanvasColor(theme.TextPrimary),
		Background: toCanvasColor(theme.GroupHeader),
		Attributes: ntcanvas.Bold,
	}
	setClippedRowText(grid, c0+1, c1-1, r0+1, padOrTruncate(" "+cm.Title, innerW), hdrStyle)

	for i, item := range cm.Items {
		row := r0 + 2 + i
		if row >= r1 {
			break
		}
		prefix := "  "
		st := fillStyle
		if i == cm.HoverIdx {
			prefix = "▸ "
			st = ntcanvas.CellStyle{
				Foreground: toCanvasColor(theme.TextPrimary),
				Background: toCanvasColor(rgba(2, 132, 199, 255)),
				Attributes: ntcanvas.Bold,
			}
		}
		setClippedRowText(grid, c0+1, c1-1, row, padOrTruncate(prefix+item, innerW), st)
	}
}
