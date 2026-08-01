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
worker hosts divide the newsgroups between them without double-crawling — and a
worker that loses a lease mid-pass (deploy takeover, expiry) cancels that pass
immediately rather than overlapping the new owner's writes. It can
own its own minimal catalogue (standalone / demo) or, in **host-sink mode**,
hand every assembled release to a rich host's NZB domain — the seam by which a
production site adopts this plugin in place of an in-tree crawler.

## Surface

Routes:

- **admin** (`Core.Router.Admin("usenet")`, RequireRole(Admin) applied by the
  host): `GET /admin/plugin/usenet/status.json` — machine-readable crawl status
  (both passes, fleet, workers, staging/build depth, recent errors) for
  monitors and scripts. Carries provider hostnames and error text, so it is
  admin-gated, not public. Note for monitors: since 2026-07 `active_groups`
  counts active newsgroups (it used to count per-(backbone, group) state rows,
  so the value dropped on multi-backbone installs at that deploy), the
  `total_nzbs`/`staged_articles` figures are planner estimates, not exact
  counts, and the never-assigned `pending_releases`/`ready_releases` fields
  (constant 0 for their whole life) were removed. Accurate from ANY process: the worker publishes its
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
    `junk-move`, `junk-order`, `junk-toggle` (the junk-rule ORDER editor:
    rules listed in evaluation order with lifetime hit counts, share and
    drift, so a high-volume rule sitting late is visible — see PIPELINE.md
    §6), `filter-add`, `filter-toggle`, `filter-del`, `filter-reset`,
    `poster-watch-add`, `poster-watch-del`. Each action
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

See [PIPELINE.md](PIPELINE.md) for the article flow end to end — buffers,
cadences, measured timings, and where the cost actually sits. Read it before
changing the crawl or build path.

## Data

Owns the **`usenet` Postgres schema** (loon scopes `search_path` to it;
unqualified names in migrations resolve there). Migrations `001`–`028`, embedded
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
- `poster_watch` / `poster_hits` — the same accounting asked the other way
  round: `filter_hits` answers "which rule drops the most", which is right when
  tuning rules and useless when an operator says "this poster puts out a
  hundred releases a day and I have four of them". A watched pattern is matched
  case-insensitively against each article's `From`, and every outcome for it is
  tallied per (poster, stage, reason) — including the SUCCESSES, because
  "nothing at all for this poster" and "all of it junked at ingest" look
  identical from the outside and have opposite fixes. Migration 023.
- `build_outcomes` — per-day, per-reason counts of what the build pass did with
  every candidate set (`built`, `incomplete`, `duplicate`, `junk`, `blacklist`,
  `blocked_ext`, `empty`, and the four error reasons), plus one sample subject
  each. Where `filter_hits` attributes a drop to a *rule*, this accounts for
  *every* candidate — including the two outcomes the pass never used to report,
  `incomplete` and `duplicate`, which are usually the largest. Bucketed by day
  rather than all-time because the question is almost always "what changed".
- `staging_census` — one row per build pass covering the stretch between "the
  articles are staged" and "a release exists", which was previously
  unobservable end to end. Three different mechanisms can destroy a completed
  release in that gap — Redis evicting keys under `maxmemory`, the staging TTL,
  and a ready queue deeper than the per-pass draw — and all three remove work
  silently, so a release could stage, complete, queue itself and vanish without
  touching `build_outcomes`, the job log, the error log or any counter. The
  columns are cheap (one `INFO` plus two O(1) reads) and the signal is in the
  DELTA between rows: `evicted_keys` climbing means Redis is destroying staged
  work; `ready_depth` far above `sampled` pass after pass means arrivals outpace
  the drain; `fossil_dropped` counts releases that completed and then expired
  before the builder drew them. Rendered as the **Staging health** card on the
  Jobs tab, and pruned to 14 days. Migration 024.
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
  ready queue, `staging_ttl_hours` knob). The pg backend is CORRECT and its
  hot write path is batched (`stageArticles` inserts through a chunked unnest
  INSERT); prefer `redis` when crawling a full feed, `pg` when durability
  matters more than throughput. One known pg cost: the SQL candidate
  pre-filter is looser than the in-memory completeness check (it cannot see
  per-file segment totals), so a multi-file set the SQL admits but the check
  refuses is re-drawn — articles re-loaded — every build pass until it
  completes or ages out on the prune horizon.
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
- `pluginapi.UsenetJunkSweepName` — optional: the host's stored-catalogue
  junk-sweep attribution counters, shown as a third card on the Filters tab
  (ingest hits say what was dropped; the sweep says what got past ingest and
  had to be tagged afterwards). Absent, the card is hidden.

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

