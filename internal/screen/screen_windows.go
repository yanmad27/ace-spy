// Package screen captures screen areas with GDI in physical pixels.
package screen

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/lxn/win"
)

// Grabber captures a fixed screen rectangle, optionally upscaled, into a reusable DIB.
type Grabber struct {
	x, y, w, h int32
	dw, dh     int32
	memDC      win.HDC
	bmp        win.HBITMAP
	oldObj     win.HGDIOBJ
	bits       unsafe.Pointer
}

func NewGrabber(x, y, w, h, scale int) (*Grabber, error) {
	if w <= 0 || h <= 0 || scale < 1 {
		return nil, errors.New("invalid capture area")
	}
	g := &Grabber{x: int32(x), y: int32(y), w: int32(w), h: int32(h), dw: int32(w * scale), dh: int32(h * scale)}

	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer win.ReleaseDC(0, screenDC)

	g.memDC = win.CreateCompatibleDC(screenDC)
	if g.memDC == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	bih := win.BITMAPINFOHEADER{
		BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
		BiWidth:       g.dw,
		BiHeight:      -g.dh,
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: win.BI_RGB,
	}
	g.bmp = win.CreateDIBSection(g.memDC, &bih, win.DIB_RGB_COLORS, &g.bits, 0, 0)
	if g.bmp == 0 || g.bits == nil {
		win.DeleteDC(g.memDC)
		return nil, errors.New("CreateDIBSection failed")
	}
	g.oldObj = win.SelectObject(g.memDC, win.HGDIOBJ(g.bmp))
	win.SetStretchBltMode(g.memDC, win.HALFTONE)
	win.SetBrushOrgEx(g.memDC, 0, 0, nil)
	return g, nil
}

// Grab returns a fresh BGRA buffer (alpha forced opaque) of the scaled size.
func (g *Grabber) Grab() ([]byte, int, int, error) {
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return nil, 0, 0, errors.New("GetDC failed")
	}
	var ok bool
	if g.dw == g.w && g.dh == g.h {
		ok = win.BitBlt(g.memDC, 0, 0, g.w, g.h, screenDC, g.x, g.y, win.SRCCOPY|win.CAPTUREBLT)
	} else {
		ok = win.StretchBlt(g.memDC, 0, 0, g.dw, g.dh, screenDC, g.x, g.y, g.w, g.h, win.SRCCOPY|win.CAPTUREBLT)
	}
	win.ReleaseDC(0, screenDC)
	if !ok {
		return nil, 0, 0, fmt.Errorf("blit failed (error %d)", win.GetLastError())
	}
	n := int(g.dw) * int(g.dh) * 4
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(g.bits), n))
	for i := 3; i < n; i += 4 {
		out[i] = 0xFF
	}
	return out, int(g.dw), int(g.dh), nil
}

func (g *Grabber) Close() {
	if g.memDC != 0 {
		win.SelectObject(g.memDC, g.oldObj)
		win.DeleteObject(win.HGDIOBJ(g.bmp))
		win.DeleteDC(g.memDC)
		g.memDC = 0
	}
}

type Rect struct{ X, Y, W, H int }

func VirtualScreen() Rect {
	return Rect{
		X: int(win.GetSystemMetrics(win.SM_XVIRTUALSCREEN)),
		Y: int(win.GetSystemMetrics(win.SM_YVIRTUALSCREEN)),
		W: int(win.GetSystemMetrics(win.SM_CXVIRTUALSCREEN)),
		H: int(win.GetSystemMetrics(win.SM_CYVIRTUALSCREEN)),
	}
}
