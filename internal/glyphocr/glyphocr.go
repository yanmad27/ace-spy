// Package glyphocr reads Ace Online chat by template matching. The game draws chat
// with Verdana 12px without anti-aliasing, so every character is an exact bitmap;
// a line is decoded by dynamic programming over its pixel columns, choosing the
// glyph sequence whose bitmaps best explain the text pixels.
package glyphocr

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/bits"
	"sort"
	"strings"
	"sync"
)

//go:embed verdana12.json
var atlasJSON []byte

const (
	// Text pixels: chat colours always have a bright red or green channel, the
	// game's chat background is dark blue.
	inkThreshold = 110
	linePitch    = 15
	// Costs are mismatched pixels x costScale minus matched ink pixels, so equal
	// mismatches prefer the glyph that explains more ink (ợ over ơ when the dot is there).
	costScale    = 64
	skipPenalty  = 3 * costScale
	widthPenalty = 1 * costScale
	// A run of this many blank glyph widths ends the message: anything further right
	// is not chat (e.g. the game's mouse cursor drawn over the chat box).
	maxGapSpaces = 4
	cacheSize    = 4096
	// A row inked across this share of the width is UI chrome (the chat box's border),
	// not text: it hides whatever text pixels lie under it.
	borderShare = 0.75
)

type glyph struct {
	text    string
	adv     int
	cols    []uint32
	over    []uint32 // ink drawn right of the advance, into the next glyph's cell
	care    uint32   // rows that matter for this glyph (its ink rows ±1)
	outside uint32   // rows of this line outside care: any ink there is unexplained
	ink     int
}

type cached struct {
	spans   []Span
	unknown []Span
	cost    int
}

// Span is one decoded glyph and the pixel columns it occupies in the capture.
type Span struct {
	Text string
	X, W int
}

// Line is one decoded chat line: its cell top row and glyphs, left to right.
type Line struct {
	Top   int
	Spans []Span
	// Visible has bit r set when cell row r is neither outside the image nor under
	// UI chrome; 0 means all rows are visible.
	Visible uint32
	// Unknown are runs of inked columns no glyph explains (Text is empty).
	Unknown []Span
}

// Text joins the line's glyphs, dropping anything after a wide blank gap (not chat,
// e.g. the mouse cursor) and collapsing runs of spaces.
func (l Line) Text() string {
	var sb strings.Builder
	for _, sp := range l.Spans {
		sb.WriteString(sp.Text)
	}
	text := strings.TrimLeft(sb.String(), " ")
	if i := strings.Index(text, strings.Repeat(" ", maxGapSpaces)); i >= 0 {
		text = text[:i]
	}
	return strings.Join(strings.Fields(text), " ")
}

type Engine struct {
	gmu    sync.RWMutex // guards glyphs and byText against Learn
	glyphs []glyph
	cellH  int
	top    int

	byText  map[string]*glyph
	profile []int  // ink per cell row, summed over letters and digits
	own     uint32 // cell rows that belong to the line itself

	mu    sync.Mutex
	cache map[uint64]cached
	order []uint64
}

func New() (*Engine, error) {
	var atlas struct {
		CellHeight int `json:"cellHeight"`
		Top        int `json:"top"`
		Glyphs     map[string]struct {
			Adv  int      `json:"adv"`
			Cols []uint32 `json:"cols"`
			Over []uint32 `json:"over"`
		} `json:"glyphs"`
	}
	if err := json.Unmarshal(atlasJSON, &atlas); err != nil {
		return nil, err
	}
	if atlas.CellHeight <= 0 || atlas.CellHeight > 32 {
		return nil, errors.New("invalid glyph atlas")
	}
	e := &Engine{cellH: atlas.CellHeight, top: atlas.Top, cache: map[uint64]cached{}}
	// Rows that belong to this line only; outside them the neighbouring lines'
	// descenders and diacritics (15px pitch, 20px cell) overlap.
	var own uint32
	for r := atlas.Top + 1; r < atlas.Top+13 && r < atlas.CellHeight; r++ {
		own |= 1 << r
	}
	e.own = own
	for text, g := range atlas.Glyphs {
		e.glyphs = append(e.glyphs, e.makeGlyph(text, g.Adv, g.Cols, g.Over))
	}
	e.index()
	e.profile = make([]int, atlas.CellHeight)
	for _, g := range e.glyphs {
		if len(g.text) != 1 || !(g.text[0] >= 'a' && g.text[0] <= 'z' || g.text[0] >= '0' && g.text[0] <= '9' || g.text == "[" || g.text == "]") {
			continue
		}
		for _, c := range g.cols {
			for r := 0; r < atlas.CellHeight; r++ {
				e.profile[r] += int(c >> r & 1)
			}
		}
	}
	return e, nil
}

