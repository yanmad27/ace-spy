// Package tesseract runs the Tesseract CLI on BGRA screen captures.
package tesseract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const runTimeout = 20 * time.Second

type Engine struct {
	exe      string
	tessdata string
	lang     string
}

// LangCode maps a BCP-47 tag to a Tesseract language code.
func LangCode(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "vi", "vi-vn":
		return "vie"
	case "en", "en-us", "en-gb":
		return "eng"
	}
	return lang
}

// New locates tesseract and verifies the language data is available.
// appDir is searched for a bundled tesseract/ and tessdata/ directory.
func New(exeOverride, appDir, lang string) (*Engine, error) {
	exe, err := find(exeOverride, appDir)
	if err != nil {
		return nil, err
	}
	e := &Engine{exe: exe, lang: LangCode(lang)}
	if dir := filepath.Join(appDir, "tessdata"); fileExists(filepath.Join(dir, e.lang+".traineddata")) {
		e.tessdata = dir
	}
	langs, err := e.listLangs()
	if err != nil {
		return nil, err
	}
	for _, l := range langs {
		if l == e.lang {
			return e, nil
		}
	}
	return nil, fmt.Errorf("tesseract at %s has no %q language data (have: %s). Put %s.traineddata in %s",
		exe, e.lang, strings.Join(langs, ", "), e.lang, filepath.Join(appDir, "tessdata"))
}

func (e *Engine) Exe() string { return e.exe }

func (e *Engine) Close() {}

func (e *Engine) baseArgs() []string {
	if e.tessdata == "" {
		return nil
	}
	return []string{"--tessdata-dir", e.tessdata}
}

func (e *Engine) listLangs() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.exe, append(e.baseArgs(), "--list-langs")...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run %s --list-langs: %w: %s", e.exe, err, strings.TrimSpace(string(out)))
	}
	var langs []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(strings.ToLower(l), "list of available") {
			langs = append(langs, l)
		}
	}
	return langs, nil
}

// Recognize returns non-empty text lines, top to bottom.
func (e *Engine) Recognize(bgra []byte, w, h int) ([]string, error) {
	if w <= 0 || h <= 0 || len(bgra) != w*h*4 {
		return nil, errors.New("invalid image buffer")
	}
	var img bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&img, Preprocess(bgra, w, h)); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	args := append(e.baseArgs(), "stdin", "stdout", "-l", e.lang, "--oem", "1", "--psm", "6")
	cmd := exec.CommandContext(ctx, e.exe, args...)
	hideWindow(cmd)
	cmd.Stdin = &img
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var lines []string
	for _, l := range strings.Split(stdout.String(), "\n") {
		if l = strings.TrimSpace(strings.TrimRight(l, "\r\f")); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// Preprocess converts to grayscale and inverts light-on-dark text (typical game chat)
// so Tesseract sees dark text on a light background.
func Preprocess(bgra []byte, w, h int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	var sum uint64
	for i, j := 0, 0; i < len(bgra); i, j = i+4, j+1 {
		y := (114*uint32(bgra[i]) + 587*uint32(bgra[i+1]) + 299*uint32(bgra[i+2])) / 1000
		g.Pix[j] = uint8(y)
		sum += uint64(y)
	}
	if sum/uint64(w*h) < 128 {
		for i := range g.Pix {
			g.Pix[i] = 255 - g.Pix[i]
		}
	}
	return g
}

func find(override, appDir string) (string, error) {
	if override != "" {
		if fileExists(override) {
			return override, nil
		}
		return "", fmt.Errorf("tesseract not found at %s", override)
	}
	name := "tesseract" + exeSuffix
	candidates := []string{filepath.Join(appDir, "tesseract", name)}
	if p, err := exec.LookPath("tesseract"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, platformCandidates()...)
	for _, c := range candidates {
		if fileExists(c) {
			return c, nil
		}
	}
	return "", errors.New(installHint)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
