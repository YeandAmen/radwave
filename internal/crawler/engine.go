// Package crawler coordinates bounded, rate-limited source refreshes.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

var ErrAlreadyRunning = errors.New("a source refresh is already running")
var ErrNotOpportunity = errors.New("no application or opportunity announcement was found in this content")

type Engine struct {
	Store    *store.Store
	Fetcher  ingest.Fetcher
	X        ingest.XReader
	Jobs     ingest.JobReader
	Interval time.Duration
	MaxItems int
	Logger   *slog.Logger
	mu       sync.Mutex
	running  bool
	current  model.Run
	stopped  bool
	wg       sync.WaitGroup
}

func New(s *store.Store, fetcher ingest.Fetcher, interval time.Duration) *Engine {
	return &Engine{Store: s, Fetcher: fetcher, Interval: interval, MaxItems: 20, Logger: slog.Default()}
}

func (e *Engine) Running() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.running }
func (e *Engine) Wait()         { e.wg.Wait() }

// Shutdown prevents a scheduler or request from starting work while the
// database is being closed, then drains already-started runs.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	e.wg.Wait()
}

func (e *Engine) begin(ctx context.Context, sourceID string, force bool) (model.Run, []model.Source, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return model.Run{}, nil, context.Canceled
	}
	if e.running {
		return model.Run{}, nil, ErrAlreadyRunning
	}
	sources, err := e.Store.Sources(ctx)
	if err != nil {
		return model.Run{}, nil, err
	}
	selected := []model.Source{}
	found, now := sourceID == "", time.Now().UTC()
	for _, source := range sources {
		if sourceID != "" && source.ID != sourceID {
			continue
		}
		found = true
		if sourceID == "" && !source.Enabled {
			continue
		}
		if !force && source.NextCrawlAt != nil && source.NextCrawlAt.After(now) {
			continue
		}
		selected = append(selected, source)
	}
	if !found {
		return model.Run{}, nil, store.ErrNotFound
	}
	// Lightweight API responses surface new jobs before slower article expansion.
	sort.SliceStable(selected, func(i, j int) bool {
		return strings.HasPrefix(selected[i].Kind, "jobs_") && !strings.HasPrefix(selected[j].Kind, "jobs_")
	})
	run := model.Run{ID: model.NewID("run"), StartedAt: now, Status: "running", Errors: []string{}}
	acquired, err := e.Store.AcquireCrawlLease(ctx, run.ID, 10*time.Minute)
	if err != nil {
		return run, nil, err
	}
	if !acquired {
		return model.Run{}, nil, ErrAlreadyRunning
	}
	if err := e.Store.SaveRun(ctx, run); err != nil {
		e.Store.ReleaseCrawlLease(context.WithoutCancel(ctx), run.ID)
		return run, nil, err
	}
	e.running, e.current = true, run
	e.wg.Add(1)
	return run, selected, nil
}

func (e *Engine) Run(ctx context.Context, sourceID string, force bool) (model.Run, error) {
	_, sources, err := e.begin(ctx, sourceID, force)
	if err != nil {
		return model.Run{}, err
	}
	defer e.wg.Done()
	return e.complete(ctx, sources, force)
}

func (e *Engine) Start(ctx context.Context, sourceID string, force bool) (model.Run, error) {
	run, sources, err := e.begin(ctx, sourceID, force)
	if err != nil {
		return run, err
	}
	go func() {
		defer e.wg.Done()
		finished, err := e.complete(ctx, sources, force)
		if err != nil {
			e.Logger.Error("crawl persistence failed", "error", err)
		}
		e.Logger.Info("source refresh finished", "run", finished.ID, "status", finished.Status, "seen", finished.OpportunitiesSeen, "created", finished.Created, "updated", finished.Updated)
	}()
	return run, nil
}

type sourceResult struct {
	seen, created, updated int
	warnings               []string
	err                    error
}

