package crawler_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/crawler"
	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
)

type xFixture struct {
	page  ingest.XPage
	err   error
	batch []model.Post
}

func (x *xFixture) Configured() bool { return true }
func (x *xFixture) Page(context.Context, model.Source, int) (ingest.XPage, error) {
	return x.page, x.err
}
func (x *xFixture) Post(context.Context, string) (model.Post, error) {
	if len(x.page.Posts) > 0 {
		return x.page.Posts[0], x.err
	}
	return model.Post{}, x.err
}
func (x *xFixture) Posts(context.Context, []string) ([]model.Post, error) { return x.batch, x.err }

func TestXCheckpointMetricsHistoryAndPlatformFiltering(t *testing.T) {
	ctx, s := context.Background(), database(t)
	source, _, err := s.AddSource(ctx, model.Source{Name: "X Grants", URL: "https://x.com/search?q=" + url.QueryEscape("grant apply"), Kind: "x_search", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	post := model.Post{URL: "https://x.com/i/status/100", Title: "Community Grant", Body: "Applications are open for community projects.\nApply now: https://apply.example.org/grant\nApplication deadline: 17 December 2026", Author: "@grants", CaptureMethod: "x_api", Reactions: model.Int64(2), Reposts: model.Int64(1)}
	x := &xFixture{page: ingest.XPage{Posts: []model.Post{post}, NextToken: "second-page", NewestID: "100"}}
	e := crawler.New(s, &fakeFetcher{responses: map[string]response{}}, time.Minute)
	e.X = x
	run, err := e.Run(ctx, "", true)
	if err != nil || run.Created != 1 {
		t.Fatalf("first X crawl: %+v, %v", run, err)
	}
	checkpoint, err := s.Source(ctx, source.ID)
	if err != nil || checkpoint.SinceID != "" || checkpoint.PendingSinceID != "100" || checkpoint.NextToken != "second-page" {
		t.Fatal("advanced checkpoint before pending pages were saved")
	}
	post.Reactions, post.Reposts = model.Int64(8), model.Int64(3)
	x.page = ingest.XPage{NewestID: "104"}
	x.batch = []model.Post{post}
	run, err = e.Run(ctx, "", true)
	if err != nil || run.Updated != 1 {
		t.Fatalf("known X metrics refresh: %+v, %v", run, err)
	}
	checkpoint, err = s.Source(ctx, source.ID)
	if err != nil || checkpoint.SinceID != "104" || checkpoint.NextToken != "" || checkpoint.PendingSinceID != "" {
		t.Fatal("X checkpoint did not finish cleanly")
	}
	history, err := s.History(ctx, "x-100")
	if err != nil || len(history) != 2 || history[0].ReactionsDelta == nil || *history[0].ReactionsDelta != 6 || history[0].RepostsDelta == nil || *history[0].RepostsDelta != 2 {
		t.Fatal("X engagement changes were not preserved")
	}
	items, total, err := s.List(ctx, model.Query{Platform: "x", Status: "all"})
	if err != nil || total != 1 || items[0].PostAuthor != "@grants" {
		t.Fatal("X records do not participate in the shared Radwave feed")
	}
	stats, err := s.Stats(ctx)
	if err != nil || stats.Platforms["x"] != 1 {
		t.Fatal("combined-feed platform counts are incorrect")
	}
}

func TestXAccessFailurePreservesCheckpointAndBacksOff(t *testing.T) {
	ctx, s := context.Background(), database(t)
	source, _, err := s.AddSource(ctx, model.Source{Name: "X Search", URL: "https://x.com/search?q=grant", Kind: "x_search", Enabled: true, SinceID: "99"})
	if err != nil {
		t.Fatal(err)
	}
	e := crawler.New(s, &fakeFetcher{responses: map[string]response{}}, 30*time.Minute)
	e.X = &xFixture{err: &ingest.XAPIError{Status: 402}}
	run, err := e.Run(ctx, "", true)
	if err != nil || run.Status != "error" {
		t.Fatal("X API failure was not reported")
	}
	stored, err := s.Source(ctx, source.ID)
	if err != nil || stored.Status != "blocked" || stored.SinceID != "99" || stored.NextCrawlAt == nil || time.Until(*stored.NextCrawlAt) < 5*time.Hour {
		t.Fatal("blocked X source lost its checkpoint or ignored backoff")
	}
}

func TestXPastedImportStaysPausedAndSeparatesOrganizerFromPublisher(t *testing.T) {
	ctx, s := context.Background(), database(t)
	e := crawler.New(s, &fakeFetcher{responses: map[string]response{}}, 30*time.Minute)
	input := crawler.ImportInput{
		URL:   "https://twitter.com/grants/status/1234567890123456789",
		Title: "Community Grant", Organizer: "Community Foundation",
		Text: "Applications are open for a community grant. Apply now: https://apply.example.org/grant",
	}
	result, err := e.Import(ctx, input)
	if err != nil || !result.Created || result.Opportunity.Organizer != "Community Foundation" || result.Opportunity.PostAuthor != "" {
		t.Fatalf("pasted X import invented a publisher or lost the supplied organizer: %+v, %v", result, err)
	}
	source, err := s.Source(ctx, result.Opportunity.SourceID)
	if err != nil || source.Kind != "x_post" || source.Enabled {
		t.Fatal("pasting X text enabled API polling")
	}
	e.X = &xFixture{page: ingest.XPage{Posts: []model.Post{{
		URL: "https://x.com/i/status/1234567890123456789", Title: input.Title, Body: input.Text,
		Author: "@grants", CaptureMethod: "x_api",
	}}}}
	result, err = e.Import(ctx, crawler.ImportInput{URL: input.URL, Organizer: input.Organizer})
	if err != nil || result.Created || result.Opportunity.PostAuthor != "@grants" || result.Opportunity.Organizer != "Community Foundation" {
		t.Fatalf("API import confused the publisher with the organizer: %+v, %v", result, err)
	}
	source, err = s.Source(ctx, source.ID)
	if err != nil || source.Enabled {
		t.Fatal("an API import changed the existing source's paused setting")
	}
	result, err = e.Import(ctx, crawler.ImportInput{URL: "https://x.com/i/web/status/1234567890123456789", Text: input.Text})
	if err != nil || result.Created || result.Opportunity.PostAuthor != "@grants" || result.Opportunity.Organizer != "Community Foundation" {
		t.Fatal("a text reimport lost known publisher or organizer details")
	}
}
