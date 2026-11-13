package glyphocr

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yanmad27/ace-spy/internal/chatparse"
)

func lev(a, b []rune) int {
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

// loadNative decodes a capture taken at the given scale and box-downsamples it
// back to native resolution, as BGRA.
func loadNative(t *testing.T, path string, scale int) ([]byte, int, int) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	w, h := b.Dx()/scale, b.Dy()/scale
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					rr, gg, bb, _ := img.At(b.Min.X+x*scale+dx, b.Min.Y+y*scale+dy).RGBA()
					r, g, bl = r+rr>>8, g+gg>>8, bl+bb>>8
				}
			}
			n := uint32(scale * scale)
			i := (y*w + x) * 4
			out[i], out[i+1], out[i+2], out[i+3] = byte(bl/n), byte(g/n), byte(r/n), 255
		}
	}
	_ = image.Rect
	return out, w, h
}

// Set REAL_SAMPLES to a directory of real x2 captures (N.png) with transcriptions (N.txt).
func TestRealSamples(t *testing.T) {
	dir := os.Getenv("REAL_SAMPLES")
	if dir == "" {
		t.Skip("REAL_SAMPLES not set")
	}
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	truths, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	total, exact, charErr, chars := 0, 0, 0, 0
	for _, tf := range truths {
		raw, _ := os.ReadFile(tf)
		want := strings.Split(strings.TrimSpace(string(raw)), "\n")
		pix, w, h := loadNative(t, strings.TrimSuffix(tf, ".txt")+".png", 2)
		start := time.Now()
		lines, err := e.Recognize(pix, w, h)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d lines in %s", filepath.Base(tf), len(lines), time.Since(start).Round(time.Millisecond))
		msgs := chatparse.Messages(lines)
		for _, wl := range want {
			total++
			wl = strings.Join(strings.Fields(wl), " ")
			best, bl := 1<<30, ""
			for _, m := range msgs {
				if d := lev([]rune(m), []rune(wl)); d < best {
					best, bl = d, m
				}
			}
			chars += len([]rune(wl))
			charErr += min(best, len([]rune(wl)))
			if best == 0 {
				exact++
			} else if os.Getenv("VERBOSE") != "" {
				t.Logf("want %q\n         got  %q", wl, bl)
			}
		}
	}
	t.Logf("exact messages %d/%d, character accuracy %.2f%%", exact, total, 100*(1-float64(charErr)/float64(chars)))
}
