# RoleBase / Kaggle job catalogue

Radwave uses [Chandransh Binjola's job-listings dataset](https://www.kaggle.com/datasets/chandranshbinjola/job-listings) as a complete daily catalogue alongside live job APIs, LinkedIn and career feeds.

The 30 October 2026 archive contains 1,075,463 rows. Normalization removes one duplicate URL identity: 965,376 records were open in that snapshot and 110,086 had a recorded closure. It includes 144,597 entry-level-friendly and 60,258 remote records. These are dataset observations, not confirmation that an employer is accepting applications at this moment.

## Storage and search

The full ZIP is retained in bounded database chunks (about 40 MB for this version), with a manifest switched atomically after validation. Job rows are searched through a compact, cached Go index. Only requested results are normalized into the existing opportunity response shape; the import does not create a million duplicated live rows or observation histories.

Stable identities use canonical application URLs. Live records sharing those URLs retain their current descriptions and take precedence in the combined view. Search supports career field, seniority, exact normalized skills, remote flags, entry-level-friendly flags, maximum stated experience, and dataset risk signals. Pagination searches the full catalogue.

Closed-at dates mark listings closed; they are not application deadlines. Original `posted`, `first_seen`, snapshot date, source ATS, pay, openings, team and skills are preserved. Dataset classifications and risk scores are attributed as inferred/heuristic. Original job descriptions are not included in the published dataset, so Radwave displays extracted facts and the employer's application link.

`jobs_by_company.csv`, `open_jobs_daily.csv` and `skill_demand_daily.csv` supply attributed employer, daily-volume and skill-demand summaries available through `GET /api/catalog`.

## Import a downloaded archive

```sh
go run ./cmd/radar inspect-kaggle -from /path/to/archive.zip
go run ./cmd/radar import-kaggle -from /path/to/archive.zip \
  -env-file /private/path/production.env
```

For hosting, the resumable owner-authenticated upload avoids local database-connectivity constraints:

```sh
node scripts/upload-kaggle.cjs https://radwave.vercel.app \
  /path/to/archive.zip /private/path/hosting-owner.env
```

The uploader checks stored chunk sizes, transfers missing chunks concurrently, verifies the SHA-256 checksum, validates CSV headers/rows, and commits the manifest. An incomplete upload or invalid/older archive leaves the current catalogue intact.

## Daily API refresh

Set `KAGGLE_API_TOKEN` only in the server environment. The existing scheduler checks due sources; the catalogue uses a 24-hour cadence. Dataset metadata is checked first, and unchanged versions reuse the stored snapshot. Downloads use the pinned dataset version and keep credentials out of source URLs and responses.

`scripts/kaggle-credentials.cjs` supports private credential status, authentication check, Vercel provisioning, and credential-safe local import. Database files, downloaded archives and private token files are ignored by Git.

API examples:

```text
/api/opportunities?platform=kaggle&field=security&remote=true&fresher=true&sort=newest
/api/opportunities?level=junior&skill=python&max_years=2&max_risk=1
/api/opportunities?platform=kaggle&status=closed
/api/export?format=csv&platform=kaggle&field=healthcare
```

Exports include the selected dataset fields and remain capped at 10,000 results. The full snapshot remains searchable through pagination and filters.

Attribution: Binjola, C. (2026). _RoleBase Job Listings: Open Jobs in Every Field_. Kaggle. Dataset metadata lists the licence as “Other (specified in description)”; employer postings remain their source material. Source code and collection details: https://github.com/chandranshB/roleBase.
