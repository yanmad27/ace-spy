package tesseract

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yanmad27/ace-spy/internal/chatparse"
)

func levR(a, b []rune) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

func sim(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	return 1 - float64(levR(ra, rb))/float64(max(len(ra), len(rb), 1))
}

func bestSim(lines []string, want string) float64 {
	best := 0.0
	for _, l := range lines {
		best = max(best, sim(l, want))
	}
	return best
}

func grayOf(pix []byte, w, h int, f func(b, g, r byte) uint8) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for i, j := 0, 0; i < len(pix); i, j = i+4, j+1 {
		g.Pix[j] = f(pix[i], pix[i+1], pix[i+2])
	}
	return g
}

func invDark(g *image.Gray) *image.Gray {
	var s uint64
	for _, v := range g.Pix {
		s += uint64(v)
	}
	if s/uint64(len(g.Pix)) < 128 {
		for i := range g.Pix {
			g.Pix[i] = 255 - g.Pix[i]
		}
	}
	return g
}

func otsuBin(g *image.Gray) *image.Gray {
	var hist [256]int
	for _, v := range g.Pix {
		hist[v]++
	}
	var sum, sumB, wB, best float64
	for i, c := range hist {
		sum += float64(i * c)
	}
	thr := 0
	for t := 0; t < 256; t++ {
		wB += float64(hist[t])
		if wB == 0 {
			continue
		}
		wF := float64(len(g.Pix)) - wB
		if wF == 0 {
			break
		}
		sumB += float64(t * hist[t])
		mB, mF := sumB/wB, (sum-sumB)/wF
		if v := wB * wF * (mB - mF) * (mB - mF); v > best {
			best, thr = v, t
		}
	}
	for i, v := range g.Pix {
		if int(v) > thr {
			g.Pix[i] = 255
		} else {
			g.Pix[i] = 0
		}
	}
	return g
}

func resize(g *image.Gray, f float64) *image.Gray {
	w, h := int(float64(g.Rect.Dx())*f), int(float64(g.Rect.Dy())*f)
	out := image.NewGray(image.Rect(0, 0, w, h))
	sw, sh := g.Rect.Dx(), g.Rect.Dy()
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/f - 0.5
		y0 := max(int(fy), 0)
		y1 := min(y0+1, sh-1)
		dy := max(fy-float64(y0), 0)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/f - 0.5
			x0 := max(int(fx), 0)
			x1 := min(x0+1, sw-1)
			dx := max(fx-float64(x0), 0)
			p := func(xx, yy int) float64 { return float64(g.Pix[yy*sw+xx]) }
			v := p(x0, y0)*(1-dx)*(1-dy) + p(x1, y0)*dx*(1-dy) + p(x0, y1)*(1-dx)*dy + p(x1, y1)*dx*dy
			out.Pix[y*w+x] = uint8(v + 0.5)
		}
	}
	return out
}

// splitRows cuts a black-on-white image into text rows using the horizontal projection.
func splitRows(g *image.Gray) []*image.Gray {
	w, h := g.Rect.Dx(), g.Rect.Dy()
	ink := make([]bool, h)
	for y := 0; y < h; y++ {
		n := 0
		for x := 0; x < w; x++ {
			if g.Pix[y*w+x] < 128 {
				n++
			}
		}
		ink[y] = n > 0
	}
	var rows []*image.Gray
	for y := 0; y < h; {
		if !ink[y] {
			y++
			continue
		}
		y0 := y
		for y < h && ink[y] {
			y++
		}
		if y-y0 < 6 {
			continue
		}
		top, bot := max(y0-4, 0), min(y+4, h)
		r := image.NewGray(image.Rect(0, 0, w, bot-top+8))
		for i := range r.Pix {
			r.Pix[i] = 255
		}
		for yy := top; yy < bot; yy++ {
			copy(r.Pix[(yy-top+4)*w:(yy-top+5)*w], g.Pix[yy*w:(yy+1)*w])
		}
		rows = append(rows, r)
	}
	return rows
}

