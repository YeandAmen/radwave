package crawler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/crawler"
	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
)

type jobFixture struct {
	posts []model.Post
	err   error
}

func (f *jobFixture) Configured(string) bool { return true }
func (f *jobFixture) Read(context.Context, model.Source, int) ([]model.Post, error) {
	return f.posts, f.err
}

func TestJobAPIUsesSharedStoreHistoryAndSourceCadence(t *testing.T) {
	ctx, s := context.Background(), database(t)
	source, _, err := s.AddSource(ctx, model.Source{Name: "Employer jobs", Kind: "jobs_arbeitnow", URL: "https://www.arbeitnow.com/api/job-board-api", Enabled: true, IntervalMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	post := model.Post{Identity: "arbeitnow-job-test", URL: "https://www.arbeitnow.com/jobs/engineer", Title: "Research Engineer", Body: "Design reliable systems for the research team.", Author: "Example Labs", CaptureMethod: "job_api", Location: "Berlin", Job: &model.JobDetails{Provider: "Arbeitnow", Salary: "EUR 60000 per year"}}
	f := &jobFixture{posts: []model.Post{post}}
	e := crawler.New(s, &fakeFetcher{responses: map[string]response{}}, 30*time.Minute)
	e.Jobs = f
	run, err := e.Run(ctx, "", true)
	if err != nil || run.Created != 1 {
		t.Fatalf("job API crawl: %+v, %v", run, err)
	}
	stored, err := s.Source(ctx, source.ID)
	if err != nil || stored.NextCrawlAt == nil || stored.LastCrawledAt == nil || stored.NextCrawlAt.Sub(*stored.LastCrawledAt) != 15*time.Minute {
		t.Fatal("job source cadence was replaced by the default 30-minute schedule")
	}
	post.Job = &model.JobDetails{Provider: "Arbeitnow", Salary: "EUR 70000 per year"}
	f.posts = []model.Post{post}
	run, err = e.Run(ctx, "", true)
	if err != nil || run.Created != 0 || run.Updated != 1 {
		t.Fatal("job identity or salary updates did not use the shared upsert")
	}
	items, total, err := s.List(ctx, model.Query{Platform: "jobs", Kind: "job", Status: "all"})
	if err != nil || total != 1 || items[0].Organizer != "Example Labs" || items[0].Job == nil || items[0].Job.Salary != "EUR 70000 per year" {
		t.Fatal("job records were absent from shared filters")
	}
	history, err := s.History(ctx, post.Identity)
	if err != nil || len(history) != 2 || history[0].Changes[0] != "Job details updated" || history[0].Reactions != nil {
		t.Fatal("job changes or unknown engagement were not preserved")
	}
	f.err = &ingest.JobAccessError{Provider: "Arbeitnow", Status: 429}
	run, err = e.Run(ctx, "", true)
	if err != nil || run.Status != "error" {
		t.Fatal("job API failure was hidden")
	}
	retained, err := s.Get(ctx, post.Identity)
	if err != nil || retained.Job.Salary != "EUR 70000 per year" {
		t.Fatal("API failure replaced a captured job")
	}
}

func TestJobFeedsAcceptRoleTitlesWithoutOpportunityKeywords(t *testing.T) {
	ctx, s := context.Background(), database(t)
	feedURL, roleURL := "https://university.example.org/RSS/rss.aspx", "https://university.example.org/Vacancy.aspx?ref=ABC123"
	_, _, err := s.AddSource(ctx, model.Source{Name: "University careers", URL: feedURL, Kind: "rss_jobs", Enabled: true, IntervalMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	feed := `<rss><channel><item><title>Senior Systems Architect</title><link>` + roleURL + `</link><description>Design resilient campus systems.</description></item></channel></rss>`
	page := `<link rel="canonical" href="https://university.example.org/"><article><h1>Current Job Opportunities</h1><p>Design resilient campus systems.</p><h2>Qualifications</h2><p>Experience with production systems is required.</p><p>Closing date: 17 December 2026</p></article>`
	f := &fakeFetcher{responses: map[string]response{feedURL: {result: ingest.FetchResult{Body: []byte(feed)}}, roleURL: {result: ingest.FetchResult{Body: []byte(page)}}}}
	e := crawler.New(s, f, 30*time.Minute)
	run, err := e.Run(ctx, "", true)
	if err != nil || run.Created != 1 {
		t.Fatalf("job feed discarded a valid role title: %+v, %v", run, err)
	}
	o, err := s.GetByURL(ctx, roleURL)
	if err != nil || o.Kind != "job" || o.Title != "Senior Systems Architect" || o.Job == nil || o.Deadline != "2026-12-17" || len(o.Eligibility) != 1 {
		t.Fatal("job feed lost the normal extraction and article expansion")
	}
}

func TestPastedLinkedInJobUsesCanonicalIdentityAndJobType(t *testing.T) {
	ctx, s := context.Background(), database(t)
	e := crawler.New(s, &fakeFetcher{responses: map[string]response{}}, 30*time.Minute)
	input := crawler.ImportInput{URL: "https://gh.linkedin.com/jobs/view/research-engineer-at-example-4484260323", Organizer: "Example Research", Text: "Research Engineer\nDesign reliable systems for our research team.\nQualifications\nExperience with Go is required."}
	result, err := e.Import(ctx, input)
	if err != nil || !result.Created || result.Opportunity.ID != "linkedin-job-4484260323" || result.Opportunity.Kind != "job" || result.Opportunity.Job == nil {
		t.Fatalf("pasted job import: %+v, %v", result, err)
	}
	input.URL = "https://www.linkedin.com/jobs/view/4484260323?trackingId=share"
	result, err = e.Import(ctx, input)
	if err != nil || result.Created {
		t.Fatal("a LinkedIn job URL alias created a duplicate")
	}
	if err := crawler.ValidateSource(model.Source{Name: "API", URL: "https://api.adzuna.com/v1/api/jobs/gb/search/1?app_key=private", Kind: "web_page"}); err == nil {
		t.Fatal("a job API credential could be stored by overriding source type")
	}
}

func TestRichJobFeedKeepsSalaryAndDeadlineWithoutPortalExpansion(t *testing.T) {
	ctx, s := context.Background(), database(t)
	feedURL, roleURL := "https://university.example.org/RSS/rss.aspx", "https://university.example.org/rss/click.aspx?ref=ABC123"
	_, _, err := s.AddSource(ctx, model.Source{Name: "University careers", URL: feedURL, Kind: "rss_jobs", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	body := "Salary: GBP 40000 per year\nClosing date: 17 December 2026\n" + strings.Repeat("Research and maintain resilient campus systems. ", 30)
	feed := `<rss><channel><item><title>Systems Architect</title><link>` + roleURL + `</link><description><![CDATA[` + body + `]]></description></item></channel></rss>`
	f := &fakeFetcher{responses: map[string]response{feedURL: {result: ingest.FetchResult{Body: []byte(feed)}}}}
	e := crawler.New(s, f, 30*time.Minute)
	run, err := e.Run(ctx, "", true)
	if err != nil || run.Created != 1 || f.calls != 1 {
		t.Fatal("rich job RSS unnecessarily fetched a portal page")
	}
	o, err := s.GetByURL(ctx, roleURL)
	if err != nil || o.Deadline != "2026-12-17" || o.Job.Salary != "GBP 40000 per year" || o.CaptureMethod != "rss_summary" {
		t.Fatal("rich job RSS lost its salary, deadline, or provenance")
	}
}
