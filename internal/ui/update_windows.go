package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/yanmad27/ace-spy/internal/config"
	"github.com/yanmad27/ace-spy/internal/updater"
)

const (
	updateRepo     = "yanmad27/ace-spy"
	updateInterval = 30 * time.Minute
	firstCheck     = 10 * time.Second
)

// autoUpdate checks GitHub Releases periodically; when a newer version exists it
// downloads and verifies it in the background, then swaps files and restarts.
func (a *app) autoUpdate() {
	c := updater.New(updateRepo)
	appDir := filepath.Dir(a.cfgPath)
	wait := firstCheck
	for {
		time.Sleep(wait)
		wait = updateInterval
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		rel, err := c.Latest(ctx)
		if err != nil || !updater.Newer(rel.Version, a.version) {
			cancel()
			continue
		}
		a.asyncLogf("Downloading update v%s…", rel.Version)
		stage, err := c.Stage(ctx, rel, appDir)
		cancel()
		if err != nil {
			a.asyncLogf("Update v%s failed: %v", rel.Version, err)
			continue
		}
		a.mw.Synchronize(func() { a.applyUpdate(rel.Version, appDir, stage) })
		return
	}
}

func (a *app) applyUpdate(version, appDir, stage string) {
	wasRunning := a.cancel != nil
	stopped := a.stopped
	a.stop()
	if wasRunning && stopped != nil {
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
		}
	}
	a.engineMu.Lock()
	if a.engine != nil {
		a.engine.Close()
		a.engine = nil
	}
	a.engineMu.Unlock()

	if err := updater.Install(appDir, stage); err != nil {
		a.logf("Update v%s failed: %v", version, err)
		if wasRunning {
			a.start()
		}
		return
	}
	a.readFields()
	if err := config.Save(a.cfgPath, a.cfg); err != nil {
		a.logf("Save config failed: %v", err)
	}
	args := []string{"--updated"}
	if wasRunning {
		args = append(args, "--start")
	}
	exe, err := os.Executable()
	if err == nil {
		err = exec.Command(exe, args...).Start()
	}
	if err != nil {
		a.logf("Updated to v%s; restart Ace Spy manually (%v)", version, err)
		return
	}
	os.Exit(0)
}
