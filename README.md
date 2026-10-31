# Opportunity Radar

**Good opportunities. Less noise.**

**Live app: [opportunity-radar-yeandamen.vercel.app](https://opportunity-radar-yeandamen.vercel.app)**

A Go-powered crawler and a calm, human-friendly dashboard for programs, scholarships, fellowships, grants, internships, competitions, and other opportunities around the world.

[![CI](https://github.com/YeandAmen/opportunity-radar/actions/workflows/ci.yml/badge.svg)](https://github.com/YeandAmen/opportunity-radar/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

![Opportunity Radar dashboard](docs/dashboard-desktop.png)

## What it does

- **Discover and refresh:** public LinkedIn posts, exposed post links on public LinkedIn pages, RSS/Atom feeds, and individual opportunity articles.
- **Make announcements actionable:** type, deadline, eligibility, benefits, application link, location when stated, discovery region, and topic tags.
- **Keep the evidence:** original announcement, source URL, capture time, extraction evidence, and a clear indication of missing details.
- **Track updates and engagement:** content/deadline changes, reaction/comment counts when exposed, and observation histories with count deltas.
- **Help people shortlist:** search, type/region/status filters, deadline-first and popularity sorting, grid/list layouts, saved opportunities, and shareable detail links.
- **Make the feed useful elsewhere:** filtered CSV/JSON exports and an RSS digest.
- **Run simply:** one binary, embedded assets, pure-Go SQLite, built-in scheduling, and a non-root Docker image.

The first version is source-based: it captures accessible content from the publishers you track. LinkedIn does not expose a complete public feed of all posts. A blocked source is visible in **Sources & updates**; previously captured details stay available.

## Quick start

Go 1.26 or newer is required.

```sh
git clone https://github.com/YeandAmen/opportunity-radar.git
cd opportunity-radar
go run ./cmd/radar serve
```

Open **http://127.0.0.1:8080**.

On a new database, Radar installs three starter sources and begins a public-source refresh:

1. The supplied **Nkabom Collaborative – UESD** business-incubation post on LinkedIn.
2. **Opportunity Desk**, a global opportunity feed.
3. **Opportunities for Africans**, which includes international programs relevant to African applicants.

The Nkabom example initially appears as a **reference snapshot captured on 30 October 2026**. A successful live refresh replaces it with current public-page data. Its stated deadline is **3 November 2026**, and its stated age range is **18–35**. Its location and country restrictions are left unconfirmed because they are not stated in the post.

The application creates `data/radar.db`. Your database, local environment files, and generated test artifacts are ignored by Git.

### Build a standalone binary

```sh
make build
./dist/opportunity-radar serve
```

No Node.js installation, database server, LLM key, or LinkedIn login is needed to run the application.

## Using your radar

### Add an opportunity

Click **Add opportunity** and enter an individual LinkedIn post or opportunity-article URL. Radar fetches the readable public announcement and extracts its fields.

If the page is unavailable, expand **Have the post text?** and paste the announcement. The result is marked **Text import**. Include the eligibility, application link, and complete deadline in the pasted text for the clearest result.

### Track a source

Open **Sources & updates → Add source**. Give it a name and a URL, and choose or automatically detect its type:

| Source                               | Discovery and refresh behavior                                          |
| ------------------------------------ | ----------------------------------------------------------------------- |
| RSS / Atom feed                      | Discover returned entries and expand their publisher articles.          |
| Public LinkedIn company/profile page | Discover the individual post links visible on the returned public page. |
| Individual LinkedIn post             | Refresh the announcement and publicly exposed engagement counts.        |
| Opportunity article                  | Refresh one publisher announcement.                                     |

Sources can be paused. Pausing preserves existing opportunities and their histories.

The default schedule is every **30 minutes**, with **20 feed entries/page links per source**, **two concurrent sources**, and at least **two seconds between requests to the same host**. Failed access uses backoff; the source panel shows the next check time. **Refresh sources** explicitly retries enabled sources.

### Shortlist and share

Use a card's bookmark button to save it. **Saved opportunities** is personal to that browser and origin. Copy a detail link to share an opportunity from a hosted instance.

Press **/** to focus search. Use **Export** for the current filters, or subscribe to `/feed.xml` for an opportunity digest.

## Commands

```sh
# Serve the dashboard with scheduled refreshes
go run ./cmd/radar serve

# Refresh enabled sources once and print the crawl report
go run ./cmd/radar crawl

# Refresh a specific source ID from /api/sources
go run ./cmd/radar crawl -source source-ID

# Import a public post/article
go run ./cmd/radar import -url "https://www.linkedin.com/posts/..."

# Import supplied text with its original source URL
go run ./cmd/radar import -url "https://www.linkedin.com/posts/..." \
  -text-file announcement.txt -organizer "Program host"

# Print structured records or CSV
go run ./cmd/radar export -format json
go run ./cmd/radar export -format csv

# Start an empty instance with manual refreshes
go run ./cmd/radar serve -seed=false -auto-crawl=false
```

`-seed=false` suppresses starter-source installation; it preserves records already in the selected database. The `crawl` command reports partial failures in JSON and exits unsuccessfully if the run fails entirely or is interrupted.

## Configuration

| Environment variable  | Default          | Purpose                                                |
| --------------------- | ---------------- | ------------------------------------------------------ |
| `RADAR_ADDR`          | `127.0.0.1:8080` | Listening address.                                     |
| `RADAR_DB`            | `data/radar.db`  | SQLite database file.                                  |
| `RADAR_INTERVAL`      | `30m`            | Scheduled refresh interval; `0` disables the timer.    |
| `RADAR_AUTO_CRAWL`    | `true`           | Startup and scheduled crawling.                        |
| `RADAR_REQUEST_DELAY` | `2s`             | Minimum same-host request spacing.                     |
| `RADAR_HTTP_TIMEOUT`  | `25s`            | Timeout for each source request.                       |
| `RADAR_ADMIN_TOKEN`   | Empty            | Bearer token for management endpoints when configured. |

The corresponding flags are `-addr`, `-db`, `-interval`, `-auto-crawl`, `-request-delay`, and `-http-timeout`. `-max-items` sets the per-source entry/link limit from 1 to 50. Use `go run ./cmd/radar serve -h` for command help.

Environment values are read from the process environment. `.env.example` documents them; Go does not automatically load `.env` files. Docker Compose reads `.env` for substitution.

### Hosting and management access

The live deployment runs the Go HTTP server on Vercel with a dedicated Neon PostgreSQL database. Its data, source state, and observation histories persist across function instances and redeployments.

`cmd/server/main.go` is the Vercel entrypoint. It uses `PORT`, `DATABASE_URL`, `RADAR_ADMIN_TOKEN`, and `CRON_SECRET`. Hosted refreshes complete inside the request, and a shared database lease prevents overlapping crawls across instances. Neon connections use its secure WebSocket PostgreSQL transport.

The **Refresh hosted opportunities** GitHub workflow invokes the protected `/api/cron` endpoint every 30 minutes. This provides regular updates on Vercel Hobby without depending on a process-local timer. Configure the repository variable `RADAR_PRODUCTION_URL` and secret `RADAR_CRON_SECRET`; the latter must match Vercel's `CRON_SECRET`.

To migrate an existing SQLite radar into a fresh hosted database:

```sh
go run ./cmd/radar migrate -from data/radar.db -env-file /private/path/production.env
```

The migration copies source settings, opportunities, full observation history, and run reports. It refuses to overwrite a nonempty destination. PostgreSQL data is isolated in the `opportunity_radar` schema.

For owner setup, `scripts/setup-cloud.cjs` provisions app-scoped management/cron secrets through the authenticated Vercel CLI and saves a private owner credential file. Secret values are not printed or committed.

For a hosted instance, provide a management token and a persistent database path:

```sh
RADAR_ADDR=0.0.0.0:8080 RADAR_ADMIN_TOKEN="your-management-token" \
  ./dist/opportunity-radar serve -db /data/radar.db
```

The dashboard remains readable without the token. Its management controls prompt for the token before importing or changing sources. A non-local listening address without a token serves a read-only dashboard. Use one application replica per database and put the hosted service behind HTTPS.

### Docker

```sh
RADAR_ADMIN_TOKEN="your-management-token" docker compose up --build -d
```

Compose binds the host port to localhost and persists SQLite in the `radar-data` volume. The container runs as a non-root user. Set your desired token in the environment or a local `.env` file. With no token, the container's management API is read-only.

## API

| Method | Endpoint                  | Result                                                   |
| ------ | ------------------------- | -------------------------------------------------------- |
| GET    | `/api/meta`               | Counts, facets, latest run, and management capabilities. |
| GET    | `/api/opportunities`      | Filtered and paginated opportunities.                    |
| GET    | `/api/opportunities/{id}` | Full record and observation history.                     |
| GET    | `/api/sources`            | Source configuration and fetch status.                   |
| POST   | `/api/sources`            | Add a tracked source.                                    |
| PATCH  | `/api/sources/{id}`       | Pause/resume or rename a source.                         |
| POST   | `/api/import`             | Import a URL or supplied announcement text.              |
| POST   | `/api/crawl`              | Start a background refresh, optionally by `source_id`.   |
| POST   | `/api/unlock`             | Validate the configured management token.                |
| GET    | `/api/runs`               | Recent crawl reports.                                    |
| GET    | `/api/cron`               | Authenticated hosted refresh of sources that are due.    |
| GET    | `/api/export?format=csv`  | CSV export; `json` is also supported.                    |
| GET    | `/feed.xml`               | Recent non-closed opportunities as RSS.                  |
| GET    | `/healthz`                | Database-backed health response.                         |

Opportunity queries accept `q`, `kind` (comma-separated types), `region`, `status`, `source`, `sort`, `limit`, `offset`, and `ids` (up to 200 shortlist IDs). The list limit is at most 200; exports include at most 10,000 matching records and JSON reports the number exported.

Status values are `active` (not closed), `closing`, `open`, `rolling`, `unknown`, `closed`, and `all`. The default `active` feed includes unconfirmed deadlines, clearly labeled **Unconfirmed**. Dates are compared against the current UTC calendar day. Sort values are `deadline`, `newest`, `popular`, and `updated`.

An import request looks like:

```json
{
  "url": "https://www.linkedin.com/posts/...",
  "text": "Optional full announcement text",
  "organizer": "Optional program host",
  "title": "Optional title override"
}
```

When configured, writes require `Authorization: Bearer <RADAR_ADMIN_TOKEN>`. Tokens are not included in source exports. Unknown engagement counts are `null`, rather than zero.

## Checks

```sh
go test -race ./...
go vet ./...

# Development-only desktop/mobile browser checks
npm ci
npx playwright install chromium
npm run test:e2e
```

The browser suite uses an isolated, in-memory Go server and synthetic test announcements. It checks discovery, filters, persistence of saved opportunities, imports, token access, source pausing, filtered downloads, responsive layout, keyboard search, and automated WCAG A/AA checks on discovery. It does not depend on live LinkedIn access.

GitHub Actions runs both Go checks and desktop/mobile browser tests.

For implementation details and precise coverage behavior, see [Architecture](docs/architecture.md).

## License

[MIT](LICENSE). Linked source announcements retain their original attribution.
