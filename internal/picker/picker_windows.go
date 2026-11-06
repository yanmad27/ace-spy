// Package picker shows a frozen full-desktop screenshot and lets the user drag a rectangle.
// Must be called on a GUI thread (e.g. the walk UI thread); it runs a nested message loop.
package picker

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lxn/win"

	"github.com/yanmad27/ace-spy/internal/screen"
)

var ErrCanceled = errors.New("selection canceled")

const className = "AceSpyRegionPicker"

type state struct {
	hwnd      win.HWND
	vs        screen.Rect
	shotDC    win.HDC
	shotBmp   win.HBITMAP
	shotOld   win.HGDIOBJ
	backDC    win.HDC
	backBmp   win.HBITMAP
	backOld   win.HGDIOBJ
	pen       win.HPEN
	dragging  bool
	x0, y0    int32
	x1, y1    int32
	done      bool
	confirmed bool
}

var (
	registerOnce sync.Once
	registerErr  error
	active       *state
)

// Pick returns the selected rectangle in virtual-screen (physical pixel) coordinates.
func Pick() (screen.Rect, error) {
	if active != nil {
		return screen.Rect{}, errors.New("picker already open")
	}
	registerOnce.Do(register)
	if registerErr != nil {
		return screen.Rect{}, registerErr
	}

	s := &state{vs: screen.VirtualScreen()}
	if err := s.snapshot(); err != nil {
		return screen.Rect{}, err
	}
	defer s.free()

	cls, _ := syscall.UTF16PtrFromString(className)
	title, _ := syscall.UTF16PtrFromString("Drag to select a chat area — Esc to cancel")
	active = s
	defer func() { active = nil }()

	s.hwnd = win.CreateWindowEx(
		win.WS_EX_TOPMOST|win.WS_EX_TOOLWINDOW,
		cls, title, win.WS_POPUP,
		int32(s.vs.X), int32(s.vs.Y), int32(s.vs.W), int32(s.vs.H),
		0, 0, win.GetModuleHandle(nil), nil)
	if s.hwnd == 0 {
		return screen.Rect{}, fmt.Errorf("CreateWindowEx failed (error %d)", win.GetLastError())
	}
	win.ShowWindow(s.hwnd, win.SW_SHOW)
	win.SetForegroundWindow(s.hwnd)

	var msg win.MSG
	for !s.done {
		r := win.GetMessage(&msg, 0, 0, 0)
		if r == 0 {
			win.PostQuitMessage(int32(msg.WParam))
			break
		}
		if r == -1 {
			break
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
	if s.hwnd != 0 {
		win.DestroyWindow(s.hwnd)
	}
	if !s.confirmed {
		return screen.Rect{}, ErrCanceled
	}
	l, t, rr, b := s.norm()
	return screen.Rect{X: int(l) + s.vs.X, Y: int(t) + s.vs.Y, W: int(rr - l), H: int(b - t)}, nil
}

func register() {
	cls, _ := syscall.UTF16PtrFromString(className)
	wc := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     win.GetModuleHandle(nil),
		HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_CROSS)),
		LpszClassName: cls,
	}
	if win.RegisterClassEx(&wc) == 0 {
		registerErr = fmt.Errorf("RegisterClassEx failed (error %d)", win.GetLastError())
	}
}

func (s *state) snapshot() error {
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return errors.New("GetDC failed")
	}
	defer win.ReleaseDC(0, screenDC)
	w, h := int32(s.vs.W), int32(s.vs.H)

	s.shotDC = win.CreateCompatibleDC(screenDC)
	s.shotBmp = win.CreateCompatibleBitmap(screenDC, w, h)
	s.backDC = win.CreateCompatibleDC(screenDC)
	s.backBmp = win.CreateCompatibleBitmap(screenDC, w, h)
	if s.shotDC == 0 || s.shotBmp == 0 || s.backDC == 0 || s.backBmp == 0 {
		s.free()
		return errors.New("failed to allocate screenshot buffers")
	}
	s.shotOld = win.SelectObject(s.shotDC, win.HGDIOBJ(s.shotBmp))
	s.backOld = win.SelectObject(s.backDC, win.HGDIOBJ(s.backBmp))
	if !win.BitBlt(s.shotDC, 0, 0, w, h, screenDC, int32(s.vs.X), int32(s.vs.Y), win.SRCCOPY|win.CAPTUREBLT) {
		s.free()
		return errors.New("screen capture failed")
	}
	lb := win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: win.COLORREF(0x0000FF)}
	s.pen = win.ExtCreatePen(win.PS_GEOMETRIC|win.PS_SOLID, 2, &lb, 0, nil)
	return nil
}

