// Package ingest turns publicly available source material into conservative,
// evidence-backed opportunity records.
package ingest

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/YeandAmen/radwave/internal/model"
)

var (
	monthNames    = `(?:January|February|March|April|May|June|July|August|September|October|November|December|Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sep|Sept|Oct|Nov|Dec)`
	dateDMY       = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(` + monthNames + `)\.?[\s,]+(20\d{2})\b`)
	dateMDY       = regexp.MustCompile(`(?i)\b(` + monthNames + `)\.?\s+(\d{1,2})(?:st|nd|rd|th)?[\s,]+(20\d{2})\b`)
	dateISO       = regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}\b`)
	deadlineRE    = regexp.MustCompile(`(?i)(?:application\s+deadline|deadline|closing\s+date|apply\s+by|applications?\s+clos(?:e|ing)(?:\s+on)?|submit\s+(?:your\s+)?application\s+by)\s*[:\-–]?\s*([^\n]{0,170})`)
	urlRE         = regexp.MustCompile(`https?://[^\s<>"\)\]]+`)
	hashtagRE     = regexp.MustCompile(`#[\pL\pN_]+`)
	ageRE         = regexp.MustCompile(`(?i)(?:between|aged?|ages)\s+(\d{1,2})\s*(?:-|–|—|to|and)\s*(\d{1,2})(?:\s+years(?:\s+of\s+age)?)?`)
	moneyRE       = regexp.MustCompile(`(?i)(?:up\s+to\s+)?(?:US\$|USD\s*|[$€£])\s*\d[\d,]*(?:\.\d{1,2})?(?:\s*(?:USD|EUR|GBP))?`)
	locationRE    = regexp.MustCompile(`(?im)^(?:\s*[📍🌍]\s*)?(?:location|based\s+in|venue|program(?:me)?\s+location)\s*:\s*([^\n]{2,100})`)
	opportunityRE = regexp.MustCompile(`(?i)\b(?:call for applications|applications? (?:are )?(?:now )?(?:open|invited)|invited to apply|apply now|apply by|application deadline|call for proposals|we(?:'re| are) hiring|register now)\b`)
	bulletRE      = regexp.MustCompile(`^\s*(?:[-•*]|\d+[.)])\s*`)
	actionLineRE  = regexp.MustCompile(`(?i)^(?:apply (?:now|here)|click here to (?:apply|submit)|submit (?:your )?application|for more information|https?://)`)
)

func IsOpportunity(p model.Post) bool {
	if p.Job != nil {
		return true
	}
	if opportunityRE.MatchString(p.Body) {
		return true
	}
	// A named fellowship/scholarship/etc. in the title or feed category is a
	// useful discovery signal. A mention buried in unrelated prose is not.
	return classify(p.Title+" "+strings.Join(p.Categories, " ")) != "program"
}

