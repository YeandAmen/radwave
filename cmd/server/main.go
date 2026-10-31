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

	"github.com/YeandAmen/opportunity-radar/internal/deployment"
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
	slog.Info("hosted Opportunity Radar is ready", "port", port)
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
