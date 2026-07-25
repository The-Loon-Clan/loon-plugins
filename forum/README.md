# forum plugin

The site-wide discussion board: admin-curated categories → threads → replies with Discord-style emoji reaction bars, quote-replies with notifications, and a recruitment-thread visibility mode. It appears to users at `/community/forums/*` (index, category, thread, new-thread pages), to mods/admins at `/admin/forum-categories`, and as the "Community Spotlight" card on the public home page.

It is the second `pkg/core` plugin after the wiki. NOT part of this plugin: the Reddit-style user-owned communities (`/c/*`, the `communities` plugin), the member directory, and the community-moderation auto-hide machinery (the host stamps `hidden_at`; the forum SQL only *respects* it).

## Surface

Routes are registered in `Provision` on the raw `c.Router.Engine()` (keeping their historical top-level paths so bookmarks/templates don't break):

- **Authed (site default gate — `c.Auth.Authenticate()`)** on `/community/forums`:
  - `GET  ""` — category index (`Forums`)
  - `GET  /category/:id` — thread list (`ForumCategory`)
  - `GET  /thread/:id` — post list (`ForumThread`)
  - `GET  /new` — new-thread form (`NewThread`)
  - `POST /threads` — create thread (`CreateThread`)
  - `POST /thread/:id/reply` — reply (`ReplyThread`)
  - `POST /post/:id/edit` — edit post, owner-only (`EditPost`)
  - `POST /post/:id/delete` — delete post, owner-or-admin (`DeletePost`)
  - `POST /post/:id/react` — toggle reaction, JSON response (`ReactPost`)
  - `POST /thread/:id/delete` — delete thread, owner-or-admin (`DeleteThread`)

  Write handlers gate on the loaded session user themselves (redirect/JSON-401 when `userID == 0`), matching the pre-extraction behaviour.

- **Mod+ (`c.Auth.RequireUser(core.RoleMod)`)** on `/community/forums/thread`:
  - `POST /:id/pin` (`AdminPinThread`), `POST /:id/lock` (`AdminLockThread`) — both toggle.

- **Admin/mod (`c.Auth.RequireUser(core.RoleMod)`)** on `/admin/forum-categories`:
  - `GET ""`, `POST ""` (create), `POST /:id` (update), `POST /:id/delete`, `POST /:id/merge`.

**Process kinds:** `Metadata.Processes` is empty → **web-only** (registers routes; no worker/background work).

## Data

Tables live in the **public schema** (pre-PG17-consolidation, same as the wiki plugin), shipped via the host's numbered core migrations — the plugin declares no `Metadata.Migrations` of its own:

- `forum_categories` — mig **18** (created), **115** (color/icon), **196** (dedupe + `UNIQUE` index on `name`).
- `forum_threads` — mig **18**; `hidden_at`/`hidden_reason` added mig **203**; `thread_type` (CHECK `discussion`|`recruitment`) added mig **251**.
- `forum_posts` — mig **18**; `quoted_post_id` (FK `ON DELETE SET NULL`) added mig **123**; `hidden_at` via mig **203**.
- `forum_post_reactions` — mig **123**, composite PK `(post_id, user_id, emoji)`.

Reads `users` (username/role/avatar_path) via JOIN throughout. All list/detail queries filter `hidden_at IS NULL`.

**Gotchas:** `forum_posts.id` is `INT` (SERIAL), not BIGINT (mig 18/123). Recruitment visibility is enforced in SQL (`GetForumPosts`) so other applicants' rows never cross the wire — the OP is the lowest-id post.

## Dependencies

- **Core services** (constructor args via `NewHandlers`): `core.AuthService` (session user + role gate), `core.UsersService` (`DisplayName` for like notifications), `core.NotificationsService` (quote/like pings; self-notify skip lives in the host's `Notify`). Store is built from `core.Storage` (`c.Storage.DB()`) and routes from `core.Router` (`c.Router.Engine()`).
- **Store:** self-contained `*PGStore`, built in `Provision` from `c.Storage.DB()` (`NewPGStore(db)`). The `Store` interface is the former `storage.ForumRepository` surface plus `PostContext` (moved here from the notification repo because it is forum SQL). Two impls: `PGStore` (prod) and `MemStore` (in-memory, tests).
- **Config keys:** (none) — no `plugins.forum.*` config is read.
- **Metadata.Requires:** (none) — no peer-plugin dependencies.
- **`SetDeps` (required, before `core.Boot`):** the host seams the pages render through — `BaseData` (page-chrome injector), `Markdown` (post/quote body → safe HTML, the same renderer the host uses for its other user-authored surfaces), `Paginate` (builds the view-model the host's pagination partial consumes, returned as `any`). `Provision` fails loud when unset.
- **Host templates (the template contract):** the plugin renders the HOST's templates by name — `community_forums.html`, `community_category.html`, `community_thread.html`, `community_new_thread.html`, `admin_forum_categories.html` — so the host owns the skin. A host adopting this plugin supplies those five templates in its template set.

## Hooks & Callbacks

- **Extensions PUBLISHED (`Core.Register`):** `forum.spotlight` (`SpotlightName`, type `SpotlightFunc`) — the home-page Community Spotlight reader: the N most-recently-active threads, `nil` on error or when empty so the host skips the card. A host looks it up after Boot and assigns its home-page hook (ameNZB: `wireForumSpotlight` in `cmd/forum_wiring.go`). Notification kinds `forum_quote`/`forum_like` must stay in sync with the host preference UI's constants: quote fires on a reply that quotes an existing post; like fires on the "added" leg of a reaction toggle.
- **Extensions CONSUMED (`Core.Lookup`):** (none).

## Lifecycle

- **Provision:** verifies `SetDeps` was called (fails loud otherwise), builds the `*PGStore` from `c.Storage.DB()`, constructs `Handlers` over the Auth/Users/Notifications services, registers the three route groups on `c.Router.Engine()`, and publishes the `forum.spotlight` extension. Fails loud if `DB()` or `Engine()` is nil.
- **Start / Stop:** no-ops — the plugin runs no background work.

## Files

- `plugin.go` — `core.Plugin` lifecycle: `init()` registration, `Deps`/`SetDeps`, `Provision` (build store + handlers, register routes, publish the spotlight extension), no-op `Start`/`Stop`.
- `store.go` — the `Store` interface (categories/threads/posts/reactions/sidebars + `PostContext`) and the `PostContext` projection type.
- `models.go` — row structs: `ForumCategory`, `ForumThread`, `ForumPost`, `ForumReactionCount`, `ForumActivityItem`, `ForumContributor`, and the `ForumThreadType*` constants.
- `pg.go` — `PGStore`, the production Postgres impl over `sqlx.DB`.
- `mem.go` — `MemStore`, the in-memory impl with `Seed*` helpers + deterministic `SetClock`.
- `handlers.go` — public + write handlers, the reaction-emoji allowlist (`isAllowedReactionEmoji`, must match the picker in `community_thread.html`), notification dispatch.
- `admin.go` — category management handlers (create/update/delete/merge) + `redirectCategories`.
- `mem_test.go` — table-driven MemStore tests (categories, threads, posts, reactions, sidebars, gating).
- `forum_test.go` — pure-helper tests (reaction-emoji allowlist).

## Testing

- **Unit-tested (no DB):** the `MemStore` mirrors every `Store` method and is exercised end-to-end in `mem_test.go` — category rollups/ordering/counts, duplicate-name rejection, delete/merge guard rails, thread pinning/hiding/gating, quote excerpts, recruitment visibility, reaction round-trips, and the two sidebar aggregations. `forum_test.go` covers the pure `isAllowedReactionEmoji` allowlist (each allowed emoji, look-alikes without the variation selector, and injection attempts).
- **Needs integration tests (live DB):** `PGStore` is thin SQL delegation — the LATERAL last-reply subqueries, the `GROUP BY` rollups, the recruitment `visibilityClause` string assembly, the `ON CONFLICT DO NOTHING` reaction toggle, the transactional thread+first-post insert, and the FK `ON DELETE CASCADE`/`SET NULL` behaviour can only be verified against real Postgres. The MemStore approximates these but a mocked-DB test would (per the repo's "mocked DB tests masked a migration bug" lesson) not prove the SQL.
