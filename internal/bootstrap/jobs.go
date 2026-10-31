package bootstrap

import (
	"context"

	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

// JobSources are publisher endpoints verified to return job listings or real
// feed XML. API credentials are not required by these starter sources.
func JobSources() []model.Source {
	return []model.Source{
		{Name: "Arbeitnow · European jobs", URL: "https://www.arbeitnow.com/api/job-board-api", Kind: "jobs_arbeitnow", Enabled: true, IntervalMinutes: 15},
		{Name: "Arbeitnow · UK jobs", URL: "https://www.arbeitnow.co.uk/api/job-board-api", Kind: "jobs_arbeitnow", Enabled: true, IntervalMinutes: 15},
		{Name: "LinkedIn · Software jobs in Ghana", URL: "https://www.linkedin.com/jobs/search?keywords=software&location=Ghana&sortBy=DD", Kind: "linkedin_jobs", Enabled: true, IntervalMinutes: 15},
		{Name: "University of Bath · Careers", URL: "https://www.bath.ac.uk/jobs/RSS/rss.aspx", Kind: "rss_jobs", Enabled: true, IntervalMinutes: 10},
		{Name: "Lancaster University · Careers", URL: "https://hr-jobs.lancs.ac.uk/RSS/rss.aspx", Kind: "rss_jobs", Enabled: true, IntervalMinutes: 10},
		{Name: "Acted · Humanitarian jobs", URL: "https://www.acted.org/en/jobs/feed", Kind: "rss_jobs", Enabled: true, IntervalMinutes: 10},
		{Name: "fundsforNGOs · Funding calls", URL: "https://www2.fundsforngos.org/feed", Kind: "rss", Enabled: true, IntervalMinutes: 30},
	}
}

func SeedJobs(ctx context.Context, s *store.Store) error {
	for _, source := range JobSources() {
		source.URL = ingest.CanonicalURL(source.URL)
		if _, _, err := s.AddSource(ctx, source); err != nil {
			return err
		}
	}
	return nil
}
