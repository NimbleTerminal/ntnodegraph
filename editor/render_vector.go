// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"sync"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
	"nimbleterminal.dev/ntfonts"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

const (
	MaxVectorEdgePx = 4096
)

var (
	fontInitOnce sync.Once
	parsedMono   *opentype.Font
	parsedBold   *opentype.Font

	fontMu    sync.Mutex
	monoFaces = make(map[int]font.Face)
	boldFaces = make(map[int]font.Face)
)

func initEmbeddedFonts() {
	fontInitOnce.Do(func() {
		if raw, err := ntfonts.Embedded.Face("JetBrains Mono", "Regular"); err == nil {
			parsedMono, _ = opentype.Parse(raw)
		}
		if raw, err := ntfonts.Embedded.Face("JetBrains Mono", "Bold"); err == nil {
			parsedBold, _ = opentype.Parse(raw)
		}
	})
}

// withFontFace acquires a cached font.Face at the quantized pixel size (8..32)
// under fontMu so concurrent vector renders remain 100% race-free.
func withFontFace(px int, bold bool, fn func(face font.Face)) {
	initEmbeddedFonts()
	sz := min(32, max(8, px))

	fontMu.Lock()
	defer fontMu.Unlock()

	cache := monoFaces
	src := parsedMono
	if bold {
		cache = boldFaces
		if parsedBold != nil {
			src = parsedBold
		}
	}
	face, ok := cache[sz]
	if !ok {
		if src != nil {
			if f, err := opentype.NewFace(src, &opentype.FaceOptions{
				Size:    float64(sz),
				DPI:     72,
				Hinting: font.HintingFull,
			}); err == nil {
				face = f
			}
		}
		if face == nil {
			face = basicfont.Face7x13
		}
		cache[sz] = face
	}
	fn(face)
}

type pathOp struct {
	x, y float32
	move bool
}

// boundedScanner implements rasterx.Scanner by buffering path vertices and
// rasterizing/compositing ONLY the tight integer bounding rectangle of each
// shape into dst. Standard rasterx.ScannerGV resets and sweeps the entire
// viewport buffer on every single Draw()/Clear() call, which costs hundreds of
// milliseconds per frame on Retina canvases with ~90 shapes.
type boundedScanner struct {
	r       vector.Rasterizer
	dst     *image.RGBA
	clip    image.Rectangle
	uniform image.Uniform
	src     image.Image
	ops     []pathOp
	minX    fixed.Int26_6
	minY    fixed.Int26_6
	maxX    fixed.Int26_6
	maxY    fixed.Int26_6
}

func (s *boundedScanner) resetTarget(dst *image.RGBA) {
	s.dst = dst
	s.clip = image.Rectangle{}
	s.uniform.C = color.RGBA{A: 255}
	s.src = &s.uniform
	s.Clear()
}

func (s *boundedScanner) recordBounds(a fixed.Point26_6) {
	if a.X < s.minX {
		s.minX = a.X
	}
	if a.X > s.maxX {
		s.maxX = a.X
	}
	if a.Y < s.minY {
		s.minY = a.Y
	}
	if a.Y > s.maxY {
		s.maxY = a.Y
	}
}

func (s *boundedScanner) Start(a fixed.Point26_6) {
	s.recordBounds(a)
	s.ops = append(s.ops, pathOp{x: float32(a.X) / 64.0, y: float32(a.Y) / 64.0, move: true})
}

func (s *boundedScanner) Line(b fixed.Point26_6) {
	s.recordBounds(b)
	s.ops = append(s.ops, pathOp{x: float32(b.X) / 64.0, y: float32(b.Y) / 64.0, move: false})
}

func (s *boundedScanner) Draw() {
	if len(s.ops) == 0 || s.dst == nil || s.minX > s.maxX || s.minY > s.maxY {
		return
	}
	x0 := int(s.minX>>6) - 1
	y0 := int(s.minY>>6) - 1
	x1 := int((s.maxX+63)>>6) + 1
	y1 := int((s.maxY+63)>>6) + 1
	bbox := image.Rect(x0, y0, x1, y1).Intersect(s.dst.Bounds())
	if !s.clip.Empty() {
		bbox = bbox.Intersect(s.clip)
	}
	if bbox.Empty() {
		return
	}

	s.r.Reset(bbox.Dx(), bbox.Dy())
	s.r.DrawOp = draw.Over
	ox := float32(bbox.Min.X)
	oy := float32(bbox.Min.Y)
	for _, op := range s.ops {
		if op.move {
			s.r.MoveTo(op.x-ox, op.y-oy)
		} else {
			s.r.LineTo(op.x-ox, op.y-oy)
		}
	}
	s.r.Draw(s.dst, bbox, s.src, bbox.Min)
}

func (s *boundedScanner) GetPathExtent() fixed.Rectangle26_6 {
	return fixed.Rectangle26_6{
		Min: fixed.Point26_6{X: s.minX, Y: s.minY},
		Max: fixed.Point26_6{X: s.maxX, Y: s.maxY},
	}
}

func (s *boundedScanner) SetBounds(_, _ int) {
	s.Clear()
}

func (s *boundedScanner) SetColor(clr any) {
	switch c := clr.(type) {
	case color.Color:
		s.uniform.C = c
		s.src = &s.uniform
	case rasterx.ColorFunc:
		s.src = colorFuncImg(c)
	}
}

type colorFuncImg rasterx.ColorFunc

func (f colorFuncImg) ColorModel() color.Model { return color.RGBAModel }
func (f colorFuncImg) Bounds() image.Rectangle { return image.Rect(-1e9, -1e9, 1e9, 1e9) }
func (f colorFuncImg) At(x, y int) color.Color { return f(x, y) }

func (s *boundedScanner) SetWinding(_ bool) {}

func (s *boundedScanner) SetClip(rect image.Rectangle) {
	s.clip = rect
}

func (s *boundedScanner) Clear() {
	s.ops = s.ops[:0]
	const mxfi = fixed.Int26_6(math.MaxInt32)
	s.minX, s.minY = mxfi, mxfi
	s.maxX, s.maxY = -mxfi, -mxfi
}

type vectorRenderCtx struct {
	scanner boundedScanner
	filler  *rasterx.Filler
	dasher  *rasterx.Dasher
}

var vectorCtxPool = sync.Pool{
	New: func() any {
		ctx := &vectorRenderCtx{
			scanner: boundedScanner{ops: make([]pathOp, 0, 256)},
		}
		ctx.filler = rasterx.NewFiller(64, 64, &ctx.scanner)
		ctx.dasher = rasterx.NewDasher(64, 64, &ctx.scanner)
		return ctx
	},
}

func fillSolidRGBA(dst *image.RGBA, c color.NRGBA) {
	pix := dst.Pix
	if len(pix) < 4 {
		return
	}
	wBytes := dst.Bounds().Dx() * 4
	if wBytes <= 0 {
		return
	}
	// Seed first pixel and exponentially double across row 0, then across rows.
	pix[0], pix[1], pix[2], pix[3] = c.R, c.G, c.B, c.A
	for filled := 4; filled < wBytes; filled *= 2 {
		copy(pix[filled:wBytes], pix[:filled])
	}
	h := dst.Bounds().Dy()
	if dst.Stride == wBytes {
		total := wBytes * h
		for filled := wBytes; filled < total; filled *= 2 {
			copy(pix[filled:total], pix[:filled])
		}
		return
	}
	row0 := pix[:wBytes]
	for y := 1; y < h; y++ {
		off := y * dst.Stride
		copy(pix[off:off+wBytes], row0)
	}
}

