package legacy_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/legacy"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

func TestTwitterArchiveJoinsRadwaveWithoutImportingAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twitter.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE opportunities(id INTEGER PRIMARY KEY,tweet_id TEXT,type TEXT,title TEXT,description TEXT,url TEXT,author TEXT,tags TEXT,scraped_at TEXT);CREATE TABLE users(email TEXT,api_key TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO opportunities VALUES(1,'1234567890123456789','bounty','[BOUNTY] Open-source security bounty','Bug bounty applications are open. Apply now: https://apply.example.org/bounty','https://twitter.com/i/web/status/1234567890123456789','@security','["web3"]','2026-10-30T10:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users VALUES('test@example.org','test-account-secret')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	destination, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	result, err := legacy.ImportTwitter(context.Background(), path, destination)
	if err != nil || result.Created != 1 {
		t.Fatalf("archive import: %+v, %v", result, err)
	}
	items, total, err := destination.List(context.Background(), model.Query{Platform: "x", Status: "all"})
	if err != nil || total != 1 || items[0].ID != "x-1234567890123456789" || items[0].Kind != "bounty" || items[0].CaptureMethod != "x_archive" {
		t.Fatal("legacy opportunities were not adapted into the shared feed")
	}
	if items[0].Reactions != nil || items[0].Comments != nil {
		t.Fatal("archive import invented engagement metrics")
	}
	capturedAt := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	if items[0].PublishedAt != nil || !items[0].FirstSeenAt.Equal(capturedAt) || !items[0].LastFetchedAt.Equal(capturedAt) {
		t.Fatal("archive capture time was lost or presented as a publication date")
	}
	if result, err := legacy.ImportTwitter(context.Background(), path, destination); err != nil || result.Created != 0 {
		t.Fatal("archive import duplicated a known X identity")
	}
	live := items[0]
	live.CaptureMethod, live.SourceKind = "x_api", "x_post"
	live.LastFetchedAt, live.UpdatedAt = capturedAt.Add(time.Hour), capturedAt.Add(time.Hour)
	live.Reactions, live.Reposts = model.Int64(12), model.Int64(3)
	if _, err := destination.Upsert(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if result, err := legacy.ImportTwitter(context.Background(), path, destination); err != nil || result.Created != 0 || result.Updated != 0 {
		t.Fatal("older archive replaced a newer live capture")
	}
	preserved, err := destination.Get(context.Background(), live.ID)
	if err != nil || preserved.CaptureMethod != "x_api" || preserved.Reactions == nil || *preserved.Reactions != 12 || preserved.Reposts == nil || *preserved.Reposts != 3 {
		t.Fatal("archive replay discarded the live capture's provenance or metrics")
	}
}
