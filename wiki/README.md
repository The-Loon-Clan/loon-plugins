# wiki plugin

The site's knowledge base: admin-authored **topics** (folders like Help / FAQ / guides) each containing long-form **markdown posts**. Readers browse it at `/wiki` (landing page with a category sidebar, "Recent Changes" and "Popular Articles" panels, and a "Random Page" shortcut); mods and admins manage topics and posts under `/admin/wiki`.

It is the first real `pkg/core` (loon-framework) plugin POC — models, storage, handlers, and routes all live in this one package, wired to the host through the Core mediator at Provision time.

## Surface

Routes keep their historical top-level paths (not the `/plugin/wiki/*` default) because `RouterService.Engine()` grants direct access to the Gin engine, and moving them would break bookmarks / templates / sitemap URLs.

- **Public** — group `/wiki`, gated by `c.Auth.Authenticate()` (follows the site access policy: login-required in closed mode, anonymous browsing in public mode):
  - `GET /wiki` — landing (Index)
  - `GET /wiki/recent` — RecentChanges
  - `GET /wiki/random` — Random (302 to a random post)
  - `GET /wiki/:topic` — Topic
  - `GET /wiki/:topic/:post` — Post (bumps view_count best-effort)
- **Admin** — group `/admin/wiki`, gated by `c.Auth.RequireUser(core.RoleMod)` (mod-or-above):
  - `GET /admin/wiki` — admin landing (AdminIndex)
  - Topic CRUD: `GET /topics/new`, `POST /topics`, `GET /topics/:id/edit`, `POST /topics/:id/update`, `POST /topics/:id/delete`
  - Post CRUD: `GET /posts/new`, `POST /posts`, `GET /posts/:id/edit`, `POST /posts/:id/update`, `POST /posts/:id/delete`
  - `POST /admin/wiki/upload` — image upload for post markdown (UploadImage)
- **api** — none.
- **Process kinds** — `Metadata.Processes` is empty → **web-only** (the safe default; registers routes, no worker/background jobs).

## Data

- Owns two tables, both in the **public schema** via the core **numbered** migrations (pre-PG17-consolidation), so `Metadata.Migrations` is empty:
  - `wiki_topics` — migration **6**; `icon` + `color` columns added in host
    migration **281**. Both default to `''`, which means *use the default*:
    the icon falls back to the slug-derived glyph and the colour to the theme
    accent, so a topic nobody has edited looks exactly as it always did.
    `icon` stores a KEY from `TopicIcons` (`icons.go`), never markup — topic
    icons render as inline SVG, so a free-text field would be stored XSS for
    the sake of a folder picture. `color` is normalized to `#rrggbb` on the
    way in and anything else becomes empty.
  - `wiki_posts` — migration **6** (FK `topic_id → wiki_topics(id) ON DELETE CASCADE`); `view_count BIGINT` column + `idx_wiki_posts_view_count` added in migration **250**.
- **Reads** the host `users` table (LEFT JOIN in RecentPosts / PopularPosts / RandomPost for `created_by_username`).
- The move to a dedicated `wiki` schema (at which point `Metadata.Migrations` takes over) is deferred to the PG17 baseline consolidation.
- Not owned here: the cross-entity wiki-**edit** review framework (`wiki_edits` / `wiki_edit_changes`) — that is host-owned (`storage.WikiEditRepository`), unrelated to this knowledge base.

## Dependencies

- **Core services consumed** (off `*core.Core`):
  - `Storage` — `c.Storage.DB()` supplies the shared pool the `PGStore` is built over.
  - `Router` — `c.Router.Engine()` for direct Gin engine access (historical top-level paths).
  - `Auth` — `c.Auth.Authenticate()` / `c.Auth.RequireUser(core.RoleMod)` route gates; `Handlers` holds `core.AuthService` and `CreatePost` uses `auth.CurrentUser(c)` to stamp `created_by`.
