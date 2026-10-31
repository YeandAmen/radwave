// Package crawler coordinates bounded, rate-limited source refreshes.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
	result := sourceResult{warnings: []string{}}
	headers := http.Header{}
	if source.Kind == "rss" && !force {
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
	if source.Kind == "rss" {
		posts, err := ingest.ParseFeed(response.Body, response.URL, e.MaxItems)
		if err != nil {
			result.err = err
			return e.finishSource(ctx, source, result)
		}
		for _, entry := range posts {
			if ctx.Err() != nil {
				result.err = ctx.Err()
				break
			}
			if !ingest.IsOpportunity(entry) {
				continue
			}
			post := entry
			article, fetchErr := e.Fetcher.Fetch(ctx, entry.URL, nil)
			if fetchErr == nil {
				full, parseErr := ingest.ParsePage(article.Body, article.URL)
				if parseErr == nil {
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
	} else if source.Kind == "linkedin_page" {
		links, err := ingest.DiscoverLinkedIn(response.Body, response.URL, e.MaxItems)
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
	interval := e.Interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
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
	post := model.Post{URL: ingest.CanonicalURL(input.URL), Body: strings.TrimSpace(input.Text), Title: input.Title, Author: input.Organizer, CaptureMethod: "manual_import"}
	if post.Body != "" {
		if known, err := e.Store.GetByURL(ctx, post.URL); err == nil {
			post.URL = known.URL
		} else if !errors.Is(err, store.ErrNotFound) {
			return store.UpsertResult{}, err
		}
	}
	if post.Body == "" {
		if kind := ingest.SourceKind(input.URL); kind == "rss" || kind == "linkedin_page" {
			return store.UpsertResult{}, errors.New("add feeds and profile/company pages under Sources; import an individual post or article here")
		}
		response, err := e.Fetcher.Fetch(ctx, input.URL, nil)
		if err != nil {
			return store.UpsertResult{}, err
		}
		post, err = ingest.ParsePage(response.Body, response.URL)
		if err != nil {
			return store.UpsertResult{}, err
		}
		if input.Title != "" {
			post.Title = input.Title
		}
		if input.Organizer != "" {
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
	source, _, err := e.Store.AddSource(ctx, model.Source{URL: ingest.CanonicalURL(input.URL), Name: name, Kind: ingest.SourceKind(input.URL), Enabled: true})
	if err != nil {
		return store.UpsertResult{}, err
	}
	now := time.Now().UTC()
	result, err := e.Store.Upsert(ctx, ingest.Extract(post, source, now))
	if err != nil {
		return result, err
	}
	if post.CaptureMethod == "public_page" {
		source.LastCrawledAt, source.Status, source.LastError, source.Failures, source.ItemCount = &now, "ok", "", 0, 1
		next := now.Add(max(e.Interval, 30*time.Minute))
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
