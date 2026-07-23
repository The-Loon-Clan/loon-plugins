# Running the crawler on its own host

Short version: **yes — a worker-only process runs the crawler on its own host,
and two workers can now run in parallel via leases.**

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

## Other ways to scale ingest

Before adding a host, note that two things scale throughput without touching
deployment topology: raise `connections` (up to what the account allows), or add
a provider on a **different backbone**. The second also improves completeness,
since backbones expire different articles and staging merges them by message-id.

## What leases do NOT solve

They coordinate this plugin's jobs. They do not stop an operator pointing two
workers at one provider account and over-subscribing its connection limit — see
the caveat above — and they are not a general cluster lock: another plugin's
jobs are its own business.
