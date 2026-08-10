# uploads plugin

The member-facing upload domain: getting bytes onto the site, and managing the
rows that come out. Members see it as the Upload tabs and the My Uploads page
under account settings.

**Status: slice 1 of 5.** The owner's management page is here; the upload flows
themselves are still host-side. This was the largest single surface left in the
ameNZB host when the plugin extraction closed — ~3,400 handler lines across
seven files — and it is being lifted a slice at a time because each slice is
independently useful and a half-finished 3,400-line lift is not.

## Surface

| Route | Access | What |
|---|---|---|
| `GET /account-settings/uploads` | member | Three tabs — public uploads, private NZBs, private torrents — each paginated independently (`?tab=`, `page`/`npage`/`tpage`) |
| `POST /account-settings/uploads/bulk` | member | One row or all rows: delete, restore, anonymous, permanently anonymous |
| `POST /account-settings/uploads/torrent-visibility` | member | Keep a private request's artifact private |

Mounted at the paths the host page already used, not new ones — a member with
the tab bookmarked must not be able to tell the code moved.

Process kinds (`Metadata.Processes`): `web`. Nothing here is scheduled. The
jobs that groom uploaded rows afterwards (tag fill, title cleaning, junk
sweeps) belong to the catalog and curation domains, not to this one.

## Data

Owns no tables. Every table it touches — `nzbs`, `nzb_requests`,
`agent_tokens` — is host-owned and read by host pages, so this is a host-data
worker in the `feeds` / `curation` mould rather than a schema owner.

## Dependencies

- Core services consumed: `Auth` (route gate), `Router`, `Errors`.
- `Deps` via `SetDeps` before `core.Boot`. Every field is required and
  `Provision` refuses a partial wiring: a management page that renders an empty
  list looks exactly like a member who has never uploaded anything, so a
  missing seam must fail loudly at boot rather than quietly at read time.
  - `Viewer`, `ListUploads` (returns all three tab groups in one call, so the
    agent-queue lookup is shared rather than repeated per tab)
  - `Actions` — nine owner-scoped mutations (see `deps.go`)
  - `RenderPage`, `CSRFToken`, `RenderPagination`
- No config keys, no `Metadata.Requires`.

**Owner scoping is the host's job.** Every action takes the member's id and the
row id, and the host's query is scoped by both. This package never asserts
"trust me, this row belongs to them" — that assertion is one bug away from
letting a member delete somebody else's upload.

## Hooks & Callbacks

- Host hooks SET: (none).
- Extensions PUBLISHED: (none).
- Extensions CONSUMED: (none).

## Lifecycle

- Provision: validates the full `Deps`, mounts three routes behind
  `RequireUser`. Start / Stop: no-ops.

## Files

- `plugin.go` — registration, metadata, route table, the viewer gate.
- `deps.go` — the host seams and the DTOs that cross.
- `views.go` — the three handlers, the flash redirect, byte humanising.
- `templates/uploads.html` — the page fragment.

## Testing

Unit-tested: a full render, the empty state (which must still be a whole page,
because `html/template` streams and a missing field truncates it silently), the
POST-form-count-versus-`_csrf`-count invariant on two different tabs, both of
the irreversible action's guards, byte humanising, and flash escaping.

`TestEachTabRendersItsOwnContent` exists because of a regression this page
shipped with for exactly one commit: the first cut served a single merged list
and ignored `?tab=`, so the account-settings links straight to
`?tab=private-nzb` and `?tab=private-torrent` silently showed the public list.
Three independently-paginated tabs is what the surface being replaced did, and
"lift, don't rewrite" is in this repo's conventions for this reason.

Needs integration (live DB): nothing here — the data access is entirely host
seams, exercised by the host's own storage tests.

## The remaining slices

Ordered smallest-first, each independently shippable:

2. **Private torrent + private NZB upload** (`bulk_torrent_handler.go` 378,
   `bulk_private_nzb_handler.go` 218). Self-contained, one flow each.
3. **The unified upload pages** (`upload_unified.go` 648) — public and private
   NZB upload forms plus their POST handlers.
4. **The batch review flows** (`community_batch.go` 586, `private_batch.go`
   537). Two review UIs over the same idea, and the pair should probably become
   one on the way across rather than be lifted twice.
5. **The scripted bulk API** (`bulk_handler.go` 638) — `/api/bulk/*` with its
   own token scheme. Last because `BulkHandler.IngestOne` is the primitive
   every other slice calls, so moving it early would mean the host importing
   the plugin, which is backwards.

A note for whoever takes slice 5: `myUploadsStore` in the host's
`my_uploads_handler.go` is an empty interface with a doc comment describing
everything it was meant to narrow. The handler uses the whole composite
instead. That refactor was started and abandoned, and slice 1 replaces it
rather than finishing it.
