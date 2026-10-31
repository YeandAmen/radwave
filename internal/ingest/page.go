package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/YeandAmen/radwave/internal/model"
	nethtml "golang.org/x/net/html"
)

var ErrNoPublicPost = errors.New("this page does not expose readable public post content; paste the original post text to import it")

func ParsePage(data []byte, pageURL string) (model.Post, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return model.Post{}, err
	}
	post := model.Post{URL: CanonicalURL(pageURL), CaptureMethod: "public_page", Categories: []string{}, Links: []model.Link{}}
	if canonical, ok := doc.Find(`link[rel="canonical"]`).First().Attr("href"); ok {
		candidate := UnwrapLink(canonical, pageURL)
		original, _ := url.Parse(pageURL)
		u, parseErr := url.Parse(candidate)
		if parseErr == nil && (strings.EqualFold(u.Hostname(), original.Hostname()) || IsLinkedIn(u.Hostname()) && IsLinkedIn(original.Hostname())) {
			post.URL = CanonicalURL(candidate)
		}
	}
	linkedIn := IsLinkedInHost(pageURL)
	for _, node := range jsonLD(doc) {
		if !hasType(node, "SocialMediaPosting") && !(hasType(node, "Article") || hasType(node, "NewsArticle") || hasType(node, "BlogPosting")) {
			continue
		}
		if linkedIn && !hasType(node, "SocialMediaPosting") {
			continue
		}
		post.Title = stringValue(node["headline"])
		post.Body = stringValue(node["articleBody"])
		post.PublishedAt = parseTime(stringValue(node["datePublished"]))
		if author, ok := node["author"].(map[string]any); ok && linkedIn {
			post.Author = stringValue(author["name"])
		}
		// Read only the post's own counters, never comment likes or followers.
		for _, counter := range asObjects(node["interactionStatistic"]) {
			action := stringValue(counter["interactionType"])
			if object, ok := counter["interactionType"].(map[string]any); ok {
				action = stringValue(object["@type"])
			}
			count := numberValue(counter["userInteractionCount"])
			if strings.HasSuffix(action, "LikeAction") {
				post.Reactions = count
			} else if strings.HasSuffix(action, "CommentAction") {
				post.Comments = count
			}
		}
		if post.Comments == nil {
			post.Comments = numberValue(node["commentCount"])
		}
		break
	}
	if post.Title == "" {
		post.Title = model.CleanSpace(doc.Find("h1").First().Text())
	}
	if linkedIn {
		content := doc.Find(`[data-test-id="main-feed-activity-card__commentary"]`).First()
		if post.Body == "" {
			post.Body = selectionText(content)
		}
		if post.Author == "" {
			post.Author = model.CleanSpace(doc.Find(`[data-tracking-control-name$="feed-actor-name"]`).First().Text())
		}
		if raw, exists := doc.Find(`[data-num-reactions]`).First().Attr("data-num-reactions"); exists {
			post.Reactions = parseCount(raw)
		}
		if raw, exists := doc.Find(`[data-num-comments]`).First().Attr("data-num-comments"); exists {
			post.Comments = parseCount(raw)
		}
		post.Links = selectionLinks(content, pageURL)
		if strings.TrimSpace(post.Body) == "" {
			return model.Post{}, ErrNoPublicPost
		}
	} else {
		content := doc.Find("article .entry-content, article .post-content, .entry-content, [itemprop='articleBody'], .article-content").First()
		if content.Length() == 0 {
			content = doc.Find("article").First()
		}
		if content.Length() == 0 {
			content = doc.Find("main").First()
		}
		if content.Length() > 0 {
			content = content.Clone()
			content.Find("script,style,nav,aside,footer,form,button,.sharedaddy,.post-share,.social-share,.author-box,.related-posts,.comments,.comment-list,.newsletter,.advertisement").Remove()
			if text := selectionText(content); len(text) > len(post.Body) {
				post.Body = text
			}
			post.Links = selectionLinks(content, pageURL)
		}
		if post.Title == "" {
			post.Title, _ = doc.Find(`meta[property="og:title"]`).Attr("content")
		}
		if post.Body == "" {
			post.Body, _ = doc.Find(`meta[name="description"]`).Attr("content")
		}
		if post.PublishedAt == nil {
			value, _ := doc.Find(`meta[property="article:published_time"]`).Attr("content")
			post.PublishedAt = parseTime(value)
		}
		doc.Find(`a[rel="category tag"], .post-categories a, .post-cat a`).Each(func(_ int, s *goquery.Selection) {
			post.Categories = append(post.Categories, model.CleanSpace(s.Text()))
		})
	}
	if strings.TrimSpace(post.Body) == "" {
		return model.Post{}, errors.New("no readable article text was found on this page")
	}
	post.Title = html.UnescapeString(post.Title)
	return post, nil
}

