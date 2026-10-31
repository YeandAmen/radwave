// Vercel's Go framework discovers this HTTP entrypoint and supplies PORT.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/YeandAmen/radwave/internal/deployment"
	"github.com/YeandAmen/radwave/internal/ingest"
)

func main() {
	if err := serve(); err != nil {
		slog.Error("hosted server stopped", "error", err)
		os.Exit(1)
	}
}

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = os.Getenv("RADAR_DATABASE_URL")
	}
	if url == "" {
		url = os.Getenv("RADAR_DB")
	}
	app, err := deployment.New(ctx, deployment.Config{
		DatabaseURL: url, Addr: ":" + port, AdminToken: os.Getenv("RADAR_ADMIN_TOKEN"), CronSecret: os.Getenv("CRON_SECRET"),
		RequireSharedStorage: os.Getenv("VERCEL") != "",
		XBearerToken:         os.Getenv("X_BEARER_TOKEN"),
		JobCredentials:       ingest.JobCredentials{AdzunaAppID: os.Getenv("ADZUNA_APP_ID"), AdzunaAppKey: os.Getenv("ADZUNA_APP_KEY"), USAJobsAPIKey: os.Getenv("USAJOBS_API_KEY"), USAJobsEmail: os.Getenv("USAJOBS_EMAIL"), ReedAPIKey: os.Getenv("REED_API_KEY"), JoobleAPIKey: os.Getenv("JOOBLE_API_KEY")},
	})
	if err != nil {
		return err
	}
	defer app.Close()
	server := &http.Server{
		Addr: ":" + port, Handler: app.Handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 260 * time.Second, IdleTimeout: 60 * time.Second,
	}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	slog.Info("hosted Radwave is ready", "port", port)
	select {
	case <-ctx.Done():
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
