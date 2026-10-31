package ingest

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/YeandAmen/radwave/internal/model"
)

var ErrNoPublicJob = errors.New("this page does not expose a readable public job listing; paste the job description to import it")
var jobInternship = regexp.MustCompile(`(?i)\b(?:internships?|intern)\b`)
var salaryLine = regexp.MustCompile(`(?im)^\s*(?:salary|pay|compensation)\s*[:\-]\s*([^\n]{2,150})`)
var employmentLine = regexp.MustCompile(`(?im)^\s*(?:employment type|job type|contract type|work type)\s*:\s*([^\n]{2,100})`)

func jobPosting(node map[string]any, post model.Post) (model.Post, error) {
	post.Title = stringValue(node["title"])
	post.Body = PlainHTML(stringValue(node["description"]))
	post.Author = stringValue(jobObject(node["hiringOrganization"])["name"])
	post.PublishedAt = parseTime(stringValue(node["datePosted"]))
	post.Deadline = jobISODate(stringValue(node["validThrough"]))
	post.CaptureMethod = "public_job"
	post.Job = &model.JobDetails{EmploymentType: strings.Join(jobNames(node["employmentType"]), " · "), DeadlineLabel: "Listing valid through"}
	if IsLinkedInHost(post.URL) {
		post.Job.Provider, post.Job.ProviderURL = "LinkedIn", "https://www.linkedin.com/jobs"
	}
	for _, location := range asObjects(node["jobLocation"]) {
		address := jobObject(location["address"])
		country := jobText(address["addressCountry"])
		if country == "" {
			country = jobText(jobObject(address["addressCountry"])["name"])
		}
		parts := []string{}
		for _, value := range []string{jobText(address["addressLocality"]), jobText(address["addressRegion"]), country} {
			if value != "" && !model.Contains(parts, value) {
				parts = append(parts, value)
			}
		}
		if len(parts) > 0 {
			if post.Location != "" {
				post.Location += " · "
			}
			post.Location += strings.Join(parts, ", ")
		}
	}
	if strings.EqualFold(stringValue(node["jobLocationType"]), "TELECOMMUTE") {
		remote := true
		post.Job.Remote = &remote
	}
	for _, salary := range asObjects(node["baseSalary"]) {
		amount := jobObject(salary["value"])
		low, high := amount["minValue"], amount["maxValue"]
		if amount["value"] != nil {
			low, high = amount["value"], amount["value"]
		}
		unit := jobText(amount["unitText"])
		if label := map[string]string{"YEAR": "per year", "MONTH": "per month", "WEEK": "per week", "DAY": "per day", "HOUR": "per hour"}[strings.ToUpper(unit)]; label != "" {
			unit = label
		}
		post.Job.Salary = jobSalary(low, high, jobText(salary["currency"]), unit)
		if post.Job.Salary != "" {
			break
		}
	}
	if direct, ok := node["directApply"].(bool); ok && direct {
		post.ApplicationURL = post.URL
	}
	post.Links = linksFromHTML(stringValue(node["description"]), post.URL)
	if strings.TrimSpace(post.Title) == "" || strings.TrimSpace(post.Body) == "" {
		return model.Post{}, ErrNoPublicJob
	}
	return post, nil
}

func linkedInJobFallback(doc *goquery.Document, post model.Post) (model.Post, error) {
	content := doc.Find(".description__text .show-more-less-html__markup, .show-more-less-html__markup, [data-test-id='job-details-description']").First()
	post.Body = selectionText(content)
	if strings.TrimSpace(post.Body) == "" {
		return model.Post{}, ErrNoPublicJob
	}
	post.Title = model.CleanSpace(doc.Find("h1.top-card-layout__title, h1").First().Text())
	post.Author = model.CleanSpace(doc.Find(".topcard__org-name-link, .top-card-layout__first-subline a").First().Text())
	post.Location = model.CleanSpace(doc.Find(".topcard__flavor--bullet, .top-card-layout__first-subline .topcard__flavor").First().Text())
	post.Links, post.CaptureMethod = selectionLinks(content, post.URL), "public_job"
	post.Job = &model.JobDetails{Provider: "LinkedIn", ProviderURL: "https://www.linkedin.com/jobs"}
	doc.Find(".description__job-criteria-item").Each(func(_ int, criterion *goquery.Selection) {
		label := strings.ToLower(model.CleanSpace(criterion.Find("h3").Text()))
		if label == "employment type" {
			post.Job.EmploymentType = model.CleanSpace(criterion.Find("span").Text())
		}
	})
	if match := salaryLine.FindStringSubmatch(post.Body); len(match) > 1 {
		post.Job.Salary = strings.TrimSpace(match[1])
	}
	return post, nil
}

func DiscoverLinkedInJobs(data []byte, pageURL string, limit int) ([]string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	result, seen := []string{}, map[string]bool{}
	add := func(raw string) {
		target := UnwrapLink(raw, pageURL)
		id := LinkedInJobID(target)
		if id != "" && !seen[id] && len(result) < limit {
			seen[id] = true
			result = append(result, CanonicalURL(target))
		}
	}
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) { raw, _ := s.Attr("href"); add(raw) })
	for _, node := range jsonLD(doc) {
		if hasType(node, "JobPosting") {
			add(stringValue(node["url"]))
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no public LinkedIn job links are exposed; add an individual job URL or a job API source")
	}
	return result, nil
}

func validJobApplication(raw string) string {
	u, err := ValidateURL(raw)
	if err != nil || u == nil {
		return ""
	}
	return raw
}

func IsLinkedInJobSearch(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && IsLinkedIn(u.Hostname()) && (strings.HasPrefix(u.Path, "/jobs/search") || strings.HasPrefix(u.Path, "/company/") && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/jobs"))
}
