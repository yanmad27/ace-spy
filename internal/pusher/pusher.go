// Package pusher delivers captured chat lines to the ace-spy server with batching,
// bounded buffering and exponential backoff.
package pusher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxQueue     = 10000
	maxBatch     = 100
	flushDelay   = 300 * time.Millisecond
	minBackoff   = time.Second
	maxBackoff   = 30 * time.Second
	requestLimit = 15 * time.Second
	// base64 inflates by 4/3; stay under the 4 MB server limit.
	maxSampleBytes = 2900 << 10
)

type Message struct {
	Region string   `json:"region"`
	Text   string   `json:"text"`
	TS     int64    `json:"ts"`
	Colors []string `json:"colors,omitempty"` // colours of the trailing tokens of Text
}

type payload struct {
	Client   string    `json:"client"`
	Messages []Message `json:"messages"`
}

type Pusher struct {
	endpoint   string
	samplesURL string
	token      string
	client     string
	http       *http.Client
	onStatus   func(string)

	mu      sync.Mutex
	queue   []Message
	dropped int
	notify  chan struct{}
}

func New(serverURL, token, clientName string, onStatus func(string)) *Pusher {
	if onStatus == nil {
		onStatus = func(string) {}
	}
	base := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	return &Pusher{
		endpoint:   base + "/api/ingest",
		samplesURL: base + "/api/samples",
		token:      strings.TrimSpace(token),
		client:     clientName,
		http:       &http.Client{Timeout: requestLimit},
		onStatus:   onStatus,
		notify:     make(chan struct{}, 1),
	}
}

func (p *Pusher) Enqueue(msgs ...Message) {
	if len(msgs) == 0 {
		return
	}
	p.mu.Lock()
	p.queue = append(p.queue, msgs...)
	if over := len(p.queue) - maxQueue; over > 0 {
		p.queue = append([]Message(nil), p.queue[over:]...)
		p.dropped += over
	}
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *Pusher) Run(ctx context.Context) {
	backoff := minBackoff
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.notify:
		}
		if !sleep(ctx, flushDelay) {
			return
		}
		for {
			batch := p.peek()
			if len(batch) == 0 {
				break
			}
			retry, err := p.post(ctx, batch)
			if err == nil || !retry {
				p.ack(len(batch))
				backoff = minBackoff
				if err != nil {
					p.onStatus(fmt.Sprintf("push rejected, dropped %d lines: %v", len(batch), err))
				} else {
					p.onStatus(fmt.Sprintf("pushed %d lines", len(batch)))
				}
				continue
			}
			p.onStatus(fmt.Sprintf("push failed (retry in %s): %v", backoff, err))
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

func (p *Pusher) peek() []Message {
	p.mu.Lock()
	dropped := p.dropped
	p.dropped = 0
	batch := append([]Message(nil), p.queue[:min(len(p.queue), maxBatch)]...)
	p.mu.Unlock()
	if dropped > 0 {
		p.onStatus(fmt.Sprintf("queue full, dropped %d oldest lines", dropped))
	}
	return batch
}

func (p *Pusher) ack(n int) {
	p.mu.Lock()
	p.queue = p.queue[n:]
	p.mu.Unlock()
}

// UploadSample sends a captured chat image (BGRA) with its raw OCR lines, so OCR
// can be tuned against real game frames.
func (p *Pusher) UploadSample(ctx context.Context, region string, bgra []byte, w, h int, ocr []string) error {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(bgra) && i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bgra[i+2], bgra[i+1], bgra[i], 255
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		return err
	}
	if buf.Len() > maxSampleBytes {
		return fmt.Errorf("sample image too large (%d bytes); lower the upscale", buf.Len())
	}
	body, err := json.Marshal(map[string]any{"client": p.client, "region": region, "ocr": ocr, "png": buf.Bytes()})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.samplesURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// post returns retry=false for permanent rejections (malformed payload).
func (p *Pusher) post(ctx context.Context, batch []Message) (retry bool, err error) {
	body, err := json.Marshal(payload{Client: p.client, Messages: batch})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.http.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 == 2 {
		return false, nil
	}
	err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return false, err
	}
	return true, err
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