// RenderSceneVector renders sc through cam into a fresh *image.RGBA of size
// (widthPx, heightPx) using pure-Go anti-aliased vector geometry and embedded
// NimbleTerminal fonts.
func RenderSceneVector(sc *Scene, cam Camera, widthPx, heightPx int, theme Theme, hudLabel string) *image.RGBA {
	w := min(MaxVectorEdgePx, max(16, widthPx))
	h := min(MaxVectorEdgePx, max(16, heightPx))
	cam = cam.normalized()

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fillSolidRGBA(dst, theme.CanvasBG)

	drawVectorGrid(dst, cam, w, h, theme)
	if sc == nil {
		return dst
	}

	vctx := vectorCtxPool.Get().(*vectorRenderCtx)
	vctx.scanner.resetTarget(dst)
	filler := vctx.filler
	dasher := vctx.dasher
	defer func() {
		vctx.scanner.dst = nil
		filler.Clear()
		dasher.Clear()
		vectorCtxPool.Put(vctx)
	}()

	// 1. Groups (drawn behind wires and nodes).
	for _, gb := range sc.Groups {
		drawVectorGroup(dst, filler, dasher, gb, cam, theme)
	}

	// 2. Existing wires (cubic Bezier curves with casing, type color, and kind styling).
	for _, wp := range sc.Wires {
		drawVectorWire(filler, dasher, wp, cam, theme)
	}

	// 3. Interactive wire-drag preview (drawn behind sockets, above settled wires).
	if sc.DragPreview != nil && sc.DragPreview.Active {
		drawVectorDragPreview(dasher, *sc.DragPreview, cam, theme)
	}

	// 4. Nodes and typed port sockets.
	for _, nb := range sc.Nodes {
		if nb.GroupHidden {
			continue
		}
		drawVectorNode(dst, filler, dasher, nb, cam, theme)
	}

	// 5. Marquee box selection overlay.
	if sc.Marquee != nil && (sc.Marquee.W > 1 || sc.Marquee.H > 1) {
		drawVectorMarquee(filler, dasher, *sc.Marquee, cam, theme)
	}

	// 6. Crisp HUD badge in bottom-left corner.
	if hudLabel != "" && w >= 180 && h >= 48 {
		drawVectorHUD(dst, filler, hudLabel, w, h, theme)
	}

	// 7. Floating right-click context menu overlay.
	if sc.ContextMenu != nil && len(sc.ContextMenu.Items) > 0 {
		drawVectorContextMenu(dst, filler, dasher, *sc.ContextMenu, w, h, theme)
	}

	return dst
}

func drawVectorGrid(dst *image.RGBA, cam Camera, w, h int, theme Theme) {
	worldStep := 24.0
	for worldStep*cam.Zoom < 14.0 {
		worldStep *= 2
	}
	majorEvery := 4

	startWorld := cam.ScreenToWorld(0, 0)
	endWorld := cam.ScreenToWorld(float64(w), float64(h))

	firstCol := int(math.Floor(startWorld.X / worldStep))
	lastCol := int(math.Ceil(endWorld.X / worldStep))
	firstRow := int(math.Floor(startWorld.Y / worldStep))
	lastRow := int(math.Ceil(endWorld.Y / worldStep))

	if (lastCol-firstCol)*(lastRow-firstRow) > 25000 {
		return
	}

	for gx := firstCol; gx <= lastCol; gx++ {
		sx := int(math.Round((float64(gx)*worldStep - cam.PanX) * cam.Zoom))
		if sx < 0 || sx >= w {
			continue
		}
		isMajorX := gx%majorEvery == 0
		for gy := firstRow; gy <= lastRow; gy++ {
			sy := int(math.Round((float64(gy)*worldStep - cam.PanY) * cam.Zoom))
			if sy < 0 || sy >= h {
				continue
			}
			isMajor := isMajorX && (gy%majorEvery == 0)
			if isMajor {
				setDot(dst, sx, sy, theme.GridMajor, 1)
			} else {
				dst.SetRGBA(sx, sy, color.RGBA{R: theme.GridMinor.R, G: theme.GridMinor.G, B: theme.GridMinor.B, A: 255})
			}
		}
	}
}

func setDot(dst *image.RGBA, cx, cy int, c color.NRGBA, radius int) {
	b := dst.Bounds()
	rc := color.RGBA{R: c.R, G: c.G, B: c.B, A: 255}
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx != 0 && dy != 0 {
				continue
			}
			px, py := cx+dx, cy+dy
			if px >= b.Min.X && px < b.Max.X && py >= b.Min.Y && py < b.Max.Y {
				dst.SetRGBA(px, py, rc)
			}
		}
	}
}

