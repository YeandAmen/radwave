package crawler_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/bootstrap"
	"github.com/YeandAmen/radwave/internal/crawler"
	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

type response struct {
	result ingest.FetchResult
	err    error
}
type fakeFetcher struct {
	mu          sync.Mutex
	responses   map[string]response
	calls       int
	lastHeaders http.Header
	gate        chan struct{}
}

func (f *fakeFetcher) Fetch(ctx context.Context, raw string, headers http.Header) (ingest.FetchResult, error) {
	f.mu.Lock()
	f.calls++
	f.lastHeaders = headers.Clone()
	r := f.responses[raw]
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ingest.FetchResult{}, ctx.Err()
		}
	}
	if r.result.URL == "" {
		r.result.URL = raw
	}
	return r.result, r.err
}
func (f *fakeFetcher) set(raw string, r response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[raw] = r
}

func database(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRSSRefreshConditionalRequestsAndFailedArticleRetention(t *testing.T) {
	ctx, s := context.Background(), database(t)
	feedURL, articleURL := "https://publisher.example/feed", "https://publisher.example/fellowship"
	source, _, err := s.AddSource(ctx, model.Source{Name: "Publisher", URL: feedURL, Kind: "rss", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	feed := `<rss><channel><item><title>Builders Fellowship</title><link>` + articleURL + `</link><category>Fellowships</category><description>Deadline: November 2, 2026. Applications are open for the Builders Fellowship.</description></item></channel></rss>`
	article := `<article><h1>Builders Fellowship</h1><div class="entry-content"><p>Deadline: December 17, 2026</p><p>Applications are open for builders developing practical technology with meaningful public benefit.</p><h2>Eligibility</h2><ul><li>Applicants from all countries may apply.</li></ul><h2>Application</h2><p><a href="https://apply.example.org/fellowship">Submit your application</a></p></div></article>`
	f := &fakeFetcher{responses: map[string]response{feedURL: {result: ingest.FetchResult{Body: []byte(feed), Headers: http.Header{"Etag": []string{`"v1"`}}}}, articleURL: {result: ingest.FetchResult{Body: []byte(article)}}}}
	e := crawler.New(s, f, time.Nanosecond)
	run, err := e.Run(ctx, "", true)
	if err != nil || run.Created != 1 || run.Status != "completed" {
		t.Fatalf("first crawl: %+v, %v", run, err)
	}
	o, err := s.GetByURL(ctx, articleURL)
	if err != nil || o.Deadline != "2026-12-17" || len(o.Eligibility) != 1 || o.CaptureMethod != "public_page" {
		t.Fatalf("full article not captured: %+v, %v", o, err)
	}
	stored, err := s.Source(ctx, source.ID)
	if err != nil || stored.ETag != `"v1"` {
		t.Fatalf("ETag not persisted: %+v, %v", stored, err)
	}
	f.set(feedURL, response{result: ingest.FetchResult{NotModified: true}})
	run, err = e.Run(ctx, "", false)
	if err != nil || run.Created != 0 || run.OpportunitiesSeen != 1 || f.lastHeaders.Get("If-None-Match") != `"v1"` {
		t.Fatalf("conditional refresh failed: %+v, %v", run, err)
	}
	f.set(feedURL, response{result: ingest.FetchResult{Body: []byte(feed)}})
	f.set(articleURL, response{err: &ingest.FetchError{Status: 403, URL: articleURL}})
	run, err = e.Run(ctx, "", true)
	if err != nil || run.Status != "partial" || run.Updated != 0 {
		t.Fatalf("fallback crawl: %+v, %v", run, err)
	}
	retained, err := s.GetByURL(ctx, articleURL)
	if err != nil || retained.Deadline != o.Deadline || len(retained.Eligibility) != 1 || !retained.LastFetchedAt.Equal(o.LastFetchedAt) {
		t.Fatal("a truncated RSS summary overwrote previously captured article details")
	}
}

func TestBlockedSourcesBackOffAndPausedSourcesAreSkipped(t *testing.T) {
	ctx, s := context.Background(), database(t)
	raw := "https://www.linkedin.com/posts/example-activity-7520960460347768832-x"
	source, _, err := s.AddSource(ctx, model.Source{Name: "LinkedIn", URL: raw, Kind: "linkedin_post", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{responses: map[string]response{raw: {err: &ingest.FetchError{Status: 999, URL: raw}}}}
	e := crawler.New(s, f, 30*time.Minute)
	run, err := e.Run(ctx, "", false)
	if err != nil || run.Status != "error" || crawler.RunError(run) == nil {
		t.Fatalf("hidden fetch failure: %+v, %v", run, err)
	}
	stored, err := s.Source(ctx, source.ID)
	if err != nil || stored.Status != "blocked" || stored.NextCrawlAt == nil || time.Until(*stored.NextCrawlAt) < 5*time.Hour {
		t.Fatalf("missing backoff: %+v, %v", stored, err)
	}
	run, err = e.Run(ctx, "", false)
	if err != nil || run.SourcesChecked != 0 || f.calls != 1 {
		t.Fatal("a scheduled refresh ignored backoff")
	}
	paused := false
	if _, err := s.ConfigureSource(ctx, source.ID, &paused, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(ctx, "", true); err != nil || f.calls != 1 {
		t.Fatal("refresh-all fetched a paused source")
	}
}

func TestSingleFlightCancellationAndShutdown(t *testing.T) {
	s := database(t)
	raw := "https://example.org/opportunity"
	if _, _, err := s.AddSource(context.Background(), model.Source{Name: "Test", URL: raw, Kind: "web_page", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{gate: make(chan struct{}), responses: map[string]response{}}
	e := crawler.New(s, f, 30*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := e.Start(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, "", true); !errors.Is(err, crawler.ErrAlreadyRunning) {
		t.Fatal("allowed overlapping source runs")
	}
	cancel()
	done := make(chan struct{})
	go func() { e.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel/drain the running crawl")
	}
	if _, err := e.Start(context.Background(), "", true); !errors.Is(err, context.Canceled) {
		t.Fatal("allowed new work after shutdown")
	}
	runs, err := s.Runs(context.Background())
	if err != nil || len(runs) != 1 || runs[0].Status != "interrupted" {
		t.Fatalf("lost interrupted run: %+v, %v", runs, err)
	}
}

func TestManualReferenceAliasImportDoesNotFetchOrDuplicate(t *testing.T) {
	ctx, s := context.Background(), database(t)
	if err := bootstrap.Seed(ctx, s); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{responses: map[string]response{}}
	e := crawler.New(s, f, 30*time.Minute)
	result, err := e.Import(ctx, crawler.ImportInput{URL: bootstrap.ExampleShareURL + "?utm_source=share", Text: bootstrap.ExampleBody, Organizer: "Nkabom Collaborative - UESD"})
	if err != nil || result.Created || result.Opportunity.ID != "linkedin-7520960460347768832" || result.Opportunity.CaptureMethod != "manual_import" || result.Opportunity.Reactions != nil || f.calls != 0 {
		t.Fatalf("bad manual alias import: %+v, %v", result, err)
	}
	_, total, err := s.List(ctx, model.Query{Status: "all"})
	if err != nil || total != 1 {
		t.Fatal("the share URL created a duplicate of its known canonical activity")
	}
}
