package store_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/model"
	"github.com/YeandAmen/opportunity-radar/internal/store"
)

func opportunity(now time.Time) model.Opportunity {
	return model.Opportunity{ID: "test", URL: "https://example.org/program", Title: "Youth program", Kind: "program", Region: "Not stated", Body: "Applications are open.", Deadline: now.AddDate(0, 0, 4).Format("2006-01-02"), DeadlineState: "dated", CaptureMethod: "public_page", Eligibility: []string{}, Benefits: []string{}, Tags: []string{}, Evidence: map[string]string{}, Missing: []string{}, FirstSeenAt: now, UpdatedAt: now, LastFetchedAt: now, Reactions: model.Int64(234)}
}

func TestPersistenceDeduplicationAndObservationDeltas(t *testing.T) {
	ctx, path, now := context.Background(), filepath.Join(t.TempDir(), "radar.db"), time.Now().UTC()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	o := opportunity(now)
	first, err := s.Upsert(ctx, o)
	if err != nil || !first.Created {
		t.Fatalf("create: %+v, %v", first, err)
	}
	o.LastFetchedAt, o.UpdatedAt = now.Add(time.Minute), now.Add(time.Minute)
	unchanged, err := s.Upsert(ctx, o)
	if err != nil || unchanged.Created || unchanged.Updated || !unchanged.Opportunity.UpdatedAt.Equal(now) {
		t.Fatalf("unchanged poll incorrectly changed the record: %+v, %v", unchanged, err)
	}
	o.LastFetchedAt, o.UpdatedAt = now.Add(2*time.Minute), now.Add(2*time.Minute)
	o.Reactions, o.Comments = model.Int64(236), model.Int64(20)
	o.Deadline = now.AddDate(0, 0, 6).Format("2006-01-02")
	updated, err := s.Upsert(ctx, o)
	if err != nil || !updated.Updated {
		t.Fatalf("update: %+v, %v", updated, err)
	}
	history, err := s.History(ctx, o.ID)
	if err != nil || len(history) != 3 || history[0].ReactionsDelta == nil || *history[0].ReactionsDelta != 2 {
		t.Fatalf("bad history: %+v, %v", history, err)
	}
	if history[0].CommentsDelta != nil {
		t.Fatal("invented a change from an unknown comment count")
	}
	stale := opportunity(now)
	stale.Reactions = model.Int64(1)
	if _, err := s.Upsert(ctx, stale); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Get(ctx, o.ID)
	if err != nil || loaded.Reactions == nil || *loaded.Reactions != 236 || loaded.Deadline != o.Deadline {
		t.Fatalf("stale write or persistence failed: %+v, %v", loaded, err)
	}
	items, total, err := s.List(ctx, model.Query{Status: "active"})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("duplicate record: %d, %v", total, err)
	}
}

func TestSourceSettingsSurviveCrawlerWritesAndETagsPersist(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, _, err := s.AddSource(ctx, model.Source{Name: "Original", URL: "https://example.org/feed", Kind: "rss", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	paused, customName := false, "My source"
	if _, err := s.ConfigureSource(ctx, source.ID, &paused, &customName); err != nil {
		t.Fatal(err)
	}
	source.ETag, source.Status, source.ItemCount = `"v2"`, "ok", 3
	if _, err := s.RecordSourceFetch(ctx, source); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Source(ctx, source.ID)
	if err != nil || loaded.Enabled || loaded.Name != customName || loaded.ETag != `"v2"` {
		t.Fatalf("lost settings: %+v, %v", loaded, err)
	}
	existing, created, err := s.AddSource(ctx, model.Source{URL: source.URL, Name: "Replacement", Enabled: true})
	if err != nil || created || existing.ETag != `"v2"` || existing.Enabled {
		t.Fatalf("idempotent add lost stored state: %+v, %v", existing, err)
	}
}

func TestFilteringUnknownCountsAndConcurrentMonotonicUpdates(t *testing.T) {
	ctx, now := context.Background(), time.Now().UTC()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o := opportunity(now)
	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := o
			next.LastFetchedAt = now.Add(time.Duration(i) * time.Second)
			next.UpdatedAt = next.LastFetchedAt
			next.Reactions = model.Int64(int64(i))
			if _, err := s.Upsert(ctx, next); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	loaded, err := s.Get(ctx, o.ID)
	if err != nil || loaded.Reactions == nil || *loaded.Reactions != 14 {
		t.Fatalf("lost the newest concurrent write: %+v, %v", loaded, err)
	}
	unknown := opportunity(now)
	unknown.ID = "unknown"
	unknown.URL = "https://example.org/unknown"
	unknown.Title = "100% scholarship"
	unknown.Kind = "scholarship"
	unknown.Deadline = ""
	unknown.DeadlineState = "unknown"
	unknown.Reactions = nil
	if _, err := s.Upsert(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.List(ctx, model.Query{Search: "100%", Status: "unknown"})
	if err != nil || total != 1 || items[0].Reactions != nil || items[0].Status != "unknown" {
		t.Fatalf("unknown values or literal search lost: %+v, %v", items, err)
	}
	items, _, err = s.List(ctx, model.Query{IDs: []string{"unknown"}, Status: "all"})
	if err != nil || len(items) != 1 || items[0].ID != "unknown" {
		t.Fatal("shortlist ID filtering failed")
	}
	stats, err := s.Stats(ctx)
	if err != nil || stats.Total != 2 || stats.Available != 2 || stats.ClosingSoon != 1 {
		t.Fatalf("bad stats: %+v, %v", stats, err)
	}
}