func Extract(p model.Post, source model.Source, now time.Time) model.Opportunity {
	body := normalizeLines(p.Body)
	title := cleanTitle(p.Title, body)
	kind := classify(title + " " + strings.Join(p.Categories, " "))
	if kind == "program" {
		kind = classify(body)
	}
	o := model.Opportunity{
		ID: model.StableID(CanonicalURL(p.URL)), URL: CanonicalURL(p.URL), Title: title,
		Kind: kind, Organizer: p.Author, Summary: summarize(body, title), Body: body,
		DeadlineState: "unknown", Region: "Not stated", Eligibility: []string{},
		Benefits: []string{}, Tags: []string{}, Missing: []string{}, Evidence: map[string]string{},
		SourceID: source.ID, SourceName: source.Name, SourceKind: source.Kind, SourceURL: source.URL,
		CaptureMethod: p.CaptureMethod, ExtractionVersion: "rules-1", PublishedAt: p.PublishedAt,
		FirstSeenAt: now.UTC(), UpdatedAt: now.UTC(), LastFetchedAt: now.UTC(), Reactions: p.Reactions, Comments: p.Comments,
		Reposts: p.Reposts, Quotes: p.Quotes,
	}
	if p.Identity != "" {
		o.ID = p.Identity
	}
	if p.Job != nil {
		job := *p.Job
		if job.Salary == "" {
			if match := salaryLine.FindStringSubmatch(body); len(match) > 1 {
				job.Salary = strings.TrimSpace(match[1])
			}
		}
		if job.EmploymentType == "" {
			if match := employmentLine.FindStringSubmatch(body); len(match) > 1 {
				job.EmploymentType = strings.TrimSpace(match[1])
			}
		}
		o.Job, o.Kind = &job, "job"
		if jobInternship.MatchString(p.Title + " " + job.EmploymentType) {
			o.Kind = "internship"
		}
	}
	if IsLinkedInHost(p.URL) {
		if id := LinkedInIdentity(p.URL); id != "" {
			o.ID = id
		}
	}
	if id := XPostID(p.URL); id != "" {
		o.ID = "x-" + id
		o.URL = "https://x.com/i/status/" + id
		o.PostAuthor, o.Organizer = p.Author, ""
	}
	o.Deadline, o.DeadlineState, o.DeadlineText = extractDeadline(body)
	if o.DeadlineText != "" {
		o.Evidence["deadline"] = o.DeadlineText
	}
	o.Eligibility = section(body, []string{"eligibility", "eligibility criteria", "requirements", "who can apply", "who should apply", "qualifications"})
	if match := ageRE.FindString(body); match != "" && !containsText(o.Eligibility, match) {
		o.Eligibility = append(o.Eligibility, match)
	}
	if len(o.Eligibility) > 0 {
		o.Evidence["eligibility"] = strings.Join(o.Eligibility, "\n")
	}
	o.Benefits = section(body, []string{"benefits", "funding", "what you will receive", "what you get", "scholarship coverage"})
	if match := moneyRE.FindString(title); match != "" {
		o.Funding = strings.TrimSpace(match)
	} else if match := moneyRE.FindString(strings.Join(o.Benefits, " ")); match != "" {
		o.Funding = strings.TrimSpace(match)
	} else if regexp.MustCompile(`(?i)fully[ -]funded`).MatchString(title + " " + body) {
		o.Funding = "Fully funded"
	}
	if o.Funding != "" {
		o.Evidence["funding"] = o.Funding + " (source wording; see benefits for coverage)"
	}
	if match := locationRE.FindStringSubmatch(body); len(match) > 1 {
		o.Location = strings.TrimSpace(match[1])
		o.Evidence["location"] = strings.TrimSpace(match[0])
	}
	o.Region, o.Evidence["region"] = extractRegion(p.Categories, body)
	if o.Evidence["region"] == "" {
		delete(o.Evidence, "region")
	}
	o.ApplicationURL, o.Evidence["application_url"] = applicationLink(p, o.URL)
	if o.ApplicationURL == "" {
		delete(o.Evidence, "application_url")
	}
	o.Tags = tags(title + " " + body)
	if p.Job != nil {
		if p.Location != "" {
			o.Location, o.Evidence["location"] = model.CleanSpace(p.Location), "Location supplied by the job source: "+p.Location
		}
		if deadline := jobISODate(p.Deadline); deadline != "" {
			label := p.Job.DeadlineLabel
			if label == "" {
				label = "Application closing date"
			}
			o.Deadline, o.DeadlineState, o.DeadlineText = deadline, "dated", label+": "+deadline
			o.Evidence["deadline"] = o.DeadlineText
		}
		if application := validJobApplication(p.ApplicationURL); application != "" {
			o.ApplicationURL, o.Evidence["application_url"] = application, "Application URL supplied by the job source"
		}
		if o.Job.Salary != "" {
			o.Evidence["salary"] = o.Job.Salary + " (job source wording)"
		}
		if o.Job.EmploymentType != "" {
			o.Evidence["employment_type"] = o.Job.EmploymentType
		}
		o.Funding = ""
		delete(o.Evidence, "funding")
	}
	if o.DeadlineState == "unknown" {
		o.Missing = append(o.Missing, "confirmed deadline")
	}
	if len(o.Eligibility) == 0 {
		o.Missing = append(o.Missing, "eligibility requirements")
	}
	if o.Location == "" {
		label := "program location"
		if p.Job != nil {
			label = "job location"
		}
		o.Missing = append(o.Missing, label)
	}
	if o.ApplicationURL == "" {
		o.Missing = append(o.Missing, "direct application link")
	}
	o.Decorate(now)
	return o
}

func IsLinkedInHost(raw string) bool {
	u, err := ValidateURL(raw)
	return err == nil && IsLinkedIn(u.Hostname())
}

