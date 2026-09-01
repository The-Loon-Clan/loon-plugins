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
| The newsgroup gate (`group_gate.go`) | **real** — ported from the host, ships inert |
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

**One test file, and only one.** A test over a stub pins the stub, so the
extraction stubs stay untested until their bodies land. `group_gate_test.go` is
the exception because the gate is not a stub: it is a mirror of host code, and
the two copies had already drifted apart once without anything noticing. Those
cases are the host's own, ported alongside it.

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
| `Deps.AllowTitleGuess` | **optional** | The newsgroup gate's jurisdiction test. `nil` (the default) falls back to the allowlist patterns, which ship empty — so nothing is gated. See below. |

The clean/entangled split is the finding this exercise produced, and
`pluginapi/anidb.go` and `JOBS-AS-PLUGINS.md` carry the reasoning for each.

Core: `Scheduler` only.

## The newsgroup gate

The scanner answers "which anime is this release" for every untagged row, using
a title matcher that has no way to say *this isn't anime at all*. On ameNZB,
over the 14 days to 2026-08-31, that produced 12,247 tags:

| Where the row came from | Tags | Strict precision |
|---|---|---|
| `alt.binaries.multimedia.anime.highspeed` | 7,945 | 83% |
| every other newsgroup | 4,302 | ~15% |

Off the anime groups the matcher is not wrong at the margin, it is guessing:
"Adobe Audition 2026" reached an anime romanized *O-di-syeon*, "Any Video
Downloader Pro" reached one called *Downloader*, "The Bear S03" reached *Mori no
Kuma-san*. Any catalogue of this size holds entries whose English title is an
ordinary phrase, and a group carrying western TV, music and software keeps
producing releases that spell those phrases.

So the gate is about **jurisdiction, not confidence**: in a group whose traffic
is not anime, the scanner has no business guessing by title at all.

### It ships OFF

```yaml
plugins:
  anidbscraper:
    group_allowlist: ""       # empty (the default) = no gate at all
    group_gate_mode: refuse   # refuse | exact | report | off
```

The allowlist is empty on purpose. ameNZB ships `*anime*` because it measured
its own 62 crawled groups; that is a fact about ameNZB's traffic, not about
newsgroups. **A host that upgrades past this file and configures nothing tags
exactly what it tagged before** — pinned by `TestUnconfiguredGateIsInert`. An
empty allowlist means "no gate", never "block everything".

Patterns are one per line or comma-separated, case-insensitive, `*` matches any
run of characters, `#` starts a comment. A release is allowed if **any** group
it was seen in matches **any** pattern, so a crosspost into an anime group still
counts. A release with **no** newsgroup is always allowed — on the host that is
an upload or a scraped release, and refusing on absent evidence would silently
stop tagging them.

| Mode | Out-of-jurisdiction rows |
|---|---|
| `refuse` (default) | No title matching at all. |
| `exact` | Only an exact whole-title index hit — no prefix walk, no containment. Requires the injected `Matcher` to also implement `pluginapi.ExactTitleMatcher`; `Provision` refuses to boot otherwise rather than quietly running the fuzzy matcher the mode exists to switch off. |
| `report` | Nothing is blocked; gated rows are counted and the run log says what enforcing would have cost. Run this first when tuning an allowlist. |
| `off` | No gate, even with an allowlist configured. |

An unrecognised mode falls back to `refuse` and logs why. It does not turn the
gate off — that is the failure the gate exists to prevent — and it does not stop
the worker booting over one config string.

### Two ways to say "in scope"

The allowlist reads `pluginapi.NzbRow.Groups`, which is **optional**: a host
whose `NzbTagSink.UntaggedBatch` leaves it empty is a host where every row looks
like "no newsgroup", which the gate allows. That is what keeps the field
backwards-compatible, and it is also the way to configure a gate and get
nothing — so a configured pattern gate that saw no newsgroup on a single row of
a non-empty scan says so, loudly, in the run log.

If jurisdiction on your site is not a question about newsgroups — a tracker
category, a source flag, an origin column — wire `Deps.AllowTitleGuess` instead.
It replaces the allowlist (setting both is a `Provision` error, because the
plugin would have to ignore one of them); `group_gate_mode` still decides what
an out-of-scope row may be tagged by.

### It is a mirror — change both copies

`group_gate.go` is a 1:1 port of ameNZB's
`indexer-site/pkg/services/anidb_scan_group_gate.go`, down to the mode names and
the shape of the allow/verdict decision, and that host file names this one back.
The two already diverged once: the host added the gate, this copy kept guessing,
and any site running the plugin kept the pre-fix behaviour. **A change to the
decision belongs in both files, with its cases in both test files.**

Two differences are deliberate and worth knowing:

- ameNZB re-reads the allowlist from its job-settings table once per 5,000-row
  batch, so an operator can widen it mid-run. loon's `core.Job` carries no
  config vars, so this copy reads `config.yml` in `Provision` — a change needs a
  worker restart.
- The host's verdict carries a fourth field, `anilist`, permitting its
  last-resort AniList search on allowed rows only. This port has no such step
  yet, so the field would have no reader; it comes back with that body.

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
plugin.go          lifecycle, the three jobs, the run bodies and their EXTRACT markers
deps.go            the ports, why each is clean or entangled, and the one optional seam
group_gate.go      the newsgroup gate — a mirror of the host's, and the measurement behind it
group_gate_test.go the host's gate cases, ported with it
```

## Finishing it

Either complete the extraction or retire the package. It is currently a job
registered on a real scheduler with a body that cannot succeed, and that is the
one state it should not stay in — an operator sees three jobs on `/admin/jobs`
and one of them fails every run.

The markers say where each body comes from. The order that makes sense is
`buildTitleIndex` first (nothing else works without the index), then the
scanner, then the fill.
