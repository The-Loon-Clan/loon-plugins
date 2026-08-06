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

`Metadata.Processes`: `["web"]`.

Private lists are gated in the handler (`!Public && UserID != viewer`), on both
the detail page and the ZIP route — the second one matters, since a bulk
download would otherwise be a way around the first.

## Data
Owns no tables and runs no migrations. Everything goes through `Deps`.

## Dependencies
- **Core services**: `Router`, `Auth.Authenticate`.
- **Store**: none. `SetDeps(Deps{...})` from the composition root before
  `core.Boot`; `Provision` rejects a partial wiring.
- **Config keys / Metadata.Requires**: (none).

Most of the seam carries no types — ids in, error out. Three things do:

- `ListRef` / `ItemRef` — the few fields the plugin actually reasons about
  (ownership, visibility, name; NZB id and filename) plus `Raw`, the host's
  own record.
- The `any` returns on `UserLists` and `NzbAndLists`, which the plugin never
  inspects.

`Raw` is the honest part. These pages render **host-owned** templates that read
a dozen fields the plugin has no opinion about — cover art, item counts, grab
totals. Mirroring a struct the plugin does not understand would break the page
the first time the host added a column, so the row rides through untouched.
When the templates move here, `Raw` goes away.

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
- `deps.go` — `ListRef`, `ItemRef`, `Deps`.
- `handlers.go` — the thirteen handlers, ZIP building, dedup, filename
  sanitising.

## Testing
Unit-tested: `sanitizeListFilename` across every reserved character (it
derives a `Content-Disposition` filename from user-supplied text), and
`dedupPublicLists` — cross-axis dedup, within-axis order, within-axis
duplicates, empty and absent axes, and rows the host could not resolve.
`rawItems` is covered for passing host rows through by identity.

Needs integration (live DB): the list queries themselves and the ownership
rules they encode, which live in the host's repository and are tested there.
The ZIP route wants an end-to-end test with real gzipped payloads; it has
none today.
