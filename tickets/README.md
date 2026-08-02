# tickets plugin

Support tickets. A member opens one, staff reply, and the author may opt a
ticket into being publicly readable so an answer helps the next person with the
same question.

Users see it at `/support`; staff triage at `/admin/tickets`.

## Surface

- **authed** — `GET /support`, `POST /support`, `GET /support/:id`,
  `POST /support/:id/reply`, `POST /support/:id/public`
- **staff** — `GET /admin/tickets`, `POST /admin/tickets/:id/status`,
  `POST /admin/tickets/:id/note`

Process kinds: none declared, so it provisions everywhere the host runs. There
is no background work.

## Data

Two host-owned tables: `support_tickets` and `ticket_replies`.

The plugin owns neither. On the origin site they arrived as host migrations 41
and 44 (with `public` added by 207) and stayed in the public schema through the
extraction — relocating live tables is a data migration, and this port moved
code. `schema.sql` documents both, plus the six indexes, for a host that lacks
them.

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

- Unit-tested: ticket visibility (`ticketVisibleTo` — who may read a ticket
  they do not own), and the pure helpers, in `tickets_test.go`.
- Needs integration (live DB): `PGStore` is a SQL passthrough. The status and
  note updates and the reply ordering are single statements whose correctness
  is the SQL itself, which only a real database exercises. That gap predates
  the extraction and moved with it.
