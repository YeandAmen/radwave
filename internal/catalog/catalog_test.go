package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

func fixture(t *testing.T, count int) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, _ := z.Create("jobs.csv")
	fmt.Fprintln(f, "source,company,title,url,openings,team,category,level,years_experience,employment,remote,location,skills,pay_min,pay_max,pay_currency,posted,first_seen,closed_at,ghost_score,ghost_reasons,fresher_friendly")
	for n := 0; n < count; n++ {
		closed := ""
		if n == 1 {
			closed = "2026-10-29"
		}
		fmt.Fprintf(f, "greenhouse,Example,Data Engineer %d,https://jobs.example.org/%d,1,Data,data,junior,1,full_time,True,Remote,python;sql,50000,60000,USD,2026-10-28,2026-10-27,%s,2,talent pool,True\n", n, n, closed)
	}
	d, _ := z.Create("open_jobs_daily.csv")
	fmt.Fprintln(d, "date,open_jobs,fresher_friendly_jobs\n2026-10-30,2,2")
	z.Close()
	return b.Bytes()
}

func TestDatasetFactsClosureAndRiskRemainSourceAttributed(t *testing.T) {
	idx, err := Open(context.Background(), fixture(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if idx.Info.Open != 2 || idx.Info.Closed != 1 || idx.Info.AsOf != "2026-10-30" {
		t.Fatal("snapshot totals or dates were lost")
	}
	items, total, err := idx.List(context.Background(), model.Query{Platform: "kaggle", RemoteOnly: true, FresherOnly: true, JobField: "data", JobLevel: "junior", Skill: "python"}, nil)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatal("dataset filters failed")
	}
	o := items[0]
	if o.Job.Salary != "USD 50000–60000 per year" || o.Job.RiskScore == nil || *o.Job.RiskScore != 2 || o.Job.RiskReasons != "talent pool" || o.Job.YearsExperience == nil || o.PublishedAt == nil || o.PublishedAt.Format("2006-01-02") != "2026-10-28" || o.FirstSeenAt.Format("2006-01-02") != "2026-10-27" {
		t.Fatalf("dataset facts changed: %+v", o)
	}
	if o.Status != "unknown" || o.Deadline != "" || !strings.Contains(o.Body, "original descriptions are not included") {
		t.Fatal("snapshot was presented as a verified live job or invented description")
	}
	closed, _, _ := idx.List(context.Background(), model.Query{Status: "closed"}, nil)
	if len(closed) != 1 || closed[0].Status != "closed" || closed[0].Deadline != "" {
		t.Fatal("closure date was confused with an application deadline")
	}
	found, err := idx.Get(o.ID)
	if err != nil || found.URL != o.URL {
		t.Fatal("stable detail identity failed")
	}
}

func TestFullCataloguePaginationAndLiveMergeDoNotOverwriteLiveRecords(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := New(db, "")
	if _, err := service.Import(ctx, fixture(t, 11005), 1); err != nil {
		t.Fatal(err)
	}
	old, err := db.GetByURL(ctx, "https://jobs.example.org/0")
	if err != store.ErrNotFound {
		t.Fatalf("snapshot created individual live rows: %+v %v", old, err)
	}
	now := time.Now().UTC()
	live := model.Opportunity{ID: "live", URL: "https://jobs.example.org/0", Title: "Current employer details", Body: "Full current job description", Kind: "job", FirstSeenAt: now, UpdatedAt: now, LastFetchedAt: now, SourceKind: "web_page", DeadlineState: "unknown"}
	if _, err := db.Upsert(ctx, live); err != nil {
		t.Fatal(err)
	}
	items, total, err := service.List(ctx, model.Query{Kind: "job", Sort: "newest", Offset: 10500, Limit: 10})
	if err != nil || len(items) != 10 || total != 11004 {
		t.Fatalf("large catalogue pagination: len=%d,total=%d,error=%v", len(items), total, err)
	}
	first, _, err := service.List(ctx, model.Query{Kind: "job", Sort: "newest", Limit: 1})
	if err != nil || first[0].ID != "live" {
		t.Fatal("snapshot displaced the newer live capture")
	}
	service.index = nil
	loaded, err := service.Load(ctx)
	if err != nil || loaded.Info.Open != 11004 {
		t.Fatal("compressed snapshot did not survive a fresh service load")
	}
}

func TestFailedOrOlderSnapshotKeepsCurrentCatalogue(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(":memory:")
	defer db.Close()
	s := New(db, "")
	if _, err := s.Import(ctx, fixture(t, 3), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, []byte("invalid zip"), 2); err == nil {
		t.Fatal("invalid snapshot was accepted")
	}
	info, ok, err := s.Info(ctx)
	if err != nil || !ok || info.Version != 1 || info.Open != 2 {
		t.Fatal("failed import replaced valid data")
	}
}
