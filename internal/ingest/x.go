package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
)

var ErrXNotConfigured = errors.New("X API access is not configured; set X_BEARER_TOKEN in the server environment")

// These watchlists extend the opportunity categories from twitter-scrapper,
// without its fixed year, separate database, or process-blocking rate-limit sleep.
var XWatchlists = map[string]string{
	"funding":  `(grant OR scholarship OR fellowship OR "funding opportunity") (apply OR application OR deadline) -is:retweet lang:en`,
	"programs": `(incubation OR accelerator OR hackathon OR bootcamp) (apply OR applications OR register) -is:retweet lang:en`,
	"careers":  `(internship OR "we're hiring" OR "job opening" OR "research position" OR freelance) -is:retweet lang:en`,
	"bounties": `("bug bounty" OR "open source bounty" OR "paid contribution") -is:retweet lang:en`,
}

const xFields = "id,text,author_id,created_at,entities,public_metrics,note_tweet,lang"

type XAPIError struct {
	Status  int
	Detail  string
	RetryAt *time.Time
}

func (e *XAPIError) Error() string {
	switch e.Status {
	case 401:
		return "X API rejected the bearer token; update X_BEARER_TOKEN"
	case 402:
		return "X API credits are required (HTTP 402); enable API access in the X developer account"
	case 403:
		return "X API access is unavailable for this token or endpoint (HTTP 403)"
	case 429:
		return "X API rate limit reached (HTTP 429); retry after the reported reset"
	default:
		if e.Detail != "" {
			return fmt.Sprintf("X API returned HTTP %d: %s", e.Status, e.Detail)
		}
		return fmt.Sprintf("X API returned HTTP %d", e.Status)
	}
}

type XPage struct {
	Posts     []model.Post
	NextToken string
	NewestID  string
	UserID    string
	Warnings  []string
}

type XReader interface {
	Configured() bool
	Page(context.Context, model.Source, int) (XPage, error)
	Post(context.Context, string) (model.Post, error)
	Posts(context.Context, []string) ([]model.Post, error)
}

type XClient struct {
	token   string
	client  *http.Client
	mu      sync.Mutex
	next    time.Time
	spacing time.Duration
}

func NewXClient(token string) *XClient {
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Hostname() != "api.x.com" {
			return errors.New("X API redirected to an unsupported destination")
		}
		return nil
	}}
	return &XClient{token: strings.TrimSpace(token), client: client, spacing: 2 * time.Second}
}

func (c *XClient) Configured() bool {
	return c != nil && c.token != "" && c.token != "[SENSITIVE]" && !strings.HasPrefix(c.token, "your_")
}

func (c *XClient) get(ctx context.Context, path string, params url.Values, target any) error {
	if !c.Configured() {
		return ErrXNotConfigured
	}
	c.mu.Lock()
	start := time.Now()
	if c.next.After(start) {
		start = c.next
	}
	c.next = start.Add(c.spacing)
	c.mu.Unlock()
	if delay := time.Until(start); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.x.com"+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "Radwave/0.2 (+https://github.com/YeandAmen/radwave)")
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("X API request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (5<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 5<<20 {
		return errors.New("X API response exceeds the 5 MB limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var api struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		json.Unmarshal(data, &api)
		detail := api.Detail
		if detail == "" {
			detail = api.Title
		}
		detail = strings.ReplaceAll(detail, c.token, "[redacted]")
		failure := &XAPIError{Status: response.StatusCode, Detail: truncate(model.CleanSpace(detail), 200)}
		if reset, err := strconv.ParseInt(response.Header.Get("x-rate-limit-reset"), 10, 64); err == nil {
			t := time.Unix(reset, 0).UTC()
			failure.RetryAt = &t
		} else if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil {
			t := time.Now().Add(time.Duration(seconds) * time.Second).UTC()
			failure.RetryAt = &t
		}
		return failure
	}
	if err := json.Unmarshal(data, target); err != nil {
		return errors.New("X API returned an unreadable JSON response")
	}
	return nil
}

type xURL struct {
	URL      string `json:"url"`
	Expanded string `json:"expanded_url"`
	Unwound  string `json:"unwound_url"`
}
type xEntities struct {
	URLs []xURL `json:"urls"`
}
type xTweet struct {
	ID        string           `json:"id"`
	Text      string           `json:"text"`
	AuthorID  string           `json:"author_id"`
	CreatedAt string           `json:"created_at"`
	Metrics   map[string]int64 `json:"public_metrics"`
	Entities  xEntities        `json:"entities"`
	Note      struct {
		Text     string    `json:"text"`
		Entities xEntities `json:"entities"`
	} `json:"note_tweet"`
}
type xUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}
type xResponse struct {
	Data     []xTweet `json:"data"`
	Includes struct {
		Users []xUser `json:"users"`
	} `json:"includes"`
	Meta struct {
		Next   string `json:"next_token"`
		Newest string `json:"newest_id"`
	} `json:"meta"`
	Errors []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	} `json:"errors"`
}

func xParams() url.Values {
	return url.Values{"tweet.fields": {xFields}, "expansions": {"author_id"}, "user.fields": {"id,username,name"}}
}

