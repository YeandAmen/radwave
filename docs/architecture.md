# Architecture

Radwave is a Go application with an embedded HTML/CSS/JavaScript dashboard. Standalone deployments use pure-Go SQLite; the Vercel deployment uses dedicated Neon PostgreSQL storage. It runs as one binary; Node.js is used only for development checks and deployment utilities.

```text
RSS / Atom ──────┐
LinkedIn posts/jobs ┼─> HTTP fetcher ─> source parser ─┐
Public pages ────┘                                 │
Job provider APIs ─> fixed-host JSON adapter ────────┤
X search / profiles / posts ─> X API adapter ───────┼─> shared extraction
Pasted text / legacy X archive ────────────────────┘          │
                                                             v
                             SQLite or PostgreSQL: opportunities + sources + observations
                                                             │
                                                             v
                              Go HTTP API + dashboard + CSV / JSON + RSS digest
```

## Packages

- `cmd/radar`: commands, configuration, scheduling, process lifecycle.
- `internal/model`: opportunity, source, observation, and crawl-run records.
- `internal/ingest`: URL normalization, safe HTTP fetching, JSON-LD/HTML and RSS/Atom parsing, and evidence-backed extraction.
- `internal/ingest/x.go`: fixed-host X API access, watchlist/profile pagination, long-post text, expanded links, and public engagement counters.
- `internal/ingest/jobs.go`: job-provider requests/authentication and normalization into the shared `model.Post` schema.
- `internal/ingest/job_page.go`: public `JobPosting` JSON-LD, LinkedIn job descriptions, and listing discovery.
- `internal/catalog`: complete RoleBase/Kaggle CSV parsing, compact immutable search index, daily API updates, and normalized result merging.
- `internal/legacy`: imports the original Twitter scraper's opportunity records into Radwave's schema.
- `internal/crawler`: source coordination, conditional feed requests, bounded discovery, and refresh/backoff logic.
- `internal/store`: SQLite/PostgreSQL persistence, filtering, deduplication, and update histories.
- `internal/bootstrap`: starter sources and the dated reference example.
- `internal/web`: HTTP endpoints and the embedded dashboard.
- `internal/web/assets/globe.js`: region exploration with a self-hosted MapLibre renderer and Natural Earth geometry.
- `internal/deployment`: Vercel runtime configuration with shared storage and request-scoped refreshes.
- `tests/browser`: isolated desktop/mobile functional and accessibility checks.

## Source pipeline

Twitter/X is an additional adapter in this pipeline. Search watchlists and profile timelines keep pagination/high-water checkpoints in source state; individual posts and batched captured posts refresh metrics. X API rate limits and access failures produce persisted status/backoff rather than blocking sleeps. The frontend filters a single opportunity store by platform, and all sources share the same crawl lease, extraction rules, histories, and exports.

Job APIs implement `JobReader` and return the same post records as HTML/RSS adapters. `rss_jobs` adds explicit job-board context rather than relying on scholarship-style keywords. Lightweight API sources are prioritized ahead of article expansion, and source-specific cadences are bounded by provider minimums. Credentials remain server-side; stored URLs contain only public filters. XML feed parsing supports declared legacy encodings used by university career systems.

The Kaggle catalogue is retained as a compressed, chunked snapshot with an atomic manifest. A cached Go index searches all rows and normalizes only requested results; it merges with live records without replacing their bodies. Owner-authenticated uploads are resumable and checksum-validated. Closed-at, posted, first-seen and snapshot dates remain distinct, and inferred classifications are attributed to the dataset. A daily source refresh checks the dataset version before downloading it.

1. Sources are selected from enabled records that are due for a refresh. A manual refresh can bypass the due time.
2. Up to two sources run concurrently. Requests to the same host are separated by two seconds by default.
3. Feed entries are discovered from RSS/Atom. Relevant entries are expanded by fetching the publisher's article. A public LinkedIn page is scanned for the individual post links it exposes.
4. LinkedIn post JSON-LD provides post text, author, publication time, and top-level interaction counters. HTML attributes provide a fallback. Nested comment reactions and author follower counts are not used as post engagement.
5. Extraction records type, deadline, eligibility, benefits, direct application link, topic tags, and supporting source wording. Missing details remain missing.
6. Canonical post URLs, LinkedIn activity identities, and X post identities are upserted into the shared database. Known individual-post source URLs also resolve their canonical aliases for text imports.
7. Every successful capture gets an observation. Changed deadlines, text, links, and engagement counts get explicit change labels and numeric deltas when both counts are known.
8. Source status and a crawl report are persisted. Failed access does not replace a previously captured opportunity. A failed article fetch can provide an explicitly labeled RSS summary for a new opportunity.

## Dates and confidence

Named dates with a year and ISO dates are parsed only in deadline context. Ambiguous numeric dates and dates missing a year remain unconfirmed. A rolling deadline must be explicitly stated.

Date-only deadlines are compared against the current UTC calendar date. The closing day stays available for that day. Source timezone and cutoff time may be more specific; the original announcement remains authoritative.

Region labels come from publisher categories or explicit worldwide wording. They support discovery and do not substitute for country eligibility. Profile country domains and commenter statements are not used to infer eligibility or location.

Extraction is deterministic and requires no LLM key. The extracted record includes the original body, capture method, observation time, and field evidence.

Legacy X archives preserve `scraped_at` as the capture time, not a claimed publication date. A historical capture cannot overwrite a newer live capture. Pasted X posts create paused sources, so importing text alone does not enable API polling.

## Persistence and refreshes

The database uses WAL and a busy timeout. A single connection serializes local read-modify-write operations. Older observations cannot overwrite newer records. Source configuration updates are merged with crawler metadata so a pause or rename made during a crawl is preserved.

RSS ETags and modification timestamps are persisted. A scheduled 304 skips re-reading unchanged feed entries. A partially expanded feed is retried without a cached ETag. An explicit manual refresh bypasses feed caching so publisher article edits can be captured immediately. Individual LinkedIn post sources refresh their engagement independently of RSS.

Access failures back off exponentially, capped at 24 hours. Blocked responses use at least six hours of backoff. A user-requested refresh is explicit and can retry earlier. Shutdown cancels requests, drains the active run, and saves its interrupted state.

## Deployment model

Standalone instances own a local scheduler that checks for due work at least every five minutes when enabled. Vercel instances run refreshes inside authenticated HTTP requests, triggered every five minutes by GitHub Actions, subject to scheduled-dispatch delays. Each source's cadence and backoff determine when it is actually fetched. A shared crawl lease with renewal coordinates instances; its expiry allows recovery after an interrupted invocation. Browser shortlists live in local storage, scoped to the dashboard's origin; the management token is held only in the browser tab's session storage.

The PostgreSQL adapter namespaces tables in `opportunity_radar`, translates parameter bindings and JSON lookups, and uses row locks for updates. Transaction-level advisory locking serializes schema migration across cold starts. This works with Neon's transaction pooler; no connection-local search path is required. Secure WebSockets carry PostgreSQL traffic over HTTPS, including on networks where direct PostgreSQL ports are unavailable.

The server listens on localhost by default. Listening on other interfaces without a management token gives a read-only dashboard. With a token configured, mutating API calls use bearer authentication. Browser writes are same-origin, and fetched destinations are checked against private/local IP ranges both before requests and while resolving redirects.

## Coverage

Coverage is the content exposed by tracked public sources, within the configured entry/link limit. It is not a global index of every LinkedIn post. A public profile/company page may expose no post links or return an access block even when an individual post is readable. Pasted post text is the supported import path for unavailable pages, with its provenance clearly labeled.
