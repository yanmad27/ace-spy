package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxStatusRunes = 2000
	maxResultRunes = 4000
	// A command not picked up by then is dropped, so a stale request never fires days later.
	commandTTL     = 15 * time.Minute
	recentCommands = 20
)

// Actions clients accept. Anything else is rejected before it is queued.
var controlActions = map[string]bool{
	"anydesk-status":  true,
	"anydesk-start":   true,
	"anydesk-restart": true,
	"anydesk-killall": true,
	"anydesk-diag":    true,
	"parsec-start":    true,
}

const controlSchema = `
CREATE TABLE IF NOT EXISTS checkins (
	client  TEXT   PRIMARY KEY,
	version TEXT   NOT NULL,
	status  TEXT   NOT NULL,
	seen_at BIGINT NOT NULL
);
ALTER TABLE checkins ENABLE ROW LEVEL SECURITY;
CREATE TABLE IF NOT EXISTS commands (
	id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	client     TEXT   NOT NULL,
	action     TEXT   NOT NULL,
	created_at BIGINT NOT NULL,
	taken_at   BIGINT NOT NULL DEFAULT 0,
	done_at    BIGINT NOT NULL DEFAULT 0,
	result     TEXT   NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_commands_pending ON commands(client) WHERE taken_at = 0;
ALTER TABLE commands ENABLE ROW LEVEL SECURITY;
`

type Checkin struct {
	Client  string `json:"client"`
	Version string `json:"version"`
	Status  string `json:"status"`
	SeenAt  int64  `json:"seenAt"`
}

type Command struct {
	ID        int64  `json:"id"`
	Client    string `json:"client,omitempty"`
	Action    string `json:"action"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	TakenAt   int64  `json:"takenAt,omitempty"`
	DoneAt    int64  `json:"doneAt,omitempty"`
	Result    string `json:"result,omitempty"`
}

// Checkin records that client is running and hands it the commands queued since expireBefore.
func (s *Store) Checkin(ctx context.Context, c Checkin, expireBefore int64) ([]Command, error) {
	var cmds []Command
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO checkins (client, version, status, seen_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (client) DO UPDATE SET version = EXCLUDED.version, status = EXCLUDED.status, seen_at = EXCLUDED.seen_at`,
			c.Client, c.Version, c.Status, c.SeenAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE commands SET taken_at = $2, done_at = $2, result = 'expired'
			WHERE client = $1 AND taken_at = 0 AND created_at < $3`, c.Client, c.SeenAt, expireBefore); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			UPDATE commands SET taken_at = $2
			WHERE client = $1 AND taken_at = 0
			RETURNING id, action`, c.Client, c.SeenAt)
		if err != nil {
			return err
		}
		cmds, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Command, error) {
			var c Command
			return c, r.Scan(&c.ID, &c.Action)
		})
		return err
	})
	return cmds, err
}

// CommandResult stores the outcome of a command the client took; false if there is no such open command.
func (s *Store) CommandResult(ctx context.Context, id int64, client, result string, now int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE commands SET done_at = $4, result = $3
		WHERE id = $1 AND client = $2 AND taken_at > 0 AND done_at = 0`, id, client, result, now)
	return tag.RowsAffected() == 1, err
}

func (s *Store) AddCommand(ctx context.Context, client, action string, now int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO commands (client, action, created_at) VALUES ($1, $2, $3) RETURNING id`,
		client, action, now).Scan(&id)
	return id, err
}

func (s *Store) ControlState(ctx context.Context) ([]Checkin, []Command, error) {
	rows, err := s.pool.Query(ctx, `SELECT client, version, status, seen_at FROM checkins ORDER BY seen_at DESC`)
	if err != nil {
		return nil, nil, err
	}
	clients, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Checkin])
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT id, client, action, created_at, taken_at, done_at, result
		FROM commands ORDER BY id DESC LIMIT $1`, recentCommands)
	if err != nil {
		return nil, nil, err
	}
	cmds, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Command])
	return clients, cmds, err
}

// controlAuth guards the endpoints that queue commands; they do not exist without CONTROL_TOKEN.
func (s *Server) controlAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.ControlToken == "" {
			http.NotFound(w, r)
			return
		}
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.ControlToken)) != 1 {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) checkin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client  string `json:"client"`
		Version string `json:"version"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	var c Checkin
	var err error
	if c.Client, err = clean(req.Client, maxClientRunes, false); err != nil {
		http.Error(w, "invalid client", http.StatusBadRequest)
		return
	}
	c.Version, _ = clean(req.Version, 32, true)
	if c.Status, err = clean(req.Status, maxStatusRunes, true); err != nil {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	now := time.Now()
	c.SeenAt = now.UnixMilli()
	cmds, err := s.store.Checkin(r.Context(), c, now.Add(-commandTTL).UnixMilli())
	if err != nil {
		log.Printf("checkin: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if cmds == nil {
		cmds = []Command{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds})
}

func (s *Server) commandResult(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		Client string `json:"client"`
		Result string `json:"result"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	client, err := clean(req.Client, maxClientRunes, false)
	if err != nil {
		http.Error(w, "invalid client", http.StatusBadRequest)
		return
	}
	result := strings.ToValidUTF8(req.Result, "?")
	if rs := []rune(result); len(rs) > maxResultRunes {
		result = string(rs[:maxResultRunes])
	}
	ok, err := s.store.CommandResult(r.Context(), id, client, result, time.Now().UnixMilli())
	if err != nil {
		log.Printf("command result: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such open command", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client string `json:"client"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	client, err := clean(req.Client, maxClientRunes, false)
	if err != nil {
		http.Error(w, "invalid client", http.StatusBadRequest)
		return
	}
	if !controlActions[req.Action] {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	id, err := s.store.AddCommand(r.Context(), client, req.Action, time.Now().UnixMilli())
	if err != nil {
		log.Printf("add command: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) controlState(w http.ResponseWriter, r *http.Request) {
	clients, cmds, err := s.store.ControlState(r.Context())
	if err != nil {
		log.Printf("control state: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"now": time.Now().UnixMilli(), "clients": clients, "commands": cmds})
}
