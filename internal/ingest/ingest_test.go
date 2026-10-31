package ingest_test

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/YeandAmen/radwave/internal/bootstrap"
	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
)

func TestLinkedInReferenceExtractionAndCounterScope(t *testing.T) {
	data := map[string]any{
		"@type": "SocialMediaPosting", "headline": "CALL FOR APPLICATIONS: Business Incubation Programme for Young Entrepreneurs",
		"articleBody": bootstrap.ExampleBody, "datePublished": "2026-10-27T21:31:46.434Z",
		"author":               map[string]any{"name": "Nkabom Collaborative - UESD", "interactionStatistic": map[string]any{"interactionType": "http://schema.org/FollowAction", "userInteractionCount": 2385}},
		"interactionStatistic": []any{map[string]any{"interactionType": "http://schema.org/LikeAction", "userInteractionCount": 236}, map[string]any{"interactionType": "https://schema.org/CommentAction", "userInteractionCount": 20}},
		"comment":              []any{map[string]any{"text": "Only people in Ghana may apply", "interactionStatistic": map[string]any{"interactionType": "http://schema.org/LikeAction", "userInteractionCount": 9000}}},
	}
	encoded, _ := json.Marshal(data)
	html := `<html><head><link rel="canonical" href="` + bootstrap.ExampleURL + `"><script type="application/ld+json">` + string(encoded) + `</script></head><body></body></html>`
	post, err := ingest.ParsePage([]byte(html), bootstrap.ExampleShareURL+"?utm_source=share")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	o := ingest.Extract(post, model.Source{ID: "test", Name: "Nkabom", Kind: "linkedin_post"}, now)
	if o.ID != "linkedin-7520960460347768832" || o.Kind != "program" {
		t.Fatalf("incorrect identity/type: %+v", o)
	}
	if o.Deadline != "2026-11-03" || o.Status != "closing" || o.DaysLeft == nil || *o.DaysLeft != 4 {
		t.Fatalf("incorrect deadline: %+v", o)
	}
	if o.Reactions == nil || *o.Reactions != 236 || o.Comments == nil || *o.Comments != 20 {
		t.Fatalf("read a nested counter instead of the post counters: %+v", o)
	}
	if len(o.Eligibility) != 3 || !strings.Contains(o.Eligibility[0], "18-35") {
		t.Fatalf("missing eligibility: %#v", o.Eligibility)
	}
	if o.ApplicationURL != "https://lnkd.in/dCwPpDfP" {
		t.Fatalf("application URL = %q", o.ApplicationURL)
	}
	if o.Location != "" || o.Region != "Not stated" || strings.Contains(strings.Join(o.Eligibility, " "), "Only people in Ghana") {
		t.Fatal("inferred eligibility from a commenter or profile country")
	}
	if o.Evidence["deadline"] == "" || o.Evidence["eligibility"] == "" {
		t.Fatal("missing extraction evidence")
	}
}

func TestDeadlineParsingIsConservative(t *testing.T) {
	cases := []struct{ text, deadline, state string }{
		{"Application deadline: 03 November 2026", "2026-11-03", "dated"},
		{"Deadline: November 2nd, 2026", "2026-11-02", "dated"},
		{"Deadline: NOVEMBER 30TH, 2026.", "2026-11-30", "dated"},
		{"Apply by 2026-12-17", "2026-12-17", "dated"},
		{"Event: 4 January 2027\nDeadline: 3 November 2026", "2026-11-03", "dated"},
		{"Deadline: On Rolling Basis", "", "rolling"},
		{"Deadline: 31 February 2026", "", "unknown"},
		{"Deadline: 03/11/2026", "", "unknown"},
		{"Deadline: 3 November", "", "unknown"},
		{"Program starts in 2027; deadline unspecified.", "", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			o := ingest.Extract(model.Post{URL: "https://example.org/opportunity", Title: "Youth program", Body: tc.text}, model.Source{}, time.Now())
			if o.Deadline != tc.deadline || o.DeadlineState != tc.state {
				t.Fatalf("got %q / %q; want %q / %q", o.Deadline, o.DeadlineState, tc.deadline, tc.state)
			}
		})
	}
}