Subjects are RFC 2047 **decoded at ingest**, before anything reads them. A
subject is a raw header, and a poster writing outside ASCII sends
`=?UTF-8?q?...?=`; undecoded, the title is unparseable — a base64
encoded-word can even swallow the yEnc segment counter — and reads as
punctuation soup to the junk engine, which had dropped 41.6 million
articles that way. Charsets Go has no table for are passed through
unconverted (mojibake in the right release beats an absent release), except
the stateful ISO-2022/UTF-7 family, where doing so spills ESC bytes into the
title and measurably makes things worse; those keep the raw header.

`high_special_chars` ("the title is mostly spam-grade punctuation") judges
letters and digits with Unicode, not ASCII, and takes its ratio over runes
rather than bytes. Deciding it the ASCII way counted every CJK ideograph, kana
and Hangul syllable as punctuation, so a native-script title scored ~100%
special and was dropped at ingest — on an anime indexer that excluded exactly
the Japanese-titled releases the catalogue most wants, while the rule's counter
climbed as if it were working. The host-side mirror in
`pkg/services/junk_title.go` carries the same fix (the SQL sweep clause counts a
fixed ASCII set and was never affected).

Three further corrections came out of running the engine over 20,000 titles
already in the production catalogue (`TestJunkCorpusAudit`, which skips unless
`USENET_AUDIT_CORPUS` is set): a trailing `{Tags:L0;A=ja,en;S=en,ar;}` metadata
block is stripped before the ratio (it is `;=,` almost end to end, so a long
language list read as soup); only ASCII punctuation counts, because the
obfuscation bots work in ASCII while `【】（）｜` are ordinary structure in their own
script; and a RUN of the same mark counts once, since `Keijo!!!!!!!!` and
`New Game!!` are title styles rather than garble. Together those took the
false-positive rate from 96 titles to 27 — 23 of the remainder are the
deliberate `under_1mib`/`under_5mib` size bands catching subtitle packs, manga
and audio rather than a rule defect. **Run that audit before changing a junk
rule**; none of these were visible by inspection.

Each staged set records the **article-number span** it covers (`art_lo`/`art_hi`
in its meta, folded per batch by a Lua min/max script so out-of-order and
descending-backfill arrival still record true bounds). Article numbers ascend
with posting time and one upload happens in a single run, so a real release is
near-contiguous even with other posters interleaved. A set spanning a million or
more article numbers — roughly half a day of posting on a busy group — is
therefore not one release but several unrelated posts that collided on the same
base subject, and it can NEVER complete: it is waiting on files belonging to
somebody else's upload. The forming-releases card shows the span and flags those
as `collision`, which is a different fact from "incomplete" and calls for a
different fix (tighten the base derivation, not wait longer).

The threshold is absolute rather than a ratio of articles held. Scaling it with
`Have` — the first attempt — flags every set early in its arrival, when it holds
few articles but already covers a real range, which is precisely the window an
operator is watching. A test with a genuine four-article release caught that.

The forward crawl **yields when staging is full**, at `crawl_pressure_high_pct`
(default 95, deliberately above the backfill's 85: new articles matter more than
history, so the crawl stops only when storing would actively destroy what is
already there). Under redis with an `allkeys-*` policy at its ceiling every
write evicts something to make room, and the coldest keys are precisely the
forming sets waiting between crawl visits — production evicted **97.4 million**
keys that way, roughly 640 staged releases a minute, while the crawler kept
feeding it. Skipping the round leaves the watermark where it is, so nothing is
skipped: those articles are re-read next pass. Storing them into a full backend
is what loses them. Set to 0 to disable, which is right on a backend that cannot
destroy what it holds (pg staging, or redis under `noeviction`, where a full
server refuses the write instead).

The ready queue is **swept before each draw**. `candidateGroups` takes a random
sample of `build_drain_per_pass` entries, and nothing else ever removed the dead
ones — so a queue growing faster than the draw accumulates fossils without bound
and dilutes itself. Production reached **7,403,408 entries against a 500-entry
draw, 407 of every 500 already dead**: a completed release had roughly a 1-in-
15,000 chance of being picked in a pass, and its articles expire after two
hours. That is not a queue, it is a lottery nobody wins. `reapReadyQueue` SSCANs
a bounded slice per build ROUND (`ready_reap_per_pass`, default 50,000 — the
key name predates the round/pass split), pipelines EXISTS against each entry's
`grp:` metadata, and SRems the dead — the same liveness definition
`candidateGroups` already used, applied by something that runs often enough to
matter. Bounded, with a cursor that persists across rounds, so a multi-million-
entry queue is worked down instead of stalling the round clearing it.

Staged sets that can **never complete** are evicted by the **walk-past sweep**
(same round cadence): a set still short of its claimed totals, idle past
`walk_past_grace_min` (default 15), whose whole recorded article span
(`art_lo`/`art_hi`) lies inside the group's fetched coverage
(`newsgroup_ranges`) has been offered every article it could ever receive —
absence is final, and every hour it waits for the staging TTL is an hour of
memory held against the backfill's pressure gate. `walk_past_sweep_per_round`
(default 2,000) bounds each round's examination with persistent per-group
cursors; groups covered on more than one backbone are skipped (article numbers
are per-backbone, so a mixed span cannot be judged); `walk_past_no_evict`
disables the sweep. Evictions surface as `walk_past` in the telemetry and as
their own census column — distinct from hopeless shedding, because the two
remove different populations for different reasons.

