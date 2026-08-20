# anidbscraper plugin

> **This is a scaffold, not a finished plugin.** The lifecycle wiring, job
> registration, off-peak gating and port seams are real and compile against
> loon. The scrape internals are **stubbed**, and `fetchMetadata` returns an
> error for every id it is given. Do not deploy this expecting it to scrape
> anything. Read *Status* before anything else.

The worked example for this repo: a real ameNZB background job — the AniDB
scraper — part-way through being extracted into a loon plugin.

**A worker-only, HOST-DATA plugin.** It owns no schema of its own and instead
reads and writes the *host's* `anime_metadata` and `nzbs` tables through narrow
ports in `pluginapi`. That is what makes it the honest template for roughly
forty of the site's jobs, which are all coupled to host tables the same way —
in deliberate contrast to the self-contained, schema-owning plugins like
`store` or `guestbook`, which are the easy case.

The whole point of the exercise is the *shape* of a job that cannot own its
data: what the ports have to look like, which dependencies are clean and which
are entangled, and what has to stay host-side.

## Status

| Piece | State |
|---|---|
| Lifecycle, `Provision`/`Start`/`Stop` | real |
| Job registration, off-peak and write gating, manual triggers | real |
| `Deps` ports and their fail-fast checks | real |
| `buildTitleIndex` | **partial** — combines the two catalog maps, but does not parse the AniDB titles dump |
| `fetchMetadata` | **stub** — returns an error for every aid |
| Everything else marked `// EXTRACT:` | not moved yet |

Each stub carries an `// EXTRACT:` marker naming the exact source in
`pkg/services/anidb_service.go` that moves in, with line ranges.

Two consequences an operator should know:

- The **Metadata Fill** job will error on every id. That is loud rather than
  silent, which is the right failure for a stub, but it is not a working job.
- The titles-dump fetch, when it moves, **must** switch from the bare
  `http.Get` it uses today to `core.HTTPClient` — see the marker in
  `buildTitleIndex` and CHECKLIST §3.

The production service registers **six** jobs; this wires the three that carry
the core loop.

**No tests.** This is the only package in the repo with none, and deliberately
so: a test over a stub pins the stub. It becomes a normal expectation the day
the bodies land.

## Surface

| Job | Gating | Notes |
|---|---|---|
| **AniDB Titles Index** | `MarkWrites()` | Downloads and indexes anime titles. Daily, at 00:05 local. |
| **AniDB NZB Scanner** | `MarkOffPeak()`, `MarkWrites()` | Scans NZB titles against the index and tags a matched `anime_id`. Manual trigger. |
| **AniDB Metadata Fill** | `MarkOffPeak()`, `MarkWrites()` | Fetches images and metadata from AniList. Manual trigger. **Currently errors on every id.** |

No routes, no views, no widgets. Worker only.

**Jobs are registered in `Provision`, not `Start`** — registering at Start races
the admin view's registry snapshot, so a job could exist and not be listed.

The manual triggers have no context of their own and borrow the root context
captured in `Start`; triggers only ever fire at runtime, well after Start.

## Data

**Owns no tables and ships no migrations.** Everything it touches belongs to the
host and is reached through a port.

## Dependencies

`SetDeps` must be called exactly once, in the worker process, before
`core.Boot`. A nil field is caught in `Provision` — fail fast, rather than
nil-panicking half way through a scan.

| Port | Kind | What it is |
|---|---|---|
| `pluginapi.AnimeCatalog` | **clean** | The `anime_metadata` / `anime_aliases` store. Scraper-owned data that this plugin would ideally own outright. |
| `pluginapi.NzbTagSink` | **entangled** | The write-back into the host's `nzbs` table — the deep seam, and the reason this is a host-data plugin at all. |
| `pluginapi.TitleMatcher` | **entangled** | The shared title matcher, rebuilt after each titles refresh. |
| `pluginapi.CoverStore` | **entangled** | Maps to `web/static/covers/{aid}.jpg`. |

The clean/entangled split is the finding this exercise produced, and
`pluginapi/anidb.go` and `JOBS-AS-PLUGINS.md` carry the reasoning for each.

Core: `Scheduler` only.

## Hooks & callbacks

Publishes nothing. Declares no events — which is a gap the extraction should
close, since a completed scrape is exactly the kind of thing another plugin
would want to hear about.

## Lifecycle

`Provision` checks the ports, then registers the three jobs and their triggers.
`Start` captures the root context and installs the run loops. `Stop` is a
no-op.

`nextMidnight` returns 00:05 tomorrow in local time — the production
titles-dump cadence.

## Files

```
plugin.go   lifecycle, the three jobs, the run bodies and their EXTRACT markers
deps.go     the four ports and why each is clean or entangled
```

## Finishing it

Either complete the extraction or retire the package. It is currently a job
registered on a real scheduler with a body that cannot succeed, and that is the
one state it should not stay in — an operator sees three jobs on `/admin/jobs`
and one of them fails every run.

The markers say where each body comes from. The order that makes sense is
`buildTitleIndex` first (nothing else works without the index), then the
scanner, then the fill.