func drawVectorGroup(dst *image.RGBA, filler *rasterx.Filler, dasher *rasterx.Dasher, gb GroupBox, cam Camera, theme Theme) {
	tl := cam.WorldToScreen(gb.Bounds.X, gb.Bounds.Y)
	br := cam.WorldToScreen(gb.Bounds.X+gb.Bounds.W, gb.Bounds.Y+gb.Bounds.H)
	if br.X < 0 || br.Y < 0 || tl.X > float64(dst.Bounds().Dx()) || tl.Y > float64(dst.Bounds().Dy()) {
		return
	}
	radius := max(4.0, 10.0*cam.Zoom)
	hdrH := min(br.Y-tl.Y, max(13.0, GroupHeaderHeight*cam.Zoom))

	filler.SetColor(theme.GroupFill)
	rasterx.AddRoundRect(tl.X, tl.Y, br.X, br.Y, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	filler.SetColor(theme.GroupHeader)
	rasterx.AddRoundRect(tl.X, tl.Y, br.X, tl.Y+hdrH, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	borderCol := theme.GroupBorder
	strokeW := max(1.2, 1.5*cam.Zoom)
	var dashes []float64
	if gb.Selected {
		borderCol = theme.SelectGlow
		strokeW = max(2.0, 2.5*cam.Zoom)
	} else if gb.Hovered {
		borderCol = rgba(125, 211, 252, 225)
		strokeW = max(1.6, 2.0*cam.Zoom)
		dashes = []float64{6 * cam.Zoom, 4 * cam.Zoom}
	} else {
		dashes = []float64{6 * cam.Zoom, 4 * cam.Zoom}
	}
	dasher.SetColor(borderCol)
	dasher.SetStroke(fixedFloat(strokeW), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, dashes, 0)
	rasterx.AddRoundRect(tl.X, tl.Y, br.X, br.Y, radius, radius, 0, rasterx.RoundGap, dasher)
	dasher.Draw()
	dasher.Clear()

	glyph := "▾"
	if gb.Collapsed {
		glyph = "▸"
	}
	title := fmt.Sprintf("%s %s (%d)", glyph, gb.Label, len(gb.Members))
	fontPx := max(8, int(math.Round(12.0*cam.Zoom)))
	padX := max(4.0, 6.0*cam.Zoom)
	clip := rectClip(dst.Bounds(), tl.X+padX, tl.Y+1, br.X-padX, tl.Y+hdrH)
	drawClippedText(dst, clip, int(math.Round(tl.X+max(5.0, 8.0*cam.Zoom))), int(math.Round(tl.Y+hdrH*0.74)), title, fontPx, true, theme.TextPrimary)
}

func drawVectorWire(filler *rasterx.Filler, dasher *rasterx.Dasher, wp WirePath, cam Camera, theme Theme) {
	p0 := cam.WorldToScreen(wp.FromPos.X, wp.FromPos.Y)
	c1 := cam.WorldToScreen(wp.Ctrl1.X, wp.Ctrl1.Y)
	c2 := cam.WorldToScreen(wp.Ctrl2.X, wp.Ctrl2.Y)
	p3 := cam.WorldToScreen(wp.ToPos.X, wp.ToPos.Y)

	wireColor := theme.TypeColor(wp.DataType)
	if wp.Compat == nodegraph.CompatIncompatible {
		wireColor = theme.CompatBad
	}
	if wp.Selected {
		wireColor = theme.SelectGlow
	} else if wp.Hovered {
		wireColor = rgba(186, 230, 253, 255)
	}

	coreWidth := max(1.8, 2.6*cam.Zoom)
	if wp.Selected {
		coreWidth = max(3.0, 4.0*cam.Zoom)
	} else if wp.Hovered {
		coreWidth = max(2.5, 3.4*cam.Zoom)
	}

	// Dark outer casing so overlapping wires read cleanly at crossings.
	dasher.SetColor(theme.WireCasing)
	dasher.SetStroke(fixedFloat(coreWidth+2.2*cam.Zoom), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
	dasher.Start(rasterx.ToFixedP(p0.X, p0.Y))
	dasher.CubeBezier(rasterx.ToFixedP(c1.X, c1.Y), rasterx.ToFixedP(c2.X, c2.Y), rasterx.ToFixedP(p3.X, p3.Y))
	dasher.Stop(false)
	dasher.Draw()
	dasher.Clear()

	var dashes []float64
	switch wp.Edge.Kind {
	case "frame":
		dashes = []float64{max(4, 9*cam.Zoom), max(3, 5*cam.Zoom)}
	case "event":
		dashes = []float64{max(2, 3*cam.Zoom), max(3, 5*cam.Zoom)}
	}

	dasher.SetColor(wireColor)
	dasher.SetStroke(fixedFloat(coreWidth), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, dashes, 0)
	dasher.Start(rasterx.ToFixedP(p0.X, p0.Y))
	dasher.CubeBezier(rasterx.ToFixedP(c1.X, c1.Y), rasterx.ToFixedP(c2.X, c2.Y), rasterx.ToFixedP(p3.X, p3.Y))
	dasher.Stop(false)
	dasher.Draw()
	dasher.Clear()

	// Midpoint directional arrow chevron.
	mid := evalCubicBezier(p0, c1, c2, p3, 0.50)
	ahead := evalCubicBezier(p0, c1, c2, p3, 0.54)
	dirX := ahead.X - mid.X
	dirY := ahead.Y - mid.Y
	norm := math.Hypot(dirX, dirY)
	if norm > 0.5 {
		ux, uy := dirX/norm, dirY/norm
		px, py := -uy, ux
		sz := max(3.5, 5.2*cam.Zoom)
		tip := Point{X: mid.X + ux*sz, Y: mid.Y + uy*sz}
		w1 := Point{X: mid.X - ux*sz + px*sz*0.72, Y: mid.Y - uy*sz + py*sz*0.72}
		w2 := Point{X: mid.X - ux*sz - px*sz*0.72, Y: mid.Y - uy*sz - py*sz*0.72}
		filler.SetColor(wireColor)
		filler.Start(rasterx.ToFixedP(tip.X, tip.Y))
		filler.Line(rasterx.ToFixedP(w1.X, w1.Y))
		filler.Line(rasterx.ToFixedP(w2.X, w2.Y))
		filler.Stop(true)
		filler.Draw()
		filler.Clear()
	}
}

func drawVectorDragPreview(dasher *rasterx.Dasher, drag WireDragPreview, cam Camera, theme Theme) {
	var from, to Point
	if drag.FromOutlet {
		from = drag.AnchorPos
		to = drag.CursorPos
	} else {
		from = drag.CursorPos
		to = drag.AnchorPos
	}
	c1, c2 := bezierControls(from, to)
	p0 := cam.WorldToScreen(from.X, from.Y)
	sc1 := cam.WorldToScreen(c1.X, c1.Y)
	sc2 := cam.WorldToScreen(c2.X, c2.Y)
	p3 := cam.WorldToScreen(to.X, to.Y)

	col := theme.TypeColor(drag.AnchorType)
	dashes := []float64{max(4, 7*cam.Zoom), max(3, 5*cam.Zoom)}
	if drag.SnapTarget != nil {
		col = theme.CompatOK
		dashes = nil
	}

	dasher.SetColor(col)
	dasher.SetStroke(fixedFloat(max(2.2, 3.0*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, dashes, 0)
	dasher.Start(rasterx.ToFixedP(p0.X, p0.Y))
	dasher.CubeBezier(rasterx.ToFixedP(sc1.X, sc1.Y), rasterx.ToFixedP(sc2.X, sc2.Y), rasterx.ToFixedP(p3.X, p3.Y))
	dasher.Stop(false)
	dasher.Draw()
	dasher.Clear()
}

func drawVectorNode(dst *image.RGBA, filler *rasterx.Filler, dasher *rasterx.Dasher, nb NodeBox, cam Camera, theme Theme) {
	tl := cam.WorldToScreen(nb.Bounds.X, nb.Bounds.Y)
	br := cam.WorldToScreen(nb.Bounds.X+nb.Bounds.W, nb.Bounds.Y+nb.Bounds.H)
	imgB := dst.Bounds()
	if br.X < -16 || br.Y < -16 || tl.X > float64(imgB.Dx())+16 || tl.Y > float64(imgB.Dy())+16 {
		return
	}

	radius := max(3.5, 8.0*cam.Zoom)
	shadowOff := max(2.0, 4.0*cam.Zoom)

	// Drop shadow.
	filler.SetColor(theme.CardShadow)
	rasterx.AddRoundRect(tl.X+shadowOff, tl.Y+shadowOff, br.X+shadowOff, br.Y+shadowOff, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	// Stacked component backplate for reusable graph/* subgraphs.
	if nb.IsSubgraph {
		stackOffX := max(2.5, 4.0*cam.Zoom)
		stackOffY := max(2.0, 3.5*cam.Zoom)
		filler.SetColor(rgba(13, 148, 136, 56))
		rasterx.AddRoundRect(tl.X+stackOffX, tl.Y-stackOffY, br.X+stackOffX, br.Y-stackOffY, radius, radius, 0, rasterx.RoundGap, filler)
		filler.Draw()
		filler.Clear()

		dasher.SetColor(rgba(45, 212, 191, 175))
		dasher.SetStroke(fixedFloat(max(1.0, 1.25*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
		rasterx.AddRoundRect(tl.X+stackOffX, tl.Y-stackOffY, br.X+stackOffX, br.Y-stackOffY, radius, radius, 0, rasterx.RoundGap, dasher)
		dasher.Draw()
		dasher.Clear()
	}

	// Card body.
	bodyCol := theme.CardBG
	if nb.Selected {
		bodyCol = theme.CardBGSelect
	}
	filler.SetColor(bodyCol)
	rasterx.AddRoundRect(tl.X, tl.Y, br.X, br.Y, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	// Namespace header bar (at least 13px tall when card allows so header title is never clipped at low zoom).
	hdrH := min(br.Y-tl.Y, max(13.0, HeaderHeight*cam.Zoom))
	hdrCol := theme.NamespaceColor(nb.Namespace)
	filler.SetColor(hdrCol)
	rasterx.AddRoundRect(tl.X, tl.Y, br.X, tl.Y+hdrH, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	// Card outline / selection or hover glow.
	borderCol := theme.CardBorder
	borderW := max(1.1, 1.4*cam.Zoom)
	if nb.Selected {
		borderCol = theme.SelectGlow
		borderW = max(2.2, 2.6*cam.Zoom)
	} else if nb.Hovered {
		borderCol = rgba(148, 163, 184, 210)
		borderW = max(1.5, 1.8*cam.Zoom)
	}
	dasher.SetColor(borderCol)
	dasher.SetStroke(fixedFloat(borderW), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
	rasterx.AddRoundRect(tl.X, tl.Y, br.X, br.Y, radius, radius, 0, rasterx.RoundGap, dasher)
	dasher.Draw()
	dasher.Clear()

	// Card header title renders at EVERY zoom level (MinZoom..MaxZoom) so cards are never blank.
	titlePx := max(8, int(math.Round(13.0*cam.Zoom)))
	subPx := max(8, int(math.Round(11.0*cam.Zoom)))
	leftPad := max(4.0, 8.0*cam.Zoom)
	rightToggleReserve := max(10.0, 18.0*cam.Zoom)
	if !nb.Collapsed {
		leftMostBadgeX := nb.Bounds.X + nb.Bounds.W
		for _, brk := range []Rect{nb.CollapseBadgeRect, nb.PreviewBadgeRect, nb.ParamsBadgeRect, nb.ViewerBadgeRect} {
			if brk.W > 0 && brk.X < leftMostBadgeX {
				leftMostBadgeX = brk.X
			}
		}
		if leftMostBadgeX < nb.Bounds.X+nb.Bounds.W {
			badgeScreenX := cam.WorldToScreen(leftMostBadgeX-3, nb.Bounds.Y).X
			if res := br.X - badgeScreenX; res > rightToggleReserve {
				rightToggleReserve = res
			}
		}
	}
	if br.X-tl.X < 42 {
		rightToggleReserve = 3.0
	}
	hdrClip := rectClip(imgB, tl.X+leftPad-1, tl.Y+1, br.X-rightToggleReserve, tl.Y+hdrH)
	showSubtitle := cam.Zoom >= 0.62 && !nb.Collapsed && hdrH >= 20.0
	titleBaselineY := int(math.Round(tl.Y + hdrH*0.74))
	if showSubtitle {
		titleBaselineY = int(math.Round(tl.Y + hdrH*0.52))
	}
	drawClippedText(dst, hdrClip, int(math.Round(tl.X+leftPad)), titleBaselineY, nb.Title, titlePx, true, theme.TextPrimary)
	if showSubtitle {
		drawClippedText(dst, hdrClip, int(math.Round(tl.X+leftPad)), int(math.Round(tl.Y+hdrH*0.88)), nb.Subtitle, max(8, subPx-1), false, rgba(226, 232, 240, 215))
	}
	if br.X-tl.X >= 42 {
		if !nb.Collapsed && cam.Zoom >= 0.48 && br.X-tl.X >= 84 {
			drawVectorHeaderBadges(filler, dasher, nb, cam, theme)
		}
		collapseGlyph := "▾"
		if nb.Collapsed {
			collapseGlyph = "▸"
		}
		toggleClip := rectClip(imgB, br.X-max(12.0, 20.0*cam.Zoom), tl.Y+1, br.X-2, tl.Y+hdrH)
		drawClippedText(dst, toggleClip, int(math.Round(br.X-max(9.0, 15.0*cam.Zoom))), int(math.Round(tl.Y+hdrH*0.70)), collapseGlyph, titlePx, true, theme.TextPrimary)
	}

	if !nb.Collapsed && cam.Zoom >= 0.30 {
		portRows := max(len(nb.Inlets), len(nb.Outlets))
		rowHalfClipH := max(7.0, 10.0*cam.Zoom)
		portPadX := max(5.0, 10.0*cam.Zoom)
		portBaseOffY := max(3.5, 4.0*cam.Zoom)
		for _, in := range nb.Inlets {
			sp := cam.WorldToScreen(in.Center.X, in.Center.Y)
			label := in.Name
			if in.Required {
				label += "*"
			}
			rowClip := rectClip(imgB, tl.X+portPadX, sp.Y-rowHalfClipH, (tl.X+br.X)*0.54, sp.Y+rowHalfClipH)
			drawClippedText(dst, rowClip, int(math.Round(tl.X+portPadX+1)), int(math.Round(sp.Y+portBaseOffY)), label, subPx, false, theme.TextSecondary)
		}
		for _, out := range nb.Outlets {
			sp := cam.WorldToScreen(out.Center.X, out.Center.Y)
			rowClip := rectClip(imgB, (tl.X+br.X)*0.46, sp.Y-rowHalfClipH, br.X-portPadX, sp.Y+rowHalfClipH)
			textW := measureTextWidth(out.Name, subPx, false)
			tx := max(int((tl.X+br.X)*0.48), int(math.Round(br.X-portPadX-1))-textW)
			drawClippedText(dst, rowClip, tx, int(math.Round(sp.Y+portBaseOffY)), out.Name, subPx, false, theme.TextSecondary)
		}

		// Summary pills & inline drawers (capabilities, collapsible parameters, live preview).
		if cam.Zoom >= 0.42 {
			extraY := tl.Y + hdrH + float64(max(1, portRows))*PortRowHeight*cam.Zoom + 3*cam.Zoom
			pillH := max(10.0, SummaryRowHeight*0.86*cam.Zoom)
			if len(nb.CapBadges) > 0 {
				badgeText := "cap: " + strings.Join(nb.CapBadges, ", ")
				drawSummaryPill(dst, filler, tl.X+8*cam.Zoom, extraY, br.X-8*cam.Zoom, pillH, badgeText, max(8, subPx-1), theme)
				extraY += max(pillH+2, SummaryRowHeight*cam.Zoom)
			}
			if len(nb.BoundaryBadges) > 0 {
				badgeText := strings.Join(nb.BoundaryBadges, "  ")
				drawSummaryPill(dst, filler, tl.X+8*cam.Zoom, extraY, br.X-8*cam.Zoom, pillH, badgeText, max(8, subPx-1), theme)
				extraY += max(pillH+2, SummaryRowHeight*cam.Zoom)
			}
			if nb.ParamsOpen {
				drawVectorParamsDrawer(dst, filler, dasher, nb, cam, max(8, subPx-1), theme)
			} else {
				for i := 0; i < min(2, len(nb.ParamSummary)); i++ {
					drawSummaryPill(dst, filler, tl.X+8*cam.Zoom, extraY, br.X-8*cam.Zoom, pillH, nb.ParamSummary[i], max(8, subPx-1), theme)
					extraY += max(pillH+2, SummaryRowHeight*cam.Zoom)
				}
			}
			if nb.PreviewOpen {
				drawVectorPreviewDrawer(dst, filler, dasher, nb, cam, max(8, subPx-1), theme)
			}
		}
	}

	// Draw port sockets on top of card edges.
	for _, p := range nb.Inlets {
		drawPortSocket(filler, dasher, p, cam, theme)
	}
	for _, p := range nb.Outlets {
		drawPortSocket(filler, dasher, p, cam, theme)
	}
}

func drawVectorHeaderBadges(filler *rasterx.Filler, dasher *rasterx.Dasher, nb NodeBox, cam Camera, theme Theme) {
	// 1. Viewer Tap badge (eye / lens dot).
	if nb.ViewerBadgeRect.W > 0 {
		b0 := cam.WorldToScreen(nb.ViewerBadgeRect.X, nb.ViewerBadgeRect.Y)
		b1 := cam.WorldToScreen(nb.ViewerBadgeRect.X+nb.ViewerBadgeRect.W, nb.ViewerBadgeRect.Y+nb.ViewerBadgeRect.H)
		r := max(2.5, 4.0*cam.Zoom)
		bgCol := rgba(15, 23, 42, 105)
		fgCol := rgba(226, 232, 240, 185)
		if nb.ViewerActive {
			bgCol = rgba(245, 158, 11, 235)
			fgCol = rgba(15, 23, 42, 255)
		}
		filler.SetColor(bgCol)
		rasterx.AddRoundRect(b0.X, b0.Y, b1.X, b1.Y, r, r, 0, rasterx.RoundGap, filler)
		filler.Draw()
		filler.Clear()

		cx := (b0.X + b1.X) * 0.5
		cy := (b0.Y + b1.Y) * 0.5
		rad := min(b1.X-b0.X, b1.Y-b0.Y) * 0.28
		dasher.SetColor(fgCol)
		dasher.SetStroke(fixedFloat(max(1.1, 1.3*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
		rasterx.AddCircle(cx, cy, rad, dasher)
		dasher.Draw()
		dasher.Clear()

		filler.SetColor(fgCol)
		rasterx.AddCircle(cx, cy, max(1.1, rad*0.42), filler)
		filler.Draw()
		filler.Clear()
		_ = theme
	}

	// 2. Inline Params drawer badge (3 horizontal slider tracks + knobs).
	if nb.ParamsBadgeRect.W > 0 {
		b0 := cam.WorldToScreen(nb.ParamsBadgeRect.X, nb.ParamsBadgeRect.Y)
		b1 := cam.WorldToScreen(nb.ParamsBadgeRect.X+nb.ParamsBadgeRect.W, nb.ParamsBadgeRect.Y+nb.ParamsBadgeRect.H)
		r := max(2.5, 4.0*cam.Zoom)
		bgCol := rgba(15, 23, 42, 105)
		fgCol := rgba(226, 232, 240, 195)
		if nb.ParamsOpen {
			bgCol = rgba(14, 165, 233, 235)
			fgCol = rgba(248, 250, 252, 255)
		}
		filler.SetColor(bgCol)
		rasterx.AddRoundRect(b0.X, b0.Y, b1.X, b1.Y, r, r, 0, rasterx.RoundGap, filler)
		filler.Draw()
		filler.Clear()

		padX := (b1.X - b0.X) * 0.22
		knobFracs := [3]float64{0.32, 0.72, 0.45}
		for i := 0; i < 3; i++ {
			ly := b0.Y + (b1.Y-b0.Y)*(0.28+float64(i)*0.22)
			dasher.SetColor(fgCol)
			dasher.SetStroke(fixedFloat(max(1.0, 1.2*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
			dasher.Start(rasterx.ToFixedP(b0.X+padX, ly))
			dasher.Line(rasterx.ToFixedP(b1.X-padX, ly))
			dasher.Stop(false)
			dasher.Draw()
			dasher.Clear()

			kx := b0.X + padX + (b1.X-b0.X-2*padX)*knobFracs[i]
			filler.SetColor(fgCol)
			rasterx.AddCircle(kx, ly, max(1.1, 1.5*cam.Zoom), filler)
			filler.Draw()
			filler.Clear()
		}
	}

	// 3. On-Card Preview drawer badge (mini monitor frame).
	if nb.PreviewBadgeRect.W > 0 {
		b0 := cam.WorldToScreen(nb.PreviewBadgeRect.X, nb.PreviewBadgeRect.Y)
		b1 := cam.WorldToScreen(nb.PreviewBadgeRect.X+nb.PreviewBadgeRect.W, nb.PreviewBadgeRect.Y+nb.PreviewBadgeRect.H)
		r := max(2.5, 4.0*cam.Zoom)
		bgCol := rgba(15, 23, 42, 105)
		fgCol := rgba(226, 232, 240, 195)
		if nb.PreviewOpen {
			bgCol = rgba(16, 185, 129, 235)
			fgCol = rgba(248, 250, 252, 255)
		}
		filler.SetColor(bgCol)
		rasterx.AddRoundRect(b0.X, b0.Y, b1.X, b1.Y, r, r, 0, rasterx.RoundGap, filler)
		filler.Draw()
		filler.Clear()

		padX := (b1.X - b0.X) * 0.21
		padY := (b1.Y - b0.Y) * 0.25
		dasher.SetColor(fgCol)
		dasher.SetStroke(fixedFloat(max(1.0, 1.25*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
		rasterx.AddRoundRect(b0.X+padX, b0.Y+padY, b1.X-padX, b1.Y-padY, max(1.0, 1.8*cam.Zoom), max(1.0, 1.8*cam.Zoom), 0, rasterx.RoundGap, dasher)
		dasher.Draw()
		dasher.Clear()

		filler.SetColor(fgCol)
		rasterx.AddCircle(b0.X+padX+(b1.X-b0.X-2*padX)*0.35, b0.Y+padY+(b1.Y-b0.Y-2*padY)*0.42, max(1.0, 1.3*cam.Zoom), filler)
		filler.Draw()
		filler.Clear()
	}
}

func drawVectorParamsDrawer(dst *image.RGBA, filler *rasterx.Filler, dasher *rasterx.Dasher, nb NodeBox, cam Camera, fontPx int, theme Theme) {
	imgB := dst.Bounds()
	if nb.ParamsHeaderRect.W > 0 {
		h0 := cam.WorldToScreen(nb.ParamsHeaderRect.X, nb.ParamsHeaderRect.Y)
		h1 := cam.WorldToScreen(nb.ParamsHeaderRect.X+nb.ParamsHeaderRect.W, nb.ParamsHeaderRect.Y+nb.ParamsHeaderRect.H)
		r := max(2.5, 4.0*cam.Zoom)
		filler.SetColor(rgba(15, 23, 42, 200))
		rasterx.AddRoundRect(h0.X, h0.Y, h1.X, h1.Y, r, r, 0, rasterx.RoundGap, filler)
		filler.Draw()
		filler.Clear()

		hdrFont := max(8, fontPx-1)
		clip := rectClip(imgB, h0.X+3, h0.Y, h1.X-3, h1.Y)
		title := fmt.Sprintf("▾ PARAMETERS (%d)", len(nb.ParamRows))
		drawClippedText(dst, clip, int(math.Round(h0.X+5*cam.Zoom)), int(math.Round(h0.Y+(h1.Y-h0.Y)*0.74)), title, hdrFont, true, rgba(125, 211, 252, 235))
	}

	for _, row := range nb.ParamRows {
		r0 := cam.WorldToScreen(row.Bounds.X, row.Bounds.Y)
		r1 := cam.WorldToScreen(row.Bounds.X+row.Bounds.W, row.Bounds.Y+row.Bounds.H)
		c0 := cam.WorldToScreen(row.ControlRect.X, row.ControlRect.Y)
		c1 := cam.WorldToScreen(row.ControlRect.X+row.ControlRect.W, row.ControlRect.Y+row.ControlRect.H)
		if c1.X-c0.X < 18 || c1.Y-c0.Y < 6 {
			continue
		}

		// Left parameter label.
		lblClip := rectClip(imgB, r0.X+2, r0.Y, c0.X-3, r1.Y)
		drawClippedText(dst, lblClip, int(math.Round(r0.X+4*cam.Zoom)), int(math.Round(r0.Y+(r1.Y-r0.Y)*0.72)), row.Label, fontPx, false, theme.TextSecondary)

		cr := min(4.0, (c1.Y-c0.Y)*0.32)
		valFont := max(8, fontPx-1)

		switch row.Kind {
		case InlineParamFloat, InlineParamInt:
			d0 := cam.WorldToScreen(row.DecRect.X, row.DecRect.Y)
			d1 := cam.WorldToScreen(row.DecRect.X+row.DecRect.W, row.DecRect.Y+row.DecRect.H)
			i0 := cam.WorldToScreen(row.IncRect.X, row.IncRect.Y)
			i1 := cam.WorldToScreen(row.IncRect.X+row.IncRect.W, row.IncRect.Y+row.IncRect.H)
			t0 := cam.WorldToScreen(row.TrackRect.X, row.TrackRect.Y)
			t1 := cam.WorldToScreen(row.TrackRect.X+row.TrackRect.W, row.TrackRect.Y+row.TrackRect.H)

			// Step buttons [-] and [+].
			filler.SetColor(rgba(30, 41, 59, 235))
			rasterx.AddRoundRect(d0.X, d0.Y, d1.X, d1.Y, cr, cr, 0, rasterx.RoundGap, filler)
			rasterx.AddRoundRect(i0.X, i0.Y, i1.X, i1.Y, cr, cr, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()

			dClip := rectClip(imgB, d0.X, d0.Y, d1.X, d1.Y)
			iClip := rectClip(imgB, i0.X, i0.Y, i1.X, i1.Y)
			mw := measureTextWidth("-", valFont, true)
			pw := measureTextWidth("+", valFont, true)
			drawClippedText(dst, dClip, int(math.Round((d0.X+d1.X)*0.5))-mw/2, int(math.Round(d0.Y+(d1.Y-d0.Y)*0.74)), "-", valFont, true, theme.TextSecondary)
			drawClippedText(dst, iClip, int(math.Round((i0.X+i1.X)*0.5))-pw/2, int(math.Round(i0.Y+(i1.Y-i0.Y)*0.74)), "+", valFont, true, theme.TextSecondary)

			// Slider trough + filled progress + thumb + centered value readout.
			filler.SetColor(rgba(15, 23, 42, 240))
			rasterx.AddRoundRect(t0.X, t0.Y, t1.X, t1.Y, cr, cr, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()

			frac := clampFloat(row.Fraction, 0, 1)
			fillW := (t1.X - t0.X) * frac
			if fillW > 1.5 {
				filler.SetColor(rgba(14, 165, 233, 155))
				rasterx.AddRoundRect(t0.X, t0.Y, t0.X+fillW, t1.Y, cr, cr, 0, rasterx.RoundGap, filler)
				filler.Draw()
				filler.Clear()
			}

			dasher.SetColor(rgba(51, 65, 85, 210))
			dasher.SetStroke(fixedFloat(max(0.9, 1.0*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
			rasterx.AddRoundRect(t0.X, t0.Y, t1.X, t1.Y, cr, cr, 0, rasterx.RoundGap, dasher)
			dasher.Draw()
			dasher.Clear()

			knobX := clampFloat(t0.X+fillW, t0.X+2.5, t1.X-2.5)
			filler.SetColor(rgba(186, 230, 253, 230))
			rasterx.AddCircle(knobX, (t0.Y+t1.Y)*0.5, max(2.2, (t1.Y-t0.Y)*0.24), filler)
			filler.Draw()
			filler.Clear()

			tClip := rectClip(imgB, t0.X+2, t0.Y, t1.X-2, t1.Y)
			vw := measureTextWidth(row.DisplayValue, valFont, true)
			vx := max(int(t0.X+3), int(math.Round((t0.X+t1.X)*0.5))-vw/2)
			drawClippedText(dst, tClip, vx, int(math.Round(t0.Y+(t1.Y-t0.Y)*0.75)), row.DisplayValue, valFont, true, theme.TextPrimary)

		case InlineParamEnum:
			filler.SetColor(rgba(15, 23, 42, 240))
			rasterx.AddRoundRect(c0.X, c0.Y, c1.X, c1.Y, cr, cr, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()

			dasher.SetColor(rgba(56, 189, 248, 155))
			dasher.SetStroke(fixedFloat(max(0.9, 1.0*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
			rasterx.AddRoundRect(c0.X, c0.Y, c1.X, c1.Y, cr, cr, 0, rasterx.RoundGap, dasher)
			dasher.Draw()
			dasher.Clear()

			d0 := cam.WorldToScreen(row.DecRect.X, row.DecRect.Y)
			d1 := cam.WorldToScreen(row.DecRect.X+row.DecRect.W, row.DecRect.Y+row.DecRect.H)
			i0 := cam.WorldToScreen(row.IncRect.X, row.IncRect.Y)
			i1 := cam.WorldToScreen(row.IncRect.X+row.IncRect.W, row.IncRect.Y+row.IncRect.H)
			t0 := cam.WorldToScreen(row.TrackRect.X, row.TrackRect.Y)
			t1 := cam.WorldToScreen(row.TrackRect.X+row.TrackRect.W, row.TrackRect.Y+row.TrackRect.H)

			baseY := int(math.Round(c0.Y + (c1.Y-c0.Y)*0.75))
			drawClippedText(dst, rectClip(imgB, d0.X, d0.Y, d1.X, d1.Y), int(math.Round(d0.X+2*cam.Zoom)), baseY, "◂", valFont, true, theme.TextSecondary)
			drawClippedText(dst, rectClip(imgB, i0.X, i0.Y, i1.X, i1.Y), int(math.Round(i0.X+2*cam.Zoom)), baseY, "▸", valFont, true, theme.TextSecondary)

			tClip := rectClip(imgB, t0.X+1, t0.Y, t1.X-1, t1.Y)
			vw := measureTextWidth(row.DisplayValue, valFont, false)
			vx := max(int(t0.X+2), int(math.Round((t0.X+t1.X)*0.5))-vw/2)
			drawClippedText(dst, tClip, vx, baseY, row.DisplayValue, valFont, false, rgba(125, 211, 252, 255))

		case InlineParamBool:
			pillCol := rgba(30, 41, 59, 240)
			txtCol := theme.TextMuted
			if row.BoolVal {
				pillCol = rgba(16, 185, 129, 210)
				txtCol = rgba(248, 250, 252, 255)
			}
			pr := (c1.Y - c0.Y) * 0.46
			filler.SetColor(pillCol)
			rasterx.AddRoundRect(c0.X, c0.Y, c1.X, c1.Y, pr, pr, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()

			knobR := max(2.5, (c1.Y-c0.Y)*0.33)
			knobX := c0.X + knobR + 3*cam.Zoom
			if row.BoolVal {
				knobX = c1.X - knobR - 3*cam.Zoom
			}
			filler.SetColor(rgba(248, 250, 252, 245))
			rasterx.AddCircle(knobX, (c0.Y+c1.Y)*0.5, knobR, filler)
			filler.Draw()
			filler.Clear()

			cClip := rectClip(imgB, c0.X+4, c0.Y, c1.X-4, c1.Y)
			vw := measureTextWidth(row.DisplayValue, valFont, true)
			vx := int(math.Round((c0.X+c1.X)*0.5)) - vw/2
			drawClippedText(dst, cClip, vx, int(math.Round(c0.Y+(c1.Y-c0.Y)*0.75)), row.DisplayValue, valFont, true, txtCol)

		default:
			filler.SetColor(rgba(15, 23, 42, 235))
			rasterx.AddRoundRect(c0.X, c0.Y, c1.X, c1.Y, cr, cr, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()

			dasher.SetColor(rgba(51, 65, 85, 195))
			dasher.SetStroke(fixedFloat(max(0.9, 1.0*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
			rasterx.AddRoundRect(c0.X, c0.Y, c1.X, c1.Y, cr, cr, 0, rasterx.RoundGap, dasher)
			dasher.Draw()
			dasher.Clear()

			cClip := rectClip(imgB, c0.X+4, c0.Y, c1.X-4, c1.Y)
			drawClippedText(dst, cClip, int(math.Round(c0.X+5*cam.Zoom)), int(math.Round(c0.Y+(c1.Y-c0.Y)*0.75)), row.DisplayValue, valFont, false, theme.TextPrimary)
		}
	}
}

func drawVectorPreviewDrawer(dst *image.RGBA, filler *rasterx.Filler, dasher *rasterx.Dasher, nb NodeBox, cam Camera, fontPx int, theme Theme) {
	imgB := dst.Bounds()
	if nb.PreviewHeaderRect.W > 0 {
		h0 := cam.WorldToScreen(nb.PreviewHeaderRect.X, nb.PreviewHeaderRect.Y)
		h1 := cam.WorldToScreen(nb.PreviewHeaderRect.X+nb.PreviewHeaderRect.W, nb.PreviewHeaderRect.Y+nb.PreviewHeaderRect.H)
		r := max(2.5, 4.0*cam.Zoom)
		filler.SetColor(rgba(15, 23, 42, 200))
		rasterx.AddRoundRect(h0.X, h0.Y, h1.X, h1.Y, r, r, 0, rasterx.RoundGap, filler)
		filler.Draw()
		filler.Clear()

		hdrFont := max(8, fontPx-1)
		title := "▾ PREVIEW"
		if nb.Preview != nil && nb.Preview.Outlet != "" {
			title += " · " + nb.Preview.Outlet
		}
		clip := rectClip(imgB, h0.X+3, h0.Y, h1.X-14, h1.Y)
		drawClippedText(dst, clip, int(math.Round(h0.X+5*cam.Zoom)), int(math.Round(h0.Y+(h1.Y-h0.Y)*0.74)), title, hdrFont, true, rgba(110, 231, 183, 235))

		if nb.Preview != nil && nb.Preview.Active {
			filler.SetColor(rgba(16, 185, 129, 245))
			rasterx.AddCircle(h1.X-7*cam.Zoom, (h0.Y+h1.Y)*0.5, max(2.0, 3.0*cam.Zoom), filler)
			filler.Draw()
			filler.Clear()
		}
	}

	if nb.PreviewContentRect.W <= 0 || nb.PreviewContentRect.H <= 0 {
		return
	}
	p0 := cam.WorldToScreen(nb.PreviewContentRect.X, nb.PreviewContentRect.Y)
	p1 := cam.WorldToScreen(nb.PreviewContentRect.X+nb.PreviewContentRect.W, nb.PreviewContentRect.Y+nb.PreviewContentRect.H)
	if p1.X-p0.X < 16 || p1.Y-p0.Y < 12 {
		return
	}
	r := max(3.0, 5.0*cam.Zoom)

	filler.SetColor(rgba(2, 6, 23, 248))
	rasterx.AddRoundRect(p0.X, p0.Y, p1.X, p1.Y, r, r, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	pad := max(2.0, 3.0*cam.Zoom)
	innerX0, innerY0 := p0.X+pad, p0.Y+pad
	innerX1, innerY1 := p1.X-pad, p1.Y-pad

	switch {
	case nb.Preview != nil && nb.Preview.Image != nil && !nb.Preview.Image.Bounds().Empty():
		blitPreviewRGBA(dst, nb.Preview.Image, innerX0, innerY0, innerX1, innerY1)
		if nb.Preview.Summary != "" && innerY1-innerY0 >= 34 {
			capFont := max(8, fontPx-2)
			tw := measureTextWidth(nb.Preview.Summary, capFont, false)
			cw := min(innerX1-innerX0-4, float64(tw)+10)
			ch := max(11.0, float64(capFont)+4)
			cx0 := innerX1 - cw - 2
			cy0 := innerY1 - ch - 2
			filler.SetColor(rgba(2, 6, 23, 195))
			rasterx.AddRoundRect(cx0, cy0, cx0+cw, cy0+ch, 3, 3, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()
			cClip := rectClip(imgB, cx0+2, cy0, cx0+cw-2, cy0+ch)
			drawClippedText(dst, cClip, int(math.Round(cx0+5)), int(math.Round(cy0+ch*0.76)), nb.Preview.Summary, capFont, false, rgba(226, 232, 240, 230))
		}

	case nb.Preview != nil && len(nb.Preview.Lines) > 0:
		lineFont := max(8, fontPx-2)
		lineStep := max(10.0, float64(lineFont)+3.0)
		clip := rectClip(imgB, innerX0+2, innerY0+1, innerX1-2, innerY1-1)
		curY := innerY0 + lineStep*0.86
		for idx, ln := range nb.Preview.Lines {
			if curY > innerY1-1 {
				break
			}
			col := theme.TextSecondary
			if idx == 0 {
				col = theme.TextPrimary
			}
			drawClippedText(dst, clip, int(math.Round(innerX0+4)), int(math.Round(curY)), ln, lineFont, idx == 0, col)
			curY += lineStep
		}

	case nb.Preview != nil && nb.Preview.Summary != "":
		clip := rectClip(imgB, innerX0+2, innerY0+1, innerX1-2, innerY1-1)
		drawClippedText(dst, clip, int(math.Round(innerX0+4)), int(math.Round((innerY0+innerY1)*0.55)), nb.Preview.Summary, max(8, fontPx-1), false, theme.TextSecondary)

	default:
		clip := rectClip(imgB, innerX0+2, innerY0+1, innerX1-2, innerY1-1)
		msg := "Waiting for emission..."
		mw := measureTextWidth(msg, max(8, fontPx-1), false)
		mx := max(int(innerX0+4), int(math.Round((innerX0+innerX1)*0.5))-mw/2)
		drawClippedText(dst, clip, mx, int(math.Round((innerY0+innerY1)*0.55)), msg, max(8, fontPx-1), false, theme.TextMuted)
	}

	borderCol := rgba(51, 65, 85, 215)
	if nb.ViewerActive {
		borderCol = rgba(245, 158, 11, 225)
	}
	dasher.SetColor(borderCol)
	dasher.SetStroke(fixedFloat(max(1.0, 1.2*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
	rasterx.AddRoundRect(p0.X, p0.Y, p1.X, p1.Y, r, r, 0, rasterx.RoundGap, dasher)
	dasher.Draw()
	dasher.Clear()
}

func blitPreviewRGBA(dst, src *image.RGBA, x0, y0, x1, y1 float64) {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	availW := x1 - x0
	availH := y1 - y0
	if sw <= 0 || sh <= 0 || availW < 2 || availH < 2 {
		return
	}
	scale := min(availW/float64(sw), availH/float64(sh))
	dw := max(1.0, float64(sw)*scale)
	dh := max(1.0, float64(sh)*scale)
	ox := x0 + (availW-dw)*0.5
	oy := y0 + (availH-dh)*0.5

	ix0 := int(math.Round(ox))
	iy0 := int(math.Round(oy))
	ix1 := max(ix0+1, int(math.Round(ox+dw)))
	iy1 := max(iy0+1, int(math.Round(oy+dh)))

	clip := image.Rect(ix0, iy0, ix1, iy1).Intersect(dst.Bounds())
	if clip.Empty() {
		return
	}
	invW := float64(sw) / float64(ix1-ix0)
	invH := float64(sh) / float64(iy1-iy0)

	for dy := clip.Min.Y; dy < clip.Max.Y; dy++ {
		sy := sb.Min.Y + min(sh-1, max(0, int((float64(dy-iy0)+0.5)*invH)))
		sRow := (sy - sb.Min.Y) * src.Stride
		dRow := (dy - dst.Rect.Min.Y) * dst.Stride
		for dx := clip.Min.X; dx < clip.Max.X; dx++ {
			sx := sb.Min.X + min(sw-1, max(0, int((float64(dx-ix0)+0.5)*invW)))
			si := sRow + (sx-sb.Min.X)*4
			di := dRow + (dx-dst.Rect.Min.X)*4
			sa := src.Pix[si+3]
			if sa == 255 {
				dst.Pix[di+0] = src.Pix[si+0]
				dst.Pix[di+1] = src.Pix[si+1]
				dst.Pix[di+2] = src.Pix[si+2]
				dst.Pix[di+3] = 255
			} else if sa > 0 {
				invA := uint32(255 - sa)
				dst.Pix[di+0] = uint8(min(255, uint32(src.Pix[si+0])+(uint32(dst.Pix[di+0])*invA)/255))
				dst.Pix[di+1] = uint8(min(255, uint32(src.Pix[si+1])+(uint32(dst.Pix[di+1])*invA)/255))
				dst.Pix[di+2] = uint8(min(255, uint32(src.Pix[si+2])+(uint32(dst.Pix[di+2])*invA)/255))
				dst.Pix[di+3] = 255
			}
		}
	}
}

func drawSummaryPill(dst *image.RGBA, filler *rasterx.Filler, x0, y0, x1, h float64, text string, fontPx int, theme Theme) {
	if x1-x0 < 24 || h < 7 {
		return
	}
	r := min(4.0, h*0.35)
	filler.SetColor(theme.BadgeBG)
	rasterx.AddRoundRect(x0, y0, x1, y0+h, r, r, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	clip := rectClip(dst.Bounds(), x0+4, y0, x1-4, y0+h)
	drawClippedText(dst, clip, int(x0+6), int(y0+h*0.74), text, fontPx, false, theme.TextMuted)
}

func drawPortSocket(filler *rasterx.Filler, dasher *rasterx.Dasher, p PortAnchor, cam Camera, theme Theme) {
	sp := cam.WorldToScreen(p.Center.X, p.Center.Y)
	r := max(3.2, PortSocketRadius*cam.Zoom)
	col := theme.TypeColor(p.Type)

	switch p.CompatState {
	case PortCompatValid:
		col = theme.CompatOK
		r *= 1.32
	case PortCompatInvalid:
		col = rgba(col.R/2, col.G/2, col.B/2, 130)
	}
	if p.Hovered {
		r *= 1.28
		filler.SetColor(rgba(col.R, col.G, col.B, 90))
		rasterx.AddCircle(sp.X, sp.Y, r*1.65, filler)
		filler.Draw()
		filler.Clear()
	}

	// Dark socket backing ring.
	filler.SetColor(theme.CardBG)
	rasterx.AddCircle(sp.X, sp.Y, r+1.6*cam.Zoom, filler)
	filler.Draw()
	filler.Clear()

	if p.Connected || p.CompatState == PortCompatValid {
		filler.SetColor(col)
		rasterx.AddCircle(sp.X, sp.Y, r, filler)
		filler.Draw()
		filler.Clear()
	} else {
		dasher.SetColor(col)
		dasher.SetStroke(fixedFloat(max(1.5, 2.0*cam.Zoom)), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
		rasterx.AddCircle(sp.X, sp.Y, r*0.85, dasher)
		dasher.Draw()
		dasher.Clear()
	}
}

func drawVectorHUD(dst *image.RGBA, filler *rasterx.Filler, label string, w, h int, theme Theme) {
	const fontPx = 11
	tw := measureTextWidth(label, fontPx, false)
	boxW := float64(min(w-16, tw+20))
	boxH := 22.0
	x0 := 8.0
	y0 := float64(h) - boxH - 8.0

	filler.SetColor(rgba(15, 23, 42, 215))
	rasterx.AddRoundRect(x0, y0, x0+boxW, y0+boxH, 5, 5, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	clip := rectClip(dst.Bounds(), x0+4, y0, x0+boxW-4, y0+boxH)
	drawClippedText(dst, clip, int(x0+9), int(y0+15), label, fontPx, false, theme.TextSecondary)
}

func drawVectorMarquee(filler *rasterx.Filler, dasher *rasterx.Dasher, r Rect, cam Camera, theme Theme) {
	tl := cam.WorldToScreen(r.X, r.Y)
	br := cam.WorldToScreen(r.X+r.W, r.Y+r.H)
	filler.SetColor(rgba(56, 189, 248, 36))
	rasterx.AddRect(tl.X, tl.Y, br.X, br.Y, 0, filler)
	filler.Draw()
	filler.Clear()

	dasher.SetColor(theme.SelectGlow)
	dasher.SetStroke(fixedFloat(1.6), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, []float64{5, 3}, 0)
	rasterx.AddRect(tl.X, tl.Y, br.X, br.Y, 0, dasher)
	dasher.Draw()
	dasher.Clear()
}

func drawVectorContextMenu(dst *image.RGBA, filler *rasterx.Filler, dasher *rasterx.Dasher, cm ContextMenuOverlay, w, h int, theme Theme) {
	cols := max(20, cm.CanvasCols)
	rows := max(8, cm.CanvasRows)
	c0, r0, c1, r1 := cm.CellBounds(cols, rows)
	cellW := float64(w) / float64(cols)
	cellH := float64(h) / float64(rows)

	x0 := float64(c0) * cellW
	y0 := float64(r0) * cellH
	x1 := float64(c1+1) * cellW
	y1 := float64(r1+1) * cellH
	radius := 6.0

	// Drop shadow.
	filler.SetColor(rgba(2, 6, 23, 180))
	rasterx.AddRoundRect(x0+4, y0+4, x1+4, y1+4, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	// Menu card body + header bar.
	filler.SetColor(rgba(15, 23, 42, 248))
	rasterx.AddRoundRect(x0, y0, x1, y1, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	hdrY1 := y0 + 2.0*cellH
	filler.SetColor(rgba(30, 41, 59, 255))
	rasterx.AddRoundRect(x0, y0, x1, hdrY1, radius, radius, 0, rasterx.RoundGap, filler)
	filler.Draw()
	filler.Clear()

	fontPx := max(9, min(14, int(math.Round(cellH*0.64))))
	hdrClip := rectClip(dst.Bounds(), x0+4, y0, x1-4, hdrY1)
	drawClippedText(dst, hdrClip, int(x0+8), int(y0+cellH*1.32), cm.Title, fontPx, true, theme.TextPrimary)

	for i, item := range cm.Items {
		iy0 := y0 + float64(i+2)*cellH
		iy1 := iy0 + cellH
		fg := theme.TextSecondary
		prefix := "  "
		if i == cm.HoverIdx {
			filler.SetColor(rgba(2, 132, 199, 230))
			rasterx.AddRoundRect(x0+3, iy0+1, x1-3, iy1-1, 4, 4, 0, rasterx.RoundGap, filler)
			filler.Draw()
			filler.Clear()
			fg = theme.TextPrimary
			prefix = "▸ "
		}
		rowClip := rectClip(dst.Bounds(), x0+4, iy0, x1-4, iy1)
		drawClippedText(dst, rowClip, int(x0+7), int(iy0+cellH*0.74), prefix+item, fontPx, i == cm.HoverIdx, fg)
	}

	dasher.SetColor(theme.SelectGlow)
	dasher.SetStroke(fixedFloat(1.5), fixedFloat(4), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.ArcClip, nil, 0)
	rasterx.AddRoundRect(x0, y0, x1, y1, radius, radius, 0, rasterx.RoundGap, dasher)
	dasher.Draw()
	dasher.Clear()
}

func measureTextWidth(s string, fontPx int, bold bool) int {
	var adv fixed.Int26_6
	withFontFace(fontPx, bold, func(face font.Face) {
		adv = font.MeasureString(face, s)
	})
	return adv.Ceil()
}

func drawClippedText(dst *image.RGBA, clip image.Rectangle, x, y int, s string, fontPx int, bold bool, col color.NRGBA) {
	if s == "" || clip.Empty() {
		return
	}
	sub := dst.SubImage(clip).(*image.RGBA)
	withFontFace(fontPx, bold, func(face font.Face) {
		d := font.Drawer{
			Dst:  sub,
			Src:  &image.Uniform{C: col},
			Face: face,
			Dot:  fixed.P(x, y),
		}
		d.DrawString(s)
	})
}

func rectClip(bounds image.Rectangle, x0, y0, x1, y1 float64) image.Rectangle {
	r := image.Rect(
		int(math.Floor(x0)),
		int(math.Floor(y0)),
		int(math.Ceil(x1)),
		int(math.Ceil(y1)),
	)
	return r.Intersect(bounds)
}

func fixedFloat(v float64) fixed.Int26_6 {
	return fixed.Int26_6(math.Round(v * 64.0))
}
