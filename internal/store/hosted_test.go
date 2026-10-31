package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

func TestSnapshotCopyPreservesHistoriesAndRejectsNonemptyDestination(t *testing.T) {
	ctx := context.Background()
	source, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	now := time.Now().UTC()
	o := opportunity(now)
	if _, err := source.Upsert(ctx, o); err != nil {
		t.Fatal(err)
	}
	o.Reactions, o.LastFetchedAt, o.UpdatedAt = model.Int64(240), now.Add(time.Minute), now.Add(time.Minute)
	if _, err := source.Upsert(ctx, o); err != nil {
		t.Fatal(err)
	}
	counts, err := source.CopyIntoEmpty(ctx, target)
	if err != nil || counts["opportunities"] != 1 || counts["observations"] != 2 {
		t.Fatalf("snapshot copy: %#v, %v", counts, err)
	}
	history, err := target.History(ctx, o.ID)
	if err != nil || len(history) != 2 || history[0].ReactionsDelta == nil || *history[0].ReactionsDelta != 6 {
		t.Fatal("observation history was not preserved")
	}
	if _, err := source.CopyIntoEmpty(ctx, target); err == nil {
		t.Fatal("overwrote a nonempty destination")
	}
	first, err := target.AcquireCrawlLease(ctx, "first", time.Minute)
	if err != nil || !first {
		t.Fatal("could not acquire first lease")
	}
	second, err := target.AcquireCrawlLease(ctx, "second", time.Minute)
	if err != nil || second {
		t.Fatal("allowed overlapping lease owners")
	}
	if err := target.ReleaseCrawlLease(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if active, err := target.CrawlRunning(ctx); err != nil || !active {
		t.Fatal("a different owner released the lease")
	}
	if err := target.ReleaseCrawlLease(ctx, "first"); err != nil {
		t.Fatal(err)
	}
}

// This opt-in smoke check reads the real hosted dataset and verifies that
// separate connections agree on the crawl lease, without adding test posts.
func TestHostedStoreAcrossConnections(t *testing.T) {
	dsn := os.Getenv("RADAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("hosted database smoke check is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	first, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	stats, err := first.Stats(ctx)
	if err != nil || stats.Total == 0 {
		t.Fatalf("hosted dataset unavailable: %+v, %v", stats, err)
	}
	sources, err := first.Sources(ctx)
	if err != nil || len(sources) == 0 {
		t.Fatalf("hosted sources unavailable: %v", err)
	}
	items, _, err := first.List(ctx, model.Query{Source: sources[0].ID, Status: "all"})
	if err != nil || len(items) == 0 {
		t.Fatalf("hosted source filtering failed: %v", err)
	}
	known, err := second.GetByURL(ctx, items[0].URL)
	if err != nil || known.ID != items[0].ID {
		t.Fatal("canonical URL lookup differs across connections")
	}
	if _, err := second.History(ctx, known.ID); err != nil {
		t.Fatal(err)
	}
	owner := model.NewID("hosted-check")
	acquired, err := first.AcquireCrawlLease(ctx, owner, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("could not acquire hosted test lease: %v", err)
	}
	defer first.ReleaseCrawlLease(context.Background(), owner)
	if acquired, err := second.AcquireCrawlLease(ctx, model.NewID("other-check"), time.Minute); err != nil || acquired {
		t.Fatal("hosted instances can start overlapping crawls")
	}
	if active, err := second.CrawlRunning(ctx); err != nil || !active {
		t.Fatal("active crawl is invisible to another hosted connection")
	}
}
