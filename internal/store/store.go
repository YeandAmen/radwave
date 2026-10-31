// Package store persists opportunities, source state, and observation history
// in SQLite. No database service or CGO is required.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("record not found")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection makes read-modify-write operations predictable and
	// keeps :memory: databases shared by all requests. WAL handles readers in
	// other processes; the busy timeout handles an occasional concurrent CLI.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than this application supports", version)
	}
	_, err := s.db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
		CREATE TABLE IF NOT EXISTS opportunities (
			id TEXT PRIMARY KEY, url TEXT NOT NULL UNIQUE, data BLOB NOT NULL,
			kind TEXT NOT NULL, region TEXT NOT NULL, deadline TEXT NOT NULL,
			deadline_state TEXT NOT NULL, published_at TEXT NOT NULL,
			first_seen_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			reactions INTEGER, search_text TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS opportunity_deadline ON opportunities(deadline);
		CREATE INDEX IF NOT EXISTS opportunity_kind ON opportunities(kind);
		CREATE INDEX IF NOT EXISTS opportunity_region ON opportunities(region);
		CREATE TABLE IF NOT EXISTS observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			opportunity_id TEXT NOT NULL REFERENCES opportunities(id) ON DELETE CASCADE,
			observed_at TEXT NOT NULL, data BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS observation_opportunity ON observations(opportunity_id, id DESC);
		CREATE TABLE IF NOT EXISTS sources (
			id TEXT PRIMARY KEY, url TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL, data BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, started_at TEXT NOT NULL, data BLOB NOT NULL);
		PRAGMA user_version = 1;
	`)
	return err
}

type UpsertResult struct {
	Opportunity model.Opportunity
	Created     bool
	Updated     bool
}

func (s *Store) Upsert(ctx context.Context, incoming model.Opportunity) (UpsertResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UpsertResult{}, err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT data FROM opportunities WHERE id = ? OR url = ? LIMIT 1", incoming.ID, incoming.URL).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return UpsertResult{}, err
	}
	created := errors.Is(err, sql.ErrNoRows)
	var old model.Opportunity
	changes := []string{"Discovered opportunity"}
	if !created {
		if err := json.Unmarshal(raw, &old); err != nil {
			return UpsertResult{}, err
		}
		incoming.ID = old.ID
		incoming.FirstSeenAt = old.FirstSeenAt
		if incoming.LastFetchedAt.Before(old.LastFetchedAt) {
			return UpsertResult{Opportunity: old}, nil
		}
		if incoming.PublishedAt == nil {
			incoming.PublishedAt = old.PublishedAt
		}
		changes = changedFields(old, incoming)
		if len(changes) == 0 {
			incoming.UpdatedAt = old.UpdatedAt
		}
	}
	incoming.Status, incoming.DaysLeft = "", nil
	encoded, err := json.Marshal(incoming)
	if err != nil {
		return UpsertResult{}, err
	}
	published := ""
	if incoming.PublishedAt != nil {
		published = stamp(*incoming.PublishedAt)
	}
	search := strings.ToLower(incoming.Title + " " + incoming.Organizer + " " + incoming.Body + " " + strings.Join(incoming.Tags, " "))
	_, err = tx.ExecContext(ctx, `INSERT INTO opportunities
		(id,url,data,kind,region,deadline,deadline_state,published_at,first_seen_at,updated_at,reactions,search_text)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET url=excluded.url,data=excluded.data,kind=excluded.kind,
		region=excluded.region,deadline=excluded.deadline,deadline_state=excluded.deadline_state,
		published_at=excluded.published_at,updated_at=excluded.updated_at,reactions=excluded.reactions,search_text=excluded.search_text`,
		incoming.ID, incoming.URL, encoded, incoming.Kind, incoming.Region, incoming.Deadline,
		incoming.DeadlineState, published, stamp(incoming.FirstSeenAt), stamp(incoming.UpdatedAt), incoming.Reactions, search)
	if err != nil {
		return UpsertResult{}, err
	}
	observation := model.Observation{
		OpportunityID: incoming.ID, ObservedAt: incoming.LastFetchedAt, Changes: changes,
		Reactions: incoming.Reactions, Comments: incoming.Comments, CaptureMethod: incoming.CaptureMethod,
	}
	if !created {
		observation.ReactionsDelta = delta(old.Reactions, incoming.Reactions)
		observation.CommentsDelta = delta(old.Comments, incoming.Comments)
	}
	encodedObservation, err := json.Marshal(observation)
	if err != nil {
		return UpsertResult{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO observations(opportunity_id,observed_at,data) VALUES(?,?,?)", incoming.ID, stamp(incoming.LastFetchedAt), encodedObservation)
	if err != nil {
		return UpsertResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return UpsertResult{}, err
	}
	incoming.Decorate(time.Now())
	return UpsertResult{Opportunity: incoming, Created: created, Updated: !created && len(changes) > 0}, nil
}

func (s *Store) Get(ctx context.Context, id string) (model.Opportunity, error) {
	return s.get(ctx, "SELECT data FROM opportunities WHERE id = ?", id)
}

func (s *Store) GetByURL(ctx context.Context, url string) (model.Opportunity, error) {
	return s.get(ctx, `SELECT data FROM opportunities WHERE url = ?1 OR (json_extract(data,'$.source_kind') IN ('linkedin_post','web_page') AND json_extract(data,'$.source_url') = ?1) LIMIT 1`, url)
}

func (s *Store) get(ctx context.Context, query string, arg string) (model.Opportunity, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, query, arg).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Opportunity{}, ErrNotFound
		}
		return model.Opportunity{}, err
	}
	var o model.Opportunity
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, err
	}
	o.Decorate(time.Now())
	return o, nil
}

func (s *Store) List(ctx context.Context, q model.Query) ([]model.Opportunity, int, error) {
	where, args := queryWhere(q, time.Now())
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM opportunities"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "CASE WHEN deadline = '' THEN 1 ELSE 0 END, deadline ASC, published_at DESC"
	switch q.Sort {
	case "newest":
		order = "COALESCE(NULLIF(published_at,''),first_seen_at) DESC"
	case "updated":
		order = "updated_at DESC"
	case "popular":
		order = "COALESCE(reactions,-1) DESC, published_at DESC"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 48
	}
	limit = min(limit, 10000)
	args = append(args, limit, max(0, q.Offset))
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM opportunities"+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []model.Opportunity{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		var o model.Opportunity
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, 0, err
		}
		o.Decorate(time.Now())
		items = append(items, o)
	}
	return items, total, rows.Err()
}

func queryWhere(q model.Query, now time.Time) (string, []any) {
	clauses, args := []string{}, []any{}
	if len(q.IDs) > 0 {
		placeholders := []string{}
		for _, id := range q.IDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		clauses = append(clauses, "id IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.Search != "" {
		clauses = append(clauses, `search_text LIKE ? ESCAPE '\'`)
		args = append(args, "%"+strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(q.Search))+"%")
	}
	if q.Kind != "" {
		kinds, placeholders := strings.Split(q.Kind, ","), []string{}
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		clauses = append(clauses, "kind IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.Region != "" {
		clauses = append(clauses, "region = ?")
		args = append(args, q.Region)
	}
	if q.Source != "" {
		clauses = append(clauses, "json_extract(data,'$.source_id') = ?")
		args = append(args, q.Source)
	}
	today := now.UTC().Format("2006-01-02")
	switch q.Status {
	case "active":
		clauses = append(clauses, "(deadline = '' OR deadline >= ?)")
		args = append(args, today)
	case "closing":
		clauses = append(clauses, "deadline >= ? AND deadline <= ?")
		args = append(args, today, now.UTC().AddDate(0, 0, 7).Format("2006-01-02"))
	case "open":
		clauses = append(clauses, "deadline > ?")
		args = append(args, now.UTC().AddDate(0, 0, 7).Format("2006-01-02"))
	case "closed":
		clauses = append(clauses, "deadline != '' AND deadline < ?")
		args = append(args, today)
	case "rolling", "unknown":
		clauses = append(clauses, "deadline_state = ?")
		args = append(args, q.Status)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *Store) History(ctx context.Context, id string) ([]model.Observation, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,data FROM observations WHERE opportunity_id = ? ORDER BY id DESC LIMIT 60", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []model.Observation{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var entry model.Observation
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		entry.ID = id
		history = append(history, entry)
	}
	return history, rows.Err()
}

func changedFields(a, b model.Opportunity) []string {
	changes := []string{}
	fields := []struct {
		name string
		a, b any
	}{
		{"Title updated", a.Title, b.Title}, {"Program details updated", a.Body, b.Body},
		{"Organizer updated", a.Organizer, b.Organizer}, {"Opportunity type updated", a.Kind, b.Kind},
		{"Discovery region updated", a.Region, b.Region}, {"Funding updated", a.Funding, b.Funding},
		{"Topics updated", a.Tags, b.Tags},
		{"Deadline updated", a.Deadline + a.DeadlineState, b.Deadline + b.DeadlineState},
		{"Eligibility updated", a.Eligibility, b.Eligibility}, {"Benefits updated", a.Benefits, b.Benefits},
		{"Application link updated", a.ApplicationURL, b.ApplicationURL}, {"Location updated", a.Location, b.Location},
		{"Reactions updated", a.Reactions, b.Reactions}, {"Comments updated", a.Comments, b.Comments},
		{"Source capture updated", a.CaptureMethod, b.CaptureMethod},
	}
	for _, field := range fields {
		if !reflect.DeepEqual(field.a, field.b) {
			changes = append(changes, field.name)
		}
	}
	return changes
}

func delta(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	d := *b - *a
	return &d
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
