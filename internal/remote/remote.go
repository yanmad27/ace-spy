// Package remote checks in with the server while the app is open, even when nothing is
// capturing, and runs the few fixed maintenance actions the server queues for this client.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	checkinEvery = time.Minute
	requestLimit = 15 * time.Second
	resultTries  = 3
)

type command struct {
	ID     int64  `json:"id"`
	Action string `json:"action"`
}

type Agent struct {
	checkinURL string
	resultBase string
	token      string
	client     string
	version    string
	http       *http.Client
	logf       func(string, ...any)
	status     func() string
	execute    func(string) string
}

func New(serverURL, token, client, version string, logf func(string, ...any)) *Agent {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	base := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	return &Agent{
		checkinURL: base + "/api/checkin",
		resultBase: base + "/api/commands/",
		token:      strings.TrimSpace(token),
		client:     client,
		version:    version,
		http:       &http.Client{Timeout: requestLimit},
		logf:       logf,
		status:     anydeskStatus,
		execute:    Execute,
	}
}

// Execute runs one of the known actions; anything else is refused.
func Execute(action string) string {
	switch action {
	case "anydesk-status":
		return anydeskStatus()
	case "anydesk-start":
		return anydeskStart(false, false)
	case "anydesk-restart":
		return anydeskStart(true, false)
	case "anydesk-killall":
		return anydeskStart(true, true)
	case "parsec-start":
		return parsecStart()
	default:
		return "refused: unknown action " + strconv.Quote(action)
	}
}

func (a *Agent) Run(ctx context.Context) {
	failing := false
	for {
		err := a.once(ctx)
		if err != nil && !failing && ctx.Err() == nil {
			a.logf("Check-in failed: %v", err)
		}
		failing = err != nil
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkinEvery):
		}
	}
}

func (a *Agent) once(ctx context.Context) error {
	var resp struct {
		Commands []command `json:"commands"`
	}
	if err := a.post(ctx, a.checkinURL, map[string]string{
		"client": a.client, "version": a.version, "status": a.status(),
	}, &resp); err != nil {
		return err
	}
	for _, c := range resp.Commands {
		a.logf("Remote command #%d: %s", c.ID, c.Action)
		out := a.execute(c.Action)
		a.logf("Remote command #%d: %s", c.ID, out)
		url := a.resultBase + strconv.FormatInt(c.ID, 10) + "/result"
		body := map[string]string{"client": a.client, "result": out}
		var err error
		for try := 0; try < resultTries; try++ {
			if err = a.post(ctx, url, body, nil); err == nil || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(try+1) * 2 * time.Second):
			}
		}
		if err != nil {
			a.logf("Remote command #%d: report failed: %v", c.ID, err)
		}
	}
	return nil
}

func (a *Agent) post(ctx context.Context, url string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
}
