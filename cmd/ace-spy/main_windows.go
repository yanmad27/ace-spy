package main

import (
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/yanmad27/ace-spy/internal/ui"
)

var (
	user32                            = windows.NewLazySystemDLL("user32.dll")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
)

var version = "dev"

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 so capture coordinates are physical pixels.
const dpiPerMonitorV2 = ^uintptr(3)

func main() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(dpiPerMonitorV2)
	}

	name, _ := windows.UTF16PtrFromString(`Local\ace-spy-instance`)
	mutex, err := windows.CreateMutex(nil, true, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		msg, _ := windows.UTF16PtrFromString("Ace Spy is already running.")
		title, _ := windows.UTF16PtrFromString("Ace Spy")
		windows.MessageBox(0, msg, title, windows.MB_OK|windows.MB_ICONINFORMATION)
		return
	}
	defer windows.CloseHandle(mutex)

	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Dir(exe)
	if f, err := os.OpenFile(filepath.Join(dir, "ace-spy.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	if err := ui.Run(filepath.Join(dir, "ace-spy.json"), version); err != nil {
		log.Printf("fatal: %v", err)
		msg, _ := windows.UTF16PtrFromString(err.Error())
		title, _ := windows.UTF16PtrFromString("Ace Spy")
		windows.MessageBox(0, msg, title, windows.MB_OK|windows.MB_ICONERROR)
		os.Exit(1)
	}
}
