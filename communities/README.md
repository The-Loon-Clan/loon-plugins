# communities plugin

User-owned sub-forums at `/c/*` — a member creates a community, sets its join
gating, appoints mods, writes rules, and the members post threads and replies
under it. Join gating is the interesting part: a community is open,
request-and-approve with a points escrow, or invite-only.

Users see it as the `/c` index, each community's page, and a "Following" tab on
their account settings.

## Surface

Routes, all through the host's router at their historical paths and all under
the site's default access policy (`Authenticate`); per-action gating is done
inside the handlers, because whether you may post depends on the community, not
on the route.

- **authed** — `GET /c`, `GET /c/new`, `POST /c/new`, `GET /c/:slug`,
  `POST /c/:slug/subscribe`, `GET /c/:slug/submit`, `POST /c/:slug/submit`,
  `GET /c/:slug/thread/:id`, `POST /c/:slug/thread/:id/reply`
- **mod/owner** — `GET|POST /c/:slug/settings`, `POST /c/:slug/invite`,
  `POST /c/:slug/requests/:id/approve|deny`,
  `POST /c/:slug/thread/:id/pin|lock|remove`,
  `POST /c/:slug/thread/:id/post/:pid/remove`
- **invite redemption** — `GET /c/invite/:code`

Process kinds: none declared, so it provisions everywhere the host runs. There
is no background work.

## Data

Eight host-owned tables: `communities`, `community_mods`,
`community_subscribers`, `community_rules`, `community_threads`,
`community_posts`, `community_join_requests`, `community_invites`.

The plugin owns none of them. On the origin site they arrived as host
migrations 252/253/255 in the public schema and stayed there through the
extraction — moving live tables into a plugin schema is a data migration, and
this port moved code. `schema.sql` documents them so a host that does not have
them can create them in one step.

## Dependencies

Core services consumed: `Auth` (identity), `Points` (join escrow — this was the
first plugin to exercise the Points facade, with typed
`spend_community_join` / `refund_community_join` ledger entries), `Storage`
(the shared `*sqlx.DB`), `Router` (route registration), `Errors`.

Store: self-contained `PGStore` built at Provision. No `MemStore` — the store
is a SQL passthrough and a map-backed double would test delegation rather than
behaviour.

`Deps` (SetDeps before `core.Boot`):

- `BaseData` — merges host page chrome into a template map. Communities is a
  full-page surface, so the templates stay host-side and the plugin fills them.
- `Markdown` — renders post bodies. The host owns the dialect because the same
  policy governs the forum, and two markdown flavours on one site reads as a
  bug.
- `PageOffset`, `Pagination` — the host's paging helpers, passed rather than
  copied: the value is consumed by the host's pagination *partial*, so a lifted
  copy would compile and render right up until the partial changed.
- `Files` — optional `blob.Store` for banner/icon uploads. A host without one
  loses banner images and keeps everything else.

Config keys: none. `Metadata.Requires`: none.

## Hooks & Callbacks

- Host hooks SET: **(none)** — in-tree this assigned `handlers.CommunitiesFollowed`
  directly; extracted, it publishes instead.
- Extensions PUBLISHED: `communities.followed` (`FollowedFunc`) — the account
  page's "Following" tab. The host looks it up after `core.Boot` and renders
  whatever it returns, duck-typed in the template. A host that never looks it
  up simply has no Following section.
- Extensions CONSUMED: **(none)**

## Lifecycle

- **Provision** — checks the render seams are wired (a missing one is a nil
  call on the first request, not a degraded page), builds the `PGStore` over
  `Core.Storage.DB()`, constructs the handlers over `Auth`/`Points`/`Errors`,
  registers the `/c` route group, and publishes `communities.followed`.
- **Start / Stop** — no-ops. There is no background work.

## Files

- `plugin.go` — lifecycle, route table, extension publication.
- `deps.go` — the host seams, the `communities.followed` contract, `noRowsAsNil`.
- `handlers.go` — every HTTP handler and its gating.
- `store.go` — the `Store` interface.
- `pg.go` — the Postgres implementation.
- `models.go` — plugin-private types.
- `upload.go` — banner/icon decode, resize, re-encode, store.
- `schema.sql` — the tables, for a host that lacks them.

## Testing

- Unit-tested: slug validation, join-gate decisions, and the pure helpers in
  `communities_test.go`.
- Needs integration (live DB): `PGStore` is a SQL passthrough — the join-escrow
  flow in particular spans several statements and a points call, and only a
  real database shows whether the escrow is refunded on deny. That gap predates
  the extraction and moved with it.
