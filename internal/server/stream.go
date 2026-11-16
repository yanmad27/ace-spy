package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// Streams end before the platform's function time limit; EventSource reconnects
	// and resumes from Last-Event-ID.
	streamLifetime  = 4 * time.Minute
	streamPoll      = time.Second
	streamStatus    = 15 * time.Second
	streamKeepAlive = 20 * time.Second
	streamRetryMs   = 2000
	streamBatch     = 500
)

// hub polls the newest message id once per second while any stream is open and
// wakes the streams when it changes, so one cheap indexed query serves them all.
type hub struct {
	store *Store

	mu     sync.Mutex
	subs   int
	latest int64
	wake   chan struct{}
	stop   context.CancelFunc
}

func newHub(store *Store) *hub { return &hub{store: store, wake: make(chan struct{})} }

// subscribe returns a channel closed on the next change and an unsubscribe func.
func (h *hub) subscribe() (func() <-chan struct{}, func()) {
	h.mu.Lock()
	h.subs++
	if h.subs == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		h.stop = cancel
		go h.run(ctx)
	}
	h.mu.Unlock()
	next := func() <-chan struct{} {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.wake
	}
	return next, func() {
		h.mu.Lock()
		h.subs--
		if h.subs == 0 && h.stop != nil {
			h.stop()
			h.stop = nil
		}
		h.mu.Unlock()
	}
}

func (h *hub) run(ctx context.Context) {
	t := time.NewTicker(streamPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		id, err := h.store.LatestID(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("stream hub: %v", err)
			}
			continue
		}
		h.mu.Lock()
		if id != h.latest {
			h.latest = id
			close(h.wake)
			h.wake = make(chan struct{})
		}
		h.mu.Unlock()
	}
}

// stream sends new messages as server-sent events ("message", id = last message id)
// and, every streamStatus, the viewer count and capture state ("status"). The stream
// also records the viewer's presence, so a viewer needs no other requests.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	viewer := q.Get("id")
	if viewer != "" && !viewerIDRe.MatchString(viewer) {
		http.Error(w, "invalid viewer id", http.StatusBadRequest)
		return
	}
	last, ok := parseID(r.Header.Get("Last-Event-ID"))
	if !ok {
		if last, ok = parseID(q.Get("after")); !ok {
			http.Error(w, "after is required", http.StatusBadRequest)
			return
		}
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server's WriteTimeout would cut the stream
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: %d\n\n", streamRetryMs)
	if rc.Flush() != nil {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), streamLifetime)
	defer cancel()
	next, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	send := func(event string, id int64, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if id > 0 {
			fmt.Fprintf(w, "id: %d\n", id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		return rc.Flush() == nil
	}
	flushNew := func() bool {
		for {
			msgs, err := s.store.After(ctx, "", last, streamBatch)
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				log.Printf("stream: %v", err)
				return true // try again on the next wake-up
			}
			if len(msgs) == 0 {
				return true
			}
			last = msgs[len(msgs)-1].ID
			if !send("message", last, msgs) {
				return false
			}
			if len(msgs) < streamBatch {
				return true
			}
		}
	}
	status := func() bool {
		now := time.Now()
		active := now.Add(-presenceActive).UnixMilli()
		var online int64
		if viewer != "" {
			n, err := s.store.Presence(ctx, viewer, now.UnixMilli(), active, now.Add(-presenceStale).UnixMilli())
			if err != nil {
				return ctx.Err() == nil
			}
			online = n
		}
		clients, regions, err := s.store.Capturing(ctx, active)
		if err != nil {
			return ctx.Err() == nil
		}
		return send("status", 0, map[string]any{"online": online, "clients": clients, "regions": regions})
	}

	if !status() || !flushNew() {
		return
	}
	statusT := time.NewTicker(streamStatus)
	defer statusT.Stop()
	keep := time.NewTicker(streamKeepAlive)
	defer keep.Stop()
	for {
		wake := next()
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				fmt.Fprint(w, "event: bye\ndata: {}\n\n")
				rc.Flush()
			}
			return
		case <-wake:
			if !flushNew() {
				return
			}
		case <-statusT.C:
			if !status() {
				return
			}
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// cachePublic lets the CDN share a public read response for a few seconds; with a
// view password set, responses stay private.
func (s *Server) cachePublic(w http.ResponseWriter, seconds int) {
	if s.opts.ViewPassword != "" {
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=0, s-maxage="+strconv.Itoa(seconds)+", stale-while-revalidate="+strconv.Itoa(seconds*5))
}
