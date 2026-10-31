// Package web serves the embedded dashboard and JSON API.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/YeandAmen/radwave/internal/crawler"
	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

//go:embed assets/*
var assets embed.FS

type Config struct {
	Addr              string
	AdminToken        string
	Context           context.Context
	SynchronousCrawls bool
	CronSecret        string
	CrawlTimeout      time.Duration
}

type Server struct {
	store     *store.Store
	engine    *crawler.Engine
	config    Config
	mux       *http.ServeMux
	localOnly bool
	writable  bool
}

func New(s *store.Store, engine *crawler.Engine, config Config) *Server {
	if config.Context == nil {
		config.Context = context.Background()
	}
	host, _, _ := net.SplitHostPort(config.Addr)
	local := host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	server := &Server{store: s, engine: engine, config: config, mux: http.NewServeMux(), localOnly: local, writable: local || config.AdminToken != ""}
	server.mux.HandleFunc("GET /{$}", server.index)
	server.mux.HandleFunc("GET /opportunity/{id}", server.index)
	static, _ := fs.Sub(assets, "assets")
	server.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(static))))
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("GET /api/meta", server.meta)
	server.mux.HandleFunc("GET /api/opportunities", server.opportunities)
	server.mux.HandleFunc("GET /api/opportunities/{id}", server.opportunity)
	server.mux.HandleFunc("GET /api/sources", server.sources)
	server.mux.HandleFunc("POST /api/sources", server.write(server.addSource))
	server.mux.HandleFunc("PATCH /api/sources/{id}", server.write(server.configureSource))
	server.mux.HandleFunc("POST /api/crawl", server.write(server.crawl))
	server.mux.HandleFunc("GET /api/cron", server.cron)
	server.mux.HandleFunc("POST /api/import", server.write(server.importPost))
	server.mux.HandleFunc("POST /api/unlock", server.write(func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]bool{"unlocked": true})
	}))
	server.mux.HandleFunc("GET /api/runs", server.runs)
	server.mux.HandleFunc("GET /api/export", server.export)
	server.mux.HandleFunc("GET /feed.xml", server.feed)
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; worker-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	if s.localOnly {
		host := r.Host
		if split, _, err := net.SplitHostPort(host); err == nil {
			host = split
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			fail(w, http.StatusForbidden, "unrecognized host for a local-only server")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) write(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.writable {
			fail(w, http.StatusForbidden, "management is read-only; configure RADAR_ADMIN_TOKEN to enable imports and source management")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) || u.Scheme != "http" && u.Scheme != "https" {
				fail(w, http.StatusForbidden, "cross-origin writes are not allowed")
				return
			}
		}
		if s.config.AdminToken != "" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.AdminToken)) != 1 {
				fail(w, http.StatusUnauthorized, "enter the management token to make changes")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	page, _ := assets.ReadFile("assets/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(page)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		fail(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	runs, err := s.store.Runs(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	var latest *model.Run
	if len(runs) > 0 {
		latest = &runs[0]
	}
	running, err := s.store.CrawlRunning(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"name": "Radwave", "version": "0.2.0", "stats": stats, "last_run": latest,
		"running": s.engine.Running() || running, "kinds": model.Kinds, "regions": model.Regions,
		"storage": s.store.Kind(), "hosted": s.config.SynchronousCrawls,
		"integrations": map[string]any{"x": map[string]any{"configured": s.engine.X != nil && s.engine.X.Configured(), "api_access_required": true}},
		"x_watchlists": ingest.XWatchlists,
		"management":   map[string]bool{"enabled": s.writable, "token_required": s.config.AdminToken != ""},
	})
}

func query(r *http.Request) model.Query {
	v := r.URL.Query()
	limit, _ := strconv.Atoi(v.Get("limit"))
	offset, _ := strconv.Atoi(v.Get("offset"))
	status := v.Get("status")
	if status == "" {
		status = "active"
	}
	q := model.Query{Search: v.Get("q"), Kind: v.Get("kind"), Region: v.Get("region"), Status: status, Source: v.Get("source"), Platform: v.Get("platform"), Sort: v.Get("sort"), Limit: min(max(limit, 0), 200), Offset: min(max(offset, 0), 1000000)}
	if ids := v.Get("ids"); ids != "" {
		q.IDs = strings.Split(ids, ",")
		if len(q.IDs) > 200 {
			q.IDs = q.IDs[:200]
		}
	}
	return q
}

