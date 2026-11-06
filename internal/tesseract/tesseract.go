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

// LangCode maps a BCP-47 tag to a Tesseract language code. Vietnamese is paired
// with English: players type a lot of unaccented teencode, which the Vietnamese
// model alone "corrects" into wrong diacritics (measured on real game frames:
// exact lines 25/66 with vie, 35/66 with vie+eng).
func LangCode(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "vi", "vi-vn":
		return "vie+eng"
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
	need := strings.Split(e.lang, "+")
	if dir := filepath.Join(appDir, "tessdata"); fileExists(filepath.Join(dir, need[0]+".traineddata")) {
		e.tessdata = dir
	}
	langs, err := e.listLangs()
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, l := range langs {
		have[l] = true
	}
	for _, l := range need {
		if !have[l] {
			return nil, fmt.Errorf("tesseract at %s has no %q language data (have: %s). Put %s.traineddata in %s",
				exe, l, strings.Join(langs, ", "), l, filepath.Join(appDir, "tessdata"))
		}
	}
	return e, nil
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

// Preprocess prepares a game chat capture for Tesseract. Tuned on real Ace Online
// frames (coloured text on a dark-blue background):
//   - grey = max(R, G): the blue channel is dropped because the background is blue,
//     so red/purple/green/yellow/white text all stand out (plain luminance made red
//     text vanish);
//   - upscale ×1.5 (bilinear) on top of the capture scale;
//   - Otsu binarisation, then invert so the text is black on white.
func Preprocess(bgra []byte, w, h int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for i, j := 0, 0; i < len(bgra); i, j = i+4, j+1 {
		g.Pix[j] = max(bgra[i+1], bgra[i+2])
	}
	g = resizeBilinear(g, 1.5)
	binarize(g, otsu(g))
	var sum uint64
	for _, v := range g.Pix {
		sum += uint64(v)
	}
	if sum/uint64(len(g.Pix)) < 128 {
		for i := range g.Pix {
			g.Pix[i] = 255 - g.Pix[i]
		}
	}
	return g
}

func otsu(g *image.Gray) uint8 {
	var hist [256]int
	for _, v := range g.Pix {
		hist[v]++
	}
	var sum, sumB, wB, best float64
	for i, c := range hist {
		sum += float64(i * c)
	}
	thr := 0
	for t := 0; t < 256; t++ {
		wB += float64(hist[t])
		if wB == 0 {
			continue
		}
		wF := float64(len(g.Pix)) - wB
		if wF == 0 {
			break
		}
		sumB += float64(t * hist[t])
		mB, mF := sumB/wB, (sum-sumB)/wF
		if v := wB * wF * (mB - mF) * (mB - mF); v > best {
			best, thr = v, t
		}
	}
	return uint8(thr)
}

func binarize(g *image.Gray, thr uint8) {
	for i, v := range g.Pix {
		if v > thr {
			g.Pix[i] = 255
		} else {
			g.Pix[i] = 0
		}
	}
}

func resizeBilinear(g *image.Gray, f float64) *image.Gray {
	sw, sh := g.Rect.Dx(), g.Rect.Dy()
	w, h := max(int(float64(sw)*f), 1), max(int(float64(sh)*f), 1)
	out := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := max((float64(y)+0.5)/f-0.5, 0)
		y0 := min(int(fy), sh-1)
		y1 := min(y0+1, sh-1)
		dy := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := max((float64(x)+0.5)/f-0.5, 0)
			x0 := min(int(fx), sw-1)
			x1 := min(x0+1, sw-1)
			dx := fx - float64(x0)
			a, b := float64(g.Pix[y0*sw+x0]), float64(g.Pix[y0*sw+x1])
			c, d := float64(g.Pix[y1*sw+x0]), float64(g.Pix[y1*sw+x1])
			v := a*(1-dx)*(1-dy) + b*dx*(1-dy) + c*(1-dx)*dy + d*dx*dy
			out.Pix[y*w+x] = uint8(v + 0.5)
		}
	}
	return out
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
