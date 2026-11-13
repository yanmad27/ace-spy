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

// WindowTarget binds a region to a game client window. X/Y are then relative to
// that window's client area instead of the screen.
type WindowTarget struct {
	Exe   string `json:"exe"`
	Title string `json:"title"`
	PID   uint32 `json:"pid"`
}

type Region struct {
	ID         string        `json:"id"`
	Window     *WindowTarget `json:"window,omitempty"`
	Label      string        `json:"label"`
	X          int           `json:"x"`
	Y          int           `json:"y"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	IntervalMs int           `json:"intervalMs"`
	Scale      int           `json:"scale"`
	Enabled    bool          `json:"enabled"`
}

const (
	// EngineGlyph matches the game's exact chat font (Verdana 12px) pixel by pixel.
	EngineGlyph     = "glyph"
	EngineTesseract = "tesseract"
	EngineWindows   = "windows"

	currentVersion = 2
)

var Engines = []string{EngineGlyph, EngineTesseract, EngineWindows}

type Config struct {
	Version       int      `json:"version"`
	ServerURL     string   `json:"serverUrl"`
	Token         string   `json:"token"`
	ClientName    string   `json:"clientName"`
	OCREngine     string   `json:"ocrEngine"`
	Language      string   `json:"language"`
	TesseractPath string   `json:"tesseractPath,omitempty"`
	Regions       []Region `json:"regions"`
}

func Default() Config {
	host, _ := os.Hostname()
	return Config{
		ServerURL:  "https://ace-spy.vercel.app",
		Version:    currentVersion,
		ClientName: host,
		OCREngine:  EngineGlyph,
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
	case r.Window != nil && (r.X < 0 || r.Y < 0 || r.Window.Exe == ""):
		return errors.New("invalid game window binding; pick the area again")
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
	if c.OCREngine != EngineGlyph && c.OCREngine != EngineTesseract && c.OCREngine != EngineWindows {
		return fmt.Errorf("unknown OCR engine %q", c.OCREngine)
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
	cfg.Version = 0
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version < 2 && cfg.OCREngine == EngineTesseract {
		cfg.OCREngine = EngineGlyph // v2: glyph matching replaced Tesseract as the default
	}
	cfg.Version = currentVersion
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
