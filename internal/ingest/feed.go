package ingest

import (
	"bytes"
	"encoding/xml"
	"errors"
	"html"
	"strings"

	"golang.org/x/net/html/charset"

	"github.com/YeandAmen/radwave/internal/model"
)

type feedItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Description string   `xml:"description"`
	Encoded     string   `xml:"encoded"`
	Published   string   `xml:"pubDate"`
	Categories  []string `xml:"category"`
}

type atomEntry struct {
	Title     string `xml:"title"`
	Summary   string `xml:"summary"`
	Content   string `xml:"content"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	Links     []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
}

func ParseFeed(data []byte, feedURL string, limit int) ([]model.Post, error) {
	var document struct {
		XMLName xml.Name
		Channel struct {
			Items []feedItem `xml:"item"`
		} `xml:"channel"`
		Entries []atomEntry `xml:"entry"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("the source did not return a readable RSS or Atom feed: " + err.Error())
	}
	if document.XMLName.Local != "rss" && document.XMLName.Local != "feed" {
		return nil, errors.New("expected RSS or Atom XML; this source returned a different document")
	}
	posts := []model.Post{}
	for _, item := range document.Channel.Items {
		rawURL := item.Link
		if rawURL == "" {
			rawURL = item.GUID
		}
		target := UnwrapLink(rawURL, feedURL)
		if _, err := ValidateURL(target); err != nil {
			continue
		}
		body := item.Encoded
		if body == "" {
			body = item.Description
		}
		posts = append(posts, model.Post{URL: CanonicalURL(target), Title: html.UnescapeString(item.Title), Body: cleanFeedText(PlainHTML(body)), Links: linksFromHTML(body, feedURL), Categories: item.Categories, PublishedAt: parseTime(item.Published), CaptureMethod: "rss_summary"})
		if len(posts) >= limit {
			break
		}
	}
	for _, item := range document.Entries {
		if len(posts) >= limit {
			break
		}
		rawURL := ""
		for _, link := range item.Links {
			if link.Rel == "alternate" || link.Rel == "" {
				rawURL = link.Href
				break
			}
		}
		target := UnwrapLink(rawURL, feedURL)
		if _, err := ValidateURL(target); err != nil {
			continue
		}
		body := item.Content
		if body == "" {
			body = item.Summary
		}
		published := parseTime(item.Published)
		if published == nil {
			published = parseTime(item.Updated)
		}
		categories := []string{}
		for _, category := range item.Categories {
			categories = append(categories, category.Term)
		}
		posts = append(posts, model.Post{URL: CanonicalURL(target), Title: html.UnescapeString(item.Title), Body: cleanFeedText(PlainHTML(body)), Links: linksFromHTML(body, feedURL), Categories: categories, PublishedAt: published, CaptureMethod: "rss_summary"})
	}
	return posts, nil
}

func linksFromHTML(body, base string) []model.Link {
	doc, err := documentFromString(body)
	if err != nil {
		return []model.Link{}
	}
	return selectionLinks(doc.Find("body"), base)
}

func cleanFeedText(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "The post ") && strings.Contains(line, "appeared first on") {
			lines = lines[:i]
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