Dead sets still holding most of their articles are **salvaged, not destroyed**
(`walk_past_no_salvage` to disable): the sweep hands them back (up to 25 per
round), and each is scored with the SAME rule the health job applies to stored
releases — `healthVerdict` over a data/par2 split of its gaps. Gaps covered by
surviving par2 build into a release stored **marked broken** through the
health backend (both sink modes, no contract change — a downloader's par2
repair completes it); par2-only gaps build as a NORMAL release (all data is
present; only the completeness check was holding it); gaps beyond repair
evict. Junk, blocked-extension and blacklist gates run first — salvage never
resurrects what the build path would drop. When a later re-walk completes a
salvaged release for real, the broken NZB's segment set is a strict subset of
the new one, which is exactly what `nzb-heal` purges.

Note that `stagingInfo` issues **two single-section INFO calls**, not one
multi-section call: `INFO memory stats` requires Redis 7.0, and against 6.x it
returns nothing usable so every memory and eviction field silently reads zero —
reporting a server at its ceiling as unbounded and never-evicting. The
integration test catches this only when pointed at a 6.x server, which is noted
there.

Tier decides ORDER, not entitlement — and on a site where the critical groups
are caught up, that gap is a starvation bug rather than a nuance. Critical and
normal plan in an instant, the whole remaining pass budget falls to whichever
LOW group still has a backlog, and the articles it stages fill the staging
memory whose pressure gate pauses the BACKFILL — the only job that can serve the
critical group's history. `hold_low_until_backfilled` closes it by REMOVING the
low tier from the pass (not merely ranking it last) while any critical group on
that backbone still has history to pull. Asked per backbone, because article
numbers and therefore backfill progress are per backbone. It fails OPEN: if the
check errors, the crawl proceeds rather than converting a transient query
failure into an outage for every low group. Off by default, since it
deliberately starves a tier — right when the critical group is far behind, wrong
on a site that is caught up everywhere. Both the hold and an all-held pass log
what they did; a tier that silently stops crawling is indistinguishable from a
broken crawler.

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
- `staging_census.go` — per-build-pass staging health series (migration 024).
- `subject_mime.go` — RFC 2047 encoded-word decoding at ingest.
- `subject.go` / `tags.go` — subject parsing; title-derived quality tags + category helpers.
- `junk.go` / `junk_store.go` / `seed/junk_rules.tsv` — the junk-filter engine + its data.
- `blacklist.go` / `blacklist_store.go` — the operator blacklist + filter-hit counters.
- `poster_watch.go` / `poster_watch_store.go` — per-poster outcome attribution.
- `health.go` / `health_store.go` — NZB health checking + its backend seam (internal | host).
- `lease.go` / `assign.go` — multi-host leases + term assignment.
- `adopt.go` — one-time host-state adoption for a production flip.
- `dashboard.go` / `telemetry.go` / `views.go` — status JSON, live counters, admin views.
- `newznab.go` — the Newznab/Torznab caps + feed XML.
- `newsgroup_seed.go` / `seed.go` / `seed/*.tsv` — the curated group pack + shipped data.

## Testing

Unit-tested (no DB): the junk engine (a 47-vector parity suite differentially
verified against the production filter, plus rule-order/attribution and the
size-band contract), encoded-word decoding (the production subject that was
being junked, a base64 subject hiding its segment counter, the stateful-charset
refusal, and malformed input falling back to the raw header), the poster watch (substring matching keyed by PATTERN
rather than raw `From` so one poster's history does not scatter across header
variants, nil/empty safety on the per-article path, and accumulate/drain with a
stable first sample), the blacklist matcher (per-field matching, fail-closed on
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

Also needs integration (live Redis, `-tags=integration`, `USENET_TEST_REDIS`):
the `nzb:ready` LIST→SET migration — resumption across calls, discarding
entries whose `grp:` metadata has expired while keeping live ones, two
concurrent converters losing nothing, and the WRONGTYPE self-heal driven
through `stageArticles`. Real server, not a fake: the cases turn on Redis' own
semantics (script atomicity, WRONGTYPE, LTrim dropping an emptied key,
SUnionStore), which a double would only restate as the assumptions under test.
A stale pre-migration list of 7.3M entries stalled prod's crawl→stage→build
pipeline for 23 hours, and the first fix for it introduced a converter race
that destroyed 16k of 40k entries in a differential run — both are now pinned
here, so these paths are worth a real server:

```
docker run -d --name usenet-test-redis -p 6399:6379 redis:7-alpine
USENET_TEST_REDIS=127.0.0.1:6399 go test -tags=integration -race ./usenet/
```
