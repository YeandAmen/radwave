// Package deployment constructs the shared-storage, request-scoped Vercel app.
package deployment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/bootstrap"
	"github.com/YeandAmen/opportunity-radar/internal/crawler"
	"github.com/YeandAmen/opportunity-radar/internal/ingest"
	"github.com/YeandAmen/opportunity-radar/internal/store"
	"github.com/YeandAmen/opportunity-radar/internal/web"
)

type Config struct {
	DatabaseURL          string
	AdminToken           string
	CronSecret           string
	Addr                 string
	RequireSharedStorage bool
}

type App struct {
	Handler http.Handler
	store   *store.Store
	engine  *crawler.Engine
}

func New(ctx context.Context, config Config) (*App, error) {
	if config.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL must be configured for the hosted app")
	}
	if config.RequireSharedStorage && !strings.HasPrefix(config.DatabaseURL, "postgres://") && !strings.HasPrefix(config.DatabaseURL, "postgresql://") {
		return nil, errors.New("the hosted app requires a shared PostgreSQL DATABASE_URL")
	}
	s, err := store.Open(config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := bootstrap.Seed(ctx, s); err != nil {
		s.Close()
		return nil, err
	}
	e := crawler.New(s, ingest.NewHTTPFetcher(20*time.Second, 2*time.Second), 30*time.Minute)
	app := &App{store: s, engine: e}
	app.Handler = web.New(s, e, web.Config{
		Addr: config.Addr, Context: ctx, AdminToken: config.AdminToken, CronSecret: config.CronSecret,
		SynchronousCrawls: true, CrawlTimeout: 230 * time.Second,
	})
	return app, nil
}

func (a *App) Close() error { a.engine.Shutdown(); return a.store.Close() }
