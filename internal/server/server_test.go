package server

import (
	"context"
	"encoding/json"
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
	if _, err := store.pool.Exec(ctx, `TRUNCATE messages, samples, viewers, capturers RESTART IDENTITY`); err != nil {
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
