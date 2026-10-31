package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
)

// Job APIs return the same Post records as public pages and RSS. Credentials
// stay in the server configuration, never in stored source URLs.
type JobReader interface {
	Configured(string) bool
	Read(context.Context, model.Source, int) ([]model.Post, error)
}

type JobCredentials struct {
	AdzunaAppID, AdzunaAppKey   string
	USAJobsAPIKey, USAJobsEmail string
	ReedAPIKey, JoobleAPIKey    string
}

type JobProvider struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	URL             string   `json:"url"`
	DocsURL         string   `json:"docs_url"`
	Credentials     []string `json:"credentials"`
	IntervalMinutes int      `json:"interval_minutes"`
	Note            string   `json:"note"`
}

var JobProviders = []JobProvider{
	{Kind: "jobs_arbeitnow", Name: "Arbeitnow", URL: "https://www.arbeitnow.com/api/job-board-api", DocsURL: "https://www.arbeitnow.com/blog/job-board-api", Credentials: []string{}, IntervalMinutes: 15, Note: "Keyless employer/ATS listings. Europe and UK endpoints are supported."},
	{Kind: "jobs_usajobs", Name: "USAJobs", URL: "https://data.usajobs.gov/api/search", DocsURL: "https://developer.usajobs.gov/general/quick-start", Credentials: []string{"USAJOBS_API_KEY", "USAJOBS_EMAIL"}, IntervalMinutes: 10, Note: "Federal roles; confirm each announcement's hiring eligibility."},
	{Kind: "jobs_reed", Name: "Reed", URL: "https://www.reed.co.uk/api/1.0/search", DocsURL: "https://www.reed.co.uk/developers/jobseeker", Credentials: []string{"REED_API_KEY"}, IntervalMinutes: 15, Note: "Jobseeker search API; returned search descriptions may be summaries."},
	{Kind: "jobs_jooble", Name: "Jooble", URL: "https://jooble.org/api?keywords=software", DocsURL: "https://jooble.org/api/about", Credentials: []string{"JOOBLE_API_KEY"}, IntervalMinutes: 15, Note: "Aggregated job search. Requires a server-side Jooble API key."},
	{Kind: "jobs_adzuna", Name: "Adzuna", URL: "https://api.adzuna.com/v1/api/jobs/gb/search/1", DocsURL: "https://developer.adzuna.com/docs/terms_of_service", Credentials: []string{"ADZUNA_APP_ID", "ADZUNA_APP_KEY"}, IntervalMinutes: 30, Note: "Free evaluation access; ongoing revenue-generating use requires a licence after the trial. Descriptions are snippets."},
}

func JobProviderFor(kind string) (JobProvider, bool) {
	for _, provider := range JobProviders {
		if provider.Kind == kind {
			return provider, true
		}
	}
	return JobProvider{}, false
}

type JobAccessError struct {
	Provider string
	Status   int
	Missing  []string
	RetryAt  *time.Time
}

func (e *JobAccessError) Error() string {
	if len(e.Missing) > 0 {
		return e.Provider + " API is not configured; set " + strings.Join(e.Missing, " and ") + " in the server environment"
	}
	if e.Status == 429 {
		return e.Provider + " API rate limit reached (HTTP 429); source refresh will back off"
	}
	return fmt.Sprintf("%s API returned HTTP %d; check API access and source settings", e.Provider, e.Status)
}

type JobClient struct {
	credentials JobCredentials
	client      *http.Client
	mu          sync.Mutex
	next        map[string]time.Time
	spacing     time.Duration
}

func NewJobClient(credentials JobCredentials) *JobClient {
	client := NewHTTPFetcher(20*time.Second, 2*time.Second).client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || len(via) == 0 || req.URL.Host != via[0].URL.Host {
			return errors.New("job API redirected outside its configured endpoint")
		}
		return nil
	}
	return &JobClient{credentials: credentials, client: client, next: map[string]time.Time{}, spacing: 2 * time.Second}
}

