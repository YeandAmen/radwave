package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/bootstrap"
	"github.com/YeandAmen/opportunity-radar/internal/crawler"
	"github.com/YeandAmen/opportunity-radar/internal/ingest"
	"github.com/YeandAmen/opportunity-radar/internal/model"
	"github.com/YeandAmen/opportunity-radar/internal/store"
	"github.com/YeandAmen/opportunity-radar/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	if command == "version" {
		fmt.Println("Opportunity Radar v0.1.0")
		return nil
	}
	if !model.Contains([]string{"serve", "crawl", "import", "export", "migrate"}, command) {
		return fmt.Errorf("unknown command %q; use serve, crawl, import, export, migrate, or version", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	dbPath := flags.String("db", env("RADAR_DB", "data/radar.db"), "SQLite database path")
	seed := flags.Bool("seed", true, "install public starter sources and the dated reference example")
	intervalValue := flags.String("interval", env("RADAR_INTERVAL", "30m"), "source refresh interval; 0 disables the timer")
	delayValue := flags.String("request-delay", env("RADAR_REQUEST_DELAY", "2s"), "minimum delay between requests to the same host")
	timeoutValue := flags.String("http-timeout", env("RADAR_HTTP_TIMEOUT", "25s"), "timeout per source request")
	maxItems := flags.Int("max-items", 20, "maximum feed entries or public page links per source refresh (1-50)")
	var addr, sourceID, importURL, textFile, title, organizer, format *string
	var migrateFrom, envFile *string
	var autoCrawl *bool
	switch command {
	case "serve":
		addr = flags.String("addr", env("RADAR_ADDR", "127.0.0.1:8080"), "HTTP listening address")
		autoCrawl = flags.Bool("auto-crawl", envBool("RADAR_AUTO_CRAWL", true), "refresh sources on startup and on the interval")
	case "crawl":
		sourceID = flags.String("source", "", "refresh only this source ID")
	case "import":
		importURL = flags.String("url", "", "original post or article URL")
		textFile = flags.String("text-file", "", "plain-text post content to import instead of fetching")
		title = flags.String("title", "", "optional title override")
		organizer = flags.String("organizer", "", "optional organizer name")
	case "export":
		format = flags.String("format", "json", "json or csv")
	case "migrate":
		migrateFrom = flags.String("from", "data/radar.db", "source SQLite database to copy into an empty destination")
		envFile = flags.String("env-file", "", "private environment file containing the hosted DATABASE_URL")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "migrate" {
		if *envFile != "" {
			if err := loadEnvironment(*envFile); err != nil {
				return err
			}
		}
		explicitDB := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "db" {
				explicitDB = true
			}
		})
		if !explicitDB && os.Getenv("DATABASE_URL") != "" {
			*dbPath = os.Getenv("DATABASE_URL")
		}
		if *dbPath == *migrateFrom {
			return errors.New("specify a different destination with DATABASE_URL or -db")
		}
	}
	interval, err := time.ParseDuration(*intervalValue)
	if err != nil || interval < 0 {
		return errors.New("interval must be a non-negative duration")
	}
	delay, err := time.ParseDuration(*delayValue)
	if err != nil || delay < 0 {
		return errors.New("request-delay must be a non-negative duration")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil || timeout <= 0 {
		return errors.New("http-timeout must be a positive duration")
	}
	if *maxItems < 1 || *maxItems > 50 {
		return errors.New("max-items must be between 1 and 50")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if command == "migrate" {
		source, err := store.Open(*migrateFrom)
		if err != nil {
			return err
		}
		defer source.Close()
		counts, err := source.CopyIntoEmpty(ctx, s)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(counts)
	}
	if *seed {
		if err := bootstrap.Seed(ctx, s); err != nil {
			return err
		}
	}
	engine := crawler.New(s, ingest.NewHTTPFetcher(timeout, delay), interval)
	engine.MaxItems = *maxItems
	switch command {
	case "serve":
		if err := s.RecoverRuns(ctx); err != nil {
			return err
		}
		server := &http.Server{
			Addr: *addr, Handler: web.New(s, engine, web.Config{Addr: *addr, AdminToken: os.Getenv("RADAR_ADMIN_TOKEN"), Context: ctx}),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 60 * time.Second,
		}
		errorsCh := make(chan error, 1)
		go func() { errorsCh <- server.ListenAndServe() }()
		slog.Info("Opportunity Radar is ready", "url", "http://"+*addr, "storage", s.Kind())
		if *autoCrawl {
			go engine.Schedule(ctx, interval, true)
		}
		select {
		case <-ctx.Done():
		case err := <-errorsCh:
			cancel()
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		}
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		server.Shutdown(shutdownCtx)
		engine.Shutdown()
		return nil
	case "crawl":
		run, err := engine.Run(ctx, *sourceID, true)
		if err != nil {
			return err
		}
		json.NewEncoder(os.Stdout).Encode(run)
		return crawler.RunError(run)
	case "import":
		input := crawler.ImportInput{URL: *importURL, Title: *title, Organizer: *organizer}
		if *textFile != "" {
			text, err := os.ReadFile(*textFile)
			if err != nil {
				return err
			}
			input.Text = string(text)
		}
		result, err := engine.Import(ctx, input)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result.Opportunity)
	case "export":
		items, _, err := s.List(ctx, model.Query{Status: "all", Limit: 10000, Sort: "deadline"})
		if err != nil {
			return err
		}
		if *format == "json" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(items)
		}
		if *format != "csv" {
			return errors.New("format must be json or csv")
		}
		writer := csv.NewWriter(os.Stdout)
		writer.Write([]string{"title", "kind", "deadline", "status", "application_url", "source_url"})
		for _, item := range items {
			row := []string{item.Title, item.Kind, item.Deadline, item.Status, item.ApplicationURL, item.URL}
			for i := range row {
				if text := strings.TrimSpace(row[i]); text != "" && strings.ContainsRune("=+-@", rune(text[0])) {
					row[i] = "'" + row[i]
				}
			}
			writer.Write(row)
		}
		writer.Flush()
		return writer.Error()
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func envBool(name string, fallback bool) bool {
	value, err := strconv.ParseBool(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return value
}