func classify(text string) string {
	patterns := []struct{ kind, pattern string }{
		{"scholarship", `\b(?:scholarships?|bursar(?:y|ies)|scholars program(?:me)?)\b`},
		{"fellowship", `\b(?:fellowships?|fellows program(?:me)?|post[ -]?doctoral)\b`},
		{"bounty", `\b(?:bug bount(?:y|ies)|bount(?:y|ies)|paid contributions?)\b`},
		{"freelance", `\b(?:freelanc(?:e|er|ers|ing)|contract (?:work|role|position)|consulting opportunity)\b`},
		{"research", `\b(?:research (?:position|opportunity|assistant|role)|lab opening|academic position)\b`},
		{"program", `\b(?:incubat(?:ion|or)|accelerators?|accelerator program(?:me)?)\b`},
		{"grant", `\b(?:grants?|funding call|call for proposals)\b`},
		{"internship", `\b(?:internships?|graduate (?:development|trainee)|trainee (?:program(?:me)?|associate))\b`},
		{"competition", `\b(?:competitions?|hackathons?|contests?|challenge|prize|awards?)\b`},
		{"volunteering", `\bvolunteer(?:s|ing)?\b`},
		{"conference", `\b(?:conferences?|summits?)\b`},
		{"training", `\b(?:training|bootcamps?|short courses?|workshops?)\b`},
		{"job", `\b(?:jobs?|recruitment|hiring|vacanc(?:y|ies))\b`},
	}
	for _, p := range patterns {
		if regexp.MustCompile("(?i)" + p.pattern).MatchString(text) {
			return p.kind
		}
	}
	return "program"
}

func extractDeadline(body string) (string, string, string) {
	matches := deadlineRE.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		value := match[1]
		lower := strings.ToLower(value)
		if strings.Contains(lower, "rolling") || strings.Contains(lower, "ongoing") {
			return "", "rolling", strings.TrimSpace(match[0])
		}
		if d := dateISO.FindString(value); d != "" {
			if _, err := time.Parse("2006-01-02", d); err == nil {
				return d, "dated", strings.TrimSpace(match[0])
			}
		}
		if m := dateDMY.FindStringSubmatch(value); len(m) > 3 {
			if d := namedDate(m[1], m[2], m[3]); d != "" {
				return d, "dated", strings.TrimSpace(match[0])
			}
		}
		if m := dateMDY.FindStringSubmatch(value); len(m) > 3 {
			if d := namedDate(m[2], m[1], m[3]); d != "" {
				return d, "dated", strings.TrimSpace(match[0])
			}
		}
	}
	if len(matches) > 0 {
		return "", "unknown", strings.TrimSpace(matches[0][0])
	}
	return "", "unknown", ""
}

func namedDate(day, month, year string) string {
	month = strings.ToUpper(month[:1]) + strings.ToLower(month[1:])
	if month == "Sept" {
		month = "Sep"
	}
	for _, layout := range []string{"2 January 2006", "2 Jan 2006"} {
		if d, err := time.Parse(layout, day+" "+month+" "+year); err == nil {
			return d.Format("2006-01-02")
		}
	}
	return ""
}

func cleanTitle(title, body string) string {
	title = strings.TrimSpace(title)
	if title == "" || strings.HasPrefix(title, "#") || strings.HasSuffix(title, "’s Post") || strings.HasSuffix(title, "'s Post") {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) != "" {
				title = line
				break
			}
		}
	}
	title = regexp.MustCompile(`(?i)^\s*(?:call for applications|applications (?:are )?open)\s*[:!–-]\s*`).ReplaceAllString(title, "")
	return truncate(model.CleanSpace(title), 170)
}

func summarize(body, title string) string {
	var parts []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if isHeading(line) || bulletRE.MatchString(line) || deadlineRE.MatchString(line) || strings.HasPrefix(line, "#") || strings.Contains(line, "appeared first on") || model.CleanSpace(line) == title {
			continue
		}
		line = model.CleanSpace(hashtagRE.ReplaceAllString(urlRE.ReplaceAllString(line, ""), ""))
		if utf8.RuneCountInString(line) < 55 || strings.HasPrefix(strings.ToLower(line), "call for applications") {
			continue
		}
		parts = append(parts, line)
		if len(strings.Join(parts, " ")) > 210 {
			break
		}
	}
	if len(parts) == 0 {
		return "Open the original source for the full opportunity details."
	}
	return truncate(strings.Join(parts, " "), 340)
}

func section(body string, headings []string) []string {
	var result []string
	active := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key := strings.ToLower(strings.Trim(line, " :*"))
		if model.Contains(headings, key) {
			active = true
			continue
		}
		if active && (isHeading(line) || deadlineRE.MatchString(line) || actionLineRE.MatchString(line) || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "🔗")) {
			break
		}
		if active {
			line = bulletRE.ReplaceAllString(line, "")
			if len(line) >= 8 {
				result = append(result, truncate(line, 700))
			}
			if len(result) == 8 {
				break
			}
		}
	}
	return result
}

