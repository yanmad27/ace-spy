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
	if _, err := store.pool.Exec(ctx, `TRUNCATE messages RESTART IDENTITY`); err != nil {
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
	body := `{"client":"pc","messages":[{"region":"Guild","text":"xin chào","ts":1},{"region":"Guild","text":"xin chào"},{"region":"Map","text":"đi săn"}]}`
	if r := do(t, "POST", ingest, "Bearer "+token, body); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d", r.StatusCode)
	}
	if r := do(t, "POST", ingest, "Bearer "+token, `{"client":"pc2","messages":[{"region":"Guild","text":"xin chào"},{"region":"Guild","text":"mới"}]}`); r.StatusCode != http.StatusOK {
		t.Fatalf("ingest 2: %d", r.StatusCode)
	}

	var msgs []Message
	getJSON(t, srv.URL+"/api/messages?region=Guild", &msgs)
	if len(msgs) != 2 || msgs[0].Text != "xin chào" || msgs[0].CapturedAt != 1 || msgs[1].Text != "mới" {
		t.Fatalf("messages: %+v", msgs)
	}
	var after []Message
	getJSON(t, srv.URL+"/api/messages?after="+itoa(msgs[0].ID), &after)
	if len(after) != 2 || after[0].Text != "đi săn" || after[1].Text != "mới" {
		t.Fatalf("after: %+v", after)
	}
	var regions []RegionStat
	getJSON(t, srv.URL+"/api/regions", &regions)
	if len(regions) != 2 || regions[0].Region != "Guild" || regions[0].Count != 2 || regions[1].Region != "Map" {
		t.Fatalf("regions: %+v", regions)
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
	do(t, "POST", srv.URL+"/api/ingest", "Bearer "+token, `{"messages":[{"region":"Guild","text":"x"}]}`)
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
