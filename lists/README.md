# lists plugin

Watchlists and collections. Members build named lists of releases, optionally
make them public, follow and copy each other's, download a whole list as a
ZIP, and browse everything public on a discovery grid.

The list TABLES stay host-owned — the account Following tab and the
release-page widgets read them too. This plugin owns the `/lists` surface over
them.

## Surface
All routes are authenticated (`Auth.Authenticate`):

| Route | Purpose |
|---|---|
| `GET /lists` | the member's own lists + the ones they follow |
| `POST /lists` | create |
| `POST /lists/:id/delete` | delete |
| `POST /lists/:id/visibility` | toggle public |
| `GET /lists/:id` | list detail (private lists: owner only) |
| `GET /lists/:id/download-all` | every NZB in the list as one ZIP |
| `POST /lists/:id/follow` / `unfollow` | follow state |
| `POST /lists/:id/copy` | duplicate someone else's list |
| `POST /lists/add` / `remove` | add or remove a release (AJAX) |
| `GET /release/:id/lists` | which lists contain a release |
| `GET /community/watchlists` | public discovery grid |

`Metadata.Processes`: `["web"]`. The plugin keeps its own routes rather than
becoming slot views — it is four pages, one with a path parameter
(`/lists/:id`), and the slot model is one page per slug. The host supplies
chrome on demand through `RenderPage` instead, so every URL stays where
members have it bookmarked.

Private lists are gated in the handler (`!Public && UserID != viewer`), on both
the detail page and the ZIP route — the second one matters, since a bulk
download would otherwise be a way around the first.

## Data
Owns no tables and runs no migrations. Everything goes through `Deps`. It does
own its four templates, embedded — a missing one is a build error here rather
than a 500 at runtime in the host.

## Dependencies
- **Core services**: `Router`, `Auth.Authenticate`.
- **Store**: none. `SetDeps(Deps{...})` from the composition root before
  `core.Boot`; `Provision` rejects a partial wiring.
- **Config keys / Metadata.Requires**: (none).

Most of the seam carries no types — ids in, error out. What crosses is
`List`, `Item` and `NzbRef`, all of them the plugin's own view models.

`List` in particular is a real type now, not a window onto the host's record.
That is what moving the markup bought: while the templates lived host-side
they read the host's struct directly, so the plugin had to carry rows through
opaquely and neither side could be narrowed. The pages render from `List`, and
anything the host adds to its own record is invisible here until asked for.

`Item.Card` is the one place a host value still rides along, and it is
finished HTML rather than a record. The site's release card appears on every
listing page and reads most of a release; mirroring that schema in to redraw
the host's own markup would be the wrong trade. Same for `NzbCardCSS` and
`ReportModal` — shared chrome the plugin embeds but does not own.

Two dependencies are host **policy**, deliberately not reimplemented:
`DownloadAllowed` (the pinned-browse-IP rule behind the bulk ZIP) and `Gunzip`
(NZBs are gzipped at rest by the host). A plugin that decided either would be
taking something the operator owns, and would keep serving ZIPs after the host
tightened the rule.

`NotifyFollow` is the one optional entry — nil disables the ping and following
still works.

## Hooks & Callbacks
- Host hooks SET: (none).
- Extensions PUBLISHED / CONSUMED: (none).

## Lifecycle
- **Provision**: validates `Deps`, registers the thirteen routes. No-op
  outside the `web` process.
- **Start / Stop**: no-ops — no background work.

## Files
- `plugin.go` — registration, metadata, the route table.
- `deps.go` — `List`, `Item`, `NzbRef`, `Deps`.
- `views.go` — the embedded templates, the per-page view models, and the
  render/chrome helpers.
- `templates/` — the four pages: `user_lists`, `list_detail`,
  `release_lists`, `community_watchlists`.
- `handlers.go` — the thirteen handlers, ZIP building, dedup, filename
  sanitising.

## Testing
Unit-tested: `sanitizeListFilename` across every reserved character (it
derives a `Content-Disposition` filename from user-supplied text), and
`dedupPublicLists` — cross-axis dedup, within-axis order, within-axis
duplicates, and empty or absent axes.

All four fragments are also EXECUTED, populated and empty. That is what makes
narrowing `List` safe: a template still reaching for a field left behind on
the host's record fails in the test rather than at runtime.

The view models are structs rather than maps on purpose, and this is not
theoretical. A map answers a missing key with the empty value and no error, so
when these templates moved off the host's page data the detail page's "report
this list" control — gated on a viewer field that came from the host chrome —
silently stopped rendering, with every test still green. Against a struct that
same markup is a render error. The gate is now the plugin's own `ViewerID`,
and the test asserts the control appears for a signed-in non-owner.

Needs integration (live DB): the list queries themselves and the ownership
rules they encode, which live in the host's repository and are tested there.
The ZIP route wants an end-to-end test with real gzipped payloads; it has
none today.
