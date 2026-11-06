package pusher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPusherRetriesThenDelivers(t *testing.T) {
	var attempts atomic.Int32
	var mu sync.Mutex
	var got []Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.URL.Path != "/api/ingest" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		var p payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Client != "pc1" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, p.Messages...)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := New(srv.URL+"/", "secret", "pc1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	p.Enqueue(Message{Region: "Guild", Text: "xin chào", TS: 1}, Message{Region: "Guild", Text: "bye", TS: 2})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("messages not delivered; attempts=%d", attempts.Load())
}

func TestQueueBounded(t *testing.T) {
	p := New("http://x", "t", "c", nil)
	for i := 0; i < maxQueue+10; i++ {
		p.Enqueue(Message{TS: int64(i)})
	}
	if len(p.queue) != maxQueue || p.queue[0].TS != 10 {
		t.Fatalf("len=%d first=%d", len(p.queue), p.queue[0].TS)
	}
}