func (s *Server) opportunities(w http.ResponseWriter, r *http.Request) {
	items, total, err := s.store.List(r.Context(), query(r))
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) opportunity(w http.ResponseWriter, r *http.Request) {
	opportunity, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		recordError(w, err)
		return
	}
	history, err := s.store.History(r.Context(), opportunity.ID)
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{"opportunity": opportunity, "history": history})
}

func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.store.Sources(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": sources})
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Kind    string `json:"kind"`
		Enabled *bool  `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	kind := input.Kind
	if kind == "" {
		kind = ingest.SourceKind(input.URL)
	}
	source := model.Source{Name: strings.TrimSpace(input.Name), URL: strings.TrimSpace(input.URL), Kind: kind, Enabled: input.Enabled == nil || *input.Enabled}
	if err := crawler.ValidateSource(source); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	sources, err := s.store.Sources(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	if len(sources) >= 100 {
		fail(w, http.StatusBadRequest, "the 100-source limit has been reached")
		return
	}
	source.URL = ingest.CanonicalURL(source.URL)
	source, created, err := s.store.AddSource(r.Context(), source)
	if err != nil {
		internalError(w)
		return
	}
	code := http.StatusCreated
	if !created {
		code = http.StatusOK
	}
	respond(w, code, map[string]any{"source": source, "created": created})
}

func (s *Server) configureSource(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool   `json:"enabled"`
		Name    *string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || len(name) > 120 {
			fail(w, http.StatusBadRequest, "source name must be between 1 and 120 characters")
			return
		}
		input.Name = &name
	}
	source, err := s.store.ConfigureSource(r.Context(), r.PathValue("id"), input.Enabled, input.Name)
	if err != nil {
		recordError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"source": source})
}

func (s *Server) crawl(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SourceID string `json:"source_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &input) {
		return
	}
	if s.config.SynchronousCrawls {
		s.crawlInRequest(w, r, input.SourceID, true)
		return
	}
	run, err := s.engine.Start(s.config.Context, input.SourceID, true)
	if err != nil {
		if errors.Is(err, crawler.ErrAlreadyRunning) {
			fail(w, http.StatusConflict, err.Error())
		} else {
			recordError(w, err)
		}
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *Server) cron(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.config.CronSecret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(s.config.CronSecret)) != 1 {
		fail(w, http.StatusUnauthorized, "cron authentication required")
		return
	}
	due, err := s.engine.HasDueSources(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	if !due {
		respond(w, http.StatusOK, map[string]any{"skipped": true, "reason": "no sources are due"})
		return
	}
	s.crawlInRequest(w, r, "", false)
}

func (s *Server) crawlInRequest(w http.ResponseWriter, r *http.Request, sourceID string, force bool) {
	timeout := s.config.CrawlTimeout
	if timeout <= 0 {
		timeout = 230 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	run, err := s.engine.Run(ctx, sourceID, force)
	if err != nil {
		if errors.Is(err, crawler.ErrAlreadyRunning) {
			fail(w, http.StatusConflict, err.Error())
		} else {
			recordError(w, err)
		}
		return
	}
	respond(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) importPost(w http.ResponseWriter, r *http.Request) {
	var input crawler.ImportInput
	if !decode(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	result, err := s.engine.Import(ctx, input)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	code := http.StatusOK
	if result.Created {
		code = http.StatusCreated
	}
	respond(w, code, map[string]any{"opportunity": result.Opportunity, "created": result.Created, "updated": result.Updated})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.Runs(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": runs})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "send one JSON object per request")
		return false
	}
	return true
}

func respond(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, code int, message string) {
	respond(w, code, map[string]string{"error": message})
}
func internalError(w http.ResponseWriter) {
	fail(w, http.StatusInternalServerError, "the request could not be completed")
}
func recordError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "record not found")
	} else {
		internalError(w)
	}
}
