package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
)

type jobTransport func(*http.Request) (*http.Response, error)

func (f jobTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestJobProvidersAuthenticateAndNormalizeListings(t *testing.T) {
	cases := []struct{ kind, raw, body, title, employer, capture string }{
		{"jobs_arbeitnow", "https://www.arbeitnow.com/api/job-board-api", `{"data":[{"slug":"senior-research-engineer","url":"https://www.arbeitnow.com/jobs/engineer","title":"Senior Research Engineer","company_name":"Example Labs","description":"<p>Build reliable systems.</p>","location":"Berlin","remote":true,"job_types":["Full-time"],"created_at":1793448000}]}`, "Senior Research Engineer", "Example Labs", "job_api"},
		{"jobs_adzuna", "https://api.adzuna.com/v1/api/jobs/gb/search/1?what=engineer", `{"results":[{"id":"123","redirect_url":"https://www.adzuna.co.uk/jobs/land/ad/123","title":"Software Engineer","description":"Job summary","company":{"display_name":"Example Ltd"},"location":{"display_name":"London"},"salary_min":50000,"salary_max":60000,"salary_is_predicted":1,"contract_time":"full_time","created":"2026-10-31T10:00:00Z"}]}`, "Software Engineer", "Example Ltd", "job_api_summary"},
		{"jobs_usajobs", "https://data.usajobs.gov/api/search?Keyword=engineer", `{"SearchResult":{"SearchResultItems":[{"MatchedObjectId":"123","MatchedObjectDescriptor":{"PositionTitle":"Systems Engineer","PositionURI":"https://www.usajobs.gov/GetJob/View/123","OrganizationName":"Example Agency","PositionLocationDisplay":"Washington DC","PublicationStartDate":"2026-10-31T10:00:00Z","ApplicationCloseDate":"2026-12-17T23:59:00Z","ApplyURI":["https://www.usajobs.gov/GetJob/View/123?PostingChannelID=RESTAPI"],"QualificationSummary":"Relevant engineering experience.","PositionRemuneration":[{"MinimumRange":"90000","MaximumRange":"120000","Description":"Per Year"}],"PositionSchedule":[{"Name":"Full Time"}],"UserArea":{"Details":{"JobSummary":"Design safe systems.","WhoMayApply":{"Name":"United States Citizens"},"RemoteIndicator":false}}}}]}}`, "Systems Engineer", "Example Agency", "job_api"},
		{"jobs_reed", "https://www.reed.co.uk/api/1.0/search?keywords=engineer", `[{"jobId":123,"jobUrl":"https://www.reed.co.uk/jobs/engineer/123","jobTitle":"Platform Engineer","employerName":"Example Recruiter","jobDescription":"Job summary","locationName":"Leeds","minimumSalary":null,"maximumSalary":null,"date":"31/10/2026"}]`, "Platform Engineer", "Example Recruiter", "job_api_summary"},
		{"jobs_jooble", "https://jooble.org/api?keywords=engineer&location=Accra", `{"jobs":[{"id":123,"link":"https://jobs.example.org/123","title":"Backend Engineer","company":"Example Ghana","snippet":"Job summary","location":"Accra","salary":"GHS 9000 per month","type":"Full-time","updated":"2026-10-31T10:00:00Z"}]}`, "Backend Engineer", "Example Ghana", "job_api_summary"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			credentials := JobCredentials{AdzunaAppID: "fixture-id", AdzunaAppKey: "fixture-secret", USAJobsAPIKey: "fixture-secret", USAJobsEmail: "api@example.org", ReedAPIKey: "fixture-secret", JoobleAPIKey: "fixture-secret"}
			c := NewJobClient(credentials)
			c.spacing = 0
			c.client = &http.Client{Transport: jobTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" {
					t.Fatal("job API request was not HTTPS")
				}
				switch tc.kind {
				case "jobs_adzuna":
					if r.URL.Query().Get("app_key") != "fixture-secret" || r.URL.Query().Get("results_per_page") != "20" || r.URL.Query().Get("sort_by") != "date" {
						t.Fatal("Adzuna credentials or newest-first query missing")
					}
				case "jobs_usajobs":
					if r.Header.Get("Authorization-Key") != "fixture-secret" || r.Header.Get("User-Agent") != "api@example.org" || r.URL.Query().Get("Fields") != "Full" {
						t.Fatal("USAJobs request contract was not respected")
					}
				case "jobs_reed":
					key, password, ok := r.BasicAuth()
					if !ok || key != "fixture-secret" || password != "" {
						t.Fatal("Reed Basic authentication was incorrect")
					}
				case "jobs_jooble":
					var payload map[string]any
					json.NewDecoder(r.Body).Decode(&payload)
					if r.Method != "POST" || r.URL.Path != "/api/fixture-secret" || payload["keywords"] != "engineer" || payload["location"] != "Accra" || payload["ResultOnPage"] != float64(20) {
						t.Fatal("Jooble POST contract was incorrect")
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			posts, err := c.Read(context.Background(), model.Source{Kind: tc.kind, URL: tc.raw}, 20)
			if err != nil || len(posts) != 1 {
				t.Fatalf("job API read: %v, %v", posts, err)
			}
			o := Extract(posts[0], model.Source{Kind: tc.kind, URL: tc.raw, Name: "Job source"}, time.Now())
			if o.Kind != "job" || o.Title != tc.title || o.Organizer != tc.employer || o.CaptureMethod != tc.capture || o.Job == nil || o.Reactions != nil || o.Location == "" {
				t.Fatalf("job normalization lost source facts: %+v", o)
			}
			if strings.Contains(o.SourceURL, "fixture-secret") {
				t.Fatal("job credential escaped into a stored source URL")
			}
			if tc.kind == "jobs_adzuna" && !strings.HasPrefix(o.Job.Salary, "Estimated:") {
				t.Fatal("estimated salary was presented as confirmed")
			}
			if tc.kind == "jobs_usajobs" && (o.Deadline != "2026-12-17" || !strings.Contains(strings.Join(o.Eligibility, " "), "United States Citizens")) {
				t.Fatal("federal job deadline or eligibility was lost")
			}
			if tc.kind == "jobs_reed" && (o.Job.Salary != "" || o.PublishedAt == nil) {
				t.Fatal("Reed unknown salary or defined date format was mishandled")
			}
			if tc.kind == "jobs_jooble" && o.PublishedAt != nil {
				t.Fatal("Jooble updated time was invented as a publication date")
			}
		})
	}
}

func TestJobAccessErrorsAreBoundedAndDoNotExposeCredentials(t *testing.T) {
	source := model.Source{Kind: "jobs_adzuna", URL: "https://api.adzuna.com/v1/api/jobs/gb/search/1"}
	c := NewJobClient(JobCredentials{})
	_, err := c.Read(context.Background(), source, 20)
	if !AccessBlocked(err) || !strings.Contains(err.Error(), "ADZUNA_APP_KEY") {
		t.Fatal("missing API setup was not surfaced")
	}
	c = NewJobClient(JobCredentials{AdzunaAppID: "fixture-id", AdzunaAppKey: "fixture-secret"})
	c.spacing = 0
	c.client = &http.Client{Transport: jobTransport(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request URL containing fixture-secret failed")
	})}
	_, err = c.Read(context.Background(), source, 20)
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("transport error exposed a query-string credential")
	}
	c.client = &http.Client{Transport: jobTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"3600"}}, Body: io.NopCloser(strings.NewReader("fixture-secret"))}, nil
	})}
	_, err = c.Read(context.Background(), source, 20)
	var failure *JobAccessError
	if !errors.As(err, &failure) || failure.RetryAt == nil || !AccessBlocked(err) {
		t.Fatal("rate-limit reset was lost")
	}
	c.next["api.adzuna.com"] = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err = c.Read(ctx, source, 20)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatal("job API spacing did not respect cancellation")
	}
}

