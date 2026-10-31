package web_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/crawler"
	"github.com/YeandAmen/opportunity-radar/internal/ingest"
	"github.com/YeandAmen/opportunity-radar/internal/store"
	"github.com/YeandAmen/opportunity-radar/internal/web"
)

type blockedFetcher struct{}

func (blockedFetcher) Fetch(_ context.Context, raw string, _ http.Header) (ingest.FetchResult, error) {
	return ingest.FetchResult{}, &ingest.FetchError{Status: 999, URL: raw}
}

func server(t *testing.T, addr, token string) (*httptest.Server, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := crawler.New(s, blockedFetcher{}, 30*time.Minute)
	httpServer := httptest.NewServer(web.New(s, e, web.Config{Addr: addr, AdminToken: token, Context: ctx}))
	t.Cleanup(func() { httpServer.Close(); cancel(); e.Shutdown(); s.Close() })
	return httpServer, s
}

func request(t *testing.T, client *http.Client, method, raw, token, origin string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, raw, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestManagementAuthCSRFAndPublicReadOnlyMode(t *testing.T) {
	serv, s := server(t, "127.0.0.1:8080", "test-token")
	input := map[string]any{"name": "Example", "url": "https://example.org/feed", "kind": "rss"}
	for _, tc := range []struct {
		token, origin string
		status        int
	}{{"", "", 401}, {"wrong", "", 401}, {"test-token", "https://unrelated.example", 403}, {"test-token", serv.URL, 201}} {
		response := request(t, serv.Client(), "POST", serv.URL+"/api/sources", tc.token, tc.origin, input)
		if response.StatusCode != tc.status {
			t.Fatalf("auth status %d, want %d", response.StatusCode, tc.status)
		}
	}
	sources, err := s.Sources(context.Background())
	if err != nil || len(sources) != 1 {
		t.Fatal("unauthorized requests changed source state")
	}
	response := request(t, serv.Client(), "GET", serv.URL+"/api/sources", "", "", nil)
	if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("public reading or security headers failed")
	}
	readonly, _ := server(t, "0.0.0.0:8080", "")
	response = request(t, readonly.Client(), "POST", readonly.URL+"/api/sources", "", "", input)
	if response.StatusCode != 403 {
		t.Fatal("non-local management was writable without a token")
	}
}

func TestImportDetailsShortlistExportsAndFeed(t *testing.T) {
	serv, _ := server(t, "127.0.0.1:8080", "")
	d := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	input := map[string]any{"url": "https://example.org/award?utm_source=share", "title": "=1+1 Scholarship & Opportunity", "organizer": "Example Foundation", "text": "Applications are open for a scholarship that helps students pursue their next chapter.\nEligibility\nStudents with an admission offer may apply.\nApplication\nApply now: https://apply.example.org/form\nApplication deadline: " + d}
	response := request(t, serv.Client(), "POST", serv.URL+"/api/import", "", "", input)
	if response.StatusCode != 201 {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("import %d: %s", response.StatusCode, raw)
	}
	var imported struct {
		Opportunity struct {
			ID        string `json:"id"`
			Capture   string `json:"capture_method"`
			Reactions *int64 `json:"reactions"`
		} `json:"opportunity"`
	}
	if err := json.NewDecoder(response.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}
	if imported.Opportunity.Capture != "manual_import" || imported.Opportunity.Reactions != nil {
		t.Fatal("manual import was presented as a live engagement capture")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/opportunities/"+imported.Opportunity.ID, "", "", nil)
	var detail map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil || detail["history"] == nil {
		t.Fatal("missing detail observation history")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/opportunities?ids="+imported.Opportunity.ID, "", "", nil)
	var list struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil || list.Total != 1 {
		t.Fatal("shortlist endpoint filtering failed")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/export?format=csv", "", "", nil)
	rows, err := csv.NewReader(response.Body).ReadAll()
	if err != nil || len(rows) != 2 || !strings.HasPrefix(rows[1][0], "'=1+1") || rows[1][14] != "" {
		t.Fatalf("unsafe or incorrect CSV: %#v, %v", rows, err)
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/feed.xml", "", "", nil)
	var feed struct {
		Channel struct {
			Items []struct {
				Title string `xml:"title"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(response.Body).Decode(&feed); err != nil || len(feed.Channel.Items) != 1 || feed.Channel.Items[0].Title != "=1+1 Scholarship & Opportunity" {
		t.Fatalf("invalid RSS: %+v, %v", feed, err)
	}
}

func TestValidationStopsUnsafeSourcesAndInvalidJSON(t *testing.T) {
	serv, s := server(t, "127.0.0.1:8080", "")
	response := request(t, serv.Client(), "POST", serv.URL+"/api/sources", "", "", map[string]any{"name": "Local", "url": "http://127.0.0.1/internal", "kind": "rss"})
	if response.StatusCode != 400 {
		t.Fatal("accepted a local network source")
	}
	req, err := http.NewRequest("POST", serv.URL+"/api/import", strings.NewReader(`{"url":"https://example.org"} {"url":"https://example.org"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err = serv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("accepted multiple JSON objects")
	}
	sources, err := s.Sources(context.Background())
	if err != nil || len(sources) != 0 {
		t.Fatal("invalid input created source records")
	}
}