func (e *Engine) complete(ctx context.Context, sources []model.Source, force bool) (model.Run, error) {
	e.mu.Lock()
	owner := e.current.ID
	e.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				owned, err := e.Store.RenewCrawlLease(renewCtx, owner, 10*time.Minute)
				stop()
				if err != nil || !owned {
					e.Logger.Error("crawl lease could not be renewed", "error", err)
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		releaseCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := e.Store.ReleaseCrawlLease(releaseCtx, owner); err != nil {
			e.Logger.Error("release crawl lease", "error", err)
		}
	}()
	jobs := make(chan model.Source, len(sources))
	for _, source := range sources {
		jobs <- source
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(2, len(sources)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for source := range jobs {
				result := e.crawlSource(ctx, source, force)
				e.mu.Lock()
				e.current.SourcesChecked++
				e.current.OpportunitiesSeen += result.seen
				e.current.Created += result.created
				e.current.Updated += result.updated
				if result.err != nil {
					e.current.Errors = append(e.current.Errors, source.Name+": "+result.err.Error())
				}
				for _, warning := range result.warnings {
					e.current.Errors = append(e.current.Errors, source.Name+": "+warning)
				}
				if err := e.Store.SaveRun(context.WithoutCancel(ctx), e.current); err != nil {
					e.Logger.Error("save crawl progress", "error", err)
				}
				e.mu.Unlock()
			}
		}()
	}
	workers.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().UTC()
	e.current.FinishedAt, e.current.Status = &now, "completed"
	if ctx.Err() != nil {
		e.current.Status = "interrupted"
	} else if len(e.current.Errors) > 0 {
		e.current.Status = "partial"
		if e.current.OpportunitiesSeen == 0 {
			e.current.Status = "error"
		}
	}
	e.running = false
	err := e.Store.SaveRun(context.WithoutCancel(ctx), e.current)
	return e.current, err
}

