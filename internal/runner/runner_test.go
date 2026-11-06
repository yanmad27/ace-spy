package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yanmad27/ace-spy/internal/config"
	"github.com/yanmad27/ace-spy/internal/pusher"
)

type fakeGrabber struct {
	mu     sync.Mutex
	frames [][]byte
	i      int
}

func (g *fakeGrabber) Grab() ([]byte, int, int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.frames[min(g.i, len(g.frames)-1)]
	g.i++
	return f, 1, 1, nil
}
func (g *fakeGrabber) Close() {}

type fakeOCR struct {
	calls int
	byPix map[byte][]string
}

func (o *fakeOCR) Recognize(pix []byte, _, _ int) ([]string, error) {
	o.calls++
	return o.byPix[pix[0]], nil
}

type sink struct {
	mu   sync.Mutex
	msgs []pusher.Message
}

func (s *sink) Enqueue(m ...pusher.Message) {
	s.mu.Lock()
	s.msgs = append(s.msgs, m...)
	s.mu.Unlock()
}

func TestRunnerEmitsOnlyNewLinesAndSkipsUnchangedFrames(t *testing.T) {
	g := &fakeGrabber{frames: [][]byte{{1, 0, 0, 0}, {1, 0, 0, 0}, {2, 0, 0, 0}}}
	ocr := &fakeOCR{byPix: map[byte][]string{
		1: {"—zÁZ — —", "[11:05] [Kiếm Sĩ] tối nay đi săn boss không", "I[11:06) [Pháp Sư] chờ mình một chút"},
		2: {"[11:06] [Pháp Sư] chờ mình một chút", "[11:07] [Diana] ae có đồ xin với", "® ® té mi"},
	}}
	s := &sink{}
	r := New(func(config.Region) (Grabber, error) { return g, nil }, ocr, s, Events{})

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	r.Run(ctx, []config.Region{{ID: "r1", Label: "Guild", IntervalMs: 250, Enabled: true}, {ID: "r2", Label: "Off"}})

	if ocr.calls != 2 {
		t.Fatalf("expected 2 OCR calls (unchanged frame skipped), got %d", ocr.calls)
	}
	var texts []string
	for _, m := range s.msgs {
		if m.Region != "Guild" {
			t.Fatalf("unexpected region %q", m.Region)
		}
		texts = append(texts, m.Text)
	}
	want := []string{
		"[11:05] [Kiếm Sĩ] tối nay đi săn boss không",
		"![11:06] [Pháp Sư] chờ mình một chút",
		"[11:07] [Diana] ae có đồ xin với",
	}
	if len(texts) != len(want) {
		t.Fatalf("got %q want %q", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("got %q want %q", texts, want)
		}
	}
}
