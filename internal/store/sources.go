package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
)

// AddSource is idempotent and preserves an existing source's user settings.
func (s *Store) AddSource(ctx context.Context, source model.Source) (model.Source, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return source, false, err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRowContext(ctx, s.db.lockRow("SELECT data FROM sources WHERE url = ?"), source.URL).Scan(&raw)
	if err == nil {
		existing, err := decodeSource(raw)
		return existing, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return source, false, err
	}
	if source.ID == "" {
		source.ID = "source-" + model.StableID(source.URL)
	}
	if source.Status == "" {
		source.Status = "idle"
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = time.Now().UTC()
	}
	encoded, err := encodeSource(source)
	if err != nil {
		return source, false, err
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO sources(id,url,enabled,data) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", source.ID, source.URL, source.Enabled, encoded)
	if err != nil {
		return source, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return source, false, err
	} else if affected == 0 {
		if err := tx.QueryRowContext(ctx, "SELECT data FROM sources WHERE url = ?", source.URL).Scan(&raw); err != nil {
			return source, false, err
		}
		source, err = decodeSource(raw)
		if err != nil {
			return source, false, err
		}
		return source, false, tx.Commit()
	}
	return source, true, tx.Commit()
}

// ETag/LastModified are intentionally hidden from API serialization but need
// to be saved for conditional RSS requests.
type storedSource struct {
	model.Source
	ETagValue     string `json:"etag_value"`
	ModifiedValue string `json:"modified_value"`
}

func encodeSource(source model.Source) ([]byte, error) {
	return json.Marshal(storedSource{Source: source, ETagValue: source.ETag, ModifiedValue: source.LastModified})
}

func decodeSource(raw []byte) (model.Source, error) {
	var stored storedSource
	err := json.Unmarshal(raw, &stored)
	stored.Source.ETag, stored.Source.LastModified = stored.ETagValue, stored.ModifiedValue
	return stored.Source, err
}

func (s *Store) Source(ctx context.Context, id string) (model.Source, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM sources WHERE id = ?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Source{}, ErrNotFound
		}
		return model.Source{}, err
	}
	return decodeSource(raw)
}

func (s *Store) Sources(ctx context.Context) ([]model.Source, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM sources ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.Source{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		source, err := decodeSource(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

func (s *Store) ConfigureSource(ctx context.Context, id string, enabled *bool, name *string) (model.Source, error) {
	return s.modifySource(ctx, id, func(source *model.Source) {
		if enabled != nil {
			source.Enabled = *enabled
		}
		if name != nil {
			source.Name = *name
		}
	})
}

func (s *Store) RecordSourceFetch(ctx context.Context, fetched model.Source) (model.Source, error) {
	return s.modifySource(ctx, fetched.ID, func(source *model.Source) {
		source.Status, source.LastError = fetched.Status, fetched.LastError
		source.LastCrawledAt, source.NextCrawlAt = fetched.LastCrawledAt, fetched.NextCrawlAt
		source.Failures, source.ItemCount = fetched.Failures, fetched.ItemCount
		source.ETag, source.LastModified = fetched.ETag, fetched.LastModified
	})
}

func (s *Store) modifySource(ctx context.Context, id string, change func(*model.Source)) (model.Source, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Source{}, err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRowContext(ctx, s.db.lockRow("SELECT data FROM sources WHERE id = ?"), id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Source{}, ErrNotFound
		}
		return model.Source{}, err
	}
	source, err := decodeSource(raw)
	if err != nil {
		return source, err
	}
	change(&source)
	encoded, err := encodeSource(source)
	if err != nil {
		return source, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE sources SET enabled = ?, data = ? WHERE id = ?", source.Enabled, encoded, id)
	if err != nil {
		return source, err
	}
	return source, tx.Commit()
}

func (s *Store) SaveRun(ctx context.Context, run model.Run) error {
	encoded, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,started_at,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", run.ID, stamp(run.StartedAt), encoded)
	return err
}

func (s *Store) Runs(ctx context.Context) ([]model.Run, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM runs ORDER BY started_at DESC LIMIT 20")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []model.Run{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var run model.Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) RecoverRuns(ctx context.Context) error {
	runs, err := s.Runs(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == "running" {
			now := time.Now().UTC()
			run.Status, run.FinishedAt = "interrupted", &now
			run.Errors = append(run.Errors, "The previous process stopped before this crawl finished.")
			if err := s.SaveRun(ctx, run); err != nil {
				return err
			}
		}
	}
	return nil
}