func tess(t *testing.T, tessdata string, g *image.Gray, psm string) []string {
	if psm == "lines" {
		var out []string
		for _, r := range splitRows(g) {
			out = append(out, tess(t, tessdata, r, "7")...)
		}
		return out
	}
	var buf bytes.Buffer
	png.Encode(&buf, g)
	lang := "vie"
	if i := strings.Index(psm, "|"); i >= 0 {
		psm, lang = psm[:i], psm[i+1:]
	}
	cmd := exec.Command("tesseract", "--tessdata-dir", tessdata, "stdin", "stdout", "-l", lang, "--oem", "1", "--psm", psm)
	cmd.Stdin = &buf
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestRealSamples(t *testing.T) {
	dir, data := os.Getenv("REAL_SAMPLES"), os.Getenv("TESS_TEST_APPDIR")
	if dir == "" || data == "" {
		t.Skip()
	}
	truths, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	lum := func(b, g, r byte) uint8 { return uint8((114*uint32(b) + 587*uint32(g) + 299*uint32(r)) / 1000) }
	maxRGB := func(b, g, r byte) uint8 { return max(b, g, r) }
	maxRG := func(b, g, r byte) uint8 { return max(g, r) }
	type variant struct {
		name string
		f    func(pix []byte, w, h int) *image.Gray
		psm  string
	}
	var vs []variant
	for _, psm := range []string{"6"} {
		for _, sc := range []float64{1, 1.5} {
			for cn, cf := range map[string]func(b, g, r byte) uint8{"lum": lum, "maxRGB": maxRGB, "maxRG": maxRG} {
				for _, bin := range []bool{false, true} {
					cf, sc, bin := cf, sc, bin
					name := fmt.Sprintf("psm%s %s x%.1f bin=%v", psm, cn, sc, bin)
					vs = append(vs, variant{name, func(pix []byte, w, h int) *image.Gray {
						g := grayOf(pix, w, h, cf)
						if sc != 1 {
							g = resize(g, sc)
						}
						if bin {
							otsuBin(g)
						}
						return invDark(g)
					}, psm})
				}
			}
		}
	}
	vs = append(vs, variant{"PRODUCTION Preprocess", Preprocess, "6"})
	vs = append(vs, variant{"prod vie+eng", Preprocess, "6|vie+eng"})
	vs = append(vs, variant{"x2.0 vie+eng", func(pix []byte, w, h int) *image.Gray {
		return invDark(otsuBin(resize(grayOf(pix, w, h, maxRG), 2.0)))
	}, "6|vie+eng"})
	vs = vs[len(vs)-3:]
	for _, sc := range []float64{2.0, 2.5} {
		sc := sc
		vs = append(vs, variant{fmt.Sprintf("maxRG x%.1f bin", sc), func(pix []byte, w, h int) *image.Gray {
			g := resize(grayOf(pix, w, h, maxRG), sc)
			return invDark(otsuBin(g))
		}, "6"})
	}
	vs = append(vs, variant{"prod per-line psm7", Preprocess, "lines"})
	type res struct {
		name                             string
		lineHit, msgOK, exact, exactBook int
		avg                              float64
	}
	var all []res
	total := 0
	for _, v := range vs {
		r := res{name: v.name}
		total = 0
		for _, tf := range truths {
			raw, _ := os.ReadFile(tf)
			want := strings.Split(strings.TrimSpace(string(raw)), "\n")
			pix, w, h := loadBGRA(t, strings.TrimSuffix(tf, ".txt")+".png", 1)
			lines := tess(t, data+"/tessdata", v.f(pix, w, h), v.psm)
			msgs := chatparse.Messages(lines)
			book := chatparse.NewNameBook()
			book.Messages(lines) // a previous frame of the same chat
			corrected := book.Messages(lines)
			for _, wl := range want {
				total++
				s := bestSim(lines, wl)
				r.avg += s
				if s >= 0.85 {
					r.lineHit++
				}
				if bestSim(msgs, wl) >= 0.8 {
					r.msgOK++
				}
				if bestSim(msgs, wl) >= 0.97 {
					r.exact++
				}
				if bestSim(corrected, wl) >= 0.97 {
					r.exactBook++
				}
			}
			if os.Getenv("DUMPX") == v.name {
				for _, wl := range want {
					best, bl := 0.0, ""
					for _, l := range corrected {
						if s := sim(l, wl); s > best {
							best, bl = s, l
						}
					}
					if best < 0.97 {
						t.Logf("X want=%q\n    got =%q", wl, bl)
					}
				}
			}
			if os.Getenv("DUMP") == v.name {
				for _, wl := range want {
					if bestSim(msgs, wl) < 0.8 {
						best, bl := 0.0, ""
						for _, l := range lines {
							if s := sim(l, wl); s > best {
								best, bl = s, l
							}
						}
						t.Logf("MISS want=%q\n        ocr=%q", wl, bl)
					}
				}
			}
		}
		r.avg /= float64(total)
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].msgOK > all[j].msgOK || all[i].msgOK == all[j].msgOK && all[i].avg > all[j].avg
	})
	for _, r := range all {
		t.Logf("%-24s lines≥0.85: %2d/%d  pushed≈truth: %2d  exact: %2d  exact+corrections: %2d  avgSim=%.3f", r.name, r.lineHit, total, r.msgOK, r.exact, r.exactBook, r.avg)
	}
}
