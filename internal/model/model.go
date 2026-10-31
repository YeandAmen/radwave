// Package model defines the records shared by ingestion, storage, and the API.
package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

var Kinds = []string{"program", "scholarship", "fellowship", "grant", "internship", "competition", "job", "bounty", "freelance", "research", "training", "conference", "volunteering"}
var Regions = []string{"Worldwide", "Africa", "Americas", "Asia", "Europe", "Oceania", "Not stated"}
var SourceKinds = []string{"linkedin_post", "linkedin_page", "linkedin_job", "linkedin_jobs", "rss", "rss_jobs", "web_page", "jobs_arbeitnow", "jobs_adzuna", "jobs_usajobs", "jobs_reed", "jobs_jooble", "kaggle_dataset", "x_search", "x_profile", "x_post", "x_archive"}

type JobDetails struct {
	Provider        string   `json:"provider,omitempty"`
	ProviderURL     string   `json:"provider_url,omitempty"`
	Salary          string   `json:"salary,omitempty"`
	EmploymentType  string   `json:"employment_type,omitempty"`
	DeadlineLabel   string   `json:"deadline_label,omitempty"`
	Remote          *bool    `json:"remote,omitempty"`
	Field           string   `json:"field,omitempty"`
	Level           string   `json:"level,omitempty"`
	Team            string   `json:"team,omitempty"`
	Skills          []string `json:"skills,omitempty"`
	YearsExperience *int     `json:"years_experience,omitempty"`
	FresherFriendly *bool    `json:"fresher_friendly,omitempty"`
	Openings        int      `json:"openings,omitempty"`
	DatasetAsOf     string   `json:"dataset_as_of,omitempty"`
	ClosedAt        string   `json:"closed_at,omitempty"`
	SourceATS       string   `json:"source_ats,omitempty"`
	RiskScore       *int     `json:"risk_score,omitempty"`
	RiskReasons     string   `json:"risk_reasons,omitempty"`
}