func (e *Engine) makeGlyph(text string, adv int, cols, over []uint32) glyph {
	var all uint32
	ink := 0
	for _, c := range cols {
		all |= c
		ink += bits.OnesCount32(c)
	}
	care := e.own // blank glyphs: only this line's rows, not neighbours' overlap
	if all != 0 {
		lo, hi := bits.TrailingZeros32(all), 31-bits.LeadingZeros32(all)
		care = 0
		for y := max(lo-1, 0); y <= min(hi+1, e.cellH-1); y++ {
			care |= 1 << y
		}
	}
	return glyph{text: text, adv: adv, cols: cols, over: over, care: care, outside: e.own &^ care, ink: ink}
}

func (e *Engine) index() {
	sort.SliceStable(e.glyphs, func(i, j int) bool { return e.glyphs[i].text < e.glyphs[j].text })
	e.byText = map[string]*glyph{}
	for i := range e.glyphs {
		if _, dup := e.byText[e.glyphs[i].text]; !dup {
			e.byText[e.glyphs[i].text] = &e.glyphs[i]
		}
	}
}

// Learned is a glyph taught at run time: a label for a pixel pattern the atlas lacks.
type Learned struct {
	Text string   `json:"text"`
	Cols []uint32 `json:"cols"`
}

// Learn adds glyphs to the atlas; a bitmap already known is ignored. It reports how
// many were added.
func (e *Engine) Learn(gs []Learned) int {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	known := map[string]bool{}
	for _, g := range e.glyphs {
		known[fmt.Sprint(g.cols)] = true
	}
	added := 0
	for _, g := range gs {
		if g.Text == "" || len(g.Cols) == 0 || len(g.Cols) > 16 || known[fmt.Sprint(g.Cols)] {
			continue
		}
		known[fmt.Sprint(g.Cols)] = true
		e.glyphs = append(e.glyphs, e.makeGlyph(g.Text, len(g.Cols), g.Cols, nil))
		added++
	}
	if added > 0 {
		e.index()
		e.mu.Lock()
		e.cache, e.order = map[uint64]cached{}, nil
		e.mu.Unlock()
	}
	return added
}

func (e *Engine) Close() {}

