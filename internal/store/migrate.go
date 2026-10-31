package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const schemaVersion = 2

func (s *Store) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if !s.db.postgres {
		for _, pragma := range []string{"PRAGMA journal_mode = WAL", "PRAGMA busy_timeout = 5000", "PRAGMA foreign_keys = ON"} {
			if _, err := s.db.ExecContext(ctx, pragma); err != nil {
				return err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if s.db.postgres {
		// A transaction-level advisory lock serializes cold-start migrations
		// even when the database URL points at a transaction pooler.
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(748190325)"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+postgresSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS radar_schema (id INTEGER PRIMARY KEY, version INTEGER NOT NULL)"); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, "SELECT version FROM radar_schema WHERE id = 1").Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	} else if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema %d is newer than this application supports", version)
	}
	blob, observationID := "BLOB", "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.db.postgres {
		blob, observationID = "BYTEA", "BIGSERIAL PRIMARY KEY"
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS opportunities (
			id TEXT PRIMARY KEY, url TEXT NOT NULL UNIQUE, data ` + blob + ` NOT NULL,
			kind TEXT NOT NULL, region TEXT NOT NULL, deadline TEXT NOT NULL,
			deadline_state TEXT NOT NULL, published_at TEXT NOT NULL,
			first_seen_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			reactions BIGINT, search_text TEXT NOT NULL
		)`,
		"CREATE INDEX IF NOT EXISTS opportunity_deadline ON opportunities(deadline)",
		"CREATE INDEX IF NOT EXISTS opportunity_kind ON opportunities(kind)",
		"CREATE INDEX IF NOT EXISTS opportunity_region ON opportunities(region)",
		`CREATE TABLE IF NOT EXISTS observations (
			id ` + observationID + `, opportunity_id TEXT NOT NULL REFERENCES opportunities(id) ON DELETE CASCADE,
			observed_at TEXT NOT NULL, data ` + blob + ` NOT NULL
		)`,
		"CREATE INDEX IF NOT EXISTS observation_opportunity ON observations(opportunity_id, id DESC)",
		"CREATE TABLE IF NOT EXISTS sources (id TEXT PRIMARY KEY, url TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL, data " + blob + " NOT NULL)",
		"CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, started_at TEXT NOT NULL, data " + blob + " NOT NULL)",
		"CREATE TABLE IF NOT EXISTS leases (id TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at TEXT NOT NULL)",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if s.db.postgres {
		_, err = tx.ExecContext(ctx, "INSERT INTO radar_schema(id,version) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version", schemaVersion)
	} else {
		_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
