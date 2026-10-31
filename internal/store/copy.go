package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/model"
)

// CopyIntoEmpty copies source records and their complete observation histories
// into a fresh hosted database in one destination transaction.
func (s *Store) CopyIntoEmpty(ctx context.Context, destination *Store) (map[string]int, error) {
	if s == destination {
		return nil, errors.New("source and destination must be different databases")
	}
	source, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer source.Rollback()
	target, err := destination.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer target.Rollback()
	tables := []struct {
		name    string
		columns []string
	}{
		{"sources", []string{"id", "url", "enabled", "data"}},
		{"opportunities", []string{"id", "url", "data", "kind", "region", "deadline", "deadline_state", "published_at", "first_seen_at", "updated_at", "reactions", "search_text"}},
		{"observations", []string{"id", "opportunity_id", "observed_at", "data"}},
		{"runs", []string{"id", "started_at", "data"}},
	}
	counts := map[string]int{}
	for _, table := range tables {
		var count int
		if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table.name).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, errors.New("destination already contains radar records; use a fresh database for migration")
		}
	}
	for _, table := range tables {
		columns := strings.Join(table.columns, ",")
		rows, err := source.QueryContext(ctx, "SELECT "+columns+" FROM "+table.name)
		if err != nil {
			return nil, err
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(table.columns)), ",")
		for rows.Next() {
			values := make([]any, len(table.columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return nil, err
			}
			if table.name == "runs" {
				if data, ok := values[2].([]byte); ok {
					var run model.Run
					if err := json.Unmarshal(data, &run); err != nil {
						rows.Close()
						return nil, err
					}
					if run.Status == "running" {
						now := time.Now().UTC()
						run.Status, run.FinishedAt = "interrupted", &now
						run.Errors = append(run.Errors, "Imported from a source refresh that had not finished.")
						values[2], _ = json.Marshal(run)
					}
				}
			}
			if _, err := target.ExecContext(ctx, "INSERT INTO "+table.name+"("+columns+") VALUES("+placeholders+")", values...); err != nil {
				rows.Close()
				return nil, err
			}
			counts[table.name]++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if destination.db.postgres {
		_, err := target.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('opportunity_radar.observations','id'),
			COALESCE((SELECT MAX(id) FROM observations),1), (SELECT COUNT(*) > 0 FROM observations))`)
		if err != nil {
			return nil, err
		}
	}
	if err := target.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}
