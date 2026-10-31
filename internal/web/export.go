package web

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/YeandAmen/opportunity-radar/internal/model"
)

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	q := query(r)
	q.Limit, q.Offset = 10000, 0
	items, total, err := s.store.List(r.Context(), q)
	if err != nil {
		internalError(w)
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "" && format != "json" {
		fail(w, http.StatusBadRequest, "export format must be json or csv")
		return
	}
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="opportunities.csv"`)
		writer := csv.NewWriter(w)
		writer.Write([]string{"Title", "Type", "Organizer", "Deadline", "Deadline wording", "Status", "Region label", "Location", "Funding", "Eligibility", "Benefits", "Application URL", "Source post URL", "Publisher", "Reactions", "Comments", "Capture method", "Observed at"})
		for _, o := range items {
			row := []string{o.Title, o.Kind, o.Organizer, o.Deadline, o.DeadlineText, o.Status, o.Region, o.Location, o.Funding, strings.Join(o.Eligibility, " | "), strings.Join(o.Benefits, " | "), o.ApplicationURL, o.URL, o.SourceName, countString(o.Reactions), countString(o.Comments), o.CaptureMethod, o.LastFetchedAt.Format(time.RFC3339)}
			for i := range row {
				row[i] = safeCSV(row[i])
			}
			writer.Write(row)
		}
		writer.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="opportunities.json"`)
	json.NewEncoder(w).Encode(map[string]any{"generated_at": time.Now().UTC(), "total_matching": total, "exported": len(items), "items": items})
}

func safeCSV(value string) string {
	if trimmed := strings.TrimLeft(value, " \t\r\n"); trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func countString(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Published   string `xml:"pubDate"`
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	items, _, err := s.store.List(r.Context(), model.Query{Status: "active", Sort: "updated", Limit: 50})
	if err != nil {
		internalError(w)
		return
	}
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Version string   `xml:"version,attr"`
		Channel struct {
			Title       string    `xml:"title"`
			Description string    `xml:"description"`
			Link        string    `xml:"link"`
			Items       []rssItem `xml:"item"`
		} `xml:"channel"`
	}
	feed.Version, feed.Channel.Title = "2.0", "Opportunity Radar"
	feed.Channel.Description, feed.Channel.Link = "Programs, funding, and experiences for your next chapter.", "https://github.com/YeandAmen/opportunity-radar"
	for _, o := range items {
		deadline := o.Deadline
		if deadline == "" {
			deadline = o.DeadlineState
		}
		feed.Channel.Items = append(feed.Channel.Items, rssItem{Title: o.Title, Link: o.URL, GUID: o.URL, Description: o.Summary + "\nDeadline: " + deadline + "\nEligibility: " + strings.Join(o.Eligibility, "; "), Published: o.UpdatedAt.Format(time.RFC1123Z)})
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	xml.NewEncoder(w).Encode(feed)
}