// Recognize reads chat lines from a native-resolution (scale 1) BGRA capture.
func (e *Engine) Recognize(bgra []byte, w, h int) ([]string, error) {
	lines, err := e.RecognizeLines(bgra, w, h)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range lines {
		if t := l.Text(); t != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// RecognizeLines is Recognize with glyph positions, for colour analysis.
func (e *Engine) RecognizeLines(bgra []byte, w, h int) ([]Line, error) {
	if w <= 0 || h <= 0 || len(bgra) != w*h*4 {
		return nil, errors.New("invalid image buffer")
	}
	ink := make([]bool, w*h)
	rowInk := make([]int, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			if max(bgra[i+1], bgra[i+2]) >= inkThreshold {
				ink[y*w+x] = true
				rowInk[y]++
			}
		}
	}
	hidden := make([]bool, h)
	for y := 0; y < h; y++ {
		if float64(rowInk[y]) >= borderShare*float64(w) {
			hidden[y] = true
			rowInk[y] = 0
			clear(ink[y*w : (y+1)*w])
		}
	}
	var lines []Line
	for _, top := range e.lineTops(rowInk) {
		// Skip lines cut by the capture edge; they are read whole once chat scrolls.
		if top+e.top-1 < 0 || top+e.top+15 > h {
			continue
		}
		if l, ok := e.readLine(ink, hidden, w, h, top); ok {
			lines = append(lines, l)
		}
	}
	return e.trimJunk(bgra, w, h, lines), nil
}

// lineTops finds the cell top of every text line. Lines sit on a fixed 15px pitch;
// the phase is chosen by matching the image's per-row ink against the per-row ink
// profile of the glyph atlas.
func (e *Engine) lineTops(rowInk []int) []int {
	h := len(rowInk)
	bestPhase, bestScore := 0, -1
	for p := -linePitch; p < linePitch; p++ {
		score := 0
		for y := p; y < h; y += linePitch {
			for r, wgt := range e.profile {
				if yy := y + r; yy >= 0 && yy < h {
					score += wgt * rowInk[yy]
				}
			}
		}
		if score > bestScore {
			bestPhase, bestScore = p, score
		}
	}
	var tops []int
	for y := bestPhase; y < h; y += linePitch {
		ink := 0
		for r := e.top + 4; r < e.top+13; r++ {
			if yy := y + r; yy >= 0 && yy < h {
				ink += rowInk[yy]
			}
		}
		if ink > 0 {
			tops = append(tops, y)
		}
	}
	return tops
}

func (e *Engine) column(ink []bool, w, h, x, top int) uint32 {
	var v uint32
	for r := 0; r < e.cellH; r++ {
		if y := top + r; y >= 0 && y < h && ink[y*w+x] {
			v |= 1 << r
		}
	}
	return v
}

// visibleRows marks the cell rows at top that are inside the image and not under a
// border. Glyph pixels in other rows are unknown, so they never count as mismatches.
func (e *Engine) visibleRows(hidden []bool, top int) uint32 {
	var v uint32
	for r := 0; r < e.cellH; r++ {
		if y := top + r; y >= 0 && y < len(hidden) && !hidden[y] {
			v |= 1 << r
		}
	}
	return v
}

func (e *Engine) readLine(ink, hidden []bool, w, h, top int) (Line, bool) {
	var bestLine Line
	bestCost, found := 0.0, false
	for dy := -1; dy <= 1; dy++ {
		visible := e.visibleRows(hidden, top+dy)
		cols := make([]uint32, w)
		last := -1
		for x := 0; x < w; x++ {
			cols[x] = e.column(ink, w, h, x, top+dy)
			if cols[x]&e.own != 0 {
				last = x
			}
		}
		if last < 0 {
			continue
		}
		cols = cols[:min(last+10, w)] // room for a trailing glyph's full advance
		spans, unknown, cost := e.decodeCached(cols, visible)
		norm := float64(cost) / float64(len(cols))
		if !found || norm < bestCost {
			bestLine, bestCost, found = Line{Top: top + dy, Spans: spans, Visible: visible, Unknown: unknown}, norm, true
		}
	}
	return bestLine, found
}

// coreMask covers the rows between cap height and baseline of the cell.
func (e *Engine) coreMask() uint32 {
	var m uint32
	for r := e.top + 4; r < e.top+13 && r < e.cellH; r++ {
		m |= 1 << r
	}
	return m
}

// poorMatch: the glyph was the best fit but leaves a third of the ink wrong, as when
// a character missing from the atlas is forced onto the nearest glyph.
func (e *Engine) poorMatch(g *glyph, cols []uint32, visible uint32) bool {
	wrong, ink := 0, 0
	for i, v := range cols {
		var t uint32
		if i < len(g.cols) {
			t = g.cols[i]
		}
		wrong += bits.OnesCount32((v^t)&g.care&visible) + bits.OnesCount32(v&g.outside&visible)
		ink += bits.OnesCount32(v & visible)
	}
	return wrong >= 3 && 3*wrong >= max(ink, g.ink)
}

func (e *Engine) decodeCached(cols []uint32, visible uint32) ([]Span, []Span, int) {
	h := fnv.New64a()
	b := make([]byte, 4)
	b[0], b[1], b[2], b[3] = byte(visible), byte(visible>>8), byte(visible>>16), byte(visible>>24)
	h.Write(b)
	for _, c := range cols {
		b[0], b[1], b[2], b[3] = byte(c), byte(c>>8), byte(c>>16), byte(c>>24)
		h.Write(b)
	}
	key := h.Sum64()
	e.mu.Lock()
	if c, ok := e.cache[key]; ok {
		e.mu.Unlock()
		return c.spans, c.unknown, c.cost
	}
	e.mu.Unlock()
	spans, unknown, cost := e.decode(cols, visible)
	e.mu.Lock()
	if len(e.order) >= cacheSize {
		delete(e.cache, e.order[0])
		e.order = e.order[1:]
	}
	e.cache[key] = cached{spans, unknown, cost}
	e.order = append(e.order, key)
	e.mu.Unlock()
	return spans, unknown, cost
}

// decode finds the minimum-cost segmentation of cols into glyphs (each may be one
// pixel narrower or wider than its nominal advance) and unexplained skipped columns.
// Only the visible rows of each glyph are compared. Runs of skipped columns that hold
// ink in the line's own rows are returned as unknown.
func (e *Engine) decode(cols []uint32, visible uint32) ([]Span, []Span, int) {
	e.gmu.RLock()
	defer e.gmu.RUnlock()
	n := len(cols)
	const inf = 1 << 30
	best := make([]int, n+1)
	from := make([]int, n+1)
	pick := make([]int, n+1) // glyph index, -1 = skipped column
	for i := 1; i <= n; i++ {
		best[i] = inf
	}
	core := e.coreMask()
	// A glyph whose ink is all hidden would match anything for free.
	seen := make([]bool, len(e.glyphs))
	for gi := range e.glyphs {
		for _, c := range e.glyphs[gi].cols {
			if c&visible != 0 {
				seen[gi] = true
				break
			}
		}
	}
	img := make([]uint32, 0, 8)
	for x := 0; x < n; x++ {
		if best[x] == inf {
			continue
		}
		// Remove ink the previous glyph on the best path drew into this cell
		// (e.g. the tilde of ĩ), so it is not mistaken for another glyph.
		img = img[:0]
		var prevOver []uint32
		if x > 0 && pick[x] >= 0 {
			prevOver = e.glyphs[pick[x]].over
		}
		for i := 0; i < 12 && x+i < n; i++ {
			c := cols[x+i]
			if i < len(prevOver) {
				c &^= prevOver[i]
			}
			img = append(img, c)
		}
		at := func(i int) uint32 {
			if i < len(img) {
				return img[i]
			}
			return cols[x+i]
		}
		if c := best[x] + costScale*bits.OnesCount32(at(0)&core) + skipPenalty; c < best[x+1] {
			best[x+1], from[x+1], pick[x+1] = c, x, -1
		}
		for gi := range e.glyphs {
			g := &e.glyphs[gi]
			if !seen[gi] && g.ink > 0 {
				continue
			}
			for dw := -1; dw <= 1; dw++ {
				width := g.adv + dw
				if width <= 0 || x+width > n {
					continue
				}
				cost := 0
				if dw != 0 {
					cost = widthPenalty
				}
				for i := 0; i < width && cost < best[x+width]-best[x]+costScale; i++ {
					var t uint32
					if i < len(g.cols) {
						t = g.cols[i]
					}
					v := at(i)
					cost += costScale*(bits.OnesCount32((v^t)&g.care&visible)+bits.OnesCount32(v&g.outside&visible)) - bits.OnesCount32(v&t)
				}
				for j, ov := range g.over {
					if x+width+j < n {
						cost += costScale * bits.OnesCount32(ov&visible&^cols[x+width+j]) // missing horn etc.
					}
				}
				if c := best[x] + cost; c < best[x+width] {
					best[x+width], from[x+width], pick[x+width] = c, x, gi
				}
			}
		}
	}
	var spans, unknown []Span
	for i := n; i > 0; i = from[i] {
		switch {
		case pick[i] >= 0:
			g := &e.glyphs[pick[i]]
			spans = append(spans, Span{Text: g.text, X: from[i], W: i - from[i]})
			if g.ink > 0 && e.poorMatch(g, cols[from[i]:i], visible) {
				unknown = append(unknown, Span{X: from[i], W: i - from[i]})
			}
		case cols[from[i]]&e.own&visible != 0:
			if k := len(unknown) - 1; k >= 0 && unknown[k].X == i {
				unknown[k].X, unknown[k].W = from[i], unknown[k].W+1
			} else {
				unknown = append(unknown, Span{X: from[i], W: 1})
			}
		}
	}
	for i, j := 0, len(spans)-1; i < j; i, j = i+1, j-1 {
		spans[i], spans[j] = spans[j], spans[i]
	}
	for i, j := 0, len(unknown)-1; i < j; i, j = i+1, j-1 {
		unknown[i], unknown[j] = unknown[j], unknown[i]
	}
	return spans, unknown, best[n]
}
