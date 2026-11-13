package glyphocr

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanmad27/ace-spy/internal/chatparse"
)

// HOLDOUT=dir of x2 captures: reports lines decoded vs messages accepted, and timing.
func TestHoldout(t *testing.T) {
	dir := os.Getenv("HOLDOUT")
	if dir == "" {
		t.Skip()
	}
	e, _ := New()
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	totalLines, totalMsgs := 0, 0
	var worst time.Duration
	for _, f := range files {
		pix, w, h := loadNative(t, f, 2)
		start := time.Now()
		lines, err := e.Recognize(pix, w, h)
		if err != nil {
			t.Fatal(err)
		}
		worst = max(worst, time.Since(start))
		msgs := chatparse.Messages(lines)
		totalLines += len(lines)
		totalMsgs += len(msgs)
		if len(msgs) < len(lines) {
			for _, l := range lines {
				if len(chatparse.Messages([]string{l})) == 0 {
					t.Logf("%s rejected: %q", filepath.Base(f), l)
				}
			}
		}
	}
	t.Logf("%d frames: %d lines, %d accepted messages, slowest frame %s", len(files), totalLines, totalMsgs, worst.Round(time.Millisecond))
}
