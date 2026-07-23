# Running the crawler on its own host

Short version: **yes, and it needs no special support — but run exactly one
worker.**

## What already works

The plugin declares `Processes: ["web", "worker", "api"]` and registers its jobs
only when the process is `worker` (or `all`). So a worker-only deployment runs
crawl, backfill, NZB build, tag fill, prune and health, while the web and api
processes serve search / browse / download and register no jobs at all.

They coordinate entirely through shared state — there is no direct connection
between the tiers:

| | needs Postgres | needs Redis | needs outbound NNTP |
|---|---|---|---|
| **worker** (crawler) | yes — writes staging, nzbs, watermarks, coverage | only when `staging: redis` | **yes** |
| **web** | yes — reads nzbs | no | no |
| **api** | yes — reads nzbs | no | no |

Two consequences worth noting:

- Only the **worker** ever touches Redis staging. Web and api serve from the
  `nzbs` table, so a split deployment does not need Redis reachable from the
  front end.
- Only the **worker** needs outbound NNTP and provider credentials. The web tier
  never talks to a news server, which is a useful blast-radius reduction if the
  front end is the internet-facing box.

## Running two workers (supported)

Workers coordinate through leases in the `leases` table. Two scopes:

- **`group`** — one backbone's view of one newsgroup. Crawl state is keyed
  `(backbone, group_name)`, so two workers crawling *different groups* touch
  entirely separate rows and never contend. This is the unit of parallelism:
  worker A takes `alt.binaries.anime` on Omicron while worker B takes
  `alt.binaries.hdtv` on the same backbone.
- **`job`** — the jobs that are not group-scoped (NZB build, prune, tag fill,
  health). Those drain shared staging or compete for idle connections, so they
  run once cluster-wide; the worker that doesn't get the lease logs and skips.

A worker claims what is free at the start of a pass, renews while working, and
releases at the end. Partial acquisition is normal — whatever another worker
holds simply isn't yours this pass. Leases carry an expiry, so a killed worker's
groups are picked up by the next pass rather than going dark; `lease_ttl_min`
(default 15) sets how long that takes.

Because the lease is per *group*, a second account on the SAME backbone is still
useful — it works other groups rather than sitting idle.

Two caveats:

- **The connection budget is still per account.** If both workers use the same
  provider credentials, each opens up to `connections` connections, so set that
  to roughly half your account's limit per worker, or give each worker its own
  account.
- Leases are advisory-by-convention: they stop the plugin's own jobs colliding.
  Nothing prevents an operator pointing two workers at the same account and
  over-subscribing it.

## What used to not work: two workers

There is no cross-host job lock. The plugin's `crawlMu` / `backfillMu` /
`buildMu` are ordinary in-process mutexes — they stop a manual trigger racing a
scheduled run **inside one process**, and do nothing at all between hosts.

Start a second worker and both will:

1. **Crawl the same groups at the same time.** Watermark writes use `GREATEST`
   and coverage merges, so this does not corrupt state — but every article is
   fetched twice, and you pay for it twice in bandwidth and time.
2. **Double the connection count.** This is the one that bites. Provider limits
   are per *account*, not per host: two workers each opening `connections`
   connections to the same account puts you at 2× the cap. The provider responds
   by refusing connections (482/502), the pool benches the provider, and — if a
   backup is configured — the fleet fails over to a server that is not actually
   broken.
3. **Duplicate the health sweep**, competing for the same idle connections that
   the crawler wants.

So today the supported topology is: **N web replicas, N api replicas, exactly
one worker.**

## Making multiple workers safe

Two designs, and the second is better for this workload.

### Option A — job singleton (simple, no throughput gain)

Take a Postgres advisory lock keyed on the job name at the start of each run and
skip the run if it is already held. loon already uses this pattern for plugin
migrations (`core/migrations.go`), so the mechanism is proven and needs no new
infrastructure.

Effect: a second worker becomes a warm standby. It costs nothing, breaks
nothing, and takes over automatically if the first dies. It adds **no crawl
throughput**, because only one worker is ever crawling.

### Option B — shard by backbone (real horizontal scale)

This is what the per-backbone state model makes possible, and it falls out
almost for free.

Crawl state — watermarks, server bounds, coverage — is keyed by **backbone**
(see PROVIDERS.md). Two different backbones share no mutable state whatsoever.
The only thing they do share is the staging area, and that is deduplicated by
message-id, so concurrent writes from different workers are safe by
construction.

So backbones are a natural shard key: give each worker a disjoint set of
providers and they can crawl fully in parallel without coordination beyond the
assignment itself.

That assignment wants to be *leased*, not configured, so a dead worker's
backbones get picked up rather than going dark:

- a `worker_leases` table of `(backbone, worker_id, expires_at)`;
- each worker renews its leases on a heartbeat and claims any expired ones;
- a worker only crawls backbones it currently holds.

This also fixes the connection-budget problem properly rather than by
convention: a backbone is crawled by exactly one worker, so that worker owns the
whole per-account connection allowance and no arithmetic is needed.

### Recommendation

Do **Option A first** — it is small, removes the footgun, and makes a second
worker a genuine hot standby. Reach for Option B only when one worker's
connections are actually the bottleneck, since it introduces leases, heartbeats
and expiry, all of which are new failure modes.

## Until then

Run one worker. If you need more crawl throughput, raise `connections` (up to
what your provider allows) or add a provider on a **different backbone** —
both scale ingest without touching deployment topology, and the second also
improves completeness.