type Opportunity struct {
	ID                string            `json:"id"`
	URL               string            `json:"url"`
	Title             string            `json:"title"`
	Kind              string            `json:"kind"`
	Organizer         string            `json:"organizer"`
	PostAuthor        string            `json:"post_author,omitempty"`
	Job               *JobDetails       `json:"job,omitempty"`
	Summary           string            `json:"summary"`
	Body              string            `json:"body"`
	ApplicationURL    string            `json:"application_url"`
	Deadline          string            `json:"deadline"`
	DeadlineText      string            `json:"deadline_text"`
	DeadlineState     string            `json:"deadline_state"`
	Location          string            `json:"location"`
	Region            string            `json:"region"`
	Funding           string            `json:"funding"`
	Eligibility       []string          `json:"eligibility"`
	Benefits          []string          `json:"benefits"`
	Tags              []string          `json:"tags"`
	Evidence          map[string]string `json:"evidence"`
	Missing           []string          `json:"missing"`
	SourceID          string            `json:"source_id"`
	SourceName        string            `json:"source_name"`
	SourceKind        string            `json:"source_kind"`
	SourceURL         string            `json:"source_url"`
	CaptureMethod     string            `json:"capture_method"`
	ExtractionVersion string            `json:"extraction_version"`
	PublishedAt       *time.Time        `json:"published_at"`
	FirstSeenAt       time.Time         `json:"first_seen_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	LastFetchedAt     time.Time         `json:"last_fetched_at"`
	Reactions         *int64            `json:"reactions"`
	Comments          *int64            `json:"comments"`
	Reposts           *int64            `json:"reposts"`
	Quotes            *int64            `json:"quotes"`
	Status            string            `json:"status"`
	DaysLeft          *int              `json:"days_left"`
}

// Decorate computes date-dependent presentation fields; a missing deadline is
// never presented as confirmation that applications are open.
func (o *Opportunity) Decorate(now time.Time) {
	o.Status, o.DaysLeft = "unknown", nil
	if o.Job != nil && o.Job.ClosedAt != "" {
		o.Status = "closed"
		return
	}
	if o.DeadlineState == "rolling" {
		o.Status = "rolling"
		return
	}
	d, err := time.Parse("2006-01-02", o.Deadline)
	if err != nil {
		return
	}
	utc := now.UTC()
	today := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	days := int(d.Sub(today).Hours() / 24)
	o.DaysLeft = &days
	switch {
	case days < 0:
		o.Status = "closed"
	case days <= 7:
		o.Status = "closing"
	default:
		o.Status = "open"
	}
}

type Source struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	Kind            string     `json:"kind"`
	Enabled         bool       `json:"enabled"`
	Status          string     `json:"status"`
	LastError       string     `json:"last_error"`
	LastCrawledAt   *time.Time `json:"last_crawled_at"`
	NextCrawlAt     *time.Time `json:"next_crawl_at"`
	CreatedAt       time.Time  `json:"created_at"`
	Failures        int        `json:"failures"`
	ItemCount       int        `json:"item_count"`
	IntervalMinutes int        `json:"interval_minutes,omitempty"`
	ETag            string     `json:"-"`
	LastModified    string     `json:"-"`
	SinceID         string     `json:"-"`
	NextToken       string     `json:"-"`
	PendingSinceID  string     `json:"-"`
	XUserID         string     `json:"-"`
}

type Observation struct {
	ID             int64     `json:"id"`
	OpportunityID  string    `json:"opportunity_id"`
	ObservedAt     time.Time `json:"observed_at"`
	Changes        []string  `json:"changes"`
	Reactions      *int64    `json:"reactions"`
	Comments       *int64    `json:"comments"`
	ReactionsDelta *int64    `json:"reactions_delta"`
	CommentsDelta  *int64    `json:"comments_delta"`
	Reposts        *int64    `json:"reposts"`
	Quotes         *int64    `json:"quotes"`
	RepostsDelta   *int64    `json:"reposts_delta"`
	QuotesDelta    *int64    `json:"quotes_delta"`
	CaptureMethod  string    `json:"capture_method"`
}

type Run struct {
	ID                string     `json:"id"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	Status            string     `json:"status"`
	SourcesChecked    int        `json:"sources_checked"`
	OpportunitiesSeen int        `json:"opportunities_seen"`
	Created           int        `json:"created"`
	Updated           int        `json:"updated"`
	Errors            []string   `json:"errors"`
}

type Stats struct {
	Total        int            `json:"total"`
	Available    int            `json:"available"`
	ClosingSoon  int            `json:"closing_soon"`
	UpdatedToday int            `json:"updated_today"`
	Sources      int            `json:"sources"`
	Kinds        map[string]int `json:"kinds"`
	Regions      map[string]int `json:"regions"`
	Platforms    map[string]int `json:"platforms"`
}

type Query struct {
	IDs         []string
	Search      string
	Kind        string
	Region      string
	Status      string
	Source      string
	Platform    string
	Sort        string
	Limit       int
	Offset      int
	JobField    string
	JobLevel    string
	RemoteOnly  bool
	FresherOnly bool
	Skill       string
	MaxYears    *int
	MaxRisk     *int
}

type Link struct {
	URL     string
	Text    string
	Context string
}

type Post struct {
	Identity       string
	URL            string
	Title          string
	Body           string
	Author         string
	Categories     []string
	Links          []Link
	PublishedAt    *time.Time
	Reactions      *int64
	Comments       *int64
	Reposts        *int64
	Quotes         *int64
	CaptureMethod  string
	Job            *JobDetails
	Location       string
	Deadline       string
	ApplicationURL string
}

func Platform(sourceKind string) string {
	if sourceKind == "kaggle_dataset" {
		return "kaggle"
	}
	if strings.HasPrefix(sourceKind, "jobs_") {
		return "jobs"
	}
	if strings.HasPrefix(sourceKind, "x_") {
		return "x"
	}
	if strings.HasPrefix(sourceKind, "linkedin_") {
		return "linkedin"
	}
	if sourceKind == "rss" || sourceKind == "rss_jobs" {
		return "rss"
	}
	return "web"
}

func StableID(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:12])
}

func NewID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func Contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func Int64(v int64) *int64 { return &v }

func CleanSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
