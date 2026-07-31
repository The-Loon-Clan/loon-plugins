# messages plugin

The messaging surface: a unified `/inbox` that renders direct-message threads
and system announcements in one list, the DM send/read/block flows behind it,
and the `/admin/messages` broadcast composer.

Ported from the origin site's in-tree plugin. The handlers are a **verbatim
lift** — the behaviour that shipped is the behaviour here. What changed is
everything underneath them, which is what made the port worth doing:

| Was | Now |
|---|---|
| four host repositories (`storage.DirectMessageRepository`, `InboxRepository`, `NotificationRepository`, `UserRepository`) | one `Store` this package defines, with a Postgres implementation |
| `models.User`, `models.Notification`, `models.Message` | this package's own row types, plus `core.User` |
| host user lookup + notification writes | `core.Users` and `core.Notifications` |
| host `handlers.BaseData` / `JSONOK` / `NewPagination` | the `BaseData` seam, and six lifted helpers in `web.go` |

The result imports no site package — only `loon/core` and Gin.

## Surface

Routes are mounted directly on the engine (not a view slot) so the URLs stay
exactly what they were: existing notification links, bookmarks and the inbox's
own JavaScript all point at `/inbox`.

**Authed** (`core.Auth.Authenticate`):

```
GET  /inbox                     unified list (DMs + announcements)
POST /inbox/:id/read            mark one announcement read
POST /inbox/:id/dismiss         hide one announcement for this viewer
GET  /inbox/dm                  back-compat alias → /inbox, preserving ?thread
POST /inbox/dm/send             send a DM (entitlement-gated)
POST /inbox/dm/:id/read         mark a thread read
POST /inbox/dm/:id/delete       soft-delete a thread for this side only
POST /inbox/dm/block            block a user
POST /inbox/dm/unblock          unblock a user
```

**Admin** (`core.RoleMod` and above): `GET|POST /admin/messages`,
`POST /admin/messages/:id/delete`.

Process kinds (`Metadata.Processes`): `web`. No jobs.

## Data

**Owns no tables.** On the origin site the IRC plugin also writes DMs for
whisper delivery, so the schema is shared and stays host-owned:

- `dm_threads` — one row per *pair*, ids stored `(lo, hi)` so both orderings
  collapse to one thread. Per-side `lo_deleted_at`/`hi_deleted_at` soft deletes.
- `dm_messages` — `read_at` is per-recipient; the sender's row is stamped at
  insert so "unread for me" is just `sender_id != $1 AND read_at IS NULL`.
- `dm_blocks` — symmetric: a blocker cannot message the person they blocked.
- `messages` / `message_reads` — the broadcast half of the inbox.

[`schema.sql`](schema.sql) is what a host **without** those tables runs. It
exists because the missing-schema step is what stalled every earlier port: a
`Store` interface tells you the method names, not the DDL.

## Dependencies

Core services consumed: **Auth** (`CurrentUser`, `Authenticate`,
`RequireUser`), **Users** (`GetByID`, `GetByUsername`), **Notifications**
(`Notify`), **Entitlements** (the `dm.initiate` gate), **Errors**, and
**Router** (`Engine`).

Host seams (`SetDeps`, before `core.Boot`):

```go
messages.SetDeps(messages.Deps{
    Store:    messages.NewPGStore(db),       // or your own Store
    BaseData: handlers.BaseData,             // required — page chrome
    ListUsers: nil,                          // optional — composer picker
})
```

`BaseData` is **required**: these pages render inside the host's layout, and
without it a signed-in reader gets logged-out chrome, which looks like a broken
session rather than a missing seam.

`ListUsers` is **optional**. Core has no "list every user" method — on a site of
any size that is a page-breaking query — so a host that wants the composer's
recipient dropdown supplies one, and a host that does not gets the username
field the template falls back to. The send path resolves by username either way.

Templates are the **host's**, by name: `inbox.html` and `admin_messages.html`.
Same convention as the forum plugin — a portable plugin cannot ship a site's
chrome, so it renders through the host's.

## Hooks & Callbacks

- Host hooks SET: (none).
- Extensions PUBLISHED: (none).
- Extensions CONSUMED: (none). The `dm.initiate` entitlement is read through
  `core.Entitlements`, not through the plugin that grants it — the messaging
  surface should not have to know that paid ranks exist, only whether this
  person is allowed.

## Lifecycle

**Provision** refuses to boot without `Store` or `BaseData`, builds the
handlers over core's services, and registers every route. All nine `/inbox`
routes go on **one** group: split across groups, Gin's tree lets the
parameterised routes shadow the literal ones.

**Start** / **Stop** are no-ops — this plugin is pure request surface.

## Three behaviours worth knowing

**The send gate is an entitlement, not a role check.** `canSendDM` asks core
for `dm.initiate`, so mods hold it through the role baseline and paid tiers can
grant it without this package learning what a paid tier is. It fails **closed**:
a nil service or an unresolvable user cannot send.

**Blocks are symmetric.** A blocker cannot message the person they blocked.
That is deliberate — the asymmetric version lets someone block a person and
keep talking at them.

**Soft deletes are per-side, and a new message undoes them.** Removing a thread
from your inbox does not touch the other party's copy, and their next message
brings it back — otherwise "delete" would read as "block" to the sender.

## Files

- `plugin.go` — registration, `Deps`, routes.
- `handlers.go` — the lifted request surface.
- `models.go` — the row types.
- `store.go` — the `Store` interface, both halves of the inbox.
- `pg.go` — Postgres implementation, SQL lifted verbatim.
- `viewer.go` — the session user, and the int64/int adapter.
- `web.go` — the lifted JSON + pagination helpers.
- `consts.go` — the entitlement and notification keys.
- `schema.sql` — DDL for a host that lacks the tables.

## Testing

Unit-tested: the DM send gate, in all four states that matter — mods hold it
through the role baseline, a plain user cannot until a group grants it, it
fails closed with no service, and a mod keeps access when their paid tier
lapses. Those run against loon's **real** `core.Entitlements` composition
rather than a stand-in, so they exercise the wiring a host actually gets.

Needs work: there is **no `MemStore` yet**, so the handlers themselves are
untested here and a demo host has to supply Postgres. That is the next slice —
the forum plugin's `mem.go` is the model.
