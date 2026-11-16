package glyphocr

import (
	"strings"
	"testing"
)

// A character the atlas lacks ("★" drawn as a made-up bitmap) is reported as unknown
// with its context, and once learned it is read.
func TestUnknownGlyphIsReportedThenLearned(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	star := []uint32{1 << 9, 3 << 8, 0x7f << 6, 0x3f << 7, 0x7f << 6, 3 << 8, 1 << 9}
	e.gmu.Lock()
	e.glyphs = append(e.glyphs, e.makeGlyph("★", len(star)+1, star, nil))
	e.index()
	e.gmu.Unlock()
	line := "^[10:25] [Mo] [BC] ab★cd"
	pix, w, h := drawFrame(t, e, []string{line}, [][3]byte{{0xea, 0x22, 0x22}}, 260)

	e2, _ := New()
	res, err := e2.RecognizeAll(pix, w, h)
	if err != nil {
		t.Fatal(err)
	}
	texts, unknown := res.Lines, res.Unknown
	if len(unknown) != 1 {
		t.Fatalf("unknown = %+v (texts %q)", unknown, texts)
	}
	u := unknown[0]
	if u.Color != "ea2222" || !strings.Contains(u.Context, "ab"+UnknownMark+"cd") {
		t.Fatalf("unknown = %+v", u)
	}
	if e2.Learn([]Learned{{Text: "★", Cols: u.Cols}}) != 1 {
		t.Fatal("not learned")
	}
	if e2.Learn([]Learned{{Text: "★", Cols: u.Cols}}) != 0 {
		t.Fatal("learned twice")
	}
	res, _ = e2.RecognizeAll(pix, w, h)
	texts, unknown = res.Lines, res.Unknown
	if len(texts) != 1 || texts[0] != line || len(unknown) != 0 {
		t.Fatalf("after learning: %q unknown=%d", texts, len(unknown))
	}
}
