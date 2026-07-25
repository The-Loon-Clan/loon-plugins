# usenet plugin

A self-contained Usenet indexer. It crawls a curated set of newsgroups, stages
article overviews until a release's parts are all present, assembles complete
sets into gzipped NZB files, and serves search / group-listing / NZB download
through capabilities the host's pages consume. It also health-checks the
catalogue over time (are the articles still on the server?) and filters
machine-generated junk at ingest.

Users see it two ways. **Operators** get one admin page at `/admin/p/usenet`
(loon `SlotAdminPage`), tabbed: the provider fleet (with per-row connection
test), indexing knobs, newsgroup curation, a live Crawlers dashboard
(a slim provider strip with live dial state, newsgroup coverage bars, live
crawl progress with the group-by-group bar, index stats, recently built
releases, an aggregate backfill ETA, worker panels), a **Jobs** tab (one pane
per pipeline job: status, next run, Run-now, a live log tail, plus the
Builder and NZB-health panels), and Filters — the operator blacklist plus
per-rule hit counters.
**End users** get whatever the host builds on the published index capability —
search results, group browse, and a Newznab/Torznab `/api` + `/rss` endpoint.

The plugin runs on one host or several at once. Multiple providers crawl in
parallel and complete each other's releases through shared staging; multiple
worker hosts divide the newsgroups between them without double-crawling. It can
own its own minimal catalogue (standalone / demo) or, in **host-sink mode**,
hand every assembled release to a rich host's NZB domain — the seam by which a
production site adopts this plugin in place of an in-tree crawler.

## Surface

Routes:

- **admin** (`Core.Router.Admin("usenet")`, RequireRole(Admin) applied by the
  host): `GET /admin/plugin/usenet/status.json` — machine-readable crawl status
  (both passes, fleet, workers, staging/build depth, recent errors) for
  monitors and scripts. Carries provider hostnames and error text, so it is
  admin-gated, not public. Accurate from ANY process: the worker publishes its
  in-memory pass trackers + error ring to the shared settings table every few
  seconds (`worker_telemetry`), so a split web/worker deployment serves live
  numbers too — the Crawlers tab polls this endpoint to tick in place.
- **admin views** (registered via `Core.RegisterView`, mounted by the host):
  - `SlotAdminPage` slug `usenet` — the single tabbed admin page
    (`/admin/p/usenet`): the provider fleet (per-row save/test/remove),
    indexing knobs, newsgroups, the Crawlers dashboard, the Jobs tab and the
    Filters blacklist. Actions: `knobs`, `fetch-groups`, `group`, `provider`,
    `provider-del`, `provider-test`, `provider-probe` (backbone fingerprint:
    compares article numbering against the reference provider via STAT),
    `group-tune`, `group-move`, `group-del`,
    `groups-purge`, `crawl`, `backfill`, `run-crawl`, `run-backfill`,
    `run-build`, `run-tagfill`, `run-prune`, `run-health`, `reset-backfill`,
    `filter-add`, `filter-toggle`, `filter-del`, `filter-reset`. Each action
    redirects back to its own tab.
  - `SlotJobsWidget` anchor `Usenet` — a richer card for the Usenet job group
    on the host's `/admin/jobs`.
- **public / api**: the plugin publishes read capabilities rather than mounting
  its own public routes — the host mounts `/api` + `/rss` (Newznab/Torznab) and
  the search/browse/download pages, delegating to `UsenetIndexName`. Auth on
  those is the host's to enforce (the demo leaves the API open; a real host
  checks an apikey against its user store).

Process kinds (`Metadata.Processes`): `web`, `worker`, `api`.

- **web / all**: registers the index + newznab + admin capabilities and the
  admin views.
- **worker / all**: registers and runs the six jobs (below); no view system.
  The worker ALSO registers the admin capability — a host's worker-side
  stats-cache job reads `Stats()` through it for its public stats page.
- **api**: registers the index + newznab capabilities — no jobs, no admin
  surface.

## Data