func (e *Engine) HasDueSources(ctx context.Context) (bool, error) {
	sources, err := e.Store.Sources(ctx)
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, source := range sources {
		if source.Enabled && (source.NextCrawlAt == nil || !source.NextCrawlAt.After(now)) {
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) crawlSource(ctx context.Context, source model.Source, force bool) sourceResult {
	if strings.HasPrefix(source.Kind, "jobs_") {
		return e.crawlJobSource(ctx, source)
	}
	if strings.HasPrefix(source.Kind, "x_") {
		return e.crawlXSource(ctx, source)
	}
	result := sourceResult{warnings: []string{}}
	headers := http.Header{}
	if (source.Kind == "rss" || source.Kind == "rss_jobs") && !force {
		if source.ETag != "" {
			headers.Set("If-None-Match", source.ETag)
		}
		if source.LastModified != "" {
			headers.Set("If-Modified-Since", source.LastModified)
		}
	}
	response, err := e.Fetcher.Fetch(ctx, source.URL, headers)
	if err != nil {
		result.err = err
		return e.finishSource(ctx, source, result)
	}
	if response.NotModified {
		result.seen = source.ItemCount
		return e.finishSource(ctx, source, result)
	}
	if source.Kind == "rss" || source.Kind == "rss_jobs" {
		limit := e.MaxItems
		if source.Kind == "rss_jobs" {
			limit = min(limit, 10)
		}
		posts, err := ingest.ParseFeed(response.Body, response.URL, limit)
		if err != nil {
			result.err = err
			return e.finishSource(ctx, source, result)
		}
		for _, entry := range posts {
			if source.Kind == "rss_jobs" {
				entry.Job = &model.JobDetails{}
			}
			if ctx.Err() != nil {
				result.err = ctx.Err()
				break
			}
			if !ingest.IsOpportunity(entry) {
				continue
			}
			// University career feeds often contain the complete description,
			// salary and closing date. Keep that evidence and avoid a slower
			// portal fetch that may only expose an index or access block.
			if source.Kind == "rss_jobs" && len(entry.Body) >= 1000 {
				if err := e.savePost(ctx, entry, source, &result); err != nil {
					result.err = err
					break
				}
				continue
			}
			post := entry
			article, fetchErr := e.Fetcher.Fetch(ctx, entry.URL, nil)
			if fetchErr == nil {
				full, parseErr := ingest.ParsePage(article.Body, article.URL)
				if parseErr == nil {
					if source.Kind == "rss_jobs" && full.Job == nil {
						full.Job = entry.Job
					}
					if source.Kind == "rss_jobs" {
						// Career portals often canonicalize every vacancy to an index
						// page. The feed's listing URL/title remain authoritative.
						full.URL, full.Title = entry.URL, entry.Title
					}
					full.Categories = append(full.Categories, entry.Categories...)
					if full.Title == "" {
						full.Title = entry.Title
					}
					if full.PublishedAt == nil {
						full.PublishedAt = entry.PublishedAt
					}
					post = full
				} else {
					fetchErr = parseErr
				}
			}
			if fetchErr != nil {
				result.warnings = append(result.warnings, "article details unavailable for “"+entry.Title+"”; retained the existing record or RSS summary")
				if _, err := e.Store.GetByURL(ctx, entry.URL); err == nil {
					result.seen++
					continue
				} else if !errors.Is(err, store.ErrNotFound) {
					result.err = err
					break
				}
			}
			if err := e.savePost(ctx, post, source, &result); err != nil {
				result.err = err
				break
			}
		}
		// A partially read feed must be retried rather than hidden by a 304.
		if result.err == nil && len(result.warnings) == 0 {
			source.ETag, source.LastModified = response.Headers.Get("ETag"), response.Headers.Get("Last-Modified")
		} else {
			source.ETag, source.LastModified = "", ""
		}
	} else if source.Kind == "linkedin_page" || source.Kind == "linkedin_jobs" {
		discover := ingest.DiscoverLinkedIn
		limit := e.MaxItems
		if source.Kind == "linkedin_jobs" {
			discover, limit = ingest.DiscoverLinkedInJobs, min(limit, 10)
		}
		links, err := discover(response.Body, response.URL, limit)
		if err != nil {
			result.err = err
			return e.finishSource(ctx, source, result)
		}
		for _, link := range links {
			page, err := e.Fetcher.Fetch(ctx, link, nil)
			if err != nil {
				result.warnings = append(result.warnings, err.Error())
				continue
			}
			post, err := ingest.ParsePage(page.Body, page.URL)
			if err != nil {
				result.warnings = append(result.warnings, err.Error())
				continue
			}
			if !ingest.IsOpportunity(post) {
				continue
			}
			if err := e.savePost(ctx, post, source, &result); err != nil {
				result.err = err
				break
			}
		}
	} else {
		post, err := ingest.ParsePage(response.Body, response.URL)
		if err != nil {
			result.err = err
			return e.finishSource(ctx, source, result)
		}
		if !ingest.IsOpportunity(post) {
			result.err = ErrNotOpportunity
			return e.finishSource(ctx, source, result)
		}
		result.err = e.savePost(ctx, post, source, &result)
	}
	return e.finishSource(ctx, source, result)
}

func (e *Engine) crawlJobSource(ctx context.Context, source model.Source) sourceResult {
	result := sourceResult{warnings: []string{}}
	if e.Jobs == nil {
		provider, _ := ingest.JobProviderFor(source.Kind)
		result.err = &ingest.JobAccessError{Provider: provider.Name, Missing: provider.Credentials}
		if len(provider.Credentials) == 0 {
			result.err = errors.New("job API reader is unavailable")
		}
		return e.finishSource(ctx, source, result)
	}
	posts, err := e.Jobs.Read(ctx, source, e.MaxItems)
	if err != nil {
		result.err = err
		return e.finishSource(ctx, source, result)
	}
	for _, post := range posts {
		if ctx.Err() != nil {
			result.err = ctx.Err()
			break
		}
		if err := e.savePost(ctx, post, source, &result); err != nil {
			result.err = err
			break
		}
	}
	return e.finishSource(ctx, source, result)
}

func (e *Engine) sourceInterval(source model.Source) time.Duration {
	interval := e.Interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	if source.IntervalMinutes >= 5 && source.IntervalMinutes <= 1440 {
		interval = time.Duration(source.IntervalMinutes) * time.Minute
	}
	if provider, ok := ingest.JobProviderFor(source.Kind); ok {
		interval = max(interval, time.Duration(provider.IntervalMinutes)*time.Minute)
	}
	return interval
}

func (e *Engine) crawlXSource(ctx context.Context, source model.Source) sourceResult {
	result := sourceResult{warnings: []string{}}
	if source.Kind == "x_archive" {
		result.err = errors.New("archived X imports are refreshed through a search, profile, or individual post source")
		return e.finishSource(ctx, source, result)
	}
	if e.X == nil || !e.X.Configured() {
		result.err = ingest.ErrXNotConfigured
		return e.finishSource(ctx, source, result)
	}
	posts := []model.Post{}
	var page ingest.XPage
	var err error
	if source.Kind == "x_post" {
		var post model.Post
		post, err = e.X.Post(ctx, ingest.XPostID(source.URL))
		if err == nil {
			posts = append(posts, post)
		}
	} else {
		page, err = e.X.Page(ctx, source, e.MaxItems)
		posts = page.Posts
		result.warnings = append(result.warnings, page.Warnings...)
	}
	if err != nil {
		result.err = err
		return e.finishSource(ctx, source, result)
	}
	seen := map[string]bool{}
	for _, post := range posts {
		if !ingest.IsOpportunity(post) {
			continue
		}
		id := ingest.XPostID(post.URL)
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := e.savePost(ctx, post, source, &result); err != nil {
			result.err = err
			break
		}
	}
	if result.err == nil && source.Kind != "x_post" {
		known, _, lookupErr := e.Store.List(ctx, model.Query{Source: source.ID, Status: "all", Sort: "updated", Limit: 5})
		if lookupErr != nil {
			result.err = lookupErr
		} else {
			ids := []string{}
			for _, record := range known {
				id := ingest.XPostID(record.URL)
				if id != "" && !seen[id] {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				refreshed, refreshErr := e.X.Posts(ctx, ids)
				if refreshErr != nil {
					result.warnings = append(result.warnings, "previous X post metrics could not be refreshed: "+refreshErr.Error())
				} else {
					for _, post := range refreshed {
						if err := e.savePost(ctx, post, source, &result); err != nil {
							result.err = err
							break
						}
					}
				}
			}
		}
		if result.err == nil {
			source.PendingSinceID = ingest.NewerXID(source.PendingSinceID, page.NewestID)
			source.NextToken = page.NextToken
			if source.NextToken == "" {
				source.SinceID = ingest.NewerXID(source.SinceID, source.PendingSinceID)
				source.PendingSinceID = ""
			}
			if page.UserID != "" {
				source.XUserID = page.UserID
			}
		}
	}
	return e.finishSource(ctx, source, result)
}

func (e *Engine) savePost(ctx context.Context, post model.Post, source model.Source, result *sourceResult) error {
	opportunity := ingest.Extract(post, source, time.Now())
	upsert, err := e.Store.Upsert(ctx, opportunity)
	if err != nil {
		return err
	}
	result.seen++
	if upsert.Created {
		result.created++
	}
	if upsert.Updated {
		result.updated++
	}
	return nil
}

func (e *Engine) finishSource(ctx context.Context, source model.Source, result sourceResult) sourceResult {
	now := time.Now().UTC()
	source.LastCrawledAt, source.ItemCount = &now, result.seen
	interval := e.sourceInterval(source)
	if result.err != nil {
		source.Failures++
		source.Status, source.LastError = "error", result.err.Error()
		interval = min(24*time.Hour, interval*time.Duration(1<<min(source.Failures, 6)))
		if ingest.AccessBlocked(result.err) {
			source.Status, interval = "blocked", max(interval, 6*time.Hour)
		}
	} else {
		source.Failures, source.Status, source.LastError = 0, "ok", ""
		if len(result.warnings) > 0 {
			source.Status, source.LastError = "partial", strings.Join(result.warnings, "\n")
		}
	}
	next := now.Add(interval)
	var xError *ingest.XAPIError
	if errors.As(result.err, &xError) && xError.RetryAt != nil && xError.RetryAt.After(next) {
		next = xError.RetryAt.Add(time.Second)
	}
	var jobError *ingest.JobAccessError
	if errors.As(result.err, &jobError) && jobError.RetryAt != nil && jobError.RetryAt.After(next) {
		next = jobError.RetryAt.Add(time.Second)
	}
	source.NextCrawlAt = &next
	if _, err := e.Store.RecordSourceFetch(context.WithoutCancel(ctx), source); err != nil {
		result.err = errors.Join(result.err, err)
	}
	return result
}

func (e *Engine) Schedule(ctx context.Context, interval time.Duration, initial bool) {
	start := func() {
		sources, err := e.Store.Sources(ctx)
		if err != nil {
			if ctx.Err() == nil {
				e.Logger.Error("read scheduled sources", "error", err)
			}
			return
		}
		due, now := false, time.Now()
		for _, source := range sources {
			if source.Enabled && (source.NextCrawlAt == nil || !source.NextCrawlAt.After(now)) {
				due = true
				break
			}
		}
		if !due {
			return
		}
		if _, err := e.Start(ctx, "", false); err != nil && !errors.Is(err, ErrAlreadyRunning) {
			e.Logger.Error("start scheduled refresh", "error", err)
		}
	}
	if initial {
		start()
	}
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start()
		}
	}
}

type ImportInput struct {
	URL       string `json:"url"`
	Text      string `json:"text"`
	Title     string `json:"title"`
	Organizer string `json:"organizer"`
}

func (e *Engine) Import(ctx context.Context, input ImportInput) (store.UpsertResult, error) {
	u, err := ingest.ValidateURL(input.URL)
	if err != nil {
		return store.UpsertResult{}, err
	}
	if len(input.Text) > 250000 {
		return store.UpsertResult{}, errors.New("post text exceeds the 250,000 character limit")
	}
	kind := ingest.SourceKind(input.URL)
	if strings.HasPrefix(kind, "jobs_") || kind == "linkedin_jobs" {
		return store.UpsertResult{}, errors.New("add job searches and job APIs under Sources; import an individual job URL here")
	}
	post := model.Post{URL: ingest.CanonicalURL(input.URL), Body: strings.TrimSpace(input.Text), Title: input.Title, Author: input.Organizer, CaptureMethod: "manual_import"}
	if kind == "linkedin_job" {
		post.Job = &model.JobDetails{Provider: "LinkedIn", ProviderURL: "https://www.linkedin.com/jobs"}
	}
	if strings.HasPrefix(kind, "x_") {
		post.Author = ""
	}
	if post.Body != "" {
		if known, err := e.Store.GetByURL(ctx, post.URL); err == nil {
			post.URL = known.URL
			if strings.HasPrefix(kind, "x_") {
				post.Author = known.PostAuthor
				if strings.TrimSpace(input.Organizer) == "" {
					input.Organizer = known.Organizer
				}
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return store.UpsertResult{}, err
		}
	}
	if post.Body == "" {
		if kind == "rss" || kind == "linkedin_page" || kind == "x_profile" || kind == "x_search" {
			return store.UpsertResult{}, errors.New("add feeds and profile/company pages under Sources; import an individual post or article here")
		}
		if kind == "x_post" {
			if e.X == nil {
				return store.UpsertResult{}, ingest.ErrXNotConfigured
			}
			post, err = e.X.Post(ctx, ingest.XPostID(input.URL))
		} else {
			var response ingest.FetchResult
			response, err = e.Fetcher.Fetch(ctx, input.URL, nil)
			if err == nil {
				post, err = ingest.ParsePage(response.Body, response.URL)
			}
		}
		if err != nil {
			return store.UpsertResult{}, err
		}
		if input.Title != "" {
			post.Title = input.Title
		}
		if input.Organizer != "" && !strings.HasPrefix(kind, "x_") {
			post.Author = input.Organizer
		}
	}
	if !ingest.IsOpportunity(post) {
		return store.UpsertResult{}, ErrNotOpportunity
	}
	name := post.Author
	if name == "" {
		name = u.Hostname()
	}
	enabled := !strings.HasPrefix(kind, "x_") || post.CaptureMethod == "x_api"
	source, _, err := e.Store.AddSource(ctx, model.Source{URL: ingest.CanonicalURL(input.URL), Name: name, Kind: kind, Enabled: enabled})
	if err != nil {
		return store.UpsertResult{}, err
	}
	now := time.Now().UTC()
	opportunity := ingest.Extract(post, source, now)
	if strings.HasPrefix(kind, "x_") && strings.TrimSpace(input.Organizer) != "" {
		opportunity.Organizer = strings.TrimSpace(input.Organizer)
		opportunity.Evidence["organizer"] = "Organizer supplied during import"
	}
	result, err := e.Store.Upsert(ctx, opportunity)
	if err != nil {
		return result, err
	}
	if post.CaptureMethod == "public_page" || post.CaptureMethod == "public_job" || post.CaptureMethod == "x_api" {
		source.LastCrawledAt, source.Status, source.LastError, source.Failures, source.ItemCount = &now, "ok", "", 0, 1
		next := now.Add(e.sourceInterval(source))
		source.NextCrawlAt = &next
		_, err = e.Store.RecordSourceFetch(ctx, source)
	}
	return result, err
}

func ValidateSource(source model.Source) error {
	u, err := ingest.ValidateURL(source.URL)
	if err != nil {
		return err
	}
	if !model.Contains(model.SourceKinds, source.Kind) {
		return errors.New("unsupported source type")
	}
	if len(strings.TrimSpace(source.Name)) == 0 || len(source.Name) > 120 {
		return errors.New("source name must be between 1 and 120 characters")
	}
	if source.IntervalMinutes != 0 && (source.IntervalMinutes < 5 || source.IntervalMinutes > 1440) {
		return errors.New("source refresh interval must be between 5 and 1440 minutes")
	}
	detected := ingest.SourceKind(source.URL)
	if strings.HasPrefix(source.Kind, "jobs_") {
		return ingest.ValidateJobSource(source)
	}
	if strings.HasPrefix(detected, "jobs_") {
		return errors.New("select the matching job API source type; API credentials belong in server environment variables")
	}
	if source.Kind == "linkedin_job" && ingest.LinkedInJobID(source.URL) == "" {
		return errors.New("use an individual LinkedIn job URL")
	}
	if source.Kind == "linkedin_jobs" && !ingest.IsLinkedInJobSearch(source.URL) {
		return errors.New("use a public LinkedIn job search or company jobs URL")
	}
	if strings.HasPrefix(source.Kind, "x_") {
		if !ingest.IsX(u.Hostname()) {
			return errors.New("X / Twitter sources must use an x.com or twitter.com URL")
		}
		switch source.Kind {
		case "x_search":
			if strings.TrimRight(u.Path, "/") != "/search" || strings.TrimSpace(u.Query().Get("q")) == "" || len(u.Query().Get("q")) > 512 {
				return errors.New("X search needs https://x.com/search?q=... with a query of 1–512 characters")
			}
		case "x_profile":
			if ingest.XProfile(source.URL) == "" {
				return errors.New("use an X profile URL such as https://x.com/username")
			}
		case "x_post":
			if ingest.XPostID(source.URL) == "" {
				return errors.New("use an individual X post URL")
			}
		case "x_archive":
			return errors.New("X archive sources are created by the legacy import command")
		}
	}
	if strings.HasPrefix(source.Kind, "linkedin_") && !ingest.IsLinkedIn(u.Hostname()) {
		return errors.New("LinkedIn sources must use a linkedin.com URL")
	}
	if source.Kind == "linkedin_post" && ingest.SourceKind(source.URL) != "linkedin_post" {
		return errors.New("use an individual LinkedIn post URL for this source type")
	}
	if ingest.IsLinkedIn(u.Hostname()) && (strings.HasPrefix(u.Path, "/login") || strings.HasPrefix(u.Path, "/signup") || strings.HasPrefix(u.Path, "/uas/")) {
		return errors.New("use a public post or company/profile URL")
	}
	if source.Kind == "rss" && ingest.IsLinkedIn(u.Hostname()) {
		return errors.New("LinkedIn does not expose RSS feeds; choose a public post or page")
	}
	return nil
}

func Host(raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return raw
	}
	return u.Hostname()
}

func RunError(run model.Run) error {
	if run.Status == "error" || run.Status == "interrupted" {
		return fmt.Errorf("refresh %s: %s", run.Status, strings.Join(run.Errors, "; "))
	}
	return nil
}
