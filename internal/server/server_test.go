package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const token = "0123456789abcdef"

// Requires TEST_DATABASE_URL pointing at a disposable Postgres database; tables are truncated.
func newTestServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := OpenStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(ctx, `TRUNCATE messages, samples, viewers, capturers, glyphs, captures RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	opts.IngestToken = token
	static := fstest.MapFS{"index.html": {Data: []byte("hi")}}
	srv := httptest.NewServer(New(store, static, opts))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, auth, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestIngestAuthValidationDedupeAndQuery(t *testing.T) {
	srv := newTestServer(t, Options{Dedupe: time.Minute})
	ingest := srv.URL + "/api/ingest"

	if r := do(t, "POST", ingest, "Bearer wrong", `{"messages":[]}`); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	if r := do(t, "POST", ingest, "Bearer "+token, `{"client":"pc","messages":[{"region":"","text":"x"}]}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty region: %d", r.StatusCode)
	}
	body := `{"client":"pc","messages":[{"region":"Guild","text":"![11:01] [An] xin chào bạn","ts":1},{"region":"Guild","text":"![11:01] [An] xin chào bạn"},{"region":"Map","text":"@[11:02] [Bình] đi săn boss"}]}`
	if r := do(t, "POST", ingest, "Bearer "+token, body); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d", r.StatusCode)
	}
	if r := do(t, "POST", ingest, "Bearer "+token, `{"client":"pc2","messages":[{"region":"Guild","text":"![11:01] [An] xin chao ban"},{"region":"Guild","text":"![11:03] [An] tối nay đi đâu"}]}`); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest 2: %d", r.StatusCode)
	}

	var msgs []Message
	getJSON(t, srv.URL+"/api/messages?region=Guild", &msgs)
	if len(msgs) != 2 || msgs[0].Text != "![11:01] [An] xin chào bạn" || msgs[0].CapturedAt != 1 || msgs[1].Text != "![11:03] [An] tối nay đi đâu" {
		t.Fatalf("messages: %+v", msgs)
	}
	var after []Message
	getJSON(t, srv.URL+"/api/messages?after="+itoa(msgs[0].ID), &after)
	if len(after) != 2 || after[0].Text != "@[11:02] [Bình] đi săn boss" || after[1].Text != "![11:03] [An] tối nay đi đâu" {
		t.Fatalf("after: %+v", after)
	}
	var regions []RegionStat
	getJSON(t, srv.URL+"/api/regions", &regions)
	if len(regions) != 2 || regions[0].Region != "Guild" || regions[0].Count != 2 || regions[1].Region != "Map" {
		t.Fatalf("regions: %+v", regions)
	}
}

func TestIngestDropsNoiseAndFuzzyDuplicates(t *testing.T) {
	srv := newTestServer(t, Options{Dedupe: 10 * time.Minute})
	body := `{"client":"pc","messages":[
		{"region":"Chat","text":"^[11:10] [Tổng tư lệnh] [valHellsing] ibark AE nháy nháy cmm. chú hùa"},
		{"region":"Chat","text":"—zÁZ — —"},
		{"region":"Chat","text":"3V lễ (0 4 li ® ®% t2 ma"},
		{"region":"Chat","text":"tối nay đi săn boss không"},
		{"region":"Chat","text":"[11:10] [Tổng tư lệnh] [valHellsing] bark AE nháy nháy cmm. chú hùa"},
		{"region":"Chat","text":"[11:12] [Diana] ae có đồ xin với"}
	]}`
	if r := do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, body); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d", r.StatusCode)
	}
	do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, `{"messages":[{"region":"Chat","text":"[11:12] [Dìana] ae cố đồ xin vơi"}]}`)

	var msgs []Message
	getJSON(t, srv.URL+"/api/messages", &msgs)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %+v", msgs)
	}
}

