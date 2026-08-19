# requests plugin

The community request board: filing requests for releases (with a mandatory
torrent link from an allowlisted source), voting, points-funded boosts,
scrape-assisted form prefill (nyaa / nekoBT / Tokyo Toshokan pages), torrent
search (nekoBT Torznab with Prowlarr fallback), and the fulfil / retry /
unpark lifecycle. Members see it at `/community/requests` and
`/community/request/<id>`; the calendar page's episode search rides the same
backends.

Lifted from the ameNZB host's in-repo plugin, markup included — both pages
are embedded fragments rendered through the host's `RenderPage` seam.

## Surface

- Authed (one `Authenticate` group over `/community`):
  - `GET /community/requests` — the board. Five tabs on one path via `?tab=`:
    member-filed (the default, `tab=open`), `tab=sourcing`, `tab=automated`,
    `tab=backlog`, `tab=feed`; plus the `?anime_id=` filter view, which is
    deliberately NOT origin-split ("who else wants this" wants every row).
  - `GET /community/request/:id` — detail
  - `POST /community/requests` — create (JSON or redirect, by Accept header)
  - `POST /community/requests/:id/{edit,delete,fulfill,retry,unpark,vote,boost}`
  - `POST /community/requests/bulk-delete` — mod-gated in-handler
  - `GET /community/requests/{scrape,search,lookup}` — form helpers (JSON)
  - `GET /community/calendar/search` — historical path; Torznab/Prowlarr search
- Role gates are in-handler via `Viewer.Contributor` / `Viewer.Mod` — what
  maps to those is the host's decision.
- `Metadata.Processes`: empty → web-only.

## Data

- Owns **no tables**. Everything (nzb_requests, request_votes / priorities /
  locks / actions, feed_items, the anime and NZB catalogs) is host-owned and
  shared with the host's agent-fulfilment pipeline and upload flows.
- **Origin** (`Request.Origin`, host migration 326) is read-only here: the host
  stamps which path filed each row and the board reads it to split the queue.
  It exists because the queue stopped describing itself — on 2026-08-18 the
  origin site had 6,420 open requests, 6,413 filed by its feed importer and 7
  by people. `Scope` (models.go) is the vocabulary both sides share; the words
  are the host's so a tab, its query and its badge cannot drift apart.

## Dependencies

- Core services: `Auth` (route gate), `Points` (boost spending → typed
  ledger), `Errors`, `Router`.
- Store seams via `SetDeps` (see deps.go): `RequestStore` (23 methods),
  `AnimeStore`, `NzbStore`, `FeedItemStore`, `AgentLockStore`,
  `AgentTokenStore` — interface method names deliberately match the host
  repositories they adapt.
- Chrome seams: `RenderPage` (4-arg, status crosses), `RenderPagination`
  (opaque HTML), `NzbCardCSS`, `CSRFToken`, `Markdown` (the host's
  SANITISING renderer — it crosses so there is exactly one allow-list).
- Vocabulary seams: `Viewer`, `BlockedExtension`, `SanitizeHTML` (the host's
  wiki sanitiser, for scraped page descriptions), `UpscaleOptions`,
  `PriorityTypes`, `BoostCost`, `BoostPerGB`.
- Optional (nil degrades one feature, not the page): `Prowlarr`, `Torznab`
  (`lpapi.TorznabSearch`, the feeds plugin's capability via the host's
  holder), `RefreshAnime`.
- Config keys: none. `Metadata.Requires`: none.

## Hooks & Callbacks

- Host hooks set: (none).
- Extensions PUBLISHED: (none).
- Extensions CONSUMED: (none directly — `Torznab` arrives through Deps; the
  host resolves `search.torznab` post-Boot into the holder it passes.)

## Lifecycle

- Provision: validates Deps, parses the embedded templates (FuncMap binds
  `Markdown`, so parse cannot happen at init), registers routes. Web-only.
- Start/Stop: no-ops.

## Files

- `plugin.go` — lifecycle + route table (historical paths, kept exactly).
- `deps.go` — the seam contract.
- `models.go` — full DTO mirrors of the host rows the templates read, plus
  the `Scope` vocabulary and the `?tab=` → scope map.
- `handlers.go` — the 15 route handlers + scrapers + search.
- `views.go` — embedded-template harness, chrome-key injection, local pure
  FuncMap helpers, JSON envelope copies.
- `templates/community_requests.html`, `templates/community_request_detail.html`
  — the two pages as fragments.

## Testing

- Unit-tested: boost-cost breakdown and URL validation (ported), title-meta
  and scrape parsers over HTML fixtures (ported), plus the render harness —
  every template branch executes over realistic data (open tab, feed tab,
  detail with active/failed locks, empty states), POST-form count equals
  CSRF-field count on every page, fragments carry no host chrome.
- `origin_test.go` covers the split at the handler level, where it actually
  lives: which SCOPE each `?tab=` reads (a tab wired to the wrong cut renders a
  perfectly normal page), that the default view excludes automation, that the
  needs-sourcing tab keeps shelved rows and renders what makes a request
  findable, and that a failed badge count costs a badge rather than the page.
- Needs integration (live DB/host): the 15 handlers against real stores —
  the seam adapters are exercised by the host's integration suite.
