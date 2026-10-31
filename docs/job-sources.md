# Job and opportunity sources

Radwave normalizes job APIs, public LinkedIn job pages, and job-board RSS into the same opportunity store, source manager, histories, search, shortlist, and exports.

## Live starter coverage

These publisher endpoints were checked on **31 October 2026**. Starter installation is idempotent and preserves existing pause/name settings.

| Source                                                                                                             | Transport                                  | Default cadence |
| ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------ | --------------- |
| [Arbeitnow Europe](https://www.arbeitnow.com/api/job-board-api)                                                    | Keyless JSON, employer/ATS listings        | 15 minutes      |
| [Arbeitnow UK](https://www.arbeitnow.co.uk/api/job-board-api)                                                      | Keyless JSON, employer/ATS listings        | 15 minutes      |
| [LinkedIn software jobs in Ghana](https://www.linkedin.com/jobs/search?keywords=software&location=Ghana&sortBy=DD) | Public job search and individual job pages | 15 minutes      |
| [University of Bath careers](https://www.bath.ac.uk/jobs/RSS/rss.aspx)                                             | Job RSS                                    | 10 minutes      |
| [Lancaster University careers](https://hr-jobs.lancs.ac.uk/RSS/rss.aspx)                                           | Job RSS                                    | 10 minutes      |
| [Acted humanitarian roles](https://www.acted.org/en/jobs/feed/)                                                    | NGO job RSS                                | 10 minutes      |
| [fundsforNGOs funding calls](https://www2.fundsforngos.org/feed/)                                                  | Opportunity RSS                            | 30 minutes      |

Open **Sources → Add a job source** for API setup, or **Verified job & funding feeds** in the source form to select a publisher. Individual LinkedIn job URLs can also be added through **Add a link**; pasted job descriptions keep text-import provenance.

Job-board RSS is a distinct source type: role titles such as “Senior Systems Architect” are accepted without requiring scholarship or application-announcement keywords. Public LinkedIn searches retain their keywords/location filters and deduplicate job URL aliases by listing ID.

## Additional API connectors

API keys stay in server environment variables. Source URLs contain only the provider endpoint and public search filters.

| Provider                                                     | Required environment               | Source endpoint                                  | Default minimum cadence |
| ------------------------------------------------------------ | ---------------------------------- | ------------------------------------------------ | ----------------------- |
| [USAJobs](https://developer.usajobs.gov/general/quick-start) | `USAJOBS_API_KEY`, `USAJOBS_EMAIL` | `https://data.usajobs.gov/api/search`            | 10 minutes              |
| [Reed](https://www.reed.co.uk/developers/jobseeker)          | `REED_API_KEY`                     | `https://www.reed.co.uk/api/1.0/search`          | 15 minutes              |
| [Jooble](https://jooble.org/api/about)                       | `JOOBLE_API_KEY`                   | `https://jooble.org/api`                         | 15 minutes              |
| [Adzuna](https://developer.adzuna.com/overview)              | `ADZUNA_APP_ID`, `ADZUNA_APP_KEY`  | `https://api.adzuna.com/v1/api/jobs/gb/search/1` | 30 minutes              |

The source form builds keywords/location filters and Adzuna country-market URLs. USAJobs uses its required contact-email and authorization headers; Reed uses Basic authentication with an empty password; Jooble sends JSON search parameters to its authenticated endpoint. Missing credentials and API failures are visible in source status, with backoff and preserved captures.

“Free API” is not a blanket commercial-use licence. [Adzuna's current terms](https://developer.adzuna.com/docs/terms_of_service) grant free evaluation access and require a licence for ongoing revenue-generating use after the trial. Provider attribution remains visible on job cards, and Adzuna/other summary-only APIs link users to the provider's original listing. Confirm each provider's current account limits and access when connecting credentials.

USAJobs announcements retain their stated hiring eligibility; a federal role is not automatically available to applicants worldwide. Public LinkedIn pages can return access blocks, which are reported through the existing source-status/backoff mechanism.

## Freshness and capture quality

- The hosted refresh workflow checks **every five minutes** and fetches only enabled sources that are due. GitHub scheduled runs are best-effort and may be delayed.
- Source-specific cadences and provider minimums determine the next fetch. Other starter LinkedIn/RSS sources retain their existing cadence.
- Job APIs are prioritized ahead of slower article expansion. Each refresh captures up to 20 API results; public LinkedIn job searches and job RSS expand up to 10 listings to keep requests bounded.
- Rich job RSS descriptions are captured directly, retaining their salary/closing-date evidence without an unnecessary portal request. Career-portal `ref` identifiers and feed listing titles are preserved.
- Jobs default to **newest-first** in the dashboard. Publication time and first capture are distinct; an API update timestamp is not invented as a publication date.
- Salaries, employment type, remote flags, employer, and location are recorded when supplied. Missing values stay unknown; estimated salaries are labeled.
- Schema.org `validThrough` is labeled **Listing valid through**, rather than presented as an application deadline. USAJobs' application close date remains an application deadline.
- API snippets are labeled **Job API summary**. Full descriptions and requirements remain available through the original source link.
- CSV/JSON exports use the same filters and include job provider, salary, employment type, and nullable remote information.

## Adapter structure

`internal/ingest/jobs.go` implements `JobReader` (`Configured`, `Read`) and fixed-provider JSON requests. It returns the same `model.Post` used by public-page and RSS ingestion. `internal/crawler/engine.go` coordinates those posts through shared extraction, upserts, histories, leases, scheduling, and backoff.

`internal/ingest/job_page.go` reads public `JobPosting` JSON-LD and LinkedIn's readable job-description fallback. `rss_jobs` reuses `ParseFeed` and the normal publisher-article expansion path. Job fields are optional additions to the shared opportunity schema, so older captures remain readable.
