package gamewin_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/lxn/win"

	"github.com/yanmad27/ace-spy/internal/config"
	"github.com/yanmad27/ace-spy/internal/gamewin"
	"github.com/yanmad27/ace-spy/internal/screen"
)

// Launches Notepad as a stand-in game window and captures part of it by process.
func TestResolveAndCaptureWindowByProcess(t *testing.T) {
	cmd := exec.Command("notepad.exe")
	if err := cmd.Start(); err != nil {
		t.Skip("notepad not available:", err)
	}
	defer cmd.Process.Kill()

	var wins []gamewin.Info
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if wins = gamewin.List("notepad.exe"); len(wins) > 0 {
			break
		}
	}
	if len(wins) == 0 {
		t.Skip("no notepad window appeared (non-interactive session?)")
	}

	stale := config.WindowTarget{Exe: "notepad.exe", Title: wins[0].Title, PID: 1}
	res := gamewin.NewResolver([]*config.WindowTarget{&stale})
	info, err := res.Resolve(stale)
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != wins[0].PID {
		t.Fatalf("resolved PID %d, want %d", info.PID, wins[0].PID)
	}

	g, err := screen.NewWindowGrabber(func() (win.HWND, error) { return info.HWND, nil }, 0, 0, 64, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	pix, w, h, err := g.Grab()
	if err != nil {
		t.Fatal(err)
	}
	if w != 128 || h != 64 || len(pix) != w*h*4 {
		t.Fatalf("got %dx%d (%d bytes)", w, h, len(pix))
	}
	if g.Method() != "WGC" {
		t.Fatalf("captured with %q, want Windows.Graphics.Capture", g.Method())
	}
	t.Logf("captured %dx%d via %s", w, h, g.Method())
	nonBlack := false
	for i := 0; i < len(pix); i += 4 {
		if pix[i]|pix[i+1]|pix[i+2] != 0 {
			nonBlack = true
			break
		}
	}
	if !nonBlack {
		t.Fatal("captured image is entirely black")
	}
}