Owns the **`usenet` Postgres schema** (loon scopes `search_path` to it;
unqualified names in migrations resolve there). Migrations `001`–`017`, embedded
via `//go:embed migrations/*.sql`, run under loon's plugin-migration path on
boot. Every statement is `IF NOT EXISTS` / idempotent.

Principal tables:

- `servers` — configured NNTP providers (host, port, TLS, creds, role,
  priority, connections, backbone).
- `newsgroups` — the curated group list + per-group tuning (active, crawl-depth
  override, throttle, priority tier, manual order).
- `newsgroup_state` — crawl state keyed **(backbone, group)**: watermarks,
  server bounds, backfill-done. Article numbers are per-backbone, so state
  cannot be shared across backbones (see below).
- `newsgroup_ranges` — fetched-article-number coverage per (backbone, group),
  for gap-filling backfill.
- `articles` — the staging buffer (pg mode); drained by the builder, swept by
  prune. Redis mode keeps this transient set in Redis instead.
- `nzbs` — assembled releases (**internal sink mode only**; host mode writes the
  host's table and never touches this one).
- `junk_rules` — the junk-filter rule set, seeded from `seed/junk_rules.tsv`.
- `blacklist_regexes` — the operator's editorial blacklist.
- `filter_hits` — per-rule drop counters (junk + blacklist).
- `leases`, `crawler_workers` — multi-host coordination.

Reads **`public.newsgroups` / `public.blacklist_regexes`** once, at host
adoption only (see Lifecycle), and never again.

## Dependencies

Core services consumed: **Storage** (`SchemaDB("usenet")`), **Scheduler**
(the six jobs + their loops), **Router** (the admin status route), **Logger**,
**Errors** (error sink), and **Config** (`plugins.usenet.*`). **Redis** is
consumed optionally — only when `staging: redis` is configured; its absence in
that mode is a boot refusal, not a silent fallback.

Store: **self-contained** — builds a `*PGStore` over `SchemaDB("usenet")` at
Provision. No `SetDeps`.

Config keys (`plugins.usenet.*`, all optional — the wizard/knobs own the live
values; these are defaults):

- `server.*` — seed a provider on first boot if the table is empty.
- `staging` — `pg` (durable, default) | `redis` (prod's pipeline, needs Redis).
  Redis is the tuned path (inline completeness, hopeless-set eviction, O(1)
  ready queue, `staging_ttl_hours` knob). The pg backend is CORRECT but not yet
  perf-complete: `stageArticles` inserts row-at-a-time (no batch INSERT) and
  `deleteJunkStaged` scans staged titles per sweep — fine at hobby scale,
  known-slow on a full-feed crawl. Prefer `redis` for production volume until
  those land.
- `sink` — `internal` (own `nzbs` table, default) | `host` (hand releases to the
  host's `ReleaseSink`; requires the host to register the sink + health
  capabilities). A production host pins this to `host`.
- Numeric knobs (crawl cadence, batch size, connection count, retention/crawl
  depth, backfill budget, lease TTL, assignment term, health cadence, staging
  pressure thresholds, …) — all overridable live from the admin knobs form.

`Metadata.Requires`: none. The catalog capability is consumed **optionally**
(nil-degrades to Newznab category `Other`).

## Hooks & Callbacks

Host hooks SET (`web/handlers/plugin_hooks.go`): **(none)** — this is a
self-contained plugin; it exposes itself through the capability registry and
`RegisterView`, not host func-var hooks.

Extensions PUBLISHED (`Core.Register`):

- `pluginapi.UsenetIndexName` (`usenet.index`) — the read surface: search,
  group listing, NZB fetch, Newznab query. The host's public pages + API
  consume this.
- `pluginapi.UsenetAdminName` (`usenet.admin`) — the admin/service surface
  (manual job triggers, wizard operations, crawl-progress stats). Registered
  in web/all AND worker (see process kinds above).
- `pluginapi.UsenetNewznabName` (`usenet.newznab`) — the whole Newznab/Torznab
  XML contract (caps / search / rss / get); the host mounts `/api` + `/rss`
  and delegates the parsed request here.
- `pluginapi.UsenetActivityName` (`usenet.activity`) — counts-only crawler
  liveness (current/last pass articles, staged, batches, wire bytes) sourced
  from the published worker telemetry. Sanitized for non-admin surfaces —
  no group names, hostnames, or error text — so a host can drive a public
  stats-page live widget from it. Registered in every process.
- `pluginapi.RegisterStats` — contributes indexer totals to the host's stats
  snapshot.

Extensions CONSUMED (`Core.Lookup`):

- `pluginapi.CatalogName` — optional; categorises releases for Newznab. Absent →
  category `Other`.
- `pluginapi.UsenetReleaseSinkName` / `UsenetHealthStoreName` — the host's NZB
  domain, in `sink: host` mode only. The plugin **publishes the contracts**
  (`ReleaseSink`, `ReleaseHealthStore` in `pluginapi`) and **consumes the host's
  implementations**: assembled releases go out through the sink, health
  candidates/verdicts flow through the health store. Host mode without them is a
  loud refusal.
- `pluginapi.UsenetCatalogStatsName` — optional, `sink: host` only: catalog
  totals + the health breakdown for the dashboard's Index Stats and NZB Health
  cards (in host mode the releases and verdicts live in the host's domain,
  invisible to the plugin's tables). A host should serve it from CACHED
  numbers; absent, those cards degrade to empty.

Events: emits `pluginapi.EventIngested` after a build pass creates releases, so
a host subscriber (e.g. a cache invalidator) can react. Best-effort — no host
event bus means no-op.

## Lifecycle

**Provision** (all processes): builds the `*PGStore`, reads config + applies
defaults, validates the sink mode (an unknown value fails boot rather than
silently splitting the catalogue), constructs the staging backend behind the
`stagingStore` seam, and registers the per-process capabilities (see process
kinds). The admin `status.json` route registers on web/all only. The optional
catalog plugin is looked up in **Start** (provision order isn't guaranteed),
in every process.

**Start** (worker/all only): seeds a provider from config if the table is empty;
runs **host adoption** (`adopt.go`) once in host-sink mode — carrying a legacy
in-tree crawler's newsgroups, watermarks (into per-backbone state, deriving
backfill-done from the legacy schema's convention), per-group tuning, and
blacklist so a production flip *resumes* rather than restarts; starts the
heartbeat (multi-host presence); seeds junk rules + the curated newsgroup pack;
and launches the six job loops.

The six jobs (registered on the worker, all carrying the "Usenet" prefix so
they never collide with a host's own job names in the shared registry):
**Usenet Crawler** (forward-fetch recent overviews; keeps going without waiting
for the interval while the servers still hold a backlog, unless
`crawl_no_catchup` is set), **Usenet Backfill** (walk history downward, filling
coverage gaps), **Usenet Builder** (assemble complete staged sets, filter,
store through the sink), **Usenet Tag Fill**, **Usenet Prune** (stale-staging +
junk sweep; NZB retention is opt-in, default keep-forever), **Usenet Health
Check** (STAT segments, record healthy/broken/dead).

**Stop**: no-op — the jobs derive from the root context and unwind on
`SIGTERM`; leases expire (or are taken over by a replacement worker once the
heartbeat goes stale).

## Architecture notes

Two facts shape most of the design:

- **Article numbers are per-backbone; message-ids are global.** Watermarks,
  coverage ranges, and backfill state are keyed by backbone because two
  backbones number the same articles differently — merging them would silently
  skip real content. Staging dedup, by contrast, is keyed by message-id, so two
  providers on *different* backbones complete each other's releases. Content
  identity (`content_hash`) is `sha256(sorted message-ids)[:16]` — the same
  scheme the host uses, so dedup is shared across the plugin and the host at
  adoption.
- **Assembly is delivery-agnostic below "the bytes arrived."** Everything the
  builder does after a set is complete — junk/blacklist filtering, title +
  category classification, NZB XML — is identical whether the release lands in
  the plugin's table or the host's. Only the final store step differs, which is
  exactly the `ReleaseSink` seam.

Coordination across hosts is two layers: **leases** (a row with an expiry per
(backbone, group) or per job) guarantee no two workers crawl the same thing;
**term-based assignment** (heartbeat presence + hash partitioning within a fixed
time term) divides the groups so workers don't merely race for the leases. A
worker that joins mid-term waits for the next boundary. Exclusivity is
heartbeat-backed: a lease whose owner has written no heartbeat for two
minutes is claimable IMMEDIATELY, regardless of expiry — a deploy renames the
worker (container hostname), so without takeover every deploy idled the
crawler until the TTL lapsed. The catch-up loop retries an all-blocked pass
every 45s to ride out the takeover window.

The **junk engine** is a full, data-driven port of the production filter: 24
rules (regex + named heuristics for shapes regexes can't express) in the
production evaluation order, shipped in `seed/junk_rules.tsv`, seeded to
`junk_rules`, compiled once into an atomic in-memory matcher, and reloadable
without a restart. Size-banded rules only fire once a release's total size is
known (the build path); the per-article ingest path runs the unsized subset.

## Files

- `plugin.go` — registration, `Metadata`, Provision/Start/Stop, job + view + route wiring.
- `config.go` — the `Config` struct, defaults, live-override knob mapping.
- `store.go` / `store_iface.go` — the `Store` interface + `PGStore` construction.
- `providers.go` / `provider_store.go` / `provider_state.go` — the provider fleet, per-backbone crawl state.
- `backbones.go` — hostname → backbone identity mapping.
- `crawl.go` — the forward crawl pass (plan → fetch → stage → advance watermarks).
- `backfill.go` — the downward history walk, gap-filling.
- `ranges.go` — fetched-range coverage + the gap complement.
- `nntp.go` / `pool.go` (loon) — NNTP conn handling; the shared connection pool.
- `staging.go` / `redis_staging.go` — the `stagingStore` seam (pg | redis).
- `assemble.go` — completeness detection, NZB XML build, classification, the store handoff.
- `subject.go` / `tags.go` — subject parsing; title-derived quality tags + category helpers.
- `junk.go` / `junk_store.go` / `seed/junk_rules.tsv` — the junk-filter engine + its data.
- `blacklist.go` / `blacklist_store.go` — the operator blacklist + filter-hit counters.
- `health.go` / `health_store.go` — NZB health checking + its backend seam (internal | host).
- `lease.go` / `assign.go` — multi-host leases + term assignment.
- `adopt.go` — one-time host-state adoption for a production flip.
- `dashboard.go` / `telemetry.go` / `views.go` — status JSON, live counters, admin views.
- `newznab.go` — the Newznab/Torznab caps + feed XML.
- `newsgroup_seed.go` / `seed.go` / `seed/*.tsv` — the curated group pack + shipped data.

## Testing

Unit-tested (no DB): the junk engine (a 47-vector parity suite differentially
verified against the production filter, plus rule-order/attribution and the
size-band contract), the blacklist matcher (per-field matching, fail-closed on
unknown field, one-bad-pattern isolation), coverage-cell math, backfill gap
complement, term-assignment partitioning (every group to exactly one worker,
stable across churn), content-hash identity (pinned cross-repo to the host's
scheme), assembly classification order, the sink/health backend contracts, and
the Newznab/telemetry formatting.

Needs integration (live Postgres, `-tags=integration`, `USENET_TEST_DSN`): the
coordination SQL (lease exclusivity, heartbeat, the atomic claim guard), the
per-backbone stats/coverage queries, blacklist + filter-hit persistence, and
**host adoption** — the last one asserts against the *real* legacy schema, which
is how it caught two column-shape divergences the in-memory tests could not.
Run the full suite (unit + integration, `-race`) against a throwaway Postgres;
the coordination and adoption guarantees are the pieces that fail silently and
expensively if wrong, so they are only trustworthy against a real database.