func usableJobCredential(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "[SENSITIVE]" && !strings.HasPrefix(value, "your-") && !strings.HasPrefix(value, "your_")
}

func (c *JobClient) missing(kind string) []string {
	provider, ok := JobProviderFor(kind)
	if !ok {
		return []string{"supported job provider"}
	}
	values := map[string]string{}
	if c != nil {
		values = map[string]string{"ADZUNA_APP_ID": c.credentials.AdzunaAppID, "ADZUNA_APP_KEY": c.credentials.AdzunaAppKey, "USAJOBS_API_KEY": c.credentials.USAJobsAPIKey, "USAJOBS_EMAIL": c.credentials.USAJobsEmail, "REED_API_KEY": c.credentials.ReedAPIKey, "JOOBLE_API_KEY": c.credentials.JoobleAPIKey}
	}
	missing := []string{}
	for _, name := range provider.Credentials {
		if !usableJobCredential(values[name]) {
			missing = append(missing, name)
		}
	}
	return missing
}

func (c *JobClient) Configured(kind string) bool {
	_, supported := JobProviderFor(kind)
	return c != nil && supported && len(c.missing(kind)) == 0
}

// ValidateJobSource prevents a credential-bearing API URL or an arbitrary
// destination from being stored or used by an authenticated adapter.
func ValidateJobSource(source model.Source) error {
	u, err := ValidateURL(source.URL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Port() != "" {
		return errors.New("job API sources require the provider's HTTPS endpoint")
	}
	path := strings.TrimRight(u.Path, "/")
	allowed := []string{}
	valid := false
	switch source.Kind {
	case "jobs_arbeitnow":
		valid = (u.Hostname() == "www.arbeitnow.com" || u.Hostname() == "www.arbeitnow.co.uk") && path == "/api/job-board-api"
		allowed = []string{"visa_sponsorship", "page"}
	case "jobs_adzuna":
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 6 {
			page, pageErr := strconv.Atoi(parts[5])
			valid = u.Hostname() == "api.adzuna.com" && parts[0] == "v1" && parts[1] == "api" && parts[2] == "jobs" && len(parts[3]) == 2 && parts[4] == "search" && pageErr == nil && page > 0 && page <= 20
		}
		allowed = []string{"what", "where", "what_exclude", "category", "salary_min", "salary_max", "full_time", "part_time", "permanent", "contract", "max_days_old"}
	case "jobs_usajobs":
		valid = u.Hostname() == "data.usajobs.gov" && strings.EqualFold(path, "/api/search")
		allowed = []string{"Keyword", "PositionTitle", "LocationName", "JobCategoryCode", "Organization", "HiringPath", "RemoteIndicator", "DatePosted", "Page", "Radius"}
	case "jobs_reed":
		valid = u.Hostname() == "www.reed.co.uk" && path == "/api/1.0/search"
		allowed = []string{"keywords", "locationName", "distanceFromLocation", "employerId", "permanent", "contract", "temp", "partTime", "fullTime", "minimumSalary", "maximumSalary", "graduate", "resultsToSkip"}
	case "jobs_jooble":
		valid = u.Hostname() == "jooble.org" && path == "/api"
		allowed = []string{"keywords", "location", "radius", "salary", "page"}
	}
	if !valid {
		return errors.New("use the documented, credential-free endpoint for this job provider")
	}
	for key, values := range u.Query() {
		if !model.Contains(allowed, key) {
			return errors.New("unsupported job search parameter; API credentials belong in server environment variables")
		}
		if len(values) != 1 || len(values[0]) > 500 {
			return errors.New("job search parameters must be single values of at most 500 characters")
		}
	}
	return nil
}

func (c *JobClient) Read(ctx context.Context, source model.Source, limit int) ([]model.Post, error) {
	if err := ValidateJobSource(source); err != nil {
		return nil, err
	}
	provider, _ := JobProviderFor(source.Kind)
	if !c.Configured(source.Kind) {
		return nil, &JobAccessError{Provider: provider.Name, Missing: c.missing(source.Kind)}
	}
	limit = max(1, min(50, limit))
	u, _ := url.Parse(source.URL)
	params := u.Query()
	headers := http.Header{"Accept": {"application/json"}, "User-Agent": {"Radwave/0.2 (+https://github.com/YeandAmen/radwave)"}}
	method := http.MethodGet
	var body []byte
	switch source.Kind {
	case "jobs_adzuna":
		params.Set("app_id", strings.TrimSpace(c.credentials.AdzunaAppID))
		params.Set("app_key", strings.TrimSpace(c.credentials.AdzunaAppKey))
		params.Set("results_per_page", strconv.Itoa(limit))
		params.Set("sort_by", "date")
		params.Set("content-type", "application/json")
	case "jobs_usajobs":
		headers.Set("Authorization-Key", strings.TrimSpace(c.credentials.USAJobsAPIKey))
		headers.Set("User-Agent", strings.TrimSpace(c.credentials.USAJobsEmail))
		params.Set("ResultsPerPage", strconv.Itoa(limit))
		params.Set("SortField", "opendate")
		params.Set("SortDirection", "desc")
		params.Set("Fields", "Full")
	case "jobs_reed":
		headers.Set("Authorization", "Basic "+basicJobAuth(strings.TrimSpace(c.credentials.ReedAPIKey)))
		params.Set("resultsToTake", strconv.Itoa(limit))
	case "jobs_jooble":
		method = http.MethodPost
		u.Path = "/api/" + strings.TrimSpace(c.credentials.JoobleAPIKey)
		payload := map[string]any{"ResultOnPage": limit, "page": "1"}
		for key := range params {
			payload[key] = params.Get(key)
		}
		body, _ = json.Marshal(payload)
		params = url.Values{}
		headers.Set("Content-Type", "application/json")
	}
	u.RawQuery = params.Encode()
	c.mu.Lock()
	start := time.Now()
	if c.next[u.Hostname()].After(start) {
		start = c.next[u.Hostname()]
	}
	c.next[u.Hostname()] = start.Add(c.spacing)
	c.mu.Unlock()
	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not prepare the job API request")
	}
	req.Header = headers
	response, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New(provider.Name + " API request failed; the source could not be refreshed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &JobAccessError{Provider: provider.Name, Status: response.StatusCode}
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds >= 0 {
			retry := time.Now().Add(time.Duration(seconds) * time.Second)
			failure.RetryAt = &retry
		} else if retry, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			failure.RetryAt = &retry
		}
		return nil, failure
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (5<<20)+1))
	if err != nil || len(data) > 5<<20 {
		return nil, errors.New(provider.Name + " API response was unreadable or exceeded the 5 MB limit")
	}
	return parseJobAPI(data, source.Kind, limit)
}

