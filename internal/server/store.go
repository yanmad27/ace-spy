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
	// Replaces is the id of an earlier, worse OCR reading of this message, now hidden.
	Replaces int64 `json:"replaces,omitempty"`
	// revises is the client's earlier reading this one supersedes (ingest only).
	revises string
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
ALTER TABLE messages ADD COLUMN IF NOT EXISTS replaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT false;
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
CREATE TABLE IF NOT EXISTS viewers (
	id      TEXT   PRIMARY KEY,
	seen_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_viewers_seen ON viewers(seen_at);
ALTER TABLE viewers ADD COLUMN IF NOT EXISTS first_seen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE viewers ENABLE ROW LEVEL SECURITY;
CREATE TABLE IF NOT EXISTS capturers (
	client  TEXT   NOT NULL,
	region  TEXT   NOT NULL,
	seen_at BIGINT NOT NULL,
	PRIMARY KEY (client, region)
);
ALTER TABLE capturers ENABLE ROW LEVEL SECURITY;
CREATE TABLE IF NOT EXISTS glyphs (
	hash       TEXT   PRIMARY KEY,
	cols       BIGINT[] NOT NULL,
	color      TEXT   NOT NULL,
	context    TEXT   NOT NULL,
	region     TEXT   NOT NULL,
	count      BIGINT NOT NULL,
	label      TEXT   NOT NULL DEFAULT '',
	first_seen BIGINT NOT NULL,
	last_seen  BIGINT NOT NULL
);
ALTER TABLE glyphs ENABLE ROW LEVEL SECURITY;
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
INSERT INTO messages (client, region, text, captured_at, received_at, colors, replaces)
SELECT t.client, t.region, t.text, t.captured_at, t.received_at, t.colors, t.replaces
FROM unnest($1::text[], $2::text[], $3::text[], $4::bigint[], $5::bigint[], $6::text[], $7::bigint[])
	WITH ORDINALITY AS t(client, region, text, captured_at, received_at, colors, replaces, ord)
ORDER BY t.ord
RETURNING ` + msgCols

const msgCols = `id, client, region, text, captured_at, received_at, colors, replaces`

// Insert stores messages and hides the rows they replace, in one transaction.
// Returned rows are ordered by id.
func (s *Store) Insert(ctx context.Context, msgs []Message) ([]Message, error) {
	n := len(msgs)
	clients, regions, texts := make([]string, n), make([]string, n), make([]string, n)
	captured, received := make([]int64, n), make([]int64, n)
	colors := make([]string, n)
	replaces := make([]int64, n)
	var hide []int64
	for i, m := range msgs {
		clients[i], regions[i], texts[i], captured[i], received[i] = m.Client, m.Region, m.Text, m.CapturedAt, m.ReceivedAt
		colors[i] = strings.Join(m.Colors, " ")
		replaces[i] = m.Replaces
		if m.Replaces > 0 {
			hide = append(hide, m.Replaces)
		}
	}
	var out []Message
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, insertSQL, clients, regions, texts, captured, received, colors, replaces)
		if err != nil {
			return err
		}
		if out, err = collectMessages(rows); err != nil {
			return err
		}
		if len(hide) > 0 {
			_, err = tx.Exec(ctx, `UPDATE messages SET hidden = true WHERE id = ANY($1)`, hide)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Recent returns up to limit visible messages (id and text only) in region received at
// or after since, newest first.
func (s *Store) Recent(ctx context.Context, region string, since int64, limit int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, text FROM messages WHERE region = $1 AND received_at >= $2 AND NOT hidden
		ORDER BY id DESC LIMIT $3`, region, since, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		err := r.Scan(&m.ID, &m.Text)
		return m, err
	})
}

// Before returns up to limit messages with id < beforeID (0 = latest), oldest first.
func (s *Store) Before(ctx context.Context, region string, beforeID int64, limit int) ([]Message, error) {
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	msgs, err := s.query(ctx, `
		SELECT `+msgCols+` FROM messages
		WHERE id < $1 AND ($2 = '' OR region = $2) AND NOT hidden
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
		SELECT `+msgCols+` FROM messages
		WHERE id > $1 AND ($2 = '' OR region = $2) AND NOT hidden
		ORDER BY id ASC LIMIT $3`, afterID, region, limit)
}

func (s *Store) query(ctx context.Context, sql string, args ...any) ([]Message, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}

func collectMessages(rows pgx.Rows) ([]Message, error) {
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		var colors string
		err := r.Scan(&m.ID, &m.Client, &m.Region, &m.Text, &m.CapturedAt, &m.ReceivedAt, &colors, &m.Replaces)
		m.Colors = strings.Fields(colors)
		return m, err
	})
	if out == nil {
		out = []Message{}
	}
	return out, err
}

