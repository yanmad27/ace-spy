// Package gamewin finds game windows by process so a region can follow a specific
// game client (several clients of the same game can run side by side).
package gamewin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"github.com/yanmad27/ace-spy/internal/config"
)

type Info struct {
	HWND  win.HWND
	PID   uint32
	Exe   string
	Title string
	area  int
}

func (i Info) Target() *config.WindowTarget {
	return &config.WindowTarget{Exe: i.Exe, Title: i.Title, PID: i.PID}
}

var procGetWindowTextW = windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowTextW")

var shellExes = map[string]bool{"explorer.exe": true, "searchhost.exe": true, "shellexperiencehost.exe": true}

// AtPoint returns the top-level application window under a screen point, ignoring
// this process and the Windows shell.
func AtPoint(x, y int) (Info, bool) {
	h := win.WindowFromPoint(win.POINT{X: int32(x), Y: int32(y)})
	if h == 0 {
		return Info{}, false
	}
	h = win.GetAncestor(h, win.GA_ROOT)
	info, err := describe(h)
	if err != nil || info.PID == uint32(os.Getpid()) || shellExes[strings.ToLower(info.Exe)] {
		return Info{}, false
	}
	return info, true
}

// ClientOrigin returns the screen position and size of a window's client area.
func ClientOrigin(h win.HWND) (x, y, w, hgt int, err error) {
	var rc win.RECT
	if !win.GetClientRect(h, &rc) {
		return 0, 0, 0, 0, errors.New("GetClientRect failed")
	}
	pt := win.POINT{}
	if !win.ClientToScreen(h, &pt) {
		return 0, 0, 0, 0, errors.New("ClientToScreen failed")
	}
	return int(pt.X), int(pt.Y), int(rc.Right - rc.Left), int(rc.Bottom - rc.Top), nil
}

func describe(h win.HWND) (Info, error) {
	var pid uint32
	win.GetWindowThreadProcessId(h, &pid)
	if pid == 0 {
		return Info{}, errors.New("no process for window")
	}
	exe, err := processExe(pid)
	if err != nil {
		return Info{}, err
	}
	buf := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	var rc win.RECT
	win.GetClientRect(h, &rc)
	return Info{
		HWND: h, PID: pid, Exe: exe,
		Title: syscall.UTF16ToString(buf[:n]),
		area:  int(rc.Right-rc.Left) * int(rc.Bottom-rc.Top),
	}, nil
}

func processExe(pid uint32) (string, error) {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(p)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return filepath.Base(syscall.UTF16ToString(buf[:n])), nil
}

// List returns the visible top-level windows of exe, one per process (the one with
// the largest client area), sorted by PID.
func List(exe string) []Info {
	best := map[uint32]Info{}
	cb := syscall.NewCallback(func(h, _ uintptr) uintptr {
		hw := win.HWND(h)
		if !win.IsWindowVisible(hw) {
			return 1
		}
		info, err := describe(hw)
		if err != nil || !strings.EqualFold(info.Exe, exe) {
			return 1
		}
		if cur, ok := best[info.PID]; !ok || info.area > cur.area {
			best[info.PID] = info
		}
		return 1
	})
	_ = windows.EnumWindows(cb, unsafe.Pointer(nil))
	out := make([]Info, 0, len(best))
	for _, i := range best {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].PID < out[b].PID })
	return out
}

// Resolver maps saved window targets to live windows. When a game client restarted
// (its PID changed), the target is re-attached to a client not already used by
// another target, preferring the same window title.
type Resolver struct {
	mu      sync.Mutex
	saved   map[uint32]bool
	remap   map[uint32]uint32
	claimed map[uint32]bool
}

func NewResolver(targets []*config.WindowTarget) *Resolver {
	r := &Resolver{saved: map[uint32]bool{}, remap: map[uint32]uint32{}, claimed: map[uint32]bool{}}
	for _, t := range targets {
		if t != nil {
			r.saved[t.PID] = true
		}
	}
	return r
}

func (r *Resolver) Resolve(t config.WindowTarget) (Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	windowsOfExe := List(t.Exe)
	if len(windowsOfExe) == 0 {
		return Info{}, fmt.Errorf("game window %s is not running", t.Exe)
	}
	alive := map[uint32]Info{}
	for _, w := range windowsOfExe {
		alive[w.PID] = w
	}
	if pid, ok := r.remap[t.PID]; ok {
		if w, ok := alive[pid]; ok {
			return w, nil
		}
	}
	if w, ok := alive[t.PID]; ok {
		r.claimed[w.PID] = true
		return w, nil
	}
	var pick *Info
	for pass := 0; pass < 2 && pick == nil; pass++ {
		for i := range windowsOfExe {
			w := windowsOfExe[i]
			taken := r.claimed[w.PID] || (r.saved[w.PID] && w.PID != t.PID)
			if taken || (pass == 0 && w.Title != t.Title) {
				continue
			}
			pick = &w
			break
		}
	}
	if pick == nil {
		return Info{}, fmt.Errorf("no free %s window (all running clients are used by other regions)", t.Exe)
	}
	r.remap[t.PID] = pick.PID
	r.claimed[pick.PID] = true
	return *pick, nil
}
