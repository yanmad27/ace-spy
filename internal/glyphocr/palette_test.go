package glyphocr

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// PALETTE_DIR=dir of native frames: distinct exact colours of real glyphs.
func TestPalette(t *testing.T) {
	dir := os.Getenv("PALETTE_DIR")
	if dir == "" {
		t.Skip()
	}
	e, _ := New()
	tpl := map[string][]uint32{}
	for _, g := range e.glyphs {
		tpl[g.text] = g.cols
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	count := map[string]int{}
	for _, f := range files {
		pix, w, h := loadNative(t, f, 1)
		if w > 1000 { // x2 frames
			continue
		}
		lines, _ := e.RecognizeLines(pix, w, h)
		for _, l := range lines {
			for _, sp := range l.Spans {
				cols := tpl[sp.Text]
				seen := map[[3]byte]int{}
				for x := sp.X; x < sp.X+sp.W && x < w; x++ {
					for r := 0; r < e.cellH; r++ {
						y := l.Top + r
						if x-sp.X >= len(cols) || cols[x-sp.X]>>r&1 == 0 || y < 0 || y >= h {
							continue
						}
						i := (y*w + x) * 4
						if max(pix[i+1], pix[i+2]) < inkThreshold {
							continue
						}
						seen[[3]byte{pix[i+2], pix[i+1], pix[i]}]++
					}
				}
				if len(seen) == 1 {
					for c := range seen {
						count[fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])]++
					}
				}
			}
		}
	}
	type kv struct {
		c string
		n int
	}
	var l []kv
	for c, n := range count {
		l = append(l, kv{c, n})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n })
	for _, x := range l {
		t.Logf("PAL %s %d", x.c, x.n)
	}
}
