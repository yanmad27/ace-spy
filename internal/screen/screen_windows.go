// Package screen captures screen areas with GDI in physical pixels.
package screen

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procPrintWindow = user32.NewProc("PrintWindow")
	procIsWindow    = user32.NewProc("IsWindow")
)

func isWindow(h win.HWND) bool {
	r, _, _ := procIsWindow.Call(uintptr(h))
	return r != 0
}

const (
	pwClientOnly        = 0x1
	pwRenderFullContent = 0x2
)

// dib is a top-down 32bpp DIB selected into its own memory DC.
type dib struct {
	dc   win.HDC
	bmp  win.HBITMAP
	old  win.HGDIOBJ
	bits unsafe.Pointer
	w, h int32
}

func newDIB(w, h int32) (*dib, error) {
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer win.ReleaseDC(0, screenDC)
	d := &dib{w: w, h: h, dc: win.CreateCompatibleDC(screenDC)}
	if d.dc == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	bih := win.BITMAPINFOHEADER{
		BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
		BiWidth:       w,
		BiHeight:      -h,
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: win.BI_RGB,
	}
	d.bmp = win.CreateDIBSection(d.dc, &bih, win.DIB_RGB_COLORS, &d.bits, 0, 0)
	if d.bmp == 0 || d.bits == nil {
		win.DeleteDC(d.dc)
		return nil, errors.New("CreateDIBSection failed")
	}
	d.old = win.SelectObject(d.dc, win.HGDIOBJ(d.bmp))
	win.SetStretchBltMode(d.dc, win.HALFTONE)
	win.SetBrushOrgEx(d.dc, 0, 0, nil)
	return d, nil
}

func (d *dib) pixels() []byte { return unsafe.Slice((*byte)(d.bits), int(d.w)*int(d.h)*4) }

// copyOpaque returns a copy of the pixels with alpha forced to 255.
func (d *dib) copyOpaque() []byte {
	out := append([]byte(nil), d.pixels()...)
	for i := 3; i < len(out); i += 4 {
		out[i] = 0xFF
	}
	return out
}

func (d *dib) free() {
	if d != nil && d.dc != 0 {
		win.SelectObject(d.dc, d.old)
		win.DeleteObject(win.HGDIOBJ(d.bmp))
		win.DeleteDC(d.dc)
		d.dc = 0
	}
}

// blit copies src(x,y,w,h) into the whole of dst, scaling if sizes differ.
func blit(dst *dib, src win.HDC, x, y, w, h int32) bool {
	if dst.w == w && dst.h == h {
		return win.BitBlt(dst.dc, 0, 0, w, h, src, x, y, win.SRCCOPY|win.CAPTUREBLT)
	}
	return win.StretchBlt(dst.dc, 0, 0, dst.w, dst.h, src, x, y, w, h, win.SRCCOPY|win.CAPTUREBLT)
}

// Grabber captures a fixed screen rectangle, optionally upscaled.
type Grabber struct {
	x, y, w, h int32
	out        *dib
}

func NewGrabber(x, y, w, h, scale int) (*Grabber, error) {
	if w <= 0 || h <= 0 || scale < 1 {
		return nil, errors.New("invalid capture area")
	}
	out, err := newDIB(int32(w*scale), int32(h*scale))
	if err != nil {
		return nil, err
	}
	return &Grabber{x: int32(x), y: int32(y), w: int32(w), h: int32(h), out: out}, nil
}

func (g *Grabber) Grab() ([]byte, int, int, error) {
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return nil, 0, 0, errors.New("GetDC failed")
	}
	ok := blit(g.out, screenDC, g.x, g.y, g.w, g.h)
	win.ReleaseDC(0, screenDC)
	if !ok {
		return nil, 0, 0, fmt.Errorf("blit failed (error %d)", win.GetLastError())
	}
	return g.out.copyOpaque(), int(g.out.w), int(g.out.h), nil
}

func (g *Grabber) Close() { g.out.free() }

// WindowGrabber captures a rectangle relative to a window's client area using
// PrintWindow, so the game window may be covered by other windows or moved.
// It falls back to a screen copy when PrintWindow yields a black image.
type WindowGrabber struct {
	resolve    func() (win.HWND, error)
	hwnd       win.HWND
	x, y, w, h int32
	src        *dib
	out        *dib
}

func NewWindowGrabber(resolve func() (win.HWND, error), x, y, w, h, scale int) (*WindowGrabber, error) {
	if w <= 0 || h <= 0 || scale < 1 {
		return nil, errors.New("invalid capture area")
	}
	out, err := newDIB(int32(w*scale), int32(h*scale))
	if err != nil {
		return nil, err
	}
	return &WindowGrabber{resolve: resolve, x: int32(x), y: int32(y), w: int32(w), h: int32(h), out: out}, nil
}

func (g *WindowGrabber) Grab() ([]byte, int, int, error) {
	if g.hwnd == 0 || !isWindow(g.hwnd) {
		h, err := g.resolve()
		if err != nil {
			return nil, 0, 0, err
		}
		g.hwnd = h
	}
	if win.IsIconic(g.hwnd) {
		return nil, 0, 0, errors.New("game window is minimized")
	}
	var rc win.RECT
	if !win.GetClientRect(g.hwnd, &rc) {
		g.hwnd = 0
		return nil, 0, 0, errors.New("game window closed")
	}
	cw, ch := rc.Right-rc.Left, rc.Bottom-rc.Top
	if g.x+g.w > cw || g.y+g.h > ch {
		return nil, 0, 0, fmt.Errorf("area is outside the game window (%dx%d); re-pick the area", cw, ch)
	}
	if g.src == nil || g.src.w != cw || g.src.h != ch {
		g.src.free()
		src, err := newDIB(cw, ch)
		if err != nil {
			return nil, 0, 0, err
		}
		g.src = src
	}

	r, _, _ := procPrintWindow.Call(uintptr(g.hwnd), uintptr(g.src.dc), pwClientOnly|pwRenderFullContent)
	if r == 0 || g.regionBlack() {
		if err := g.copyFromScreen(); err != nil {
			return nil, 0, 0, err
		}
	}
	if !blit(g.out, g.src.dc, g.x, g.y, g.w, g.h) {
		return nil, 0, 0, fmt.Errorf("blit failed (error %d)", win.GetLastError())
	}
	return g.out.copyOpaque(), int(g.out.w), int(g.out.h), nil
}

func (g *WindowGrabber) regionBlack() bool {
	px := g.src.pixels()
	stride := int(g.src.w) * 4
	for y := int(g.y); y < int(g.y+g.h); y += 2 {
		row := px[y*stride+int(g.x)*4 : y*stride+int(g.x+g.w)*4]
		for i := 0; i < len(row); i += 4 {
			if row[i]|row[i+1]|row[i+2] != 0 {
				return false
			}
		}
	}
	return true
}

func (g *WindowGrabber) copyFromScreen() error {
	pt := win.POINT{}
	if !win.ClientToScreen(g.hwnd, &pt) {
		return errors.New("ClientToScreen failed")
	}
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return errors.New("GetDC failed")
	}
	defer win.ReleaseDC(0, screenDC)
	if !win.BitBlt(g.src.dc, g.x, g.y, g.w, g.h, screenDC, pt.X+g.x, pt.Y+g.y, win.SRCCOPY|win.CAPTUREBLT) {
		return fmt.Errorf("screen copy failed (error %d)", win.GetLastError())
	}
	return nil
}

func (g *WindowGrabber) Close() {
	g.src.free()
	g.out.free()
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
