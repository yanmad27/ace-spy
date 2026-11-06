package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.3.1", "0.3.0", true}, {"0.10.0", "0.9.9", true}, {"1.0.0", "0.99.99", true},
		{"0.3.0", "0.3.0", false}, {"0.2.9", "0.3.0", false}, {"0.4.0", "dev", false}, {"x", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStageAndInstall(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{
		"ace-spy.exe":              "new-exe",
		"tesseract/tesseract.exe":  "new-tess",
		"tessdata/vie.traineddata": "vie",
	})
	sum := sha256.Sum256(zipBytes)
	zipName := "ace-spy-0.4.0-windows-amd64.zip"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.4.0","assets":[{"name":%q,"browser_download_url":"%s/zip"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, zipName, srv.URL, srv.URL)
		case "/zip":
			w.Write(zipBytes)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), zipName)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	app := t.TempDir()
	os.WriteFile(filepath.Join(app, "ace-spy.exe"), []byte("old-exe"), 0o755)
	os.WriteFile(filepath.Join(app, "ace-spy.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(app, "tesseract"), 0o755)
	os.WriteFile(filepath.Join(app, "tesseract", "tesseract.exe"), []byte("old-tess"), 0o755)

	c := &Client{APIBase: srv.URL, Repo: "o/r", HTTP: srv.Client()}
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.4.0" || !Newer(rel.Version, "0.3.0") {
		t.Fatalf("release %+v", rel)
	}
	stage, err := c.Stage(context.Background(), rel, app)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(app, stage); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"ace-spy.exe":              "new-exe",
		"ace-spy.exe.old":          "old-exe",
		"tesseract/tesseract.exe":  "new-tess",
		"tessdata/vie.traineddata": "vie",
		"ace-spy.json":             "{}",
	} {
		got, err := os.ReadFile(filepath.Join(app, path))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	Cleanup(app)
	if _, err := os.Stat(filepath.Join(app, "ace-spy.exe.old")); !os.IsNotExist(err) {
		t.Errorf("old exe not cleaned up")
	}
}

func TestStageRejectsBadChecksum(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{"ace-spy.exe": "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sums" {
			fmt.Fprintf(w, "%064d  a.zip\n", 0)
			return
		}
		w.Write(zipBytes)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client()}
	_, err := c.Stage(context.Background(), Release{ZipName: "a.zip", ZipURL: srv.URL + "/zip", SumsURL: srv.URL + "/sums"}, t.TempDir())
	if err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "evil.zip")
	os.WriteFile(p, makeZip(t, map[string]string{"../evil.txt": "x"}), 0o644)
	if err := Extract(p, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected zip-slip rejection")
	}
}
