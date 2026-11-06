// Package runner periodically captures each configured region, OCRs it, extracts
// new chat lines and hands them to the pusher.
package runner

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/yanmad27/ace-spy/internal/chatdiff"
	"github.com/yanmad27/ace-spy/internal/chatparse"
	"github.com/yanmad27/ace-spy/internal/config"
	"github.com/yanmad27/ace-spy/internal/pusher"
)

type Grabber interface {
	Grab() (bgra []byte, width, height int, err error)
	Close()
}

type GrabberFactory func(r config.Region) (Grabber, error)

type Recognizer interface {
	Recognize(bgra []byte, width, height int) ([]string, error)
}

type Sink interface {
	Enqueue(msgs ...pusher.Message)
}

type Events struct {
	OnLines func(regionID string, visible []string, fresh []string)
	OnError func(regionID string, err error)
	// OnSample receives a captured frame and its raw OCR lines every SampleEvery.
	OnSample func(reg config.Region, bgra []byte, w, h int, ocr []string)
}

const SampleEvery = 15 * time.Minute

type Runner struct {
	newGrabber GrabberFactory
	ocr        Recognizer
	sink       Sink
	events     Events
}

func New(newGrabber GrabberFactory, ocr Recognizer, sink Sink, events Events) *Runner {
	if events.OnLines == nil {
		events.OnLines = func(string, []string, []string) {}
	}
	if events.OnError == nil {
		events.OnError = func(string, error) {}
	}
	return &Runner{newGrabber: newGrabber, ocr: ocr, sink: sink, events: events}
}

// Run blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context, regions []config.Region) {
	var wg sync.WaitGroup
	for _, reg := range regions {
		if !reg.Enabled {
			continue
		}
		wg.Add(1)
		go func(reg config.Region) {
			defer wg.Done()
			r.loop(ctx, reg)
		}(reg)
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context, reg config.Region) {
	g, err := r.newGrabber(reg)
	if err != nil {
		r.events.OnError(reg.ID, fmt.Errorf("init capture: %w", err))
		return
	}
	defer g.Close()

	var tracker chatdiff.Tracker
	var lastHash uint64
	var lastSample time.Time
	ticker := time.NewTicker(time.Duration(reg.IntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := r.tick(reg, g, &tracker, &lastHash, &lastSample); err != nil {
			r.events.OnError(reg.ID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) tick(reg config.Region, g Grabber, tracker *chatdiff.Tracker, lastHash *uint64, lastSample *time.Time) error {
	pix, w, h, err := g.Grab()
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	hs := fnv.New64a()
	hs.Write(pix)
	sum := hs.Sum64()
	if sum == *lastHash {
		return nil
	}
	lines, err := r.ocr.Recognize(pix, w, h)
	if err != nil {
		return fmt.Errorf("ocr: %w", err)
	}
	*lastHash = sum
	if r.events.OnSample != nil && time.Since(*lastSample) >= SampleEvery {
		*lastSample = time.Now()
		r.events.OnSample(reg, pix, w, h, lines)
	}
	msgs := chatparse.Messages(lines)
	fresh := tracker.Feed(msgs)
	r.events.OnLines(reg.ID, msgs, fresh)
	if len(fresh) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	out := make([]pusher.Message, len(fresh))
	for i, text := range fresh {
		out[i] = pusher.Message{Region: reg.Label, Text: text, TS: now}
	}
	r.sink.Enqueue(out...)
	return nil
}
