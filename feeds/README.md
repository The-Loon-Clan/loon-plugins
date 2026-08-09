# feeds plugin

Release-feed discovery. Polls public torrent feeds — Nyaa, AniRena, Tokyo
Toshokan, and (with an API key) nekoBT's Torznab endpoint — and auto-creates
community requests for new anime releases, deduplicated by info_hash and
title against the host's recent requests. Every observed item is also
recorded to the host's `feed_items` table, which backs the public Feed tab,
and items whose `[Group]` title prefix matches a curated release group are
mirrored into that group's archive. A top-searched pass rides the same run:
popular member queries that returned zero grabs get a Nyaa search, and the
best-seeded hit becomes a request suggestion.

The same package publishes the on-demand **`search.torznab`** capability
(nekoBT search), because both halves share the one config key and HTTP
plumbing — but they are otherwise independent: the search registers in every
process, the importer runs only where jobs run.

Users see the results on the host's request board (`/community/requests`,
including its Feed tab) and in release-group Archive tabs; the plugin itself
serves no pages.

## Surface

- Routes: none.
- Jobs: **Torrent Feed Import** (worker; default every 30 min, boot delay
  3 min). The name is the historical host name on purpose — the admin's
  `job_interval:` override is keyed on it. Manual trigger and interval
  override work exactly as they did against the host service.
- `Metadata.Processes`: `["web", "worker", "api"]` — all three, because
  `search.torznab` has consumers on each; the importer gates on
  `c.Process == "worker" || "all"`.

## Data

- Owns **no tables**. Writes host data through seams only: requests,
  feed_items, the release-group archive. Reads host search analytics and the
  airing calendar the same way.

## Dependencies

- Core services: `Config` (`plugins.feeds.*`), `HTTPClient` (`Media()` for
  the trusted feed endpoints; `Whitelisted(30s, "anirena.com")` for .torrent
  URLs arriving *in* feed content — upstream data gets the SSRF guard and a
  host pin), `Errors` (archive cross-write failures), `Scheduler` registry
  via `loon/schedule` directly.
- `loon/bencode` — derives an info_hash from a fetched .torrent when
  AniRena's RSS carries no magnet (their current shape).
- Store: none. `SetJobDeps(JobDeps)` supplies function seams from the host
  (see `deps.go` for the full set): request create/priority/dedup-keys,
  feed-item upsert, four anime lookups, failed-search analytics, airing
  calendar entries, release-group slug lookup + archive upsert, and three
  site-vocabulary functions (`NormalizeTitle`, `BlockedExtension`,
  `SlugifyGroup`) crossed as functions so each rule has exactly one copy.
  Required in any process that runs jobs; Provision fails loud there without
  it. Web/api processes need no deps.
- Config: `plugins.feeds.nekobt_api_key` — enables the nekoBT feed source
  and backs the search capability. Empty is ordinary (three public sources
  still poll; search reports unavailable). The ameNZB host defaults it from
  its legacy `app.nekobt_api_key`, so existing deployments need no config
  edit.
- `Metadata.Requires`: none.

## Hooks & Callbacks

- Host hooks set: (none).
- Extensions PUBLISHED:
  - `search.torznab` (`lpapi.TorznabSearch`) — every process. On-demand
    nekoBT search; `Available()` is false without a key, `Search` returns
    `(nil, nil)` then.
  - `feeds.status` (`lpapi.FeedsStatus`) — worker only. Per-source poll
    health + last-run outcome counts; the host's `GET /ops/feeds` reads it.
    A lookup miss means "the importer does not run in this process".
- Extensions CONSUMED: (none).

## Lifecycle

- Provision: reads config, builds HTTP clients, registers `search.torznab`;
  in job processes also validates JobDeps, registers the job + its manual
  trigger and `feeds.status`.
- Start: launches `schedule.ServiceLoop` with the root context — SIGTERM
  interrupts both the inter-tick sleep and (via per-item `SleepCtx`) a run
  in flight, which the host service it replaced never did.
- Stop: no-op; the loop exits on context cancel.

## Files

- `plugin.go` — lifecycle, config, job + capability registration.
- `deps.go` — `JobDeps` seams + the DTOs that cross them + feed-item status
  constants (kept in sync with the host's `storage.FeedItemStatus*`).
- `importer.go` — the run loop, per-item gate, request builder, airing
  index, top-searched pass, release-group archive cross-write.
- `sources.go` — the four fetchers with parsing split out per source, plus
  the .torrent info_hash fallback.
- `torznab.go` — the published `search.torznab` implementation.
- `status.go` — the status book behind `feeds.status`.

## Testing

- Unit-tested: all four feed parsers against realistic fixtures (category
  filters, remake/magnet-less drops, hash lowering, size parsing, attr
  fallbacks), the .torrent info_hash fallback over httptest (including junk
  and 404), the per-item gate ordering, the old-year filter, sorting, magnet
  hash extraction, airing index + matching, request building (blocked
  extensions, title metadata, anime resolution via airing index and TVDB),
  failed-search picking, archive keying and cross-write skip paths, and the
  status book. Key guards are mutation-verified.
- Needs integration (live network/DB): the fetchers against the real feed
  endpoints, and the end-to-end run loop against a host — covered
  operationally by `GET /ops/feeds` after deploy rather than by tests.
