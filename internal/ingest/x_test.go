package ingest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
)

type xTransport func(*http.Request) (*http.Response, error)

func (f xTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testXClient(body string, status int, check func(*http.Request)) *XClient {
	c := NewXClient("test-x-private-token")
	c.spacing = 0
	c.client = &http.Client{Transport: xTransport(func(r *http.Request) (*http.Response, error) {
		if check != nil {
			check(r)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	return c
}

func TestXSearchMapsLongTextExpandedLinksAndMetrics(t *testing.T) {
	body := `{"data":[{"id":"1234567890123456789","text":"Truncated announcement","author_id":"42","created_at":"2026-10-31T10:00:00Z","note_tweet":{"text":"CALL FOR APPLICATIONS: Global Builders Fellowship\nApplications are open for builders from all countries.\nEligibility\nApplicants from all countries may apply.\nApply now: https://t.co/form\nApplication deadline: 17 December 2026","entities":{"urls":[{"url":"https://t.co/form","expanded_url":"https://apply.example.org/builders"}]}},"public_metrics":{"like_count":0,"reply_count":4,"retweet_count":3,"quote_count":2}}],"includes":{"users":[{"id":"42","username":"builders","name":"Global Builders"}]},"meta":{"newest_id":"1234567890123456789","next_token":"next-page"}}`
	c := testXClient(body, 200, func(r *http.Request) {
		if r.URL.Hostname() != "api.x.com" || r.URL.Path != "/2/tweets/search/recent" {
			t.Fatal("request escaped the fixed X API endpoint")
		}
		if r.Header.Get("Authorization") != "Bearer test-x-private-token" {
			t.Fatal("missing X authentication")
		}
		if r.URL.Query().Get("since_id") != "100" || r.URL.Query().Get("next_token") != "old-page" {
			t.Fatal("checkpoint was not sent")
		}
	})
	source := model.Source{ID: "watchlist", Name: "X Funding", Kind: "x_search", URL: "https://x.com/search?q=fellowship", SinceID: "100", NextToken: "old-page"}
	page, err := c.Page(context.Background(), source, 20)
	if err != nil || len(page.Posts) != 1 || page.NextToken != "next-page" {
		t.Fatalf("search mapping: %+v, %v", page, err)
	}
	o := Extract(page.Posts[0], source, time.Now())
	if o.ID != "x-1234567890123456789" || o.Kind != "fellowship" || o.ApplicationURL != "https://apply.example.org/builders" || o.Deadline != "2026-12-17" {
		t.Fatalf("X announcement lost important fields: %+v", o)
	}
	if o.Reactions == nil || *o.Reactions != 0 || o.Comments == nil || *o.Comments != 4 || o.Reposts == nil || *o.Reposts != 3 || o.Quotes == nil || *o.Quotes != 2 {
		t.Fatal("post metrics were lost or zero became unknown")
	}
	if o.Organizer != "" || o.PostAuthor != "@builders" {
		t.Fatal("publisher was incorrectly asserted as program organizer")
	}
}

func TestXBillingErrorsAndCancellationAreBounded(t *testing.T) {
	c := testXClient(`{"title":"Payment Required","detail":"test-x-private-token has no credits"}`, 402, nil)
	_, err := c.Post(context.Background(), "1234567890123456789")
	var failure *XAPIError
	if !errors.As(err, &failure) || failure.Status != 402 || !AccessBlocked(err) || strings.Contains(err.Error(), "test-x-private-token") {
		t.Fatalf("unsafe or hidden billing error: %v", err)
	}
	c.next = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err = c.Post(ctx, "1234567890123456789")
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatal("X rate spacing blocked a cancelled request")
	}
	if _, err := NewXClient("").Post(context.Background(), "1234567890123456789"); !errors.Is(err, ErrXNotConfigured) {
		t.Fatal("missing credential was not reported")
	}
}

func TestXURLAliasesAndUnknownMetrics(t *testing.T) {
	for _, raw := range []string{"https://twitter.com/builder/status/1234567890123456789?utm_source=share", "https://x.com/i/web/status/1234567890123456789", "https://x.com/i/status/1234567890123456789"} {
		if CanonicalURL(raw) != "https://x.com/i/status/1234567890123456789" || SourceKind(raw) != "x_post" {
			t.Errorf("post alias was not deduplicated: %s", raw)
		}
	}
	if SourceKind("https://twitter.com/builders") != "x_profile" || XProfile("https://x.com/login") != "" || SourceKind("https://x.com/search?q=grant") != "x_search" {
		t.Fatal("X source routing is incorrect")
	}
	posts := xPosts([]xTweet{{ID: "999", Text: "Applications are open for a grant."}}, nil)
	if len(posts) != 1 || posts[0].Reactions != nil || posts[0].Comments != nil {
		t.Fatal("unavailable X metrics became zero")
	}
}
