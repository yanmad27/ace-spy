package winocr

import (
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestRecognizeEnglishText(t *testing.T) {
	e, err := New("en-US")
	if err != nil {
		if strings.Contains(err.Error(), "is not installed") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	defer e.Close()

	f, err := os.Open("../tesseract/testdata/chat.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	const scale = 2
	w, h := b.Dx()*scale, b.Dy()*scale
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x/scale, b.Min.Y+y/scale).RGBA()
			i := (y*w + x) * 4
			pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(bl>>8), byte(g>>8), byte(r>>8), 255
		}
	}
	lines, err := e.Recognize(pix, w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("lines: %q", lines)
	if len(lines) < 3 || !strings.Contains(strings.Join(lines, " "), "boss") {
		t.Fatalf("unexpected OCR output: %q", lines)
	}
}
