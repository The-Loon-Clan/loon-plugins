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

- **authed** — `GET /c`, `GET /c/new`, `POST /c`, `GET /c/:slug`,
  `POST /c/:slug/subscribe`, `GET /c/:slug/submit`, `POST /c/:slug/submit`,
  `GET /c/:slug/thread/:id`, `POST /c/:slug/thread/:id/reply`
- **mod/owner** — `GET|POST /c/:slug/settings`, `GET /c/:slug/requests`,
  `POST /c/:slug/invites`, `POST /c/:slug/requests/:rid/approve|deny`,
  `POST /c/:slug/thread/:id/pin|lock|remove`,
  `POST /c/:slug/thread/:id/post/:pid/remove`
- **invite redemption** — `GET /c/join/:code`

Process kinds: none declared, which core treats as **web-only**. There is no
background work.

The plugin owns all seven pages' markup: `templates/*.html` are embedded
fragments rendered from the plugin's private template set, wrapped in the
host's chrome through `Deps.RenderPage`. View models are structs, not maps —
a fragment reading a field nobody supplies is a render error the tests
catch, not an empty string in production.

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

`Deps` (SetDeps before `core.Boot`) — required on **both** contracts:

- `Markdown` — renders post bodies. The host owns the dialect because the same
  policy governs the forum, and it sanitises: a second allow-list inside the
  plugin would be a stored-XSS bug waiting on whichever copy is laxer.
- `PageOffset` — the host's paging arithmetic (shapes the query, not markup).

Current contract (the plugin renders its own embedded fragments):

- `RenderPage` — wraps a finished fragment in the site chrome; status crosses
  because the create form re-renders with 400 on validation failure.
- `CSRFToken` — feeds every POST form's hidden `_csrf` input.
- `RenderPagination` — the host's pager as finished HTML.
- `RenderEditor` — the host's shared markdown editor as finished HTML (the
  new-thread form uses it).
- `RelativeTime` — the site's time wording.

Optional on either contract:

- `Files` — `blob.Store` for banner/icon uploads. A host without one loses
  banner images and keeps everything else.

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

- **Provision** — checks a complete render contract is wired (a missing seam
  is a nil call on the first request, not a degraded page), parses the
  embedded fragments on the current contract (a parse failure fails boot,
  not the first page view), builds the `PGStore` over `Core.Storage.DB()`,
  constructs the handlers over `Auth`/`Points`/`Errors`, registers the `/c`
  route group, and publishes `communities.followed`.
- **Start / Stop** — no-ops. There is no background work.

## Files

- `plugin.go` — lifecycle, route table, extension publication.
- `deps.go` — the host seams (both contracts), the `communities.followed`
  contract, `noRowsAsNil`.
- `views.go` — embedded fragment set, FuncMap, struct view models, the
  dual-contract `render`, the widget seam helpers, the rune-safe
  `initial`/excerpt helpers.
- `templates/*.html` — the seven page fragments, lifted verbatim from the
  origin host (chrome stripped; the pagination partial and md-editor arrive
  pre-rendered through the seams).
- `handlers.go` — every HTTP handler and its gating.
- `store.go` — the `Store` interface.
- `pg.go` — the Postgres implementation.
- `models.go` — plugin-private types.
- `upload.go` — banner/icon decode, resize, re-encode, store.
- `schema.sql` — the tables, for a host that lacks them.

## Testing

- Unit-tested: slug validation, join-gate decisions, and the pure helpers in
  `communities_test.go`.
- Render-tested (`views_test.go`): every fragment executes over realistic
  fixture data with a content marker proving the data landed and a
  chrome-leak check proving no host chrome is embedded; every POST form is
  counted against its `_csrf` field (a mismatch is a form that 403s on
  submit); the empty states render; the fragment set parses in one pass; and
  a half-wired mixture is refused — a host that wired some render seams would serve some pages and blank others.
- Needs integration (live DB): `PGStore` is a SQL passthrough — the join-escrow
  flow in particular spans several statements and a points call, and only a
  real database shows whether the escrow is refunded on deny. That gap predates
  the extraction and moved with it.
