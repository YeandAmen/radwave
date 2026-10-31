package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
)

func (s *Store) Stats(ctx context.Context) (model.Stats, error) {
	stats := model.Stats{Kinds: map[string]int{}, Regions: map[string]int{}, Platforms: map[string]int{}}
	rows, err := s.db.QueryContext(ctx, "SELECT kind,region,deadline,deadline_state,updated_at,json_extract(data,'$.source_kind') FROM opportunities")
	if err != nil {
		return stats, err
	}
	now, today := time.Now(), time.Now().UTC().Format("2006-01-02")
	for rows.Next() {
		var o model.Opportunity
		var updated string
		var sourceKind sql.NullString
		if err := rows.Scan(&o.Kind, &o.Region, &o.Deadline, &o.DeadlineState, &updated, &sourceKind); err != nil {
			rows.Close()
			return stats, err
		}
		o.Decorate(now)
		stats.Total++
		if o.Status != "closed" {
			stats.Available++
			stats.Kinds[o.Kind]++
			stats.Regions[o.Region]++
			stats.Platforms[model.Platform(sourceKind.String)]++
		}
		if o.Status == "closing" {
			stats.ClosingSoon++
		}
		if strings.HasPrefix(updated, today) {
			stats.UpdatedToday++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return stats, err
	}
	rows.Close()
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sources WHERE enabled = 1").Scan(&stats.Sources)
	return stats, err
}
