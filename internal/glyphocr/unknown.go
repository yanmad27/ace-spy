package glyphocr

import (
	"fmt"
	"hash/fnv"
	"math/bits"
	"strings"
)

const (
	maxUnknownWidth = 14
	minUnknownInk   = 3
	// UnknownMark stands for the unknown glyph in Unknown.Context.
	UnknownMark = "□"
)

// Unknown is an inked pattern on a chat line that no atlas glyph explains, drawn in
// the same colour as the line's text (so it is text, not background art).
type Unknown struct {
	Hash    string   `json:"hash"`
	Cols    []uint32 `json:"cols"` // cell-relative bitmap, one word per pixel column
	Color   string   `json:"color"`
	Context string   `json:"context"` // the line's text with UnknownMark where it sits
}

// RecognizeAll is RecognizeColored plus the unknown glyphs found on the lines.
func (e *Engine) RecognizeAll(bgra []byte, w, h int) ([]string, [][]string, []Unknown, error) {
	lines, err := e.RecognizeLines(bgra, w, h)
	if err != nil {
		return nil, nil, nil, err
	}
	var texts []string
	var colors [][]string
	var unknown []Unknown
	for _, l := range lines {
		text := l.Text()
		if text == "" {
			continue
		}
		toks := e.tokenColors(bgra, w, h, l)
		if len(toks) != len(strings.Fields(text)) {
			toks = nil
		}
		texts = append(texts, text)
		colors = append(colors, toks)
		unknown = append(unknown, e.unknowns(bgra, w, h, l)...)
	}
	return texts, colors, unknown, nil
}

func (e *Engine) unknowns(bgra []byte, w, h int, l Line) []Unknown {
	if len(l.Unknown) == 0 {
		return nil
	}
	var lineColors [][3]byte
	for _, sp := range l.Spans {
		if sp.Text == " " {
			continue
		}
		if c := e.spanColor(bgra, w, h, l, sp); c.ink && c.uniform {
			lineColors = append(lineColors, c.rgb)
		}
	}
	// Rows a glyph of this line may use: above the cap for diacritics, below the
	// baseline for descenders, but not the neighbouring lines' rows.
	var rows uint32
	for r := max(e.top-1, 0); r <= min(e.top+14, e.cellH-1); r++ {
		rows |= 1 << r
	}
	if l.Visible != 0 {
		rows &= l.Visible
	}
	var out []Unknown
	for _, u := range l.Unknown {
		cols := make([]uint32, 0, u.W)
		var first [3]byte
		n, uniform := 0, true
		for x := u.X; x < u.X+u.W && x < w; x++ {
			var c uint32
			for r := 0; r < e.cellH; r++ {
				y := l.Top + r
				if rows>>r&1 == 0 || y < 0 || y >= h {
					continue
				}
				p := (y*w + x) * 4
				if max(bgra[p+1], bgra[p+2]) < inkThreshold {
					continue
				}
				c |= 1 << r
				px := [3]byte{bgra[p+2], bgra[p+1], bgra[p]}
				if n == 0 {
					first = px
				} else if !near(first, px) {
					uniform = false
				}
				n++
			}
			cols = append(cols, c)
		}
		for len(cols) > 0 && cols[0] == 0 {
			cols = cols[1:]
		}
		for len(cols) > 0 && cols[len(cols)-1] == 0 {
			cols = cols[:len(cols)-1]
		}
		ink := 0
		for _, c := range cols {
			ink += bits.OnesCount32(c)
		}
		if !uniform || ink < minUnknownInk || len(cols) == 0 || len(cols) > maxUnknownWidth ||
			max(first[0], first[1], first[2]) < minTextBrightness || !nearAny(first, lineColors) {
			continue
		}
		cols = append(cols, 0) // inter-glyph gap
		hs := fnv.New64a()
		fmt.Fprint(hs, cols)
		out = append(out, Unknown{
			Hash:    fmt.Sprintf("%016x", hs.Sum64()),
			Cols:    cols,
			Color:   fmt.Sprintf("%02x%02x%02x", first[0], first[1], first[2]),
			Context: context(l, u),
		})
	}
	return out
}

func nearAny(c [3]byte, list [][3]byte) bool {
	for _, o := range list {
		if near(c, o) {
			return true
		}
	}
	return false
}

// context is the line's text with the glyphs inside u replaced by UnknownMark.
func context(l Line, u Span) string {
	var sb strings.Builder
	marked := false
	for _, sp := range l.Spans {
		inside := sp.X >= u.X && sp.X < u.X+u.W
		if !marked && (inside || sp.X >= u.X+u.W) {
			sb.WriteString(UnknownMark)
			marked = true
		}
		if !inside {
			sb.WriteString(sp.Text)
		}
	}
	if !marked {
		sb.WriteString(UnknownMark)
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}
