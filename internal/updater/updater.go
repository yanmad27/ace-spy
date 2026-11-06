// Package updater self-updates the client from GitHub Releases: it finds the latest
// release, downloads the Windows zip, verifies its SHA-256 against SHA256SUMS.txt,
// stages it and swaps it in next to the running executable.
package updater

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxZipBytes   = 300 << 20
	oldSuffix     = ".old"
	stageDirName  = ".update"
	assetPrefix   = "ace-spy-"
	assetSuffix   = "-windows-amd64.zip"
	sumsAssetName = "SHA256SUMS.txt"
)

type Release struct {
	Version string
	ZipName string
	ZipURL  string
	SumsURL string
}

type Client struct {
	APIBase string // e.g. https://api.github.com
	Repo    string // owner/name
	HTTP    *http.Client
}

func New(repo string) *Client {
	return &Client{APIBase: "https://api.github.com", Repo: repo, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Latest returns the newest published release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIBase+"/repos/"+c.Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, err
	}
	rel := Release{Version: strings.TrimPrefix(body.TagName, "v")}
	for _, a := range body.Assets {
		switch {
		case a.Name == sumsAssetName:
			rel.SumsURL = a.URL
		case strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, assetSuffix):
			rel.ZipName, rel.ZipURL = a.Name, a.URL
		}
	}
	if rel.Version == "" || rel.ZipURL == "" || rel.SumsURL == "" {
		return Release{}, errors.New("latest release has no Windows zip or checksums")
	}
	return rel, nil
}

// Newer reports whether version a is newer than b (major.minor.patch).
// A non-release build such as "dev" never updates.
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Stage downloads and verifies the release and extracts it into appDir/.update.
// It returns the staging directory.
func (c *Client) Stage(ctx context.Context, rel Release, appDir string) (string, error) {
	sums, err := c.get(ctx, rel.SumsURL, 1<<16)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	want, err := checksumFor(string(sums), rel.ZipName)
	if err != nil {
		return "", err
	}
	zipPath := filepath.Join(appDir, rel.ZipName+".part")
	defer os.Remove(zipPath)
	if err := c.download(ctx, rel.ZipURL, zipPath, want); err != nil {
		return "", err
	}
	stage := filepath.Join(appDir, stageDirName)
	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}
	if err := Extract(zipPath, stage); err != nil {
		os.RemoveAll(stage)
		return "", err
	}
	if _, err := os.Stat(filepath.Join(stage, "ace-spy.exe")); err != nil {
		os.RemoveAll(stage)
		return "", errors.New("release zip has no ace-spy.exe")
	}
	return stage, nil
}

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (c *Client) download(ctx context.Context, url, dst, wantSHA string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download zip: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxZipBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxZipBytes {
		return errors.New("release zip too large")
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, wantSHA)
	}
	return nil
}

func checksumFor(sums, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", name)
}

// Extract unpacks a zip into dest, rejecting entries that escape dest.
func Extract(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		target := filepath.Join(root, filepath.FromSlash(f.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, maxZipBytes)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Install moves every top-level entry of stage into appDir. Existing entries are
// renamed to "<name>.old" first (Windows allows renaming a running exe), and are
// restored if any step fails.
func Install(appDir, stage string) error {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	type moved struct{ target, backup string }
	var done []moved
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			os.RemoveAll(done[i].target)
			if done[i].backup != "" {
				os.Rename(done[i].backup, done[i].target)
			}
		}
	}
	for _, e := range entries {
		target := filepath.Join(appDir, e.Name())
		backup := ""
		if _, err := os.Lstat(target); err == nil {
			backup = target + oldSuffix
			if err := os.RemoveAll(backup); err != nil {
				rollback()
				return fmt.Errorf("remove stale %s: %w", backup, err)
			}
			if err := os.Rename(target, backup); err != nil {
				rollback()
				return fmt.Errorf("back up %s: %w", e.Name(), err)
			}
		}
		if err := os.Rename(filepath.Join(stage, e.Name()), target); err != nil {
			if backup != "" {
				os.Rename(backup, target)
			}
			rollback()
			return fmt.Errorf("install %s: %w", e.Name(), err)
		}
		done = append(done, moved{target, backup})
	}
	return os.RemoveAll(stage)
}

// Cleanup removes leftovers of a previous update. Errors are ignored because the
// old executable may still be locked for a moment after a restart.
func Cleanup(appDir string) {
	os.RemoveAll(filepath.Join(appDir, stageDirName))
	matches, _ := filepath.Glob(filepath.Join(appDir, "*"+oldSuffix))
	for _, m := range matches {
		os.RemoveAll(m)
	}
}