- **Store** — **self-contained**, no `SetDeps`. `Provision` builds a `PGStore` over `c.Storage.DB()`; tables are plugin-private-in-intent (currently public schema). `Store` is the plugin's own interface with two impls: `PGStore` (production) and `MemStore` (in-memory, tests). SQL is byte-identical to the pre-extraction `pkg/storage/postgres/wiki.go`.
- **Config keys** (`plugins.wiki.*`) — none.
- **Metadata.Requires** (peer plugins) — none.
- **`SetDeps` (required, before `core.Boot`)** — the host seams: `BaseData` (site template chrome), `Markdown` (the renderer the host's wiki pages use — authors are mods+, so richer markup than user-authored surfaces is fine), and `Files` (a `loon/blob.Store`) for admin image uploads — the plugin saves under the store's `wiki-uploads/` namespace and the host decides where that lives (local static dir today, a remote file host later). `Provision` fails loud when unset. JSON error envelopes are plugin-local.
- **Host templates (the template contract)** — the plugin renders the HOST's templates by name: `wiki.html`, `wiki_topic.html`, `wiki_post.html`, `admin_wiki.html`, `admin_wiki_topic_form.html`, `admin_wiki_post_form.html`. A host adopting this plugin supplies those six in its template set.

## Hooks & Callbacks

- **Host hooks SET** (`web/handlers/plugin_hooks.go`) — (none). No hook references wiki.
- **Extensions PUBLISHED** (`Core.Register`) — (none).
- **Extensions CONSUMED** (`Core.Lookup`) — (none).

## Lifecycle

- **Provision** — builds the `PGStore` from `c.Storage.DB()`, constructs `Handlers` with `c.Auth`, grabs `c.Router.Engine()`, and registers the `/wiki` (public) and `/admin/wiki` (mod-gated) route groups. Fails loud if `DB()` or `Engine()` is nil. Static-path routes (`/recent`, `/random`) are registered before the `/:topic` wildcard so Gin's radix tree matches them first.
- **Start / Stop** — no-ops. No background jobs, cache refreshers, or connections.

## Files

- `plugin.go` — `core.Plugin` lifecycle: `init()` registration, Metadata, Provision (builds store + handlers, registers routes); Start/Stop no-ops.
- `models.go` — package doc + `Topic`, `Post`, `RecentPost` structs.
- `store.go` — `Store` interface (the plugin's storage seam + absence contract).
- `pg.go` — `PGStore`, the production SQL implementation.
- `mem.go` — `MemStore`, the in-memory implementation for tests.
- `handlers.go` — public + admin Gin handlers, `makeSlug`, `postsByTopicMap`.
- `upload.go` — `UploadImage` handler (MIME-sniffed image upload via `blob.Store`).
- `mem_test.go` — MemStore + `makeSlug` unit tests.
- `wiki_test.go` — handler-level pure-logic tests (`postsByTopicMap`).

## Testing

- **Unit-tested** (`mem_test.go`): the full `MemStore` behavior — topic ordering + post_count join, slug/id lookups with `sql.ErrNoRows` on miss, update/delete FK cascade, server-field stamping, `updated_at` bump semantics, `AllPosts` content-blanking, RecentPosts (updated_at order + limit), PopularPosts (view-count order + tiebreak), chronological PostsByTopic, RandomPost empty/hit, and defensive cloning. Plus `makeSlug` (lowercase, punctuation strip, whitespace/dash collapse, unicode). `wiki_test.go` adds `postsByTopicMap` (flat AllPosts folded into a topic-keyed sidebar map, plus the empty-store path).
- **Template-tested host-side**: the topic icon/colour fallback is template
  logic in the host's `wiki.html`, so it is pinned by render tests in the
  host's `web/handlers` (parse the real file, assert an unset icon still draws
  the slug glyph and an explicit one overrides it). It cannot live here — the
  template is not this repo's.
- **Requires integration (live DB)**: `PGStore` — the SQL JOINs to `wiki_topics`/`users`, `ON DELETE CASCADE`, `ORDER BY RANDOM()`, `view_count` COALESCE, and the migration-6/250 schema itself are not exercised by MemStore (deliberately thin maps + mutex). The `UploadImage` storage/MIME path (bytes land wherever the host's `blob.Store` puts `wiki-uploads/*`, MIME sniffed via `blob.SniffImage`, 5 MB cap; the Store itself is unit-tested in loon) and the HTML-rendering handlers (template + BaseData chrome) also need an HTTP/integration harness rather than pure unit tests.
