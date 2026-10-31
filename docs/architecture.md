# Architecture

Opportunity Radar is a Go application with an embedded HTML/CSS/JavaScript dashboard and a pure-Go SQLite database. It runs as one binary; Node.js is used only for development browser checks and screenshots.

```text
RSS / Atom ──────┐
LinkedIn posts ──┤
Public pages ────┼─> HTTP fetcher ─> source parser ─> conservative extraction
Pasted text ─────┘                                         │
                                                          v
                               SQLite: opportunities + source state + observations
                                                          │
                                                          v
                                Go HTTP API + dashboard + CSV / JSON + RSS digest
```

## Packages

- `cmd/radar`: commands, configuration, scheduling, process lifecycle.
- `internal/model`: opportunity, source, observation, and crawl-run records.
- `internal/ingest`: URL normalization, safe HTTP fetching, JSON-LD/HTML and RSS/Atom parsing, and evidence-backed extraction.
- `internal/crawler`: source coordination, conditional feed requests, bounded discovery, and refresh/backoff logic.
- `internal/store`: SQLite persistence, filtering, deduplication, and update histories.
- `internal/bootstrap`: starter sources and the dated reference example.
- `internal/web`: HTTP endpoints and the embedded dashboard.
- `tests/browser`: isolated desktop/mobile functional and accessibility checks.

## Source pipeline

1. Sources are selected from enabled records that are due for a refresh. A manual refresh can bypass the due time.
2. Up to two sources run concurrently. Requests to the same host are separated by two seconds by default.
3. Feed entries are discovered from RSS/Atom. Relevant entries are expanded by fetching the publisher's article. A public LinkedIn page is scanned for the individual post links it exposes.
4. LinkedIn post JSON-LD provides post text, author, publication time, and top-level interaction counters. HTML attributes provide a fallback. Nested comment reactions and author follower counts are not used as post engagement.
5. Extraction records type, deadline, eligibility, benefits, direct application link, topic tags, and supporting source wording. Missing details remain missing.
6. Canonical post URLs and LinkedIn activity identities are upserted into SQLite. Known individual-post source URLs also resolve their canonical aliases for text imports.
7. Every successful capture gets an observation. Changed deadlines, text, links, and engagement counts get explicit change labels and numeric deltas when both counts are known.
8. Source status and a crawl report are persisted. Failed access does not replace a previously captured opportunity. A failed article fetch can provide an explicitly labeled RSS summary for a new opportunity.

## Dates and confidence

Named dates with a year and ISO dates are parsed only in deadline context. Ambiguous numeric dates and dates missing a year remain unconfirmed. A rolling deadline must be explicitly stated.

Date-only deadlines are compared against the current UTC calendar date. The closing day stays available for that day. Source timezone and cutoff time may be more specific; the original announcement remains authoritative.

Region labels come from publisher categories or explicit worldwide wording. They support discovery and do not substitute for country eligibility. Profile country domains and commenter statements are not used to infer eligibility or location.

Extraction is deterministic and requires no LLM key. The extracted record includes the original body, capture method, observation time, and field evidence.

## Persistence and refreshes

The database uses WAL and a busy timeout. A single connection serializes local read-modify-write operations. Older observations cannot overwrite newer records. Source configuration updates are merged with crawler metadata so a pause or rename made during a crawl is preserved.

RSS ETags and modification timestamps are persisted. A scheduled 304 skips re-reading unchanged feed entries. A partially expanded feed is retried without a cached ETag. An explicit manual refresh bypasses feed caching so publisher article edits can be captured immediately. Individual LinkedIn post sources refresh their engagement independently of RSS.

Access failures back off exponentially, capped at 24 hours. Blocked responses use at least six hours of backoff. A user-requested refresh is explicit and can retry earlier. Shutdown cancels requests, drains the active run, and saves its interrupted state.

## Deployment model

One process owns the scheduler for each database. Run one replica with a persistent disk for this version. Browser shortlists live in local storage, scoped to the dashboard's origin; the management token is held only in the browser tab's session storage.

The server listens on localhost by default. Listening on other interfaces without a management token gives a read-only dashboard. With a token configured, mutating API calls use bearer authentication. Browser writes are same-origin, and fetched destinations are checked against private/local IP ranges both before requests and while resolving redirects.

## Coverage

Coverage is the content exposed by tracked public sources, within the configured entry/link limit. It is not a global index of every LinkedIn post. A public profile/company page may expose no post links or return an access block even when an individual post is readable. Pasted post text is the supported import path for unavailable pages, with its provenance clearly labeled.
