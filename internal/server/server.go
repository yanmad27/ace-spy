package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/yanmad27/ace-spy/internal/chatdiff"
	"github.com/yanmad27/ace-spy/internal/chatparse"
)

const (
	maxBodyBytes    = 1 << 20
	maxBatch        = 500
	maxRegionRunes  = 64
	maxClientRunes  = 64
	maxTextRunes    = 2000
	defaultPage     = 200
	maxPage         = 1000
	futureTolerance = 24 * time.Hour
)

type Options struct {
	IngestToken  string
	ViewUser     string
	ViewPassword string
	Dedupe       time.Duration
	Retention    time.Duration
	CronSecret   string
}

type Server struct {
	store  *Store
	opts   Options
	static fs.FS
}

func New(store *Store, static fs.FS, opts Options) http.Handler {
	s := &Server{store: store, opts: opts, static: static}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /api/ingest", s.ingest)
	mux.Handle("GET /api/messages", s.viewAuth(http.HandlerFunc(s.messages)))
	mux.Handle("GET /api/regions", s.viewAuth(http.HandlerFunc(s.regions)))
	mux.HandleFunc("GET /api/cron/prune", s.cronPrune)
	mux.HandleFunc("POST /api/samples", s.ingestAuth(s.addSample))
	mux.HandleFunc("GET /api/samples", s.ingestAuth(s.listSamples))
	mux.HandleFunc("GET /api/samples/{id}", s.ingestAuth(s.samplePNG))
	mux.Handle("GET /", s.viewAuth(http.FileServerFS(static)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) viewAuth(next http.Handler) http.Handler {
	if s.opts.ViewPassword == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(s.opts.ViewUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(s.opts.ViewPassword)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="ace-spy", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type ingestRequest struct {
	Client   string `json:"client"`
	Messages []struct {
		Region string `json:"region"`
		Text   string `json:"text"`
		TS     int64  `json:"ts"`
	} `json:"messages"`
}

func (s *Server) validIngestToken(r *http.Request) bool {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return found && subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.IngestToken)) == 1
}

func (s *Server) ingestAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validIngestToken(r) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

const maxSampleBytes = 4 << 20 // Vercel limits request bodies to 4.5 MB

func (s *Server) addSample(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSampleBytes)
	var req struct {
		Client string   `json:"client"`
		Region string   `json:"region"`
		OCR    []string `json:"ocr"`
		PNG    []byte   `json:"png"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	region, err := clean(req.Region, maxRegionRunes, false)
	if err != nil {
		http.Error(w, "region: "+err.Error(), http.StatusBadRequest)
		return
	}
	client, _ := clean(req.Client, maxClientRunes, true)
	if !bytes.HasPrefix(req.PNG, []byte("\x89PNG\r\n\x1a\n")) {
		http.Error(w, "png: not a PNG image", http.StatusBadRequest)
		return
	}
	ocr := strings.Join(req.OCR, "\n")
	if len(ocr) > 64<<10 {
		ocr = ocr[:64<<10]
	}
	id, err := s.store.AddSample(r.Context(), Sample{Client: client, Region: region, CreatedAt: time.Now().UnixMilli(), OCR: ocr}, req.PNG)
	if err != nil {
		log.Printf("sample: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) listSamples(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Samples(r.Context(), r.URL.Query().Get("region"), parseLimit(r.URL.Query().Get("limit")))
	if err != nil {
		log.Printf("samples: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) samplePNG(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(strings.TrimSuffix(r.PathValue("id"), ".png"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	png, err := s.store.SamplePNG(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(png)
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if !s.validIngestToken(r) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req ingestRequest
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(req.Messages) > maxBatch {
		http.Error(w, fmt.Sprintf("at most %d messages per request", maxBatch), http.StatusRequestEntityTooLarge)
		return
	}
	client, err := clean(req.Client, maxClientRunes, true)
	if err != nil {
		http.Error(w, "client: "+err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	msgs := make([]Message, 0, len(req.Messages))
	for i, m := range req.Messages {
		region, err := clean(m.Region, maxRegionRunes, false)
		if err != nil {
			http.Error(w, fmt.Sprintf("messages[%d].region: %v", i, err), http.StatusBadRequest)
			return
		}
		text, err := clean(m.Text, maxTextRunes, false)
		if err != nil {
			http.Error(w, fmt.Sprintf("messages[%d].text: %v", i, err), http.StatusBadRequest)
			return
		}
		if !chatparse.Acceptable(text) {
			continue
		}
		ts := m.TS
		if ts <= 0 || ts > now.Add(futureTolerance).UnixMilli() {
			ts = nowMs
		}
		msgs = append(msgs, Message{Client: client, Region: region, Text: text, CapturedAt: ts, ReceivedAt: nowMs})
	}

	if s.opts.Dedupe > 0 {
		var err error
		if msgs, err = s.dropDuplicates(r.Context(), msgs, now.Add(-s.opts.Dedupe).UnixMilli()); err != nil {
			log.Printf("dedupe: %v", err)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}
	total := len(req.Messages)
	if len(msgs) == 0 {
		writeJSON(w, http.StatusOK, map[string]int{"accepted": 0, "dropped": total})
		return
	}
	stored, err := s.store.Insert(r.Context(), msgs)
	if err != nil {
		log.Printf("ingest: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(stored), "dropped": total - len(stored)})
}

const dedupeLookback = 500

// dropDuplicates removes messages whose folded text (no diacritics, letters and digits
// only) is within OCR noise of a recent message in the same region or earlier in the batch.
func (s *Server) dropDuplicates(ctx context.Context, msgs []Message, since int64) ([]Message, error) {
	seen := map[string][]string{}
	out := msgs[:0:0]
	for _, m := range msgs {
		keys, ok := seen[m.Region]
		if !ok {
			texts, err := s.store.RecentTexts(ctx, m.Region, since, dedupeLookback)
			if err != nil {
				return nil, err
			}
			for _, t := range texts {
				keys = append(keys, chatparse.Key(t))
			}
		}
		k := chatparse.Key(m.Text)
		if k != "" && !containsSimilarKey(keys, k) {
			out = append(out, m)
			keys = append(keys, k)
		}
		seen[m.Region] = keys
	}
	return out, nil
}

func containsSimilarKey(keys []string, k string) bool {
	for _, e := range keys {
		if chatdiff.Similar(e, k) {
			return true
		}
	}
	return false
}

func clean(s string, maxRunes int, allowEmpty bool) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("invalid UTF-8")
	}
	s = strings.TrimSpace(norm.NFC.String(s))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	n := utf8.RuneCountInString(s)
	if n == 0 && !allowEmpty {
		return "", errors.New("empty")
	}
	if n > maxRunes {
		return "", fmt.Errorf("longer than %d characters", maxRunes)
	}
	return s, nil
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseLimit(q.Get("limit"))
	region := q.Get("region")
	var (
		msgs []Message
		err  error
	)
	if after, ok := parseID(q.Get("after")); ok {
		msgs, err = s.store.After(r.Context(), region, after, limit)
	} else {
		before, _ := parseID(q.Get("before"))
		msgs, err = s.store.Before(r.Context(), region, before, limit)
	}
	if err != nil {
		log.Printf("messages: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) regions(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Regions(r.Context())
	if err != nil {
		log.Printf("regions: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// cronPrune is invoked by Vercel Cron, which sends "Authorization: Bearer $CRON_SECRET".
func (s *Server) cronPrune(w http.ResponseWriter, r *http.Request) {
	if s.opts.CronSecret == "" || s.opts.Retention <= 0 {
		http.Error(w, "pruning disabled", http.StatusNotFound)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.opts.CronSecret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	n, err := s.store.Prune(r.Context(), time.Now().Add(-s.opts.Retention).UnixMilli())
	if err != nil {
		log.Printf("prune: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

func parseID(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func parseLimit(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultPage
	}
	return min(n, maxPage)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
