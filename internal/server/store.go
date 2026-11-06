package server

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID         int64  `json:"id"`
	Client     string `json:"client"`
	Region     string `json:"region"`
	Text       string `json:"text"`
	CapturedAt int64  `json:"ts"`
	ReceivedAt int64  `json:"receivedAt"`
}

type RegionStat struct {
	Region string `json:"region"`
	Count  int64  `json:"count"`
	LastTS int64  `json:"lastTs"`
}

type Store struct {
	pool *pgxpool.Pool
}

const migrationLockID = 0x41636553707931

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	client      TEXT   NOT NULL,
	region      TEXT   NOT NULL,
	text        TEXT   NOT NULL,
	captured_at BIGINT NOT NULL,
	received_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_region_id ON messages(region, id);
CREATE INDEX IF NOT EXISTS idx_messages_dedupe ON messages(region, text, received_at);
CREATE INDEX IF NOT EXISTS idx_messages_received ON messages(received_at);
-- No policies: blocks Supabase/PostgREST anon access; the owning role used by this server is exempt.
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
`

func OpenStore(ctx context.Context, databaseURL string) (*Store, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	q := u.Query()
	q.Del("supa") // Supabase pooler marker; Postgres rejects it as a runtime parameter.
	u.RawQuery = q.Encode()
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Transaction-mode poolers (Supabase :6543, Neon -pooler) don't keep named prepared statements.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.MaxConns = 5
	cfg.MaxConnIdleTime = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

// migrate is safe to run concurrently from several cold-starting instances.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLockID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

func (s *Store) Close() { s.pool.Close() }

const insertSQL = `
INSERT INTO messages (client, region, text, captured_at, received_at)
SELECT t.client, t.region, t.text, t.captured_at, t.received_at
FROM unnest($1::text[], $2::text[], $3::text[], $4::bigint[], $5::bigint[])
	WITH ORDINALITY AS t(client, region, text, captured_at, received_at, ord)
WHERE $6::bigint = 0 OR NOT EXISTS (
	SELECT 1 FROM messages m
	WHERE m.region = t.region AND m.text = t.text AND m.received_at >= $6::bigint
)
ORDER BY t.ord
RETURNING id, client, region, text, captured_at, received_at`

// Insert stores messages in one round trip, skipping any (region, text) already
// received at or after dedupeSince (0 disables dedupe). Returned rows are ordered by id.
func (s *Store) Insert(ctx context.Context, msgs []Message, dedupeSince int64) ([]Message, error) {
	if dedupeSince > 0 {
		msgs = uniqueByRegionText(msgs)
	}
	n := len(msgs)
	clients, regions, texts := make([]string, n), make([]string, n), make([]string, n)
	captured, received := make([]int64, n), make([]int64, n)
	for i, m := range msgs {
		clients[i], regions[i], texts[i], captured[i], received[i] = m.Client, m.Region, m.Text, m.CapturedAt, m.ReceivedAt
	}
	out, err := s.query(ctx, insertSQL, clients, regions, texts, captured, received, dedupeSince)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func uniqueByRegionText(msgs []Message) []Message {
	seen := make(map[[2]string]bool, len(msgs))
	out := msgs[:0:0]
	for _, m := range msgs {
		k := [2]string{m.Region, m.Text}
		if !seen[k] {
			seen[k] = true
			out = append(out, m)
		}
	}
	return out
}

// Before returns up to limit messages with id < beforeID (0 = latest), oldest first.
func (s *Store) Before(ctx context.Context, region string, beforeID int64, limit int) ([]Message, error) {
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	msgs, err := s.query(ctx, `
		SELECT id, client, region, text, captured_at, received_at FROM messages
		WHERE id < $1 AND ($2 = '' OR region = $2)
		ORDER BY id DESC LIMIT $3`, beforeID, region, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// After returns up to limit messages with id > afterID, oldest first.
func (s *Store) After(ctx context.Context, region string, afterID int64, limit int) ([]Message, error) {
	return s.query(ctx, `
		SELECT id, client, region, text, captured_at, received_at FROM messages
		WHERE id > $1 AND ($2 = '' OR region = $2)
		ORDER BY id ASC LIMIT $3`, afterID, region, limit)
}

func (s *Store) query(ctx context.Context, sql string, args ...any) ([]Message, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		err := r.Scan(&m.ID, &m.Client, &m.Region, &m.Text, &m.CapturedAt, &m.ReceivedAt)
		return m, err
	})
	if out == nil {
		out = []Message{}
	}
	return out, err
}

func (s *Store) Regions(ctx context.Context) ([]RegionStat, error) {
	rows, err := s.pool.Query(ctx, `SELECT region, COUNT(*), MAX(captured_at) FROM messages GROUP BY region ORDER BY region`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RegionStat, error) {
		var st RegionStat
		err := r.Scan(&st.Region, &st.Count, &st.LastTS)
		return st, err
	})
	if out == nil {
		out = []RegionStat{}
	}
	return out, err
}

func (s *Store) Prune(ctx context.Context, olderThan int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM messages WHERE received_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