func DiscoverLinkedIn(data []byte, pageURL string, limit int) ([]string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	result, seen := []string{}, map[string]bool{}
	add := func(raw string) {
		target := UnwrapLink(raw, pageURL)
		u, err := url.Parse(target)
		if err != nil || !IsLinkedIn(u.Hostname()) || !(strings.HasPrefix(u.Path, "/posts/") || strings.Contains(u.Path, "/feed/update/urn:li:activity:")) {
			return
		}
		target = CanonicalURL(target)
		id := LinkedInIdentity(target)
		if id == "" {
			id = target
		}
		if !seen[id] && len(result) < limit {
			seen[id], result = true, append(result, target)
		}
	}
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		raw, _ := s.Attr("href")
		add(raw)
	})
	for _, node := range jsonLD(doc) {
		if hasType(node, "SocialMediaPosting") {
			add(stringValue(node["@id"]))
			add(stringValue(node["url"]))
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no public post links are exposed on this page; add individual post URLs or use the publisher's RSS feed")
	}
	return result, nil
}

func documentFromString(raw string) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(strings.NewReader(raw))
}

func PlainHTML(raw string) string {
	doc, err := documentFromString(raw)
	if err != nil {
		return model.CleanSpace(html.UnescapeString(raw))
	}
	return selectionText(doc.Find("body"))
}

func selectionText(s *goquery.Selection) string {
	var text strings.Builder
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.TextNode {
			text.WriteString(n.Data)
			return
		}
		if n.Type == nethtml.ElementNode && model.Contains([]string{"script", "style", "noscript", "nav", "footer", "form", "button"}, n.Data) {
			return
		}
		block := n.Type == nethtml.ElementNode && model.Contains([]string{"p", "div", "section", "article", "li", "h1", "h2", "h3", "h4", "h5", "br", "ul", "ol"}, n.Data)
		if block {
			text.WriteString("\n")
		}
		if n.Type == nethtml.ElementNode && n.Data == "li" {
			text.WriteString("• ")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if block {
			text.WriteString("\n")
		}
	}
	for _, node := range s.Nodes {
		walk(node)
	}
	return normalizeLines(text.String())
}

func selectionLinks(content *goquery.Selection, base string) []model.Link {
	links := []model.Link{}
	content.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		raw, _ := s.Attr("href")
		if target := UnwrapLink(raw, base); target != "" {
			context := s.Closest("p,li").Text()
			links = append(links, model.Link{URL: target, Text: model.CleanSpace(s.Text()), Context: model.CleanSpace(context)})
		}
	})
	return links
}

func jsonLD(doc *goquery.Document) []map[string]any {
	var nodes []map[string]any
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, entry := range value {
				collect(entry)
			}
		case map[string]any:
			nodes = append(nodes, value)
			if graph, ok := value["@graph"]; ok {
				collect(graph)
			}
		}
	}
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		decoder := json.NewDecoder(strings.NewReader(s.Text()))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			collect(value)
		}
	})
	return nodes
}

func hasType(node map[string]any, kind string) bool {
	if stringValue(node["@type"]) == kind {
		return true
	}
	if types, ok := node["@type"].([]any); ok {
		for _, candidate := range types {
			if stringValue(candidate) == kind {
				return true
			}
		}
	}
	return false
}

func stringValue(v any) string { s, _ := v.(string); return s }

func asObjects(v any) []map[string]any {
	if object, ok := v.(map[string]any); ok {
		return []map[string]any{object}
	}
	var result []map[string]any
	if list, ok := v.([]any); ok {
		for _, item := range list {
			if object, ok := item.(map[string]any); ok {
				result = append(result, object)
			}
		}
	}
	return result
}

func numberValue(v any) *int64 {
	switch value := v.(type) {
	case json.Number:
		if n, err := value.Int64(); err == nil && n >= 0 {
			return &n
		}
	case string:
		return parseCount(value)
	}
	return nil
}

func parseCount(value string) *int64 {
	n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(value), ",", ""), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

func parseTime(value string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, "Mon, 2 Jan 2006 15:04:05 -0700", "2006-01-02"} {
		if d, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			d = d.UTC()
			return &d
		}
	}
	return nil
}
