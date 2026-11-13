package glyphocr

// Ace Online draws chat without anti-aliasing in a small palette of bright colours,
// so in a native (1:1) capture every pixel of a real glyph has one exact colour.
// The game's background art behind the transparent chat box changes as the camera
// moves; its bright streaks get decoded as thin glyphs (l, I, ì, ĩ, ') glued to the
// end of a message. trimJunk cuts a line at the first glyph whose pixels are not a
// single colour of the frame's text palette.

const (
	uniformTolerance  = 24  // max channel spread inside one glyph
	minTextBrightness = 170 // dimmest chat colour measured: max channel 0xb2
	paletteMinGlyphs  = 3   // a colour must be used by this many glyphs in the frame
	crispShare        = 0.8 // share of uniform glyphs that marks a capture as native 1:1
)

type glyphColor struct {
	rgb     [3]byte
	uniform bool
	ink     bool
}

func (e *Engine) spanColor(bgra []byte, w, h, top int, sp Span) glyphColor {
	g := e.byText[sp.Text]
	if g == nil {
		return glyphColor{}
	}
	var lo, hi [3]int
	lo = [3]int{255, 255, 255}
	n := 0
	var first [3]byte
	for x := sp.X; x < sp.X+sp.W && x < w; x++ {
		i := x - sp.X
		if i >= len(g.cols) {
			break
		}
		for r := 0; r < e.cellH; r++ {
			y := top + r
			if g.cols[i]>>r&1 == 0 || y < 0 || y >= h {
				continue
			}
			p := (y*w + x) * 4
			if max(bgra[p+1], bgra[p+2]) < inkThreshold {
				continue
			}
			c := [3]byte{bgra[p+2], bgra[p+1], bgra[p]}
			if n == 0 {
				first = c
			}
			n++
			for k := 0; k < 3; k++ {
				lo[k], hi[k] = min(lo[k], int(c[k])), max(hi[k], int(c[k]))
			}
		}
	}
	if n == 0 {
		return glyphColor{}
	}
	spread := max(hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2])
	return glyphColor{rgb: first, uniform: spread <= uniformTolerance, ink: true}
}

func near(a, b [3]byte) bool {
	d := func(x, y byte) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a[0], b[0]) <= uniformTolerance && d(a[1], b[1]) <= uniformTolerance && d(a[2], b[2]) <= uniformTolerance
}

func (e *Engine) trimJunk(bgra []byte, w, h int, lines []Line) []Line {
	colors := make([][]glyphColor, len(lines))
	inked, uniform := 0, 0
	var palette [][3]byte
	var paletteCount []int
	for li, l := range lines {
		colors[li] = make([]glyphColor, len(l.Spans))
		for si, sp := range l.Spans {
			if sp.Text == " " {
				continue
			}
			c := e.spanColor(bgra, w, h, l.Top, sp)
			colors[li][si] = c
			if !c.ink {
				continue
			}
			inked++
			if !c.uniform {
				continue
			}
			uniform++
			found := false
			for pi, p := range palette {
				if near(p, c.rgb) {
					paletteCount[pi]++
					found = true
					break
				}
			}
			if !found {
				palette = append(palette, c.rgb)
				paletteCount = append(paletteCount, 1)
			}
		}
	}
	if inked == 0 || float64(uniform) < crispShare*float64(inked) {
		return lines // scaled/blurred capture: colours are not exact, skip the check
	}
	inPalette := func(c [3]byte) bool {
		if max(c[0], c[1], c[2]) < minTextBrightness {
			return false
		}
		for pi, p := range palette {
			if paletteCount[pi] >= paletteMinGlyphs && near(p, c) {
				return true
			}
		}
		return false
	}
	for li := range lines {
		for si, c := range colors[li] {
			if !c.ink || (c.uniform && inPalette(c.rgb)) {
				continue
			}
			lines[li].Spans = lines[li].Spans[:si]
			break
		}
	}
	return lines
}