func TestSamplesRequireTokenAndRoundTrip(t *testing.T) {
	srv := newTestServer(t, Options{})
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	body := `{"client":"pc","region":"BCU","ocr":["[14:11] [A] xin chào"],"png":"` + png + `"}`
	if r := do(t, "POST", srv.URL+"/api/samples", "", body); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", r.StatusCode)
	}
	if r := do(t, "POST", srv.URL+"/api/samples", "Bearer "+token, `{"region":"BCU","png":"aGVsbG8="}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-PNG: expected 400, got %d", r.StatusCode)
	}
	if r := do(t, "POST", srv.URL+"/api/samples", "Bearer "+token, body); r.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", r.StatusCode)
	}
	if r := do(t, "GET", srv.URL+"/api/samples", "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list without token: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/samples?region=BCU", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list []Sample
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].OCR != "[14:11] [A] xin chào" {
		t.Fatalf("list: %+v", list)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/samples/"+itoa(list[0].ID)+".png", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("png: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestColorsRoundTrip(t *testing.T) {
	srv := newTestServer(t, Options{})
	body := `{"messages":[
		{"region":"BCU","text":"^[15:47] [NGNhatAnHy] [AC] xin giap moon","colors":["ea2222","ea2222","00FF00","00ff00","00ff00"]},
		{"region":"BCU","text":"^[15:48] [A] [AC] bad colours here","colors":["red","00ff00"]},
		{"region":"BCU","text":"^[15:49] [B] [AC] too many","colors":["ea2222","ea2222","ea2222","ea2222","ea2222"]}
	]}`
	if r := do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, body); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d", r.StatusCode)
	}
	var msgs []Message
	getJSON(t, srv.URL+"/api/messages?region=BCU", &msgs)
	if len(msgs) != 3 {
		t.Fatalf("got %+v", msgs)
	}
	if strings.Join(msgs[0].Colors, " ") != "ea2222 ea2222 00ff00 00ff00 00ff00" {
		t.Errorf("colours %v", msgs[0].Colors)
	}
	if msgs[1].Colors != nil || msgs[2].Colors != nil {
		t.Errorf("invalid colours kept: %v %v", msgs[1].Colors, msgs[2].Colors)
	}
}

func TestViewAuth(t *testing.T) {
	srv := newTestServer(t, Options{ViewUser: "admin", ViewPassword: "pw"})
	if r := do(t, "GET", srv.URL+"/api/regions", "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/regions", nil)
	req.SetBasicAuth("admin", "pw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestCronPrune(t *testing.T) {
	srv := newTestServer(t, Options{Retention: time.Hour, CronSecret: "cron-secret"})
	do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, `{"messages":[{"region":"Guild","text":"![11:04] [An] tối nay đi săn"}]}`)
	if r := do(t, "GET", srv.URL+"/api/cron/prune", "Bearer nope", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", r.StatusCode)
	}
	if r := do(t, "GET", srv.URL+"/api/cron/prune", "Bearer cron-secret", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", r.StatusCode)
	}
	var msgs []Message
	getJSON(t, srv.URL+"/api/messages", &msgs)
	if len(msgs) != 1 {
		t.Fatalf("fresh message should survive prune: %+v", msgs)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestPresenceCountsDistinctViewers(t *testing.T) {
	srv := newTestServer(t, Options{})
	beat := func(id string) (int, int64) {
		resp, err := http.Post(srv.URL+"/api/presence", "application/json", strings.NewReader(`{"id":"`+id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct{ Online int64 }
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Online
	}
	a, b := "aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"
	for _, step := range []struct {
		id   string
		want int64
	}{{a, 1}, {b, 1}, {a, 1}, {b, 2}, {a, 2}} {
		if code, n := beat(step.id); code != 200 || n != step.want {
			t.Fatalf("beat(%s) = %d, %d; want 200, %d", step.id[:1], code, n, step.want)
		}
	}
	for _, bad := range []string{"short", "has space in the id!!", strings.Repeat("x", 65)} {
		if code, _ := beat(bad); code != http.StatusBadRequest {
			t.Errorf("beat(%q) = %d", bad, code)
		}
	}
}

