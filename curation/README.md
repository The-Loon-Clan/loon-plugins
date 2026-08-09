# curation plugin

Season/episode inference for crawled releases. Every release the crawler
indexes lands with `season_num`/`episode_num` NULL — the assembler only has
the subject line, and fansub convention drops "S1" for a first season, so a
large share of a real catalog can never be filled from titles alone. This
plugin closes the gap with metadata the site already holds and applies every
rule automatically; what no rule can infer becomes the fail-to-parse queue on
the admin page. Operators see it at `/admin/p/curation`; members see the
effect everywhere seasons matter (season tabs, completion grids, and the
Newznab API, whose `season=`/`ep=` filters are strict equality and silently
exclude unfilled rows).

Rules, in trust order (`rules.go`):

1. **title** — the host's canonical parser found a marker in the release
   title itself.
2. **meta-ordinal** — the AniDB entry the release is tagged to is named as a
   sequel season ("2nd Season", "Season 4", a roman or single-digit numeral
   suffix). AniDB names each season as its own entry, so the entry name is
   authoritative.
3. **meta-single-season** — TMDB (via the host's `completion_buckets` season
   cache) says the show has exactly one season and the entry is a seasonal
   format: it can only be season 1.
4. **non-seasonal** — movies, OVAs and specials keep NULL on purpose; the
   display layer buckets them by keyword, and a written season would relabel
   a movie as "Season 1" on every surface.
5. **unresolved** — nothing applied; the row stays NULL and is listed.

Episodes are only ever taken from the title — a season pack or a movie
correctly has none, and metadata cannot know which episode a single file is.

## Surface

- `/admin/p/curation` (SlotAdminPage, RoleAdmin, nav group Catalog) — fill
  coverage stats, the rules legend, and the newest-first season worklist with
  the decision the next sweep would take per row (computed live).
- Job **"Season Curation"** on the worker (daily, off-peak gated,
  admin-triggerable from /admin/jobs).
- Processes: `web` (page), `worker` (sweep).

## Data

- No owned tables. Reads/writes host data through seams: `nzbs`
  (season/episode fill, COALESCE-when-NULL so manual edits always win),
  `anime_metadata` and `completion_buckets` (read-only facts).

## Dependencies

- `Deps` via `SetDeps` before `core.Boot`, all fields required:
  `ListSeasonNull` / `PageSeasonNull` (worklist), `SetSeasonEpisode` (fill),
  `AnimeFacts` (entry name, format/type, TMDB season count — the host seam
  must count only `source='tmdb'` buckets, because the AniList fallback
  writes one bucket for every entry it touches), `ParseSeasonEpisode` (the
  host's one canonical title parser — deliberately not duplicated here), and
  `Stats`.
- No config keys. No `Metadata.Requires`.

## Hooks & Callbacks

- Extensions CONSUMED: `notify.ops` (`pluginapi.OpsNotifier`), looked up at
  end of each sweep to post the run summary to the operators' channel;
  absence degrades to no delivery.
- Extensions PUBLISHED: (none).
- Host hooks SET: (none).

## Lifecycle

- Provision: refuses to boot on partial Deps; registers the admin view (web)
  and the job (worker).
- Start: worker starts the daily `schedule.ServiceLoop` (15 min boot delay so
  it never races the title cleaner for the same rows). Stop: no-op.
- The sweep is stateless: still-NULL rows are re-scanned every run, which is
  what lets a newly linked TMDB id or a corrected entry name resolve
  yesterday's failures with no bookkeeping. The per-run facts cache keeps
  that at one metadata read per distinct anime.

## Files

- `plugin.go` — registration, metadata, view + job wiring.
- `deps.go` — the host seams and DTOs.
- `rules.go` — the decision engine (pure; the part worth testing hardest).
- `sweep.go` — the daily worklist walk + counters + ops notification.
- `views.go` / `templates/curation.html` — the admin page.

## Testing

- Unit-tested: the full rule table (`TestDecide`), ordinal extraction
  including the lookalikes that must NOT match (`Mob Psycho 100`,
  `Steins;Gate 0`), and a full-VM render of the page fragment.
- Needs integration (live DB): the host-side seam queries (worklist paging,
  the TMDB-source bucket count) — exercised through the host's own storage
  tests.
