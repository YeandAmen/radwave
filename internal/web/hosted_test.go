package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/crawler"
	"github.com/YeandAmen/opportunity-radar/internal/model"
	"github.com/YeandAmen/opportunity-radar/internal/store"
	"github.com/YeandAmen/opportunity-radar/internal/web"
)

func TestHostedRefreshCompletesWithinRequestAndCronRespectsBackoff(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, _, err := s.AddSource(ctx, model.Source{Name: "Tracked post", URL: "https://www.linkedin.com/posts/test-activity-7520960460347768832-x", Kind: "linkedin_post", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e := crawler.New(s, blockedFetcher{}, 30*time.Minute)
	defer e.Shutdown()
	serv := httptest.NewServer(web.New(s, e, web.Config{Addr: "127.0.0.1:8080", SynchronousCrawls: true, CronSecret: "cron-test"}))
	defer serv.Close()
	response := request(t, serv.Client(), "POST", serv.URL+"/api/crawl", "", "", map[string]any{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("hosted refresh returned %d", response.StatusCode)
	}
	var result struct {
		Run model.Run `json:"run"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Run.FinishedAt == nil || result.Run.SourcesChecked != 1 {
		t.Fatal("response returned before the source refresh finished")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/cron", "", "", nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("cron endpoint is unauthenticated")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/cron", "cron-test", "", nil)
	var skipped struct {
		Skipped bool `json:"skipped"`
	}
	if err := json.NewDecoder(response.Body).Decode(&skipped); err != nil || !skipped.Skipped {
		t.Fatal("cron ignored source backoff")
	}
	runs, err := s.Runs(ctx)
	if err != nil || len(runs) != 1 {
		t.Fatal("an idle cron created an empty refresh report")
	}
}
