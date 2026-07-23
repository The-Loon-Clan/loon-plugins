# usenet plugin

A self-contained Usenet indexer. It crawls a curated set of newsgroups, stages
article overviews until a release's parts are all present, assembles complete
sets into gzipped NZB files, and serves search / group-listing / NZB download
through capabilities the host's pages consume. It also health-checks the
catalogue over time (are the articles still on the server?) and filters
machine-generated junk at ingest.

Users see it two ways. **Operators** get an admin surface: a setup wizard
(providers, newsgroups, tuning knobs) under `/admin/settings`, a live crawl
status page at `/admin/p/crawlers` (coverage bars, per-backbone progress,
recent activity, an aggregate backfill ETA, worker/fleet panels), and a
`/admin/p/filters` page for the operator blacklist plus per-rule hit counters.
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
  admin-gated, not public.
- **admin views** (registered via `Core.RegisterView`, mounted by the host):
  - `SlotAdminSettings` slug `usenet` — the setup wizard. Actions: `server`,
    `test`, `knobs`, `fetch-groups`, `group`.
  - `SlotAdminPage` slug `crawlers` — crawl status. Actions: `crawl`,
    `backfill`, `reset-backfill`.
  - `SlotAdminPage` slug `filters` — blacklist + filter-hit counters. Actions:
    `add`, `toggle`, `delete`, `reset`.
  - `SlotJobsWidget` anchor `Usenet` — a richer card for the Usenet job group
    on the host's `/admin/jobs`.
- **public / api**: the plugin publishes read capabilities rather than mounting
  its own public routes — the host mounts `/api` + `/rss` (Newznab/Torznab) and
  the search/browse/download pages, delegating to `UsenetIndexName`. Auth on
  those is the host's to enforce (the demo leaves the API open; a real host
  checks an apikey against its user store).

Process kinds (`Metadata.Processes`): `web`, `worker`, `api`.

- **web / all**: registers the index + admin capabilities and the admin views.
- **worker / all**: registers and runs the six jobs (below); no view system.
- **api**: registers the read capability only — no jobs, no admin surface.

## Data

Owns the **`usenet` Postgres schema** (loon scopes `search_path` to it;
unqualified names in migrations resolve there). Migrations `001`–`016`, embedded
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
  (manual job triggers, wizard operations).
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

Events: emits `pluginapi.EventIngested` after a build pass creates releases, so
a host subscriber (e.g. a cache invalidator) can react. Best-effort — no host
event bus means no-op.

## Lifecycle

**Provision** (all processes): builds the `*PGStore`, reads config + applies
defaults, constructs the staging backend behind the `stagingStore` seam,
registers stats, and — on web/all — registers the index + admin capabilities
and the admin views; on all/api, looks up the optional catalog. Registers the
admin `status.json` route when a Router is present.

**Start** (worker/all only): seeds a provider from config if the table is empty;
runs **host adoption** (`adopt.go`) once in host-sink mode — carrying a legacy
in-tree crawler's newsgroups, watermarks (into per-backbone state, deriving
backfill-done from the legacy schema's convention), per-group tuning, and
blacklist so a production flip *resumes* rather than restarts; starts the
heartbeat (multi-host presence); seeds junk rules + the curated newsgroup pack;
and launches the six job loops.

The six jobs (registered on the worker): **Usenet Crawler** (forward-fetch
recent overviews), **Usenet Backfill** (walk history downward, filling coverage
gaps), **NZB Builder** (assemble complete staged sets, filter, store through the
sink), **NZB Tag Fill**, **NZB Prune** (stale-staging + junk sweep; NZB
retention is opt-in, default keep-forever), **NZB Health Check** (STAT segments,
record healthy/broken/dead).

**Stop**: no-op — the jobs derive from the root context and unwind on
`SIGTERM`; leases expire on their own.

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
worker that joins mid-term waits for the next boundary.

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

Unit-tested (no DB): the junk engine (a 44-vector parity suite differentially
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
