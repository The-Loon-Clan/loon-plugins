# playlists plugin

User-curated collections of indexed releases — UNIT3D's playlist area. A member
ticks things they mean to come back to, or publishes a set worth sharing, and
the site gets a browsable list of both.

**Self-contained by design.** It owns its schema, needs no points, no
entitlements and no external service. Its only host seams are page chrome,
paging, and two lookups it cannot do itself.

Those two lookups are the whole portability story. A plugin cannot join a
host's `users` table or query a host's release index — it does not know their
shape, and assuming one is exactly what leaves a plugin unwirable on a host
whose columns differ. So it stores ids and asks the host to resolve them.

**Deliberately absent:** following someone else's playlist, collaborative
editing, and ordering by anything but the position a member put things in. The
first two need a notion of *other people's* playlists that this does not have —
the sink below deliberately offers a member only their own — and adding them is
a permissions model, not a column.

## Surface

Two route groups rather than one with per-handler checks, so the authentication
boundary is visible in the route table.

| Route | Access | Notes |
|---|---|---|
| `GET /playlists` | `Authenticate()` | Public playlists plus the viewer's own. Anonymous sees public only — a viewer id of 0 is a real value here, meaning "public only", not an error. |
| `GET /playlists/:slug` | `Authenticate()` | A private playlist answers **404, not 403**: a 403 confirms the slug exists, which is the one thing "private" is meant to withhold. |
| `GET /playlists/new` | `RequireUser` | |
| `POST /playlists` | `RequireUser` | Create. |
| `GET/POST /playlists/:slug/edit`, `/update` | owner | |
| `POST /playlists/:slug/delete` | owner | |
| `POST /playlists/:slug/items` | owner | |
| `POST /playlists/:slug/items/:id/delete` | owner | |

Ownership goes through `pluginapi.OwnedBy`, never a bare `p.UserID == viewer`:
`Authenticate()` lets anonymous requests through in the site's public access
mode, so the viewer id really can be 0, and 0 is also the reserved system id.

No jobs. No widgets. `Processes: ["web"]`.

**Templates live in the host**, not here: `web/templates/plugin/playlists_index.html`,
`playlist_view.html`, `playlist_form.html`. The host executes all three against
the real view models in `plugin_templates_test.go`, which is what stops a
renamed field truncating a page mid-render with a 200.

## Data

Owns `playlists.playlists` and `playlists.items`.

**`slug` is unique GLOBALLY, not per user.** Two owners cannot both hold
`best-of-2026`, because the URL has no room for the owner. `slugify` therefore
has to always produce something usable — never empty, no leading or trailing
dash, at most 60 characters — and falls back to `list-<n>` for a name with
nothing usable in it, because a playlist with no address cannot be opened.

**Private by default.** `public` defaults to `false`: a collection is somebody's
working list until they say otherwise, and defaulting to public would have
published every existing one retroactively the moment the column arrived.

`items` is unique on `(playlist_id, release_id)`, so adding something twice is
not an error and not a second copy.

**A release id that no longer resolves stays in the table.** Retention removes
releases and a collection outliving its contents is normal rather than
exceptional; the row renders as unavailable instead of vanishing, so a curator
can see what they lost. Templates must handle a nil `Release`.

## Dependencies

| Seam | Required | Why the host supplies it |
|---|---|---|
| `RenderPage(c, status, title, body)` | yes | Chrome around a finished fragment. `status` crosses because the create form re-renders on a validation failure, and a seam fixed at 200 reports success while showing an error. |
| `CSRFToken(c)` | yes | Only the middleware that mints it can answer. Injected once in `render()`, so no call site can forget it and ship a form that 403s. |
| `RenderPagination(...)` | yes | The site's pager as finished HTML. The index used to build its own `<nav>` from a view model — six fields where one seam does. |
| `RelativeTime(v)` | yes | The site's time wording. |
| `RenderUserTag(name)` | no | The site's username chip — role colour, name effects, profile link. Without it the owner is a plain link to their profile. |
| `PageOffset(page, size)` | yes | The host's paging arithmetic: it shapes the query, not the markup. |
| `LookupReleases(ctx, ids)` | yes | Resolves release ids to something renderable, in ONE call for a whole page. Ids the host does not return stay nil. `Provision` refuses to start without it — a playlist that cannot resolve its releases has nothing to show. |
| `LookupUsername(ctx, id)` | no | Owner display name. Without it the owner shows as a bare id: ugly, not wrong. |

Core: `Storage.SchemaDB`, `Auth`, `Router`, and the mediator for events.

## Hooks & callbacks

**Publishes** `pluginapi.CollectionSink` under `collections.sink` — this is
where the host's cart empties. The host lets a member tick rows across a
listing and then put them somewhere, without knowing that collections are
playlists or that this plugin exists.

- `CollectionsOf` lists **the member's own playlists only**, and that is access
  control rather than convenience: the host renders whatever comes back as
  choices, so anything listed is something the member is being invited to write
  to. A public playlist belonging to somebody else is readable and must not
  appear.
- `AddToCollection` takes a whole batch in **one statement** — the caller is a
  cart and a cart holds forty things; forty round trips each re-reading
  `MAX(position)` is a page load somebody notices. The ownership check is IN the
  statement (the INSERT selects the playlist by slug *and* user_id), so a slug
  belonging to somebody else matches no row and writes nothing, with no separate
  read to race. Already-present releases are skipped, and the count returned is
  what was **added**.

**Declares** `playlists.created` and `playlists.item.added`, both stable member
events, emitted after the write commits.

Neither is countable, and the events file says so: both are free actions on rows
nobody else has to accept, so a count measures clicking. What would be worth a
badge is a curation metric — public playlists with real length, or ones other
people follow — which is an absolute count over current rows and self-heals when
a member deletes half their collection. An event stream cannot: it only ever
adds, so a "50 items" badge earned by adding fifty and deleting forty-nine would
stay earned.

## Lifecycle

`Provision` requires the four mandatory seams, opens the schema DB, declares the
events, registers the collection sink, and mounts the two route groups. `Start`
and `Stop` are no-ops — there is no background work.

Migrations run through the host's plugin-migration runner with `search_path`
scoped to the `playlists` schema, so unqualified names resolve to
`playlists.playlists` / `playlists.items`.

## Files

```
plugin.go       lifecycle, Deps, routes, handlers, the ownership gate
store.go        the Store interface, PGStore, slugify
sink.go         the CollectionSink — where the host's cart empties
events.go       the two declarations, and why neither is countable
models.go       Playlist, Item, Release
migrations/     001_init.sql
store_test.go   slugify, the ownership gate, the sink's guards
```

## Testing

`go test ./playlists/` — no database needed.

Covered: `slugify` across its edges (always a usable address, never a dangling
or doubled dash, ≤60 characters, the `list-` fallback); the ownership gate —
including that a missing playlist and somebody else's produce **byte-identical**
responses, since a difference between them is the oracle a 404 exists to avoid,
and that an anonymous viewer is refused whatever the stored owner is; that a
lookup failure is treated as not-found rather than as permission granted; and
the sink's guards, which refuse an anonymous caller, an empty slug and an empty
batch before touching a statement.

**Not covered:** the sink's SQL itself — the batch insert, the position series
and the ownership-in-the-statement check are only exercised against a real
Postgres, and there is no `playlists` integration test yet. `pluginapi/pgtest`
is the harness to write it with; `cosmetics/store_integration_test.go` is the
worked example.