func basicJobAuth(key string) string {
	req := &http.Request{Header: http.Header{}}
	req.SetBasicAuth(key, "")
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

func jobObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func jobText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	}
	return ""
}

func jobBoolean(value any) *bool {
	if boolean, ok := value.(bool); ok {
		return &boolean
	}
	if boolean, err := strconv.ParseBool(jobText(value)); err == nil {
		return &boolean
	}
	return nil
}

func jobNames(value any) []string {
	result := []string{}
	if name := jobText(value); name != "" {
		return []string{name}
	}
	if entries, ok := value.([]any); ok {
		for _, entry := range entries {
			name := jobText(entry)
			if name == "" {
				name = jobText(jobObject(entry)["Name"])
			}
			if name != "" && !model.Contains(result, name) {
				result = append(result, name)
			}
		}
	}
	return result
}

func jobSalary(minimum, maximum any, currency, interval string) string {
	low, high := jobText(minimum), jobText(maximum)
	if low == "" && high == "" {
		return ""
	}
	value := low
	if low == "" {
		value = "Up to " + high
	} else if high == "" {
		value = "From " + low
	} else if high != low {
		value += "–" + high
	}
	return model.CleanSpace(currency + " " + value + " " + interval)
}

func parseJobAPI(data []byte, kind string, limit int) ([]model.Post, error) {
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("job API did not return readable JSON")
	}
	object := jobObject(payload)
	var rows any
	switch kind {
	case "jobs_arbeitnow":
		rows = object["data"]
	case "jobs_adzuna":
		rows = object["results"]
	case "jobs_usajobs":
		rows = jobObject(object["SearchResult"])["SearchResultItems"]
	case "jobs_reed":
		rows = payload
	case "jobs_jooble":
		rows = object["jobs"]
	}
	entries, ok := rows.([]any)
	if !ok {
		return nil, errors.New("job API response did not contain the expected listings array")
	}
	provider, _ := JobProviderFor(kind)
	posts := []model.Post{}
	seen := map[string]bool{}
	for _, entry := range entries {
		row := jobObject(entry)
		post := model.Post{Categories: []string{}, CaptureMethod: "job_api", Job: &model.JobDetails{Provider: provider.Name, ProviderURL: providerLanding(kind)}}
		id := jobText(row["id"])
		switch kind {
		case "jobs_arbeitnow":
			id = jobText(row["slug"])
			post.URL, post.Title, post.Body, post.Author = jobText(row["url"]), jobText(row["title"]), jobText(row["description"]), jobText(row["company_name"])
			post.Location = jobText(row["location"])
			post.Categories = jobNames(row["tags"])
			post.Job.EmploymentType = strings.Join(jobNames(row["job_types"]), " · ")
			post.Job.Remote = jobBoolean(row["remote"])
			if seconds, err := strconv.ParseInt(jobText(row["created_at"]), 10, 64); err == nil && seconds > 0 {
				published := time.Unix(seconds, 0).UTC()
				post.PublishedAt = &published
			}
		case "jobs_adzuna":
			post.URL, post.Title, post.Body = jobText(row["redirect_url"]), jobText(row["title"]), jobText(row["description"])
			post.Author, post.Location = jobText(jobObject(row["company"])["display_name"]), jobText(jobObject(row["location"])["display_name"])
			post.Categories = []string{jobText(jobObject(row["category"])["label"])}
			post.PublishedAt = parseTime(jobText(row["created"]))
			post.Job.EmploymentType = model.CleanSpace(strings.ReplaceAll(jobText(row["contract_time"])+" "+jobText(row["contract_type"]), "_", " "))
			post.Job.Salary = jobSalary(row["salary_min"], row["salary_max"], "", "")
			if predicted := jobBoolean(row["salary_is_predicted"]); post.Job.Salary != "" && predicted != nil && *predicted {
				post.Job.Salary = "Estimated: " + post.Job.Salary
			}
			post.CaptureMethod = "job_api_summary"
		case "jobs_usajobs":
			id = jobText(row["MatchedObjectId"])
			descriptor := jobObject(row["MatchedObjectDescriptor"])
			details := jobObject(jobObject(descriptor["UserArea"])["Details"])
			post.URL, post.Title, post.Author = jobText(descriptor["PositionURI"]), jobText(descriptor["PositionTitle"]), jobText(descriptor["OrganizationName"])
			post.Location = jobText(descriptor["PositionLocationDisplay"])
			post.Body = jobText(details["JobSummary"]) + "\n" + strings.Join(jobNames(details["MajorDuties"]), "\n") + "\nEligibility\n" + jobText(jobObject(details["WhoMayApply"])["Name"]) + "\n" + jobText(descriptor["QualificationSummary"]) + "\n" + jobText(details["Requirements"]) + "\n" + jobText(details["Education"]) + "\nBenefits\n" + jobText(details["Benefits"])
			post.PublishedAt = parseTime(jobText(descriptor["PublicationStartDate"]))
			post.Deadline = jobISODate(jobText(descriptor["ApplicationCloseDate"]))
			post.Categories = jobNames(descriptor["JobCategory"])
			post.Job.EmploymentType = strings.Join(append(jobNames(descriptor["PositionSchedule"]), jobNames(descriptor["PositionOfferingType"])...), " · ")
			if remuneration, ok := descriptor["PositionRemuneration"].([]any); ok && len(remuneration) > 0 {
				pay := jobObject(remuneration[0])
				post.Job.Salary = jobSalary(pay["MinimumRange"], pay["MaximumRange"], jobText(pay["CurrencyCode"]), jobText(pay["Description"]))
			}
			if apply := jobNames(descriptor["ApplyURI"]); len(apply) > 0 {
				post.ApplicationURL = apply[0]
			}
			post.Job.Remote = jobBoolean(details["RemoteIndicator"])
		case "jobs_reed":
			id = jobText(row["jobId"])
			post.URL, post.Title, post.Body, post.Author = jobText(row["jobUrl"]), jobText(row["jobTitle"]), jobText(row["jobDescription"]), jobText(row["employerName"])
			post.Location = jobText(row["locationName"])
			post.Job.Salary = jobSalary(row["minimumSalary"], row["maximumSalary"], jobText(row["currency"]), jobText(row["salaryType"]))
			post.PublishedAt = parseTime(jobText(row["date"]))
			if post.PublishedAt == nil {
				if published, err := time.Parse("02/01/2006", jobText(row["date"])); err == nil {
					post.PublishedAt = &published
				}
			}
			post.CaptureMethod = "job_api_summary"
		case "jobs_jooble":
			post.URL, post.Title, post.Body, post.Author = jobText(row["link"]), jobText(row["title"]), jobText(row["snippet"]), jobText(row["company"])
			post.Location, post.Job.Salary, post.Job.EmploymentType = jobText(row["location"]), jobText(row["salary"]), jobText(row["type"])
			// Jooble's updated field is not a publication date.
			post.CaptureMethod = "job_api_summary"
		}
		if id == "" || strings.TrimSpace(post.Title) == "" || len(id) > 240 {
			continue
		}
		if _, err := ValidateURL(post.URL); err != nil {
			continue
		}
		post.Identity = strings.TrimPrefix(kind, "jobs_") + "-job-" + model.StableID(id)
		post.URL, post.Title, post.Body = CanonicalURL(post.URL), model.CleanSpace(PlainHTML(post.Title)), PlainHTML(post.Body)
		post.Author, post.Location = truncate(model.CleanSpace(post.Author), 180), truncate(model.CleanSpace(post.Location), 300)
		post.Job.Salary, post.Job.EmploymentType = truncate(post.Job.Salary, 200), truncate(post.Job.EmploymentType, 180)
		if post.Body == "" {
			post.Body = post.Title
		}
		if !seen[post.Identity] {
			posts = append(posts, post)
			seen[post.Identity] = true
		}
	}
	if len(entries) > 0 && len(posts) == 0 {
		return nil, errors.New("job API returned listings without usable identities, titles, or public URLs")
	}
	sort.SliceStable(posts, func(i, j int) bool {
		if posts[i].PublishedAt == nil {
			return false
		}
		if posts[j].PublishedAt == nil {
			return true
		}
		return posts[i].PublishedAt.After(*posts[j].PublishedAt)
	})
	if len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

func providerLanding(kind string) string {
	return map[string]string{"jobs_arbeitnow": "https://www.arbeitnow.com", "jobs_adzuna": "https://www.adzuna.com", "jobs_usajobs": "https://www.usajobs.gov", "jobs_reed": "https://www.reed.co.uk", "jobs_jooble": "https://jooble.org"}[kind]
}

func jobISODate(raw string) string {
	if len(raw) >= 10 {
		if _, err := time.Parse("2006-01-02", raw[:10]); err == nil {
			return raw[:10]
		}
	}
	return ""
}
