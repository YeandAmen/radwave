// Package legacy imports the original Twitter scraper's public opportunity
// records into Radwave. Account, API-key, and email-delivery tables are not read.
package legacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

type ImportResult struct {
	Read    int `json:"read"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

func ImportTwitter(ctx context.Context, path string, destination *store.Store) (ImportResult, error) {
	result := ImportResult{}
	abs, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT tweet_id,type,title,description,url,author,tags,scraped_at FROM opportunities ORDER BY id LIMIT 10000")
	if err != nil {
		return result, errors.New("the legacy database does not contain readable Twitter opportunity records")
	}
	defer rows.Close()
	var source model.Source
	for rows.Next() {
		var id, kind, title, body, original, author, tags string
		var recorded any
		if err := rows.Scan(&id, &kind, &title, &body, &original, &author, &tags, &recorded); err != nil {
			return result, err
		}
		result.Read++
		canonical := "https://x.com/i/status/" + id
		if ingest.XPostID(canonical) == "" || strings.TrimSpace(body) == "" {
			result.Skipped++
			continue
		}
		if source.ID == "" {
			var err error
			source, _, err = destination.AddSource(ctx, model.Source{Name: "X · Imported Twitter archive", URL: "https://x.com", Kind: "x_archive", Enabled: false, Status: "archive"})
			if err != nil {
				return result, err
			}
		}
		if strings.HasPrefix(title, "[") {
			if index := strings.Index(title, "]"); index >= 0 {
				title = strings.TrimSpace(title[index+1:])
			}
		}
		post := model.Post{URL: canonical, Title: title, Body: body, Author: author, CaptureMethod: "x_archive"}
		capturedAt := time.Time{}
		if text, ok := recorded.(string); ok {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05"} {
				if t, err := time.Parse(layout, text); err == nil {
					capturedAt = t.UTC()
					break
				}
			}
		} else if t, ok := recorded.(time.Time); ok {
			capturedAt = t.UTC()
		}
		opportunity := ingest.Extract(post, source, time.Now())
		if !capturedAt.IsZero() {
			opportunity.FirstSeenAt, opportunity.UpdatedAt, opportunity.LastFetchedAt = capturedAt, capturedAt, capturedAt
			opportunity.Evidence["legacy_captured_at"] = capturedAt.Format(time.RFC3339Nano)
		}
		mapped := map[string]string{"job": "job", "internship": "internship", "grant": "grant", "scholarship": "scholarship", "freelance": "freelance", "research": "research", "fellowship": "fellowship", "hackathon": "competition", "bounty": "bounty"}[kind]
		if mapped != "" {
			opportunity.Kind = mapped
			opportunity.Evidence["legacy_classification"] = "Imported from the original Twitter scraper: " + kind
		}
		var legacyTags []string
		if json.Unmarshal([]byte(tags), &legacyTags) == nil {
			for _, tag := range legacyTags {
				if len(opportunity.Tags) >= 15 {
					break
				}
				if tag != "" && !model.Contains(opportunity.Tags, tag) {
					opportunity.Tags = append(opportunity.Tags, tag)
				}
			}
		}
		opportunity.Evidence["capture_method"] = "Imported from the original Twitter scraper. Engagement counts were not recorded."
		upsert, err := destination.Upsert(ctx, opportunity)
		if err != nil {
			return result, err
		}
		if upsert.Created {
			result.Created++
		}
		if upsert.Updated {
			result.Updated++
		}
	}
	return result, rows.Err()
}