func (c *XClient) Page(ctx context.Context, source model.Source, limit int) (XPage, error) {
	params := xParams()
	params.Set("max_results", strconv.Itoa(max(10, min(100, limit))))
	if source.SinceID != "" {
		params.Set("since_id", source.SinceID)
	}
	if source.NextToken != "" {
		params.Set("next_token", source.NextToken)
	}
	path := "/2/tweets/search/recent"
	userID := source.XUserID
	if source.Kind == "x_profile" {
		if userID == "" {
			name := XProfile(source.URL)
			if name == "" {
				return XPage{}, errors.New("invalid X profile URL")
			}
			var user struct {
				Data xUser `json:"data"`
			}
			if err := c.get(ctx, "/2/users/by/username/"+url.PathEscape(name), url.Values{}, &user); err != nil {
				return XPage{}, err
			}
			userID = user.Data.ID
			if userID == "" {
				return XPage{}, errors.New("X profile could not be resolved")
			}
		}
		path = "/2/users/" + url.PathEscape(userID) + "/tweets"
		params.Set("exclude", "retweets")
		if source.NextToken != "" {
			params.Del("next_token")
			params.Set("pagination_token", source.NextToken)
		}
	} else {
		u, _ := url.Parse(source.URL)
		query := u.Query().Get("q")
		if strings.TrimSpace(query) == "" || len(query) > 512 {
			return XPage{}, errors.New("X search needs a query of 1–512 characters")
		}
		params.Set("query", query)
	}
	var response xResponse
	if err := c.get(ctx, path, params, &response); err != nil {
		return XPage{}, err
	}
	page := XPage{Posts: xPosts(response.Data, response.Includes.Users), NextToken: response.Meta.Next, NewestID: response.Meta.Newest, UserID: userID, Warnings: []string{}}
	for _, issue := range response.Errors {
		page.Warnings = append(page.Warnings, truncate(model.CleanSpace(issue.Title+" "+issue.Detail), 200))
	}
	if len(page.Posts) == 0 && len(page.Warnings) > 0 {
		return page, errors.New("X API could not return readable posts: " + strings.Join(page.Warnings, "; "))
	}
	for _, post := range page.Posts {
		page.NewestID = NewerXID(page.NewestID, XPostID(post.URL))
	}
	return page, nil
}

func (c *XClient) Post(ctx context.Context, id string) (model.Post, error) {
	if !validXID(id) {
		return model.Post{}, errors.New("invalid X post identity")
	}
	var response struct {
		Data     xTweet `json:"data"`
		Includes struct {
			Users []xUser `json:"users"`
		} `json:"includes"`
	}
	if err := c.get(ctx, "/2/tweets/"+id, xParams(), &response); err != nil {
		return model.Post{}, err
	}
	posts := xPosts([]xTweet{response.Data}, response.Includes.Users)
	if len(posts) == 0 {
		return model.Post{}, errors.New("X did not return readable post content")
	}
	return posts[0], nil
}

func (c *XClient) Posts(ctx context.Context, ids []string) ([]model.Post, error) {
	valid := []string{}
	for _, id := range ids {
		if validXID(id) && !model.Contains(valid, id) {
			valid = append(valid, id)
		}
		if len(valid) == 100 {
			break
		}
	}
	if len(valid) == 0 {
		return []model.Post{}, nil
	}
	params := xParams()
	params.Set("ids", strings.Join(valid, ","))
	var response xResponse
	if err := c.get(ctx, "/2/tweets", params, &response); err != nil {
		return nil, err
	}
	return xPosts(response.Data, response.Includes.Users), nil
}

func xPosts(tweets []xTweet, users []xUser) []model.Post {
	authors := map[string]string{}
	for _, user := range users {
		if user.Username != "" {
			authors[user.ID] = "@" + user.Username
		}
	}
	posts := []model.Post{}
	for _, tweet := range tweets {
		if !validXID(tweet.ID) {
			continue
		}
		body, entities := tweet.Text, tweet.Entities
		if tweet.Note.Text != "" {
			body, entities = tweet.Note.Text, tweet.Note.Entities
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		links := []model.Link{}
		for _, link := range entities.URLs {
			target := link.Unwound
			if target == "" {
				target = link.Expanded
			}
			if target == "" {
				target = link.URL
			}
			contextText := body
			if index := strings.Index(body, link.URL); index >= 0 {
				contextText = body[max(0, index-100):min(len(body), index+len(link.URL)+30)]
			}
			links = append(links, model.Link{URL: target, Text: link.URL, Context: contextText})
			if link.URL != "" && target != "" {
				body = strings.ReplaceAll(body, link.URL, target)
			}
		}
		author := authors[tweet.AuthorID]
		if author == "" && tweet.AuthorID != "" {
			author = "X user " + tweet.AuthorID
		}
		post := model.Post{URL: "https://x.com/i/status/" + tweet.ID, Body: body, Author: author, Links: links, PublishedAt: parseTime(tweet.CreatedAt), CaptureMethod: "x_api", Categories: []string{}}
		if count, ok := tweet.Metrics["like_count"]; ok {
			post.Reactions = model.Int64(count)
		}
		if count, ok := tweet.Metrics["reply_count"]; ok {
			post.Comments = model.Int64(count)
		}
		if count, ok := tweet.Metrics["retweet_count"]; ok {
			post.Reposts = model.Int64(count)
		}
		if count, ok := tweet.Metrics["quote_count"]; ok {
			post.Quotes = model.Int64(count)
		}
		posts = append(posts, post)
	}
	return posts
}

func validXID(id string) bool {
	if id == "" || len(id) > 22 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func NewerXID(a, b string) string {
	if len(b) > len(a) || len(b) == len(a) && b > a {
		return b
	}
	return a
}
