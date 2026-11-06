package tesseract

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	exeSuffix      = ".exe"
	createNoWindow = 0x08000000
	installHint    = "Tesseract OCR not found. Extract the whole release zip (the tesseract and tessdata folders must sit next to ace-spy.exe), or install it with:\n  winget install -e --id UB-Mannheim.TesseractOCR"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func platformCandidates() []string {
	var out []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			out = append(out,
				filepath.Join(base, "Tesseract-OCR", "tesseract.exe"),
				filepath.Join(base, "Programs", "Tesseract-OCR", "tesseract.exe"))
		}
	}
	return out
}