func isHeading(line string) bool {
	key := strings.ToLower(strings.Trim(line, " :*"))
	return model.Contains([]string{"eligibility", "eligibility criteria", "requirements", "who can apply", "who should apply", "qualifications", "benefits", "funding", "application", "applications", "how to apply", "application process", "selection", "selection criteria", "cost", "costs", "timeline", "fellowship tracks", "about", "for more information", "scholarship coverage", "what you will receive", "what you get"}, key)
}

func extractRegion(categories []string, body string) (string, string) {
	var found []string
	for _, category := range categories {
		c := strings.ToLower(strings.TrimSpace(category))
		region := ""
		switch c {
		case "africa", "ghana", "nigeria", "south africa", "kenya":
			region = "Africa"
		case "america", "americas", "north america", "south america", "canada", "usa":
			region = "Americas"
		case "asia":
			region = "Asia"
		case "europe":
			region = "Europe"
		case "australia and oceania", "australia", "oceania":
			region = "Oceania"
		}
		if region != "" && !model.Contains(found, region) {
			found = append(found, region)
		}
	}
	if len(found) >= 4 {
		return "Worldwide", "Publisher categorizes this post across multiple world regions; confirm country eligibility in the source."
	}
	global := regexp.MustCompile(`(?i)(?:applicants?|applications?|builders?|changemakers?|leaders?|people)\s+(?:from\s+)?(?:all countries|around the world|worldwide|any country)`)
	if text := global.FindString(body); text != "" {
		return "Worldwide", text
	}
	if len(found) == 1 {
		return found[0], "Publisher region category: " + found[0] + ". This is a discovery label, not proof of eligibility."
	}
	return "Not stated", ""
}

func applicationLink(p model.Post, canonical string) (string, string) {
	links := append([]model.Link{}, p.Links...)
	for _, index := range urlRE.FindAllStringIndex(p.Body, -1) {
		start := max(0, index[0]-75)
		links = append(links, model.Link{URL: strings.TrimRight(p.Body[index[0]:index[1]], ".,;"), Context: p.Body[start:index[1]]})
	}
	best, evidence, bestScore := "", "", 0
	for _, link := range links {
		target := UnwrapLink(link.URL, p.URL)
		u, err := ValidateURL(target)
		if err != nil || CanonicalURL(target) == canonical {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if IsLinkedIn(host) || strings.Contains(host, "facebook.com") || strings.Contains(host, "twitter.com") || host == "x.com" || host == "t.me" || host == "wa.me" || strings.Contains(host, "pinterest.") {
			continue
		}
		text := strings.ToLower(link.Text + " " + link.Context)
		if strings.Contains(text, "share on") || strings.Contains(text, "privacy") || strings.Contains(text, "subscribe") {
			continue
		}
		score := 0
		for _, signal := range []string{"apply", "submit", "application form", "letter of intent", "register"} {
			if strings.Contains(text, signal) {
				score += 20
			}
		}
		if host == "lnkd.in" && IsLinkedInHost(p.URL) {
			score += 10
		}
		if strings.Contains(strings.ToLower(u.Path), "apply") || strings.Contains(host, "forms.") {
			score += 5
		}
		if score > bestScore {
			best, evidence, bestScore = target, truncate(model.CleanSpace(link.Text+" "+link.Context), 230), score
		}
	}
	return best, evidence
}

func tags(text string) []string {
	lower := strings.ToLower(text)
	var result []string
	groups := []struct {
		tag   string
		words []string
	}{
		{"Entrepreneurship", []string{"entrepreneur", "incubat", "startup", "start-up"}},
		{"Agriculture", []string{"agrifood", "agri-food", "agribusiness", "aquaculture", "agriculture"}},
		{"Climate", []string{"climate", "sustainab", "green economy", "circular economy"}},
		{"Technology", []string{"technology", "technologist", "artificial intelligence", " ai ", "software", "digital"}},
		{"Research", []string{"research", "postdoctoral", "ph.d", "phd"}},
		{"Leadership", []string{"leadership", "leaders", "changemakers"}},
		{"Education", []string{"students", "university", "scholarship", "education"}},
		{"Health", []string{"health", "medical", "disease"}},
		{"Media", []string{"journalis", "media", "reporting"}},
		{"Finance", []string{"finance", "wealth", "economics"}},
		{"Youth", []string{"youth", "young people", "young entrepreneur", "young graduates"}},
	}
	for _, group := range groups {
		for _, word := range group.words {
			if strings.Contains(lower, word) {
				result = append(result, group.tag)
				break
			}
		}
	}
	return result
}

func containsText(items []string, text string) bool {
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), strings.ToLower(text)) {
			return true
		}
	}
	return false
}

func normalizeLines(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = model.CleanSpace(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