func TestJobSourceURLsRejectCredentialsAndEndpointEscapes(t *testing.T) {
	for _, source := range []model.Source{
		{Kind: "jobs_adzuna", URL: "https://api.adzuna.com/v1/api/jobs/gb/search/1?app_key=private"},
		{Kind: "jobs_jooble", URL: "https://jooble.org/api/private-key"},
		{Kind: "jobs_reed", URL: "https://evil.example.org/api/1.0/search"},
		{Kind: "jobs_usajobs", URL: "http://data.usajobs.gov/api/search"},
	} {
		if ValidateJobSource(source) == nil {
			t.Fatalf("accepted unsafe or credential-bearing job source: %+v", source)
		}
	}
	if SourceKind("https://api.adzuna.com/v1/api/jobs/gb/search/1?app_key=private") != "jobs_adzuna" {
		t.Fatal("auto-detection let a credential-bearing API URL fall back to an ordinary article")
	}
}

func TestLinkedInJobPostingPreservesStructuredFactsAndAliases(t *testing.T) {
	page := `<script type="application/ld+json">{"@type":"JobPosting","title":"Research Engineer","description":"<p>Build systems for international research projects.</p><h2>Qualifications</h2><p>Experience with Go is required.</p>","hiringOrganization":{"name":"Example Research"},"datePosted":"2026-10-31T10:00:00Z","validThrough":"2026-12-17T23:59:00Z","employmentType":"FULL_TIME","jobLocationType":"TELECOMMUTE","jobLocation":{"address":{"addressLocality":"Accra","addressCountry":"GH"}},"baseSalary":{"currency":"GHS","value":{"minValue":8000,"maxValue":12000,"unitText":"MONTH"}}}</script>`
	post, err := ParsePage([]byte(page), "https://gh.linkedin.com/jobs/view/research-engineer-at-example-4484260323?trackingId=share")
	if err != nil {
		t.Fatal(err)
	}
	o := Extract(post, model.Source{Kind: "linkedin_job"}, time.Now())
	if o.ID != "linkedin-job-4484260323" || o.URL != "https://www.linkedin.com/jobs/view/4484260323" || o.Kind != "job" || o.Organizer != "Example Research" || o.Job == nil || o.Job.Salary != "GHS 8000–12000 per month" || o.Job.Remote == nil || !*o.Job.Remote {
		t.Fatalf("lost structured job facts: %+v", o)
	}
	if o.Deadline != "2026-12-17" || o.Job.DeadlineLabel != "Listing valid through" || strings.Contains(o.DeadlineText, "Application closing date") {
		t.Fatal("job-listing validity was misrepresented as an application deadline")
	}
	if SourceKind(o.URL) != "linkedin_job" || SourceKind("https://www.linkedin.com/jobs/search?keywords=engineer") != "linkedin_jobs" {
		t.Fatal("LinkedIn job URLs were routed as ordinary posts")
	}
	search := CanonicalURL("https://gh.linkedin.com/jobs/search/?keywords=engineer&location=Ghana&sortBy=DD&trk=share")
	if !strings.Contains(search, "keywords=engineer") || !strings.Contains(search, "location=Ghana") || strings.Contains(search, "trk=") {
		t.Fatal("job search lost meaningful criteria")
	}
	links, err := DiscoverLinkedInJobs([]byte(`<a href="/jobs/view/research-engineer-4484260323">Role</a><a href="/jobs/view/4484260323?trackingId=x">Same role</a><a href="https://evil.example/jobs/view/4484260324">External</a>`), "https://www.linkedin.com/jobs/search", 20)
	if err != nil || len(links) != 1 {
		t.Fatal("job discovery did not deduplicate public listing identities")
	}
	if _, err := ParsePage([]byte(`<h1>Sign in</h1>`), o.URL); !errors.Is(err, ErrNoPublicJob) {
		t.Fatal("a login wall was accepted as job content")
	}
}
