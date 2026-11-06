//go:build !windows

package tesseract

import "os/exec"

const (
	exeSuffix   = ""
	installHint = "tesseract not found in PATH"
)

func hideWindow(*exec.Cmd) {}

func platformCandidates() []string {
	return []string{"/opt/homebrew/bin/tesseract", "/usr/local/bin/tesseract", "/usr/bin/tesseract"}
}
