// Command atlasgen renders the glyph atlas for internal/glyphocr with Windows GDI,
// the same way the game draws chat (Verdana, 12px, no anti-aliasing).
//
// Usage: atlasgen > verdana12.json
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
)

//go:embed chars.txt
var charset string

const (
	cellH, top, pad = 20, 3, 4
	fontHeight      = -12
)

type glyph struct {
	Adv      int      `json:"adv"`
	Cols     []uint32 `json:"cols"`
	Overhang bool     `json:"overhang"`
	// Ink right of the advance (e.g. the horn of ư), drawn into the next glyph's cell.
	Over []uint32 `json:"over,omitempty"`
}

type canvas struct {
	dc   win.HDC
	bits unsafe.Pointer
	w    int
}

func newCanvas(w int, font win.HFONT) *canvas {
	screen := win.GetDC(0)
	defer win.ReleaseDC(0, screen)
	dc := win.CreateCompatibleDC(screen)
	bih := win.BITMAPINFOHEADER{BiSize: uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})), BiWidth: int32(w), BiHeight: -cellH, BiPlanes: 1, BiBitCount: 32}
	var bits unsafe.Pointer
	bmp := win.CreateDIBSection(dc, &bih, win.DIB_RGB_COLORS, &bits, 0, 0)
	win.SelectObject(dc, win.HGDIOBJ(bmp))
	win.SelectObject(dc, win.HGDIOBJ(font))
	win.SetBkMode(dc, win.TRANSPARENT)
	win.SetTextColor(dc, win.COLORREF(0xFFFFFF))
	return &canvas{dc: dc, bits: bits, w: w}
}

func (c *canvas) draw(text string) {
	px := unsafe.Slice((*byte)(c.bits), c.w*cellH*4)
	clear(px)
	u, _ := syscall.UTF16FromString(text)
	win.TextOut(c.dc, pad, top, &u[0], int32(len(u)-1))
}

func (c *canvas) on(x, y int) bool {
	px := unsafe.Slice((*byte)(c.bits), c.w*cellH*4)
	return px[(y*c.w+x)*4+1] > 127
}

func advance(dc win.HDC, text string) int {
	u, _ := syscall.UTF16FromString(text)
	var sz win.SIZE
	win.GetTextExtentPoint32(dc, &u[0], int32(len(u)-1), &sz)
	return int(sz.CX)
}

func main() {
	lf := win.LOGFONT{LfHeight: fontHeight, LfWeight: 400, LfCharSet: 1, LfQuality: 3 /* NONANTIALIASED_QUALITY */}
	face, _ := syscall.UTF16FromString("Verdana")
	copy(lf.LfFaceName[:], face)
	font := win.CreateFontIndirect(&lf)
	if font == 0 {
		log.Fatal("CreateFontIndirect failed")
	}
	c := newCanvas(64, font)
	glyphs := map[string]glyph{}
	for _, r := range charset + " " {
		s := string(r)
		adv := advance(c.dc, s)
		c.draw(s)
		g := glyph{Adv: adv}
		for x := pad; x < pad+adv; x++ {
			var v uint32
			for y := 0; y < cellH; y++ {
				if c.on(x, y) {
					v |= 1 << y
				}
			}
			g.Cols = append(g.Cols, v)
		}
		for x := pad + adv; x < pad+adv+4; x++ {
			var v uint32
			for y := 0; y < cellH; y++ {
				if c.on(x, y) {
					v |= 1 << y
				}
			}
			g.Over = append(g.Over, v)
		}
		for len(g.Over) > 0 && g.Over[len(g.Over)-1] == 0 {
			g.Over = g.Over[:len(g.Over)-1]
		}
		for x := 0; x < c.w; x++ {
			if x >= pad && x < pad+adv {
				continue
			}
			for y := 0; y < cellH; y++ {
				if c.on(x, y) {
					g.Overhang = true
				}
			}
		}
		glyphs[s] = g
	}

	// Check that a rendered line is the concatenation of its glyphs.
	tests := []string{"^[15:38] [Sky_Luxury] [BC] tốn cmdt vl", "*[16:59] [Phú tổng tư lãnh] [Audi] 5 slot danh trụ, Ấy Ộ Ữ ợ ỹ đĐ"}
	wide := newCanvas(700, font)
	bad := 0
	for _, t := range tests {
		wide.draw(t)
		x := pad
		for _, r := range t {
			g := glyphs[string(r)]
			for i, v := range g.Cols {
				for y := 0; y < cellH; y++ {
					if (v>>y&1 == 1) != wide.on(x+i, y) {
						bad++
					}
				}
			}
			x += g.Adv
		}
	}
	fmt.Fprintf(os.Stderr, "%d glyphs, concatenation mismatches: %d\n", len(glyphs), bad)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"cellHeight": cellH, "top": top, "glyphs": glyphs})
}
