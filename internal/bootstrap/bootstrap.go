// Package bootstrap installs useful public sources and the user's reference
// example. The example is explicitly labeled as a dated reference import until
// the live crawler replaces it with a successful public-page observation.
package bootstrap

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

const ExampleURL = "https://www.linkedin.com/posts/uesdnkabom_nkabomcollaborative-businessincubation-activity-7520960460347768832-FWkQ"
const ExampleShareURL = "https://www.linkedin.com/posts/uesdnkabom_nkabomcollaborative-businessincubation-youthentrepreneurship-share-7520960459173359618-P2jG"

func SeedX(ctx context.Context, s *store.Store) error {
	query := ingest.XWatchlists["funding"]
	_, _, err := s.AddSource(ctx, model.Source{Name: "X · Funding & fellowships", URL: "https://x.com/search?q=" + url.QueryEscape(query), Kind: "x_search", Enabled: false, Status: "idle", LastError: "X discovery is prepared. Enable this source after X API access or credits are available."})
	return err
}

const ExampleBody = `CALL FOR APPLICATIONS: Business Incubation Programme for Young Entrepreneurs
Are you a young entrepreneur ready to grow your business?
Young people, especially those in the agri-food value chain, are invited to apply for the Nkabom Collaborative Business Incubation Programme to strengthen their entrepreneurial skills and turn their ideas into thriving enterprises.

Eligibility Criteria
1. Must be between 18-35 years of age
2. Applicants must have a clearly defined business idea, or a business plan, or a just started business, or an existing business in agro-waste processing, aquaculture, green entrepreneurship, circular economy or agribusiness.
3. Applicant must be willingness to participate fully in mentorship, incubation activities

🔗 Apply now: https://lnkd.in/dCwPpDfP
📅 Application deadline: 03 November 2026

Take the next step towards building a sustainable business!

#NkabomCollaborative #BusinessIncubation #YouthEntrepreneurship #AgriFood #UESD`

func Seed(ctx context.Context, s *store.Store) error {
	sources := []model.Source{
		{Name: "Nkabom Collaborative · UESD", URL: ExampleShareURL, Kind: "linkedin_post", Enabled: true},
		{Name: "Opportunity Desk", URL: "https://opportunitydesk.org/feed", Kind: "rss", Enabled: true},
		{Name: "Opportunities for Africans", URL: "https://www.opportunitiesforafricans.com/feed", Kind: "rss", Enabled: true},
	}
	var reference model.Source
	for i, source := range sources {
		added, _, err := s.AddSource(ctx, source)
		if err != nil {
			return err
		}
		if i == 0 {
			reference = added
		}
	}
	if err := SeedJobs(ctx, s); err != nil {
		return err
	}
	if _, err := s.Get(ctx, "linkedin-7520960460347768832"); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	published, _ := time.Parse(time.RFC3339Nano, "2026-10-27T21:31:46.434Z")
	captured, _ := time.Parse("2006-01-02", "2026-10-30")
	post := model.Post{
		URL: ExampleURL, Title: "Business Incubation Programme for Young Entrepreneurs", Body: ExampleBody,
		Author: "Nkabom Collaborative - UESD", PublishedAt: &published,
		Reactions: model.Int64(236), Comments: model.Int64(20), CaptureMethod: "reference_import",
	}
	opportunity := ingest.Extract(post, reference, captured)
	opportunity.FirstSeenAt = time.Now().UTC()
	_, err := s.Upsert(ctx, opportunity)
	return err
}
