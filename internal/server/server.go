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
	"regexp"
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
	hub    *hub
	store  *Store
	opts   Options
	static fs.FS
}

func New(store *Store, static fs.FS, opts Options) http.Handler {
	s := &Server{store: store, opts: opts, static: static, hub: newHub(store)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /api/ingest", s.ingest)
	mux.Handle("GET /api/messages", s.viewAuth(http.HandlerFunc(s.messages)))
	mux.Handle("GET /api/regions", s.viewAuth(http.HandlerFunc(s.regions)))
	mux.Handle("POST /api/presence", s.viewAuth(http.HandlerFunc(s.presence)))
	mux.Handle("GET /api/stream", s.viewAuth(http.HandlerFunc(s.stream)))
	mux.HandleFunc("POST /api/heartbeat", s.ingestAuth(s.heartbeat))
	mux.HandleFunc("POST /api/glyphs/unknown", s.ingestAuth(s.addGlyphs))
	mux.Handle("GET /api/glyphs", s.viewAuth(http.HandlerFunc(s.listGlyphs)))
	mux.HandleFunc("POST /api/glyphs/{hash}/label", s.ingestAuth(s.labelGlyph))
	mux.HandleFunc("GET /api/cron/prune", s.cronPrune)
	mux.HandleFunc("POST /api/samples", s.ingestAuth(s.addSample))
	mux.HandleFunc("GET /api/samples", s.ingestAuth(s.listSamples))
	mux.HandleFunc("GET /api/samples/{id}", s.ingestAuth(s.samplePNG))
	files := http.FileServerFS(static)
	mux.Handle("GET /", s.viewAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Purged by every deployment; browsers revalidate so a deploy shows at once.
		s.cachePublic(w, 86400)
		files.ServeHTTP(w, r)
	})))
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
		Region string   `json:"region"`
		Text   string   `json:"text"`
		TS     int64    `json:"ts"`
		Colors []string `json:"colors"`
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
		msgs = append(msgs, Message{Client: client, Region: region, Text: text, CapturedAt: ts, ReceivedAt: nowMs, Colors: cleanColors(m.Colors, text)})
	}

	// Pushing chat also proves the client is capturing (clients older than the heartbeat).
	if len(msgs) > 0 {
		name := client
		if name == "" {
			name = "unknown"
		}
		seen := map[string]bool{}
		var regions []string
		for _, m := range msgs {
			if !seen[m.Region] {
				seen[m.Region] = true
				regions = append(regions, m.Region)
			}
		}
		if err := s.store.Heartbeat(r.Context(), name, regions, nowMs, now.Add(-presenceStale).UnixMilli()); err != nil {
			log.Printf("ingest heartbeat: %v", err)
		}
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
// only) is within OCR noise of a recent message in the same region or earlier in the
// batch. A duplicate that reads better than the message it duplicates replaces it.
func (s *Server) dropDuplicates(ctx context.Context, msgs []Message, since int64) ([]Message, error) {
	type seenMsg struct {
		key, text string
		id        int64 // stored row, 0 when it is out[idx] of this batch
		idx       int
	}
	seen := map[string][]*seenMsg{}
	out := msgs[:0:0]
	for _, m := range msgs {
		entries, ok := seen[m.Region]
		if !ok {
			recent, err := s.store.Recent(ctx, m.Region, since, dedupeLookback)
			if err != nil {
				return nil, err
			}
			for _, r := range recent {
				entries = append(entries, &seenMsg{key: chatparse.Key(r.Text), text: r.Text, id: r.ID})
			}
		}
		k := chatparse.Key(m.Text)
		if k == "" {
			continue
		}
		var dup *seenMsg
		for _, e := range entries {
			if chatdiff.Similar(e.key, k) {
				dup = e
				break
			}
		}
		switch {
		case dup == nil:
			out = append(out, m)
			entries = append(entries, &seenMsg{key: k, text: m.Text, idx: len(out) - 1})
		case betterReading(dup.text, m.Text):
			if dup.id == 0 {
				m.Replaces = out[dup.idx].Replaces
				out[dup.idx] = m
			} else {
				m.Replaces = dup.id
				out = append(out, m)
				dup.id, dup.idx = 0, len(out)-1
			}
			dup.key, dup.text = k, m.Text
		}
		seen[m.Region] = entries
	}
	return out, nil
}

// betterReading reports whether b is a cleaner OCR reading of the same message as a:
// same channel and time, near-identical header, and a higher chatparse.Quality.
func betterReading(a, b string) bool {
	ha, _, okA := chatparse.Split(a)
	hb, _, okB := chatparse.Split(b)
	switch {
	case okA && okB:
		ta, tb := strings.Fields(ha), strings.Fields(hb)
		if ta[0] != tb[0] || !chatdiff.Similar(ha, hb) {
			return false
		}
	case !okA && !okB:
		if !strings.HasPrefix(a, chatparse.NoticePrefix) || !strings.HasPrefix(b, chatparse.NoticePrefix) {
			return false
		}
	default:
		return false
	}
	return chatparse.Quality(b) > chatparse.Quality(a)
}

// cleanColors keeps per-token colours only if they are well-formed hex and no more
// than the text has tokens; anything else is dropped (the viewer falls back to the
// channel colour).
func cleanColors(colors []string, text string) []string {
	if len(colors) == 0 || len(colors) >= len(strings.Fields(text)) {
		return nil
	}
	out := make([]string, len(colors))
	for i, c := range colors {
		c = strings.ToLower(c)
		if c != "" && !hexColorRe.MatchString(c) {
			return nil
		}
		if c == "" {
			c = "-"
		}
		out[i] = c
	}
	return out
}

var hexColorRe = regexp.MustCompile(`^[0-9a-f]{6}$`)

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
	// Viewers asking for the same page within a second or two share one CDN response.
	if q.Has("after") {
		s.cachePublic(w, 1)
	} else {
		s.cachePublic(w, 2)
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
	s.cachePublic(w, 10)
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

const (
	// Viewers send a heartbeat every 15 s; one missed beat is tolerated.
	presenceActive = 45 * time.Second
	presenceStale  = time.Hour
)

var viewerIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func (s *Server) presence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || !viewerIDRe.MatchString(req.ID) {
		http.Error(w, "invalid viewer id", http.StatusBadRequest)
		return
	}
	now := time.Now()
	active := now.Add(-presenceActive).UnixMilli()
	n, err := s.store.Presence(r.Context(), req.ID, now.UnixMilli(), active, now.Add(-presenceStale).UnixMilli())
	if err != nil {
		log.Printf("presence: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	clients, regions, err := s.store.Capturing(r.Context(), active)
	if err != nil {
		log.Printf("capturing: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"online": n, "clients": clients, "regions": regions})
}

const maxHeartbeatRegions = 32

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client  string   `json:"client"`
		Regions []string `json:"regions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	client, err := clean(req.Client, maxClientRunes, false)
	if err != nil || len(req.Regions) == 0 || len(req.Regions) > maxHeartbeatRegions {
		http.Error(w, "invalid heartbeat", http.StatusBadRequest)
		return
	}
	regions := make([]string, len(req.Regions))
	for i, reg := range req.Regions {
		if regions[i], err = clean(reg, maxRegionRunes, false); err != nil {
			http.Error(w, "invalid region", http.StatusBadRequest)
			return
		}
	}
	now := time.Now()
	if err := s.store.Heartbeat(r.Context(), client, regions, now.UnixMilli(), now.Add(-presenceStale).UnixMilli()); err != nil {
		log.Printf("heartbeat: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const (
	maxGlyphReport  = 100
	maxGlyphCols    = 16
	maxContextRunes = 300
	maxLabelRunes   = 4
	glyphListLimit  = 300
)

var glyphHashRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (s *Server) addGlyphs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Glyphs []Glyph `json:"glyphs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.Glyphs) == 0 || len(req.Glyphs) > maxGlyphReport {
		http.Error(w, "invalid glyph count", http.StatusBadRequest)
		return
	}
	for i, g := range req.Glyphs {
		region, rerr := clean(g.Region, maxRegionRunes, false)
		context, cerr := clean(g.Context, maxContextRunes, false)
		ok := rerr == nil && cerr == nil && glyphHashRe.MatchString(g.Hash) && hexColorRe.MatchString(g.Color) &&
			len(g.Cols) > 0 && len(g.Cols) <= maxGlyphCols && g.Count > 0 && g.Count < 1<<31
		for _, c := range g.Cols {
			ok = ok && c < 1<<24
		}
		if !ok {
			http.Error(w, fmt.Sprintf("glyphs[%d]: invalid", i), http.StatusBadRequest)
			return
		}
		req.Glyphs[i].Region, req.Glyphs[i].Context, req.Glyphs[i].Label = region, context, ""
	}
	if err := s.store.AddGlyphs(r.Context(), req.Glyphs, time.Now().UnixMilli()); err != nil {
		log.Printf("glyphs: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGlyphs(w http.ResponseWriter, r *http.Request) {
	gs, err := s.store.Glyphs(r.Context(), r.URL.Query().Get("labelled") == "1", glyphListLimit)
	if err != nil {
		log.Printf("glyphs: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("Authorization") == "" {
		s.cachePublic(w, 30)
	}
	writeJSON(w, http.StatusOK, gs)
}

func (s *Server) labelGlyph(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	var req struct {
		Label string `json:"label"`
	}
	if !glyphHashRe.MatchString(hash) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	label, err := clean(req.Label, maxLabelRunes, true)
	if err != nil || strings.TrimSpace(label) != label {
		http.Error(w, "invalid label", http.StatusBadRequest)
		return
	}
	found, err := s.store.LabelGlyph(r.Context(), hash, label)
	if err != nil {
		log.Printf("label glyph: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "unknown glyph", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
