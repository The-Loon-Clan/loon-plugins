# news plugin

Site-news system: the public `/news` feed and per-post `/news/:slug` pages, plus a mod-only admin editor under `/admin/news`. It also feeds the host home page's "Latest news" card by publishing the `news.home` extension.

Fully self-contained — it owns its `NewsPost` model and Postgres store and requires no peer plugins.

## Surface

Routes are registered in `Provision` on the shared Gin engine (not a plugin sub-router):

- **Authed** (`c.Auth.Authenticate()` — follows the site access policy):
  - `GET /news` — `NewsAll`, lists up to 50 published posts.
  - `GET /news/:slug` — `NewsDetail`, one post by slug (redirects to `/news` on miss).
- **Admin** (`c.Auth.RequireUser(core.RoleMod)` — mod or above):
  - `GET /admin/news` — `NewsList`, all posts (published + drafts).
  - `POST /admin/news` — `CreateNews`.
  - `GET /admin/news/:id/edit` — `EditNewsPage`.
  - `POST /admin/news/:id/update` — `UpdateNews`.
  - `POST /admin/news/:id/delete` — `DeleteNews`.

Process kinds: `Metadata.Processes` is empty → **web-only** (registers routes; never booted in a worker-only process).

## Data

- Owns one table, `news_posts` (`id, title, slug UNIQUE, body, published, created_at, updated_at`).
- Table lives in the **public schema** (pre-PG17-consolidation), created by the ameNZB host migration **20** — not a per-plugin `migrations/*.sql` embed. `Metadata.Migrations` is empty. An adopting host supplies the table.

## Dependencies

- **Core services**: `Storage` (`c.Storage.DB()` → builds the `*PGStore`), `Router` (`c.Router.Engine()` for route registration), `Auth` (`Authenticate()` + `RequireUser(core.RoleMod)` middleware chains), `Errors` (`c.Errors` stored on `Handlers.errs` — currently held but not actively called).
- **Store**: self-contained `*PGStore` built at `Provision` from `c.Storage.DB()` (nil DB → boot error). The `Store` interface has 7 methods (list published / list all / by id / by slug / create / update / delete). SQL is byte-identical to the pre-lift in-tree plugin.
- **`SetDeps` (required, before `core.Boot`)** — the host seams: `RenderPage` (wraps a finished fragment in the site chrome) and `Sanitize` (the host's news-body HTML sanitization policy, applied before bodies render unescaped). `Provision` fails loud when unset. `Sanitize` crosses the seam rather than being reimplemented here for the usual reason: a second allow-list is a stored-XSS bug waiting on whichever copy is laxer.
- **Config**: none. No `plugins.news.*` keys.
- **Metadata.Requires**: none (no peer plugins).
- **Templates are the plugin's**, embedded under `templates/`: `news.html`, `news_detail.html`, `admin_news.html`, `admin_news_form.html`. They were the host's until this lift, which meant the plugin could not change its own pages and the site could not delete them. An adopting host supplies chrome through `RenderPage`, not four page templates.

## Hooks & Callbacks

- **Host hooks SET**: (none — the plugin has no host import).
- **Extensions PUBLISHED** (`Core.Register`): `news.home` (`HomeFeedName`) — a `HomeFeedFunc` returning up to 4 sanitized, template-ready published posts. The host Looks it up after `core.Boot` and wires it into its home handler (ameNZB: `wireNewsHome` → `handlers.HomeNews`); a host that never looks it up simply has no news card.
- **Extensions CONSUMED** (`Core.Lookup`): (none).

## Lifecycle

- **Provision**: fails loud if `SetDeps` was skipped, validates `Storage.DB()`/`Router.Engine()` are non-nil, builds the `*PGStore` + `Handlers`, publishes the `news.home` extension, and registers the `/news` + `/admin/news` route groups.
- **Start / Stop**: no-op (no background work — no goroutines, jobs, or connections).

## Files

- `plugin.go` — plugin registration (`init()`), `Deps`/`SetDeps`, `Metadata`, `Provision` (routes + the `news.home` extension), all HTTP handlers, and the `slugify` helper.
- `models.go` — `NewsPost` struct (db tags).
- `store.go` — `Store` interface (7 methods).
- `store_pg.go` — `PGStore`, the sqlx-backed production implementation.
- `views.go` — the embedded templates and `render`.
- `templates/` — the four pages, as fragments.

## Testing

- **Unit-tested** (`news_test.go`): `slugify` — the one pure helper. Table-driven cases cover lowercasing, non-alphanumeric run collapsing to a single dash, leading/trailing dash trimming, empty/whitespace input, and Unicode stripping. It mirrors the ameNZB host `admin_handler` slug helper byte-for-byte so new-post slugs keep a consistent shape.
- **Unit-tested** (`views_test.go`): all four pages, **executed** rather than only parsed — a lift needs that, because html/template streams and a field the markup wants but the data lacks aborts the render part way through and returns half a page with nothing logged. Fixtures mirror the keys the handlers actually pass.
- **Needs integration tests (live DB)**: `PGStore` is a thin SQL passthrough — every method is a direct sqlx `Select/Get/QueryRow/Exec`. Its correctness (the `slug UNIQUE` constraint, `published = true` filter, `created_at DESC` ordering, `RETURNING` scan, `updated_at = NOW()` bump) can only be verified against a real Postgres. There is no in-memory store; a MemStore built solely to test map delegation would be coverage theater, so none was added.

## Notes / gotchas

- Bodies are rendered as `template.HTML` (unescaped). `NewsDetail` and the `news.home` card run `Deps.Sanitize` first; **`NewsAll` (the `/news` list) marks `p.Body` as raw HTML without sanitizing** — an inconsistency to be aware of (list bodies are trusted mod-authored content).
- Admin write handlers return plain 500 strings rather than a JSON envelope (these are mod-only HTML form posts, not JSON APIs).
