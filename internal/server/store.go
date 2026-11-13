package server

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID         int64    `json:"id"`
	Client     string   `json:"client"`
	Region     string   `json:"region"`
	Text       string   `json:"text"`
	CapturedAt int64    `json:"ts"`
	ReceivedAt int64    `json:"receivedAt"`
	Colors     []string `json:"colors,omitempty"`
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
DROP INDEX IF EXISTS idx_messages_dedupe;
CREATE INDEX IF NOT EXISTS idx_messages_received ON messages(received_at);
-- No policies: blocks Supabase/PostgREST anon access; the owning role used by this server is exempt.
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS colors TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS samples (
	id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	client     TEXT   NOT NULL,
	region     TEXT   NOT NULL,
	created_at BIGINT NOT NULL,
	ocr        TEXT   NOT NULL,
	png        BYTEA  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_region_id ON samples(region, id);
ALTER TABLE samples ENABLE ROW LEVEL SECURITY;
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
INSERT INTO messages (client, region, text, captured_at, received_at, colors)
SELECT t.client, t.region, t.text, t.captured_at, t.received_at, t.colors
FROM unnest($1::text[], $2::text[], $3::text[], $4::bigint[], $5::bigint[], $6::text[])
	WITH ORDINALITY AS t(client, region, text, captured_at, received_at, colors, ord)
ORDER BY t.ord
RETURNING id, client, region, text, captured_at, received_at, colors`

// Insert stores messages in one round trip. Returned rows are ordered by id.
func (s *Store) Insert(ctx context.Context, msgs []Message) ([]Message, error) {
	n := len(msgs)
	clients, regions, texts := make([]string, n), make([]string, n), make([]string, n)
	captured, received := make([]int64, n), make([]int64, n)
	colors := make([]string, n)
	for i, m := range msgs {
		clients[i], regions[i], texts[i], captured[i], received[i] = m.Client, m.Region, m.Text, m.CapturedAt, m.ReceivedAt
		colors[i] = strings.Join(m.Colors, " ")
	}
	out, err := s.query(ctx, insertSQL, clients, regions, texts, captured, received, colors)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// RecentTexts returns up to limit texts in region received at or after since, newest first.
func (s *Store) RecentTexts(ctx context.Context, region string, since int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT text FROM messages WHERE region = $1 AND received_at >= $2
		ORDER BY id DESC LIMIT $3`, region, since, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Before returns up to limit messages with id < beforeID (0 = latest), oldest first.
func (s *Store) Before(ctx context.Context, region string, beforeID int64, limit int) ([]Message, error) {
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	msgs, err := s.query(ctx, `
		SELECT id, client, region, text, captured_at, received_at, colors FROM messages
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
		SELECT id, client, region, text, captured_at, received_at, colors FROM messages
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
		var colors string
		err := r.Scan(&m.ID, &m.Client, &m.Region, &m.Text, &m.CapturedAt, &m.ReceivedAt, &colors)
		m.Colors = strings.Fields(colors)
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

// Sample is a captured chat image with its raw OCR output, kept for tuning OCR.
type Sample struct {
	ID        int64  `json:"id"`
	Client    string `json:"client"`
	Region    string `json:"region"`
	CreatedAt int64  `json:"createdAt"`
	OCR       string `json:"ocr"`
}

const samplesPerRegion = 50

func (s *Store) AddSample(ctx context.Context, sm Sample, png []byte) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO samples (client, region, created_at, ocr, png) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			sm.Client, sm.Region, sm.CreatedAt, sm.OCR, png).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM samples WHERE region = $1 AND id NOT IN (
			SELECT id FROM samples WHERE region = $1 ORDER BY id DESC LIMIT $2)`, sm.Region, samplesPerRegion)
		return err
	})
	return id, err
}

func (s *Store) Samples(ctx context.Context, region string, limit int) ([]Sample, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, client, region, created_at, ocr FROM samples
		WHERE ($1 = '' OR region = $1) ORDER BY id DESC LIMIT $2`, region, limit)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Sample])
	if out == nil {
		out = []Sample{}
	}
	return out, err
}

func (s *Store) SamplePNG(ctx context.Context, id int64) ([]byte, error) {
	var png []byte
	err := s.pool.QueryRow(ctx, `SELECT png FROM samples WHERE id = $1`, id).Scan(&png)
	return png, err
}

func (s *Store) Prune(ctx context.Context, olderThan int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM messages WHERE received_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
