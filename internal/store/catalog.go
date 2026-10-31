package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Compressed catalogues are stored in bounded chunks instead of creating a
// million duplicate opportunity/observation rows. The manifest is published
// atomically after all chunks are written.
const catalogChunkSize = 1 << 20

func (s *Store) catalogTables(ctx context.Context) error {
	s.catalogOnce.Do(func() { s.catalogErr = s.createCatalogTables(ctx) })
	return s.catalogErr
}

func (s *Store) createCatalogTables(ctx context.Context) error {
	typeName := "BLOB"
	if s.db.postgres {
		typeName = "BYTEA"
	}
	_, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS catalog_meta (id TEXT PRIMARY KEY, digest TEXT NOT NULL, data "+typeName+" NOT NULL); CREATE TABLE IF NOT EXISTS catalog_chunks (digest TEXT NOT NULL, part INTEGER NOT NULL, data "+typeName+" NOT NULL, PRIMARY KEY(digest,part))")
	return err
}

func (s *Store) SaveCatalog(ctx context.Context, digest string, metadata any, archive []byte) error {
	if err := s.catalogTables(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for part, start := 0, 0; start < len(archive); part, start = part+1, start+catalogChunkSize {
		end := min(start+catalogChunkSize, len(archive))
		if _, err := tx.ExecContext(ctx, "INSERT INTO catalog_chunks(digest,part,data) VALUES(?,?,?) ON CONFLICT(digest,part) DO NOTHING", digest, part, archive[start:end]); err != nil {
			return fmt.Errorf("store catalogue chunk %d: %w", part, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO catalog_meta(id,digest,data) VALUES('kaggle-jobs',?,?) ON CONFLICT(id) DO UPDATE SET digest=excluded.digest,data=excluded.data", digest, encoded); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM catalog_chunks WHERE digest <> ?", digest); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CatalogMeta(ctx context.Context, target any) (bool, error) {
	if err := s.catalogTables(ctx); err != nil {
		return false, err
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM catalog_meta WHERE id='kaggle-jobs'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, target)
}

func (s *Store) CatalogArchive(ctx context.Context, digest string, size int) ([]byte, error) {
	if err := s.catalogTables(ctx); err != nil {
		return nil, err
	}
	data := make([]byte, 0, size)
	for part := 0; len(data) < size; part++ {
		var chunk []byte
		if err := s.db.QueryRowContext(ctx, "SELECT data FROM catalog_chunks WHERE digest=? AND part=?", digest, part).Scan(&chunk); err != nil {
			return nil, err
		}
		if len(chunk) == 0 || len(data)+len(chunk) > size {
			return nil, errors.New("catalogue chunks are incomplete")
		}
		data = append(data, chunk...)
	}
	return data, nil
}

func (s *Store) StageCatalogChunk(ctx context.Context, digest string, part int, data []byte) error {
	if err := s.catalogTables(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO catalog_chunks(digest,part,data) VALUES(?,?,?) ON CONFLICT(digest,part) DO UPDATE SET data=excluded.data", digest, part, data)
	return err
}

func (s *Store) CatalogParts(ctx context.Context, digest string) (map[int]int, error) {
	if err := s.catalogTables(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT part,length(data) FROM catalog_chunks WHERE digest=? ORDER BY part", digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := map[int]int{}
	for rows.Next() {
		var part, size int
		if err := rows.Scan(&part, &size); err != nil {
			return nil, err
		}
		parts[part] = size
	}
	return parts, rows.Err()
}
