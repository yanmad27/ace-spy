package tesseract

import (
	"image/png"
	"os"
	"testing"

	"github.com/yanmad27/ace-spy/internal/chatdiff"
)

// Set TESS_TEST_APPDIR to a directory laid out like the release zip
// (tessdata/vie.traineddata, optionally tesseract/tesseract[.exe]) to run.
func TestRecognizeVietnameseChat(t *testing.T) {
	appDir := os.Getenv("TESS_TEST_APPDIR")
	if appDir == "" {
		t.Skip("TESS_TEST_APPDIR not set")
	}
	e, err := New("", appDir, "vi")
	if err != nil {
		t.Fatal(err)
	}

	pix, w, h := loadBGRA(t, "testdata/chat.png", 2)
	lines, err := e.Recognize(pix, w, h)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"[Bang] Kiếm Sĩ: tối nay đi săn boss không?",
		"[Thế giới] Ai bán đá cường hóa +10 không",
		"[Đội] Pháp Sư: chờ mình một chút nhé",
		"[Thế giới] Mua giáp Ảo Ảnh giá tốt",
	}
	t.Logf("got: %q", lines)
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if !chatdiff.Similar(chatdiff.Normalize(lines[i]), want[i]) {
			t.Errorf("line %d: got %q want %q", i, lines[i], want[i])
		}
	}
}

// Red chat text on the game's dark-blue background must become black on white
// (plain luminance made red text nearly invisible).
func TestPreprocessRedOnBlue(t *testing.T) {
	const w, h = 20, 20
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			if x >= 6 && x < 14 && y >= 6 && y < 14 {
				pix[i], pix[i+1], pix[i+2] = 30, 40, 230 // red text (BGR)
			} else {
				pix[i], pix[i+1], pix[i+2] = 120, 50, 20 // dark blue background
			}
			pix[i+3] = 255
		}
	}
	g := Preprocess(pix, w, h)
	cw, ch := g.Rect.Dx(), g.Rect.Dy()
	if corner := g.Pix[0]; corner != 255 {
		t.Fatalf("background should be white, got %d", corner)
	}
	if centre := g.Pix[(ch/2)*cw+cw/2]; centre != 0 {
		t.Fatalf("text should be black, got %d", centre)
	}
}

// loadBGRA decodes a PNG into BGRA, upscaled by nearest neighbour like the client capture.
func loadBGRA(t *testing.T, path string, scale int) ([]byte, int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := src.Bounds()
	w, h := b.Dx()*scale, b.Dy()*scale
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := src.At(b.Min.X+x/scale, b.Min.Y+y/scale).RGBA()
			i := (y*w + x) * 4
			out[i], out[i+1], out[i+2], out[i+3] = byte(bl>>8), byte(g>>8), byte(r>>8), 255
		}
	}
	return out, w, h
}