func TestHeartbeatMakesLive(t *testing.T) {
	srv := newTestServer(t, Options{})
	state := func() (clients int64, regions []string) {
		resp, err := http.Post(srv.URL+"/api/presence", "application/json", strings.NewReader(`{"id":"cccccccccccccccccccc"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Clients int64
			Regions []string
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Clients, out.Regions
	}
	if c, r := state(); c != 0 || len(r) != 0 {
		t.Fatalf("before heartbeat: %d %v", c, r)
	}
	if resp := do(t, "POST", srv.URL+"/api/heartbeat", "", `{"client":"pc1","regions":["ANI"]}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated heartbeat = %d", resp.StatusCode)
	}
	for _, body := range []string{`{"client":"pc1","regions":["ANI","BCU"]}`, `{"client":"pc2","regions":["ANI"]}`} {
		if resp := do(t, "POST", srv.URL+"/api/heartbeat", "Bearer "+token, body); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("heartbeat = %d", resp.StatusCode)
		}
	}
	if resp := do(t, "POST", srv.URL+"/api/heartbeat", "Bearer "+token, `{"client":"pc1","regions":[]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty heartbeat = %d", resp.StatusCode)
	}
	if c, r := state(); c != 2 || strings.Join(r, ",") != "ANI,BCU" {
		t.Fatalf("after heartbeat: %d %v", c, r)
	}
	ingest := `{"client":"pc3","messages":[{"region":"ZZZ","text":"^[14:41] [DR] [SV] +5 rag ngay di ae"}]}`
	if resp := do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, ingest); resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest = %d", resp.StatusCode)
	}
	if c, r := state(); c != 3 || strings.Join(r, ",") != "ANI,BCU,ZZZ" {
		t.Fatalf("after ingest: %d %v", c, r)
	}
}

func TestBetterReadingReplacesWorse(t *testing.T) {
	srv := newTestServer(t, Options{Dedupe: 10 * time.Minute})
	ingest := func(text string) {
		body := `{"client":"pc","messages":[{"region":"BCU","text":"` + text + `"}]}`
		if r := do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, body); r.StatusCode != http.StatusOK {
			t.Fatalf("ingest %q: %d", text, r.StatusCode)
		}
	}
	ingest("^[10:25] [ChymD] [BC] tầm nàv chỉ có DGA mới níu dc")
	var first []Message
	getJSON(t, srv.URL+"/api/messages", &first)
	ingest("^[10:25] [ChymD] [BC] tầm này chỉ có DGA mới níu dc")
	ingest("^[10:25] [ChymD] [BC] tầm nàv chỉ có DGA mới níu dc") // worse again: dropped

	var msgs []Message
	getJSON(t, srv.URL+"/api/messages", &msgs)
	if len(first) != 1 || len(msgs) != 1 {
		t.Fatalf("first=%+v now=%+v", first, msgs)
	}
	if m := msgs[0]; !strings.Contains(m.Text, "này") || m.Replaces != first[0].ID {
		t.Fatalf("got %+v, want reading with này replacing %d", m, first[0].ID)
	}
	var after []Message
	getJSON(t, srv.URL+"/api/messages?after="+itoa(first[0].ID), &after)
	if len(after) != 1 || after[0].Replaces != first[0].ID {
		t.Fatalf("poll after first = %+v", after)
	}
}

func TestGlyphReportLabelAndList(t *testing.T) {
	srv := newTestServer(t, Options{})
	report := `{"client":"pc","glyphs":[{"hash":"fde638a4c98064a1","cols":[512,768,8128,8064,8128,768,512,0],"color":"ea2222","context":"^[10:25] [Mo] [BC] ab□cd","region":"BCU","count":3}]}`
	if r := do(t, "POST", srv.URL+"/api/glyphs/unknown", "", report); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report = %d", r.StatusCode)
	}
	for i := 0; i < 2; i++ {
		if r := do(t, "POST", srv.URL+"/api/glyphs/unknown", "Bearer "+token, report); r.StatusCode != http.StatusNoContent {
			t.Fatalf("report = %d", r.StatusCode)
		}
	}
	bad := strings.Replace(report, `"ea2222"`, `"red"`, 1)
	if r := do(t, "POST", srv.URL+"/api/glyphs/unknown", "Bearer "+token, bad); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad colour = %d", r.StatusCode)
	}
	var gs []Glyph
	getJSON(t, srv.URL+"/api/glyphs", &gs)
	if len(gs) != 1 || gs[0].Count != 6 || len(gs[0].Cols) != 8 || gs[0].Label != "" {
		t.Fatalf("glyphs = %+v", gs)
	}
	var learned []Glyph
	getJSON(t, srv.URL+"/api/glyphs?labelled=1", &learned)
	if len(learned) != 0 {
		t.Fatalf("labelled before labelling: %+v", learned)
	}
	if r := do(t, "POST", srv.URL+"/api/glyphs/fde638a4c98064a1/label", "", `{"label":"★"}`); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated label = %d", r.StatusCode)
	}
	if r := do(t, "POST", srv.URL+"/api/glyphs/fde638a4c98064a1/label", "Bearer "+token, `{"label":"★"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("label = %d", r.StatusCode)
	}
	if r := do(t, "POST", srv.URL+"/api/glyphs/0000000000000000/label", "Bearer "+token, `{"label":"x"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("label missing = %d", r.StatusCode)
	}
	getJSON(t, srv.URL+"/api/glyphs?labelled=1", &learned)
	if len(learned) != 1 || learned[0].Label != "★" {
		t.Fatalf("labelled = %+v", learned)
	}
}

func TestStreamDeliversMessagesAndStatus(t *testing.T) {
	srv := newTestServer(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	open := func(lastID string) (*http.Response, *bufio.Reader) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream?after=0&id=dddddddddddddddddddd", nil)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("content type %q", ct)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	// next reads one event: its name, id and data.
	next := func(br *bufio.Reader) (event, id, data string) {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "" && event != "":
				return
			case strings.HasPrefix(line, "event: "):
				event = line[7:]
			case strings.HasPrefix(line, "id: "):
				id = line[4:]
			case strings.HasPrefix(line, "data: "):
				data = line[6:]
			}
		}
	}

	resp, br := open("")
	if ev, _, data := next(br); ev != "status" || !strings.Contains(data, `"online":1`) {
		t.Fatalf("first event %q %s", ev, data)
	}
	body := `{"client":"pc","messages":[{"region":"ANI","text":"^[10:25] [ChymD] [BC] tầm này chỉ có DGA mới níu"}]}`
	if r := do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, body); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest = %d", r.StatusCode)
	}
	ev, id, data := next(br)
	for ev == "status" {
		ev, id, data = next(br)
	}
	var msgs []Message
	if ev != "message" || json.Unmarshal([]byte(data), &msgs) != nil || len(msgs) != 1 || itoa(msgs[0].ID) != id {
		t.Fatalf("message event %q id=%q %s", ev, id, data)
	}
	resp.Body.Close()

	// Reconnecting with Last-Event-ID resumes after the last message: nothing is resent.
	resp2, br2 := open(id)
	defer resp2.Body.Close()
	if ev, _, _ := next(br2); ev != "status" {
		t.Fatalf("reconnect first event %q", ev)
	}
	done := make(chan string, 1)
	go func() {
		for {
			line, err := br2.ReadString('\n')
			if err != nil {
				done <- ""
				return
			}
			if strings.HasPrefix(line, "event: ") {
				done <- strings.TrimSpace(line[7:])
				return
			}
		}
	}()
	select {
	case e := <-done:
		if e == "message" {
			t.Fatalf("message resent after reconnect")
		}
	case <-time.After(2500 * time.Millisecond):
	}
}

func TestPublicReadsAreCDNCacheable(t *testing.T) {
	srv := newTestServer(t, Options{})
	for _, path := range []string{"/api/messages", "/api/messages?after=0", "/api/regions", "/"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "s-maxage=") {
			t.Errorf("%s Cache-Control = %q", path, cc)
		}
	}
	priv := newTestServer(t, Options{ViewUser: "u", ViewPassword: "p"})
	req, _ := http.NewRequest("GET", priv.URL+"/api/messages", nil)
	req.SetBasicAuth("u", "p")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); strings.Contains(cc, "public") {
		t.Errorf("password-protected Cache-Control = %q", cc)
	}
}

func TestRevisionReplacesPartialReading(t *testing.T) {
	srv := newTestServer(t, Options{Dedupe: 10 * time.Minute})
	post := func(body string) {
		if r := do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, body); r.StatusCode != http.StatusOK {
			t.Fatalf("ingest: %d", r.StatusCode)
		}
	}
	post(`{"client":"pc","messages":[{"region":"BCU","text":"^[12:59] [Ga 89] [BHC] ani qua map bcu an pha le"}]}`)
	var first []Message
	getJSON(t, srv.URL+"/api/messages", &first)
	// Same quality, different name: only an explicit revision may replace it.
	post(`{"client":"pc","messages":[{"region":"BCU","text":"^[12:59] [Ga_89] [BHC] ani qua map bcu an pha le"}]}`)
	var msgs []Message
	getJSON(t, srv.URL+"/api/messages", &msgs)
	if len(msgs) != 1 || msgs[0].ID != first[0].ID {
		t.Fatalf("plain duplicate should be dropped: %+v", msgs)
	}
	post(`{"client":"pc","messages":[{"region":"BCU","text":"^[12:59] [Ga_89] [BHC] ani qua map bcu an pha le","revises":"^[12:59] [Ga 89] [BHC] ani qua map bcu an pha le"}]}`)
	getJSON(t, srv.URL+"/api/messages", &msgs)
	if len(msgs) != 1 || msgs[0].Text != "^[12:59] [Ga_89] [BHC] ani qua map bcu an pha le" || msgs[0].Replaces != first[0].ID {
		t.Fatalf("revision: %+v", msgs)
	}
}

func TestCaptureUploadRequiresTokenAndRoundTrips(t *testing.T) {
	srv := newTestServer(t, Options{})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte("pcapng bytes"))
	zw.Close()
	post := func(auth string, body []byte) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/captures?client=pc&note=pids%3D1", bytes.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("", buf.Bytes()); c != http.StatusUnauthorized {
		t.Fatalf("no token = %d", c)
	}
	if c := post("Bearer "+token, []byte("plain")); c != http.StatusBadRequest {
		t.Fatalf("not gzip = %d", c)
	}
	if c := post("Bearer "+token, buf.Bytes()); c != http.StatusOK {
		t.Fatalf("upload = %d", c)
	}
	if r := do(t, "GET", srv.URL+"/api/captures", "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list without token = %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/captures/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, buf.Bytes()) {
		t.Fatalf("download mismatch: %d bytes", len(got))
	}
}
