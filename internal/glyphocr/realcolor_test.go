package glyphocr

import (
	"os"
	"testing"

	"github.com/yanmad27/ace-spy/internal/chatparse"
)

// REALCOLOR_PNG=native frame: prints messages with their captured token colours.
func TestRealColors(t *testing.T) {
	f := os.Getenv("REALCOLOR_PNG")
	if f == "" {
		t.Skip()
	}
	e, _ := New()
	pix, w, h := loadNative(t, f, 1)
	texts, colors, _ := e.RecognizeColored(pix, w, h)
	for _, m := range chatparse.MessagesColored(texts, colors) {
		t.Logf("%s  %v", m.Text, m.Colors)
	}
}
