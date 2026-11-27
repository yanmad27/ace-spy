package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOnceRunsQueuedCommandsAndReports(t *testing.T) {
	var mu sync.Mutex
	var checkin map[string]string
	results := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/checkin":
			checkin = body
			w.Write([]byte(`{"commands":[{"id":7,"action":"anydesk-restart"},{"id":8,"action":"format c:"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/commands/"):
			results[r.URL.Path] = body["result"]
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := New(srv.URL+"/", " tok ", "home", "0.3.19", nil)
	a.status = func() string { return "service=running" }
	var ran []string
	a.execute = func(action string) string {
		ran = append(ran, action)
		return Execute(action)
	}
	if err := a.once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checkin["client"] != "home" || checkin["version"] != "0.3.19" || checkin["status"] != "service=running" {
		t.Fatalf("checkin body %v", checkin)
	}
	if len(ran) != 2 {
		t.Fatalf("ran %v", ran)
	}
	if !strings.HasPrefix(results["/api/commands/8/result"], "refused") {
		t.Fatalf("unknown action result %q", results["/api/commands/8/result"])
	}
	if _, ok := results["/api/commands/7/result"]; !ok {
		t.Fatalf("no result for 7: %v", results)
	}
}

func TestOnceReportsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()
	a := New(srv.URL, "bad", "home", "x", nil)
	if err := a.once(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
