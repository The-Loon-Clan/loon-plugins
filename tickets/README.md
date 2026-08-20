# tickets plugin

Support tickets. A member opens one, staff reply. Tickets are readable by
their owner and staff only. (An opt-in public-visibility experiment ran here
until 2026-08-16; it was retired — nobody used it — and old databases keep a
dead `public` column the plugin no longer reads.)

Users see it at `/support`; staff triage at `/admin/tickets`.

## Surface

| Route | Access | Notes |
|---|---|---|
| `GET /support` | authed | The member's own tickets. |
| `POST /support` | authed | Open one. |
| `GET /support/:id` | owner or admin | See *Hooks* for who "or admin" is. |
| `POST /support/:id/reply` | owner or staff | A member's reply **reopens** a closed ticket. |
| `GET /admin/tickets` | staff | Triage. |
| `POST /admin/tickets/:id/update` | staff | Status **and** the admin note, in one form and one statement. |
| `POST /admin/tickets/:id/delete` | staff | |

`Flavours: [any]`. Process kinds: none declared, so it provisions everywhere
the host runs. There is no background work.

After an update the handler returns the staff member to wherever they came
from — the ticket detail page if the `Referer` says so, the queue otherwise —
so triaging from a ticket does not bounce them back to the list.

## Data

Two host-owned tables: `support_tickets` and `ticket_replies`.

The plugin owns neither. On the origin site they arrived as host migrations 41
and 44 and stayed in the public schema through the extraction — relocating
live tables is a data migration, and this port moved code. `schema.sql`
documents both, plus the indexes, for a host that lacks them.

## Dependencies

Core services consumed: `Storage` (the shared `*sqlx.DB`), `Router`, `Auth`
(route-level `Authenticate`), `Errors`.

Store: self-contained `PGStore` built at Provision.

`Deps` (SetDeps before `core.Boot`):

- `BaseData` — host page chrome. Tickets is a full-page surface, so the
  templates stay host-side and the plugin fills them.
- `PageOffset`, `Pagination` — the host's paging helpers, passed rather than
  copied because the value is consumed by the host's pagination *partial*.
  The plugin never reads it: `Page` and `TotalPages` are computed locally from
  numbers it already has, so the host's type stays out of its vocabulary.
- `Viewer` — identity plus the two authority questions this surface asks.
  `Staff` and `Admin` are separate on purpose: staff may reply on any ticket
  and are marked as replying for the site, admin may read a ticket they do not
  own. Collapsing them would silently widen one. **What counts as either is
  host policy** — a plugin hard-coding "role >= mod" decides something the
  operator owns.
- `OwnerRole` — the author's role name, for the detail page's role chrome.
  Optional; without it the page falls back to the default role, which is why a
  deleted account does not blank a ticket.
- `RoleBadge` — the host's role display data, rendered by the host's own
  template and never inspected here. Optional; without it, a neutral badge with
  the same field names is used, so the template renders either way.
- `NotifyNewTicket`, `NotifyReply` — optional as a pair. A host with no
  notification system still has a working ticket surface; it just does not
  announce.

Config keys: none. `Metadata.Requires`: none.

## Hooks & Callbacks

- Host hooks SET: **(none)**
- Extensions PUBLISHED: **(none)**
- Extensions CONSUMED: **(none)**

Absence is information here: unlike communities, this surface feeds nothing
else on the site.

**Visibility goes through `pluginapi.VisibleTo`**, not a bare
`t.UserID == viewerID`. A support ticket is private by definition and the
viewer id is 0 for an anonymous request, so an equality check would hand one
over to a signed-out visitor if a ticket were ever owned by 0. Privilege stays
a separate boolean for the same reason it does everywhere else: encoding
"staff" as a magic id is what once let an anonymous caller delete any comment.

## Lifecycle

- **Provision** — checks the seams a render cannot proceed without
  (`BaseData`, `Viewer`, `PageOffset`, `Pagination`), builds the `PGStore` over
  `Core.Storage.DB()`, and registers `/support` and `/admin/tickets`.
- **Start / Stop** — no-ops.

## Files

- `plugin.go` — lifecycle and the route table.
- `deps.go` — the host seams and the `Viewer` shape.
- `handlers.go` — the HTTP handlers, visibility rules and role chrome.
- `store.go` / `store_pg.go` — the `Store` interface and its Postgres impl.
- `models.go` — plugin-private types.
- `schema.sql` — the tables and indexes, for a host that lacks them.

## Testing

`go test ./tickets/` for the unit suite; `make itest` (or
`bash scripts/itest.sh ./tickets/`) for the one that needs Postgres.

- **Unit** — ticket visibility (`ticketVisibleTo`: who may read a ticket they
  do not own), the events, and the pure helpers.
- **Integration** — `reopen_integration_test.go` covers reopen-on-member-reply
  against a real database. That behaviour lives entirely in one SQL guard
  (`WHERE id=$1 AND status='closed'`), so a Go-level fake would only assert
  what the fake was told to do: the two things worth proving are that the guard
  fires for a closed ticket and stays silent for an open one, and both are
  properties of the statement. It returns a bool so the caller announces a
  reopen only when a row actually changed.
- **Still uncovered** — the rest of `PGStore` is a SQL passthrough. The status
  and note update and the reply ordering are single statements whose
  correctness is the SQL itself, and no test exercises them. That gap predates
  the extraction and moved with it; `pluginapi/pgtest` is the harness to close
  it with now.
