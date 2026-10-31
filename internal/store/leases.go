package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AcquireCrawlLease prevents two hosted instances from refreshing the same
// database concurrently. An interrupted instance releases ownership by expiry.
func (s *Store) AcquireCrawlLease(ctx context.Context, owner string, duration time.Duration) (bool, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO leases(id,owner,expires_at) VALUES('crawl',?,?)
		ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at
		WHERE leases.expires_at <= ?`, owner, stamp(now.Add(duration)), stamp(now))
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) ReleaseCrawlLease(ctx context.Context, owner string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM leases WHERE id = 'crawl' AND owner = ?", owner)
	return err
}

func (s *Store) RenewCrawlLease(ctx context.Context, owner string, duration time.Duration) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE leases SET expires_at = ? WHERE id = 'crawl' AND owner = ?", stamp(time.Now().Add(duration)), owner)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) CrawlRunning(ctx context.Context) (bool, error) {
	var expires string
	err := s.db.QueryRowContext(ctx, "SELECT expires_at FROM leases WHERE id = 'crawl'").Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expires > stamp(time.Now()), nil
}
