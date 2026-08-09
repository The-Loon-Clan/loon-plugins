# release_groups plugin

The release-group directory: public group pages with release grids, ownership
claims verified by scraping a backlink token off the claimant's own page, news
posts fanned out to followers, owner-written bios, and a mirror of each
group's external archive (nekoBT / nyaa) with owner curation (hide, bulk
request-missing). Two worker jobs keep it fresh: the weekly nekoBT
group-profile scraper and the daily per-group archive sweep.

Lifted from the ameNZB host's in-repo plugin, markup included — five pages as
embedded fragments rendered through the host's `RenderPage` seam.

## Surface

- Authed (session-optional gate; per-action checks in-handler):
  `GET /release-groups`, `GET/POST /suggest-release-group`,
  `GET /release-groups/:slug` (+ `/suggest`, `/bio`, `/archive`),
  `POST /release-groups/:slug/{claim, claim/verify, bio, follow, news,
  news/:id/delete, archive/refresh, archive/request-missing,
  archive/torrents/:torrentID/hide}`.
- Public: `GET /v/:token` — claim-token redirect, outside the auth chain on
  purpose (third-party visitors click it).
- Jobs (worker): **Release Group Scraper** (weekly) and **Release Group
  Archive Sweep** (daily, off-peak). Historical names — interval overrides
  carry over. On a split web process both register as remote stubs so
  /admin/jobs can still show them.
- `Metadata.Processes`: `["web", "worker"]`.

## Data

- Owns **no tables**. release_groups and its satellite tables stay
  host-owned: the host's review-vote claim resolution, release-page owner
  badges, profile pages, and tag service read the same rows.

## Dependencies

- Core services: `Auth`, `Errors`, `Router`; `loon/schedule` (jobs) and
  `loon/httpclient` (the nekoBT API walk) directly.
- Store: `GroupStore` via SetDeps/SetJobDeps — the 30 methods the plugin
  calls out of the host repository's 50, names matching one for one.
- Function seams (host-owned machinery shared with the host's admin
  surface): `NewClaimToken`, `Backlink`, `VerifyClaim` (returns the
  plugin's `ErrVerifyTooSoon` / `ErrVerifyTokenNotFound`), `ScrapeArchive`,
  `FetchLogo`, `Slugify`, `RenderBioMarkdown`, `Markdown`, `RelativeTime`.
- Chrome: `RenderPage`, `RenderPagination` + `RenderPaginationParam`
  (the detail page's in-tab archive pager uses its own `tpage` param),
  `NzbCardCSS`, `CSRFToken`, `GroupNzbCards` (release cards arrive
  pre-rendered from the host — the lists-plugin pattern).
- Small seams: `CachedStat` (hourly tab counts, live-count fallback),
  `NzbIDByInfoHash`, `RequestExistsByInfoHash`, `CreateRequest`, `Notify`,
  `Viewer`, `BaseURL`.
- Config keys: none. `Metadata.Requires`: none.

## Hooks & Callbacks

- Host hooks set: (none).
- Extensions PUBLISHED: (none — publishing the archive-upsert seam the
  feeds plugin currently gets from host wiring is a noted follow-up).
- Extensions CONSUMED: (none).

## Lifecycle

- Provision: worker/all — validates JobDeps, registers both jobs +
  triggers; web — remote job stubs, validates Deps, parses templates,
  registers routes.
- Start: launches both loops via `schedule.ServiceLoop` off the root
  context — the scraper's old hand-rolled loop never died at SIGTERM; both
  do now. Also binds the handlers' background context (news fan-out,
  on-demand scrape).
- Stop: no-op; loops exit on context cancel.

## Files

- `plugin.go` — lifecycle + route table (historical paths kept exactly).
- `deps.go` — GroupStore + the seam contract, web and worker halves.
- `models.go` — full DTO mirrors of the host rows the templates read.
- `handlers.go` — the 18 route handlers + follower fan-out.
- `jobs.go` — both worker loops + the nekoBT API walk.
- `views.go` — embedded-template harness, chrome injection, exact host
  FuncMap copies (initial/urlPlatform/formatBytes/derefs — parity, not
  improvement).
- `templates/*.html` — the five pages as fragments.

## Testing

- Unit-tested: all five pages render over realistic data (list, detail
  with claims/news/archive tab, archive, suggest, bio edit), empty states,
  POST-form count equals CSRF-field count on every page (six forms
  historically leaned on the host's submit-time csrf-js injection and are
  explicit now, including the follow button's fetch, which sent no token
  at all), and the scraper's HTML-strip helper.
- Needs integration (live DB/host): the handlers against real stores and
  the two jobs against the live nekoBT API — covered operationally via
  /admin/jobs after deploy.
