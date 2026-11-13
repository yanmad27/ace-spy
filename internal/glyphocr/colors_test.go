package glyphocr

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type rgb struct{ r, g, b int }

func (c rgb) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

// quantised so anti-aliasing / scaling noise collapses onto the game's palette.
func (c rgb) q() rgb {
	f := func(v int) int { return min(255, (v+16)/32*32) }
	return rgb{f(c.r), f(c.g), f(c.b)}
}

func median(cs []rgb) rgb {
	if len(cs) == 0 {
		return rgb{}
	}
	pick := func(get func(rgb) int) int {
		v := make([]int, len(cs))
		for i, c := range cs {
			v[i] = get(c)
		}
		sort.Ints(v)
		return v[len(v)/2]
	}
	return rgb{pick(func(c rgb) int { return c.r }), pick(func(c rgb) int { return c.g }), pick(func(c rgb) int { return c.b })}
}

// COLOR_FRAMES=dir of x2 captures: reports the ink colour of every line part per channel.
func TestChannelColors(t *testing.T) {
	dir := os.Getenv("COLOR_FRAMES")
	if dir == "" {
		t.Skip()
	}
	e, _ := New()
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	type key struct{ ch, layout, part string }
	stats := map[key]map[string]int{}
	examples := map[string][]string{}
	multi := map[string]int{}
	exact := map[string][]rgb{}
	for _, f := range files {
		fh, _ := os.Open(f)
		full, err := png.Decode(fh)
		fh.Close()
		if err != nil {
			continue
		}
		pix, w, h := loadNative(t, f, 2)
		lines, _ := e.RecognizeLines(pix, w, h)
		for _, l := range lines {
			text := l.Text()
			if text == "" {
				continue
			}
			// colour of each glyph: median of its ink pixels, read from the full-res frame.
			type gc struct {
				s   string
				col rgb
				ink bool
			}
			var gl []gc
			for _, sp := range l.Spans {
				var cs []rgb
				for x := sp.X; x < sp.X+sp.W; x++ {
					for r := 0; r < e.cellH; r++ {
						y := l.Top + r
						if y < 0 || y >= h || x >= w {
							continue
						}
						i := (y*w + x) * 4
						if max(pix[i+1], pix[i+2]) < inkThreshold {
							continue
						}
						R, G, B, _ := full.At(2*x, 2*y).RGBA()
						cs = append(cs, rgb{int(R >> 8), int(G >> 8), int(B >> 8)})
					}
				}
				gl = append(gl, gc{sp.Text, median(cs), len(cs) > 0})
			}
			// split into parts
			ch := ""
			i := 0
			for i < len(gl) && gl[i].s == " " {
				i++
			}
			if i < len(gl) && strings.Contains("!@#$%^*~", gl[i].s) {
				ch = gl[i].s
				i++
			}
			var parts []string
			var partCols [][]rgb
			cur := ""
			var curCols []rgb
			depth, groups := 0, 0
			inMsg := false
			flush := func(name string) {
				if cur != "" {
					parts = append(parts, name+"\t"+cur)
					partCols = append(partCols, curCols)
				}
				cur, curCols = "", nil
			}
			for ; i < len(gl); i++ {
				g := gl[i]
				if !inMsg {
					if g.s == "[" && depth == 0 {
						depth = 1
						cur, curCols = "[", nil
						if g.ink {
							curCols = append(curCols, g.col)
						}
						continue
					}
					if depth == 1 {
						cur += g.s
						if g.ink {
							curCols = append(curCols, g.col)
						}
						if g.s == "]" {
							depth = 0
							groups++
							flush(fmt.Sprintf("g%d", groups))
						}
						continue
					}
					if g.s == " " {
						continue
					}
					inMsg = true
				}
				cur += g.s
				if g.ink && g.s != " " {
					curCols = append(curCols, g.col)
				}
			}
			flush("msg")
			if groups == 0 {
				continue
			}
			layout := fmt.Sprintf("%dgrp", groups)
			for pi, p := range parts {
				name := strings.SplitN(p, "\t", 2)[0]
				cols := partCols[pi]
				if len(cols) == 0 {
					continue
				}
				// dominant quantised colour of the part, and whether the part mixes colours
				cnt := map[rgb]int{}
				for _, c := range cols {
					cnt[c.q()]++
				}
				var dom rgb
				for c, n := range cnt {
					if n > cnt[dom] {
						dom = c
					}
				}
				if name == "msg" && cnt[dom]*10 < len(cols)*8 {
					multi[ch]++
				}
				if name == "g1" {
					exact[ch] = append(exact[ch], median(cols))
				}
				k := key{ch, layout, name}
				if stats[k] == nil {
					stats[k] = map[string]int{}
				}
				stats[k][median(cols).q().hex()]++
			}
			ek := ch + " " + layout
			if len(examples[ek]) < 2 {
				examples[ek] = append(examples[ek], text)
			}
		}
	}
	var keys []key
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.ch != b.ch {
			return a.ch < b.ch
		}
		if a.layout != b.layout {
			return a.layout < b.layout
		}
		return a.part < b.part
	})
	for _, k := range keys {
		type kv struct {
			c string
			n int
		}
		var l []kv
		total := 0
		for c, n := range stats[k] {
			l = append(l, kv{c, n})
			total += n
		}
		sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n })
		var s []string
		for _, x := range l[:min(3, len(l))] {
			s = append(s, fmt.Sprintf("%s %d%%", x.c, x.n*100/total))
		}
		ch := k.ch
		if ch == "" {
			ch = "(none)"
		}
		t.Logf("COL\t%s\t%s\t%s\tn=%d\t%s", ch, k.layout, k.part, total, strings.Join(s, ", "))
	}
	for k, v := range examples {
		for _, x := range v {
			t.Logf("EX\t%s\t%s", k, x)
		}
	}
	for k, v := range exact {
		t.Logf("EXACT\t%q\t%s\tn=%d", k, median(v).hex(), len(v))
	}
	for k, v := range multi {
		t.Logf("MULTI\t%q\t%d messages with mixed colours", k, v)
	}
}
