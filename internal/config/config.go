package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	MinIntervalMs = 250
	MaxIntervalMs = 60000
	MinScale      = 1
	MaxScale      = 4
	MinRegionSide = 8
	MaxLabelLen   = 64
)

type Region struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	IntervalMs int    `json:"intervalMs"`
	Scale      int    `json:"scale"`
	Enabled    bool   `json:"enabled"`
}

type Config struct {
	ServerURL  string   `json:"serverUrl"`
	Token      string   `json:"token"`
	ClientName string   `json:"clientName"`
	Language   string   `json:"language"`
	Regions    []Region `json:"regions"`
}

func Default() Config {
	host, _ := os.Hostname()
	return Config{
		ServerURL:  "http://localhost:8080",
		ClientName: host,
		Language:   "vi",
		Regions:    []Region{},
	}
}

func NewRegion() Region {
	return Region{ID: newID(), IntervalMs: 1000, Scale: 2, Enabled: true}
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (r Region) Validate() error {
	label := strings.TrimSpace(r.Label)
	switch {
	case label == "":
		return errors.New("label is required")
	case len([]rune(label)) > MaxLabelLen:
		return fmt.Errorf("label must be at most %d characters", MaxLabelLen)
	case r.Width < MinRegionSide || r.Height < MinRegionSide:
		return fmt.Errorf("area must be at least %dx%d pixels; pick an area first", MinRegionSide, MinRegionSide)
	case r.IntervalMs < MinIntervalMs || r.IntervalMs > MaxIntervalMs:
		return fmt.Errorf("interval must be between %d and %d ms", MinIntervalMs, MaxIntervalMs)
	case r.Scale < MinScale || r.Scale > MaxScale:
		return fmt.Errorf("scale must be between %d and %d", MinScale, MaxScale)
	}
	return nil
}

func (c Config) Validate() error {
	u, err := url.Parse(strings.TrimSpace(c.ServerURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("server URL must be an absolute http(s) URL")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("ingest token is required")
	}
	if strings.TrimSpace(c.Language) == "" {
		return errors.New("OCR language is required")
	}
	seenID := map[string]bool{}
	seenLabel := map[string]bool{}
	for _, r := range c.Regions {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("region %q: %w", r.Label, err)
		}
		key := strings.ToLower(strings.TrimSpace(r.Label))
		if seenLabel[key] {
			return fmt.Errorf("duplicate region label %q", r.Label)
		}
		if r.ID == "" || seenID[r.ID] {
			return fmt.Errorf("region %q has a missing or duplicate id", r.Label)
		}
		seenLabel[key], seenID[r.ID] = true, true
	}
	return nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Regions == nil {
		cfg.Regions = []Region{}
	}
	for i := range cfg.Regions {
		if cfg.Regions[i].ID == "" {
			cfg.Regions[i].ID = newID()
		}
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ace-spy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