func (s *Store) Regions(ctx context.Context) ([]RegionStat, error) {
	rows, err := s.pool.Query(ctx, `SELECT region, COUNT(*), MAX(captured_at) FROM messages WHERE NOT hidden GROUP BY region ORDER BY region`)
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

// Presence records a heartbeat from viewer id at now and returns how many viewers,
// including this one, sent a heartbeat at or after activeSince. Other viewers count
// only from their second heartbeat, which filters out link-preview bots and pages
// closed within seconds. Rows older than staleBefore are deleted on the way.
func (s *Store) Presence(ctx context.Context, id string, now, activeSince, staleBefore int64) (int64, error) {
	var others int64
	err := s.pool.QueryRow(ctx, `
		WITH up AS (
			INSERT INTO viewers (id, seen_at, first_seen) VALUES ($1, $2, $2)
			ON CONFLICT (id) DO UPDATE SET seen_at = EXCLUDED.seen_at
		), gone AS (
			DELETE FROM viewers WHERE seen_at < $4
		)
		SELECT COUNT(*) FROM viewers WHERE seen_at >= $3 AND seen_at > first_seen AND id <> $1`,
		id, now, activeSince, staleBefore).Scan(&others)
	return others + 1, err
}

// Heartbeat records that client is capturing regions now; rows older than staleBefore go.
func (s *Store) Heartbeat(ctx context.Context, client string, regions []string, now, staleBefore int64) error {
	_, err := s.pool.Exec(ctx, `
		WITH up AS (
			INSERT INTO capturers (client, region, seen_at)
			SELECT $1, r, $3 FROM unnest($2::text[]) AS r
			ON CONFLICT (client, region) DO UPDATE SET seen_at = EXCLUDED.seen_at
		)
		DELETE FROM capturers WHERE seen_at < $4`, client, regions, now, staleBefore)
	return err
}

// Capturing returns the number of clients and the regions with a heartbeat at or after since.
func (s *Store) Capturing(ctx context.Context, since int64) (clients int64, regions []string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT client), COALESCE(array_agg(DISTINCT region ORDER BY region), '{}')
		FROM capturers WHERE seen_at >= $1`, since).Scan(&clients, &regions)
	return clients, regions, err
}

// Glyph is an unknown glyph reported by clients, optionally labelled with its text.
type Glyph struct {
	Hash      string   `json:"hash"`
	Cols      []uint32 `json:"cols"`
	Color     string   `json:"color"`
	Context   string   `json:"context"`
	Region    string   `json:"region"`
	Count     int64    `json:"count"`
	Label     string   `json:"label"`
	FirstSeen int64    `json:"firstSeen"`
	LastSeen  int64    `json:"lastSeen"`
}

// AddGlyphs records sightings of unknown glyphs, adding to their counts.
func (s *Store) AddGlyphs(ctx context.Context, gs []Glyph, now int64) error {
	batch := &pgx.Batch{}
	for _, g := range gs {
		cols := make([]int64, len(g.Cols))
		for i, c := range g.Cols {
			cols[i] = int64(c)
		}
		batch.Queue(`
			INSERT INTO glyphs (hash, cols, color, context, region, count, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
			ON CONFLICT (hash) DO UPDATE SET count = glyphs.count + EXCLUDED.count,
				context = EXCLUDED.context, last_seen = EXCLUDED.last_seen`,
			g.Hash, cols, g.Color, g.Context, g.Region, g.Count, now)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

// Glyphs lists unknown glyphs, unlabelled and most frequent first; labelled limits
// the list to glyphs with a label.
func (s *Store) Glyphs(ctx context.Context, labelled bool, limit int) ([]Glyph, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT hash, cols, color, context, region, count, label, first_seen, last_seen FROM glyphs
		WHERE NOT $1 OR label <> ''
		ORDER BY label <> '', count DESC LIMIT $2`, labelled, limit)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Glyph, error) {
		var g Glyph
		var cols []int64
		err := r.Scan(&g.Hash, &cols, &g.Color, &g.Context, &g.Region, &g.Count, &g.Label, &g.FirstSeen, &g.LastSeen)
		for _, c := range cols {
			g.Cols = append(g.Cols, uint32(c))
		}
		return g, err
	})
	if out == nil {
		out = []Glyph{}
	}
	return out, err
}

// LabelGlyph sets (or with "" clears) the text of a glyph; false if hash is unknown.
func (s *Store) LabelGlyph(ctx context.Context, hash, label string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE glyphs SET label = $2 WHERE hash = $1`, hash, label)
	return tag.RowsAffected() == 1, err
}

// LatestID is the id of the newest message, 0 when there is none.
func (s *Store) LatestID(ctx context.Context) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM messages`).Scan(&id)
	return id, err
}