func TestRSSAndAtomPreserveUsefulContent(t *testing.T) {
	rss := `<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><title>Builders &amp; Leaders Fellowship</title><link>https://example.org/fellowship?utm_medium=rss</link><pubDate>Fri, 30 Oct 2026 14:03:05 +0000</pubDate><category>Fellowships</category><content:encoded><![CDATA[<p>Deadline: November 2nd, 2026</p><p><strong>Eligibility</strong></p><ul><li>Applicants from all countries may apply.</li></ul><p><a href="https://apply.example.org/form">Apply now</a></p>]]></content:encoded></item></channel></rss>`
	posts, err := ingest.ParseFeed([]byte(rss), "https://example.org/feed", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].PublishedAt == nil || posts[0].Title != "Builders & Leaders Fellowship" {
		t.Fatalf("bad RSS result: %#v", posts)
	}
	o := ingest.Extract(posts[0], model.Source{Kind: "rss"}, time.Now())
	if len(o.Eligibility) != 1 || o.ApplicationURL != "https://apply.example.org/form" || o.Region != "Worldwide" {
		t.Fatalf("lost content: %+v", o)
	}
	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Global Scholarship</title><link rel="alternate" href="https://example.org/scholarship"/><published>2026-10-30T12:00:00Z</published><category term="Scholarships"/><summary>Applications are open. Deadline: 3 November 2026</summary></entry></feed>`
	posts, err = ingest.ParseFeed([]byte(atom), "https://example.org/feed.xml", 20)
	if err != nil || len(posts) != 1 || !ingest.IsOpportunity(posts[0]) {
		t.Fatalf("bad Atom result: %#v, %v", posts, err)
	}
	if _, err := ingest.ParseFeed([]byte(`<html>Sign in</html>`), "https://example.org/feed", 20); err == nil {
		t.Fatal("accepted a login HTML document as a feed")
	}
}

func TestDOMFallbackAndUnknownEngagement(t *testing.T) {
	html := `<html><body><h1>Organization’s Post</h1><p data-test-id="main-feed-activity-card__commentary">CALL FOR APPLICATIONS: Youth Incubation Programme<br>Applications are open for young entrepreneurs.<br>Eligibility Criteria<br>1. Must be between 18-35 years of age<br>Application deadline: 03 November 2026<br><a href="https://www.linkedin.com/redir/redirect?url=https%3A%2F%2Fapply.example.org%2Fform">Apply now</a></p><section class="comment">Ghana only</section><a data-num-reactions="0"></a></body></html>`
	post, err := ingest.ParsePage([]byte(html), bootstrap.ExampleURL)
	if err != nil {
		t.Fatal(err)
	}
	if post.Reactions == nil || *post.Reactions != 0 || post.Comments != nil {
		t.Fatal("unknown engagement was converted into zero")
	}
	o := ingest.Extract(post, model.Source{Kind: "linkedin_post"}, time.Now())
	if o.Title != "Youth Incubation Programme" || o.ApplicationURL != "https://apply.example.org/form" || strings.Contains(o.Body, "Ghana only") {
		t.Fatalf("bad fallback: %+v", o)
	}
	if _, err := ingest.ParsePage([]byte(`<html><h1>Sign in</h1></html>`), bootstrap.ExampleURL); err == nil {
		t.Fatal("accepted an authentication wall as a post")
	}
}

func TestUniversityJobFeedDecodesDeclaredLatin1Encoding(t *testing.T) {
	rss := []byte(`<?xml version="1.0" encoding="ISO-8859-1"?><rss><channel><item><title>Research Associate</title><link>https://university.example.org/jobs/123</link><description>Salary: `)
	rss = append(rss, 0xa3)
	rss = append(rss, []byte(`40000 per year</description></item></channel></rss>`)...)
	posts, err := ingest.ParseFeed(rss, "https://university.example.org/RSS/rss.aspx", 10)
	if err != nil || len(posts) != 1 || !strings.Contains(posts[0].Body, "£40000") {
		t.Fatalf("Latin-1 job RSS lost its salary text: %+v, %v", posts, err)
	}
}

func TestPublisherJSONLDTitleEntitiesAreHumanReadable(t *testing.T) {
	page := `<article><script type="application/ld+json">{"@type":"BlogPosting","headline":"Telemachus &amp; Athena Mentoring Programme","articleBody":"Applications are open for young leaders."}</script></article>`
	post, err := ingest.ParsePage([]byte(page), "https://example.org/mentoring")
	if err != nil || post.Title != "Telemachus & Athena Mentoring Programme" {
		t.Fatalf("encoded publisher headline reached the opportunity title: %q, %v", post.Title, err)
	}
}

func TestURLSafetyAndCanonicalization(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://127.0.0.1/", "http://[::1]/", "http://169.254.169.254/", "https://localhost/", "http://user:password@example.org/", "http://internal.local/", "http://100.64.0.1/"} {
		if _, err := ingest.ValidateURL(raw); err == nil {
			t.Errorf("accepted unsafe URL %q", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "::ffff:192.168.1.1", "2001:db8::1", "198.18.1.1", "203.0.113.9"} {
		if ingest.PublicIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted non-public IP %s", raw)
		}
	}
	if !ingest.PublicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("rejected a public IP")
	}
	if got := ingest.CanonicalURL("https://gh.linkedin.com/posts/test-activity-7520960460347768832-x/?utm_source=share&trk=test#comments"); got != "https://www.linkedin.com/posts/test-activity-7520960460347768832-x" {
		t.Fatalf("bad canonical URL %q", got)
	}
	if got := ingest.CanonicalURL("https://example.org/apply?program=42&utm_source=share"); got != "https://example.org/apply?program=42" {
		t.Fatalf("removed a meaningful query parameter: %q", got)
	}
	if got := ingest.CanonicalURL("https://university.example.org/Vacancy.aspx?ref=ABC123&utm_source=rss"); got != "https://university.example.org/Vacancy.aspx?ref=ABC123" {
		t.Fatal("a meaningful career-portal vacancy reference was removed")
	}
	if ingest.IsOpportunity(model.Post{Title: "Our incubation programme celebration", Body: "Congratulations to the businesses that graduated today."}) {
		t.Fatal("classified a celebration as an application opportunity")
	}
	if !ingest.IsOpportunity(model.Post{Title: "Youth incubator", Body: "Applications are now open for young entrepreneurs."}) {
		t.Fatal("missed an explicitly open program announcement")
	}
}

func TestPublicPageDiscoveryDeduplicatesActivities(t *testing.T) {
	page := `<a href="/posts/one-activity-7520960460347768832-a?trk=x">Post</a><a href="/posts/one-activity-7520960460347768832-a">Same post</a><a href="https://evil.example/posts/not-linkedin">External</a><a href="/posts/two-activity-7520960460347768833-b">Another</a>`
	links, err := ingest.DiscoverLinkedIn([]byte(page), "https://www.linkedin.com/company/example", 20)
	if err != nil || len(links) != 2 {
		t.Fatalf("discovery = %#v, %v", links, err)
	}
}