func (s *state) free() {
	if s.shotDC != 0 {
		win.SelectObject(s.shotDC, s.shotOld)
		win.DeleteDC(s.shotDC)
		s.shotDC = 0
	}
	if s.backDC != 0 {
		win.SelectObject(s.backDC, s.backOld)
		win.DeleteDC(s.backDC)
		s.backDC = 0
	}
	for _, o := range []win.HGDIOBJ{win.HGDIOBJ(s.shotBmp), win.HGDIOBJ(s.backBmp), win.HGDIOBJ(s.pen)} {
		if o != 0 {
			win.DeleteObject(o)
		}
	}
	s.shotBmp, s.backBmp, s.pen = 0, 0, 0
}

func (s *state) norm() (l, t, r, b int32) {
	return min(s.x0, s.x1), min(s.y0, s.y1), max(s.x0, s.x1), max(s.y0, s.y1)
}

func (s *state) paint(hwnd win.HWND) {
	var ps win.PAINTSTRUCT
	dc := win.BeginPaint(hwnd, &ps)
	defer win.EndPaint(hwnd, &ps)
	w, h := int32(s.vs.W), int32(s.vs.H)
	win.BitBlt(s.backDC, 0, 0, w, h, s.shotDC, 0, 0, win.SRCCOPY)

	hint := "Drag to select a chat area. Release to confirm, Esc / right-click to cancel."
	if s.dragging || s.x0 != s.x1 {
		l, t, r, b := s.norm()
		oldPen := win.SelectObject(s.backDC, win.HGDIOBJ(s.pen))
		oldBrush := win.SelectObject(s.backDC, win.GetStockObject(win.NULL_BRUSH))
		win.Rectangle_(s.backDC, l, t, r, b)
		win.SelectObject(s.backDC, oldBrush)
		win.SelectObject(s.backDC, oldPen)
		hint = fmt.Sprintf("x=%d y=%d  %dx%d", l+int32(s.vs.X), t+int32(s.vs.Y), r-l, b-t)
	}
	drawLabel(s.backDC, 12, 12, hint)
	win.BitBlt(dc, 0, 0, w, h, s.backDC, 0, 0, win.SRCCOPY)
}

func drawLabel(dc win.HDC, x, y int32, text string) {
	u, _ := syscall.UTF16FromString(text)
	n := int32(len(u) - 1)
	win.SetBkMode(dc, win.TRANSPARENT)
	win.SetTextColor(dc, win.COLORREF(0x000000))
	for _, d := range [][2]int32{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		win.TextOut(dc, x+d[0], y+d[1], &u[0], n)
	}
	win.SetTextColor(dc, win.COLORREF(0x00FFFF))
	win.TextOut(dc, x, y, &u[0], n)
}

func lparamXY(lp uintptr) (int32, int32) {
	return int32(int16(lp & 0xFFFF)), int32(int16((lp >> 16) & 0xFFFF))
}

func wndProc(hwnd win.HWND, msg uint32, wp, lp uintptr) uintptr {
	s := active
	if s == nil {
		return win.DefWindowProc(hwnd, msg, wp, lp)
	}
	switch msg {
	case win.WM_ERASEBKGND:
		return 1
	case win.WM_PAINT:
		s.paint(hwnd)
		return 0
	case win.WM_LBUTTONDOWN:
		s.x0, s.y0 = lparamXY(lp)
		s.x1, s.y1 = s.x0, s.y0
		s.dragging = true
		win.SetCapture(hwnd)
		return 0
	case win.WM_MOUSEMOVE:
		if s.dragging {
			s.x1, s.y1 = lparamXY(lp)
			s.x1 = max(0, min(s.x1, int32(s.vs.W)))
			s.y1 = max(0, min(s.y1, int32(s.vs.H)))
			win.InvalidateRect(hwnd, nil, false)
		}
		return 0
	case win.WM_LBUTTONUP:
		if s.dragging {
			s.dragging = false
			win.ReleaseCapture()
			l, t, r, b := s.norm()
			if r-l >= 8 && b-t >= 8 {
				s.confirmed, s.done = true, true
			} else {
				win.InvalidateRect(hwnd, nil, false)
			}
		}
		return 0
	case win.WM_RBUTTONUP:
		s.done = true
		return 0
	case win.WM_KEYDOWN:
		if wp == win.VK_ESCAPE {
			s.done = true
		}
		return 0
	case win.WM_CLOSE:
		s.done = true
		return 0
	case win.WM_DESTROY:
		s.hwnd = 0
		s.done = true
		return 0
	}
	return win.DefWindowProc(hwnd, msg, wp, lp)
}
