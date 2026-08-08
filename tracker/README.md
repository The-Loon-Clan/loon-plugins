# tracker plugin

A private BitTorrent tracker: the HTTP announce/scrape endpoints a torrent client
talks to, the `.torrent` download that bakes in a member's passkey, and the member
and admin pages around them. Members see a swarm listing at `/tracker`, their own
ratio accounting at `/tracker/my`, and download `.torrent` files that already
carry their announce URL; staff see one oversight page at `/admin/p/tracker`.

**Off by default.** `plugins.tracker.enabled` defaults to `false`, which is
unusual among these plugins and deliberate: a tracker is not a feature a site
accidentally wants. Everything else here (wiki, messages, forum) is inert until a
member visits it; this publishes announce endpoints, mints passkeys and starts
keeping ratio accounting the moment it is reachable.

Lifted out of a host where it was ~1,800 lines across `web/handlers`, `pkg/tracker`
and the storage layer. The protocol code came across near-verbatim — the bencode
scanner, the peer store, the announce parsing — because the `info_hash` is the
SHA-1 of raw `info` dict bytes and any re-encoding changes it.

## Surface

| Route | Access | Notes |
|---|---|---|
| `GET /api/tracker/announce/:passkey` | **public** (passkey IS the auth) | No session middleware by design: the caller is a torrent client, has no cookie, cannot follow a login redirect, and would parse a login page as a bencoded response. |
| `GET /api/tracker/scrape/:passkey` | **public** (as announce) | |
| `GET /tracker` | member + `tracker.access` | Swarm listing. |
| `GET /tracker/my` | member + `tracker.access` | Per-torrent counters, announce URL, rotate button. |
| `GET /tracker/download/:info_hash` | member + `tracker.access` | Splices the member's passkey into the announce URL; `info_bytes` are spliced **unchanged**. |
| `POST /tracker/passkey/rotate` | member + `tracker.access` | Returns the new key with a warning: rotating invalidates every `.torrent` already downloaded. |
| `GET /admin/p/tracker` | `RoleMod` (host admin gate) | One page — torrents plus every member's accounting. |

Gates, in order: the host's own auth chain (`c.Auth.RequireUser`), then the
tracker's entitlement. Order matters — checking the entitlement first would mean
resolving one for an anonymous request, and the honest answer to "may nobody use
the tracker" is a login prompt rather than a denial. A member who lacks the
entitlement is sent to `/`, not `/login`: they ARE logged in, so a login prompt
would be a lie and they would loop.

**Processes:** `web`, `api`. Both, because the host registered announce/scrape on
both and a client hits whichever hostname is in its `.torrent`. The `api` process
serves only the machine-facing pair — no templates, no session.

## Data

Owns three tables in its own `tracker` schema (migration `001_init.sql`):

- `torrents` — the catalogue, including `info_bytes` (the raw torrent) and cached
  `seeders`/`leechers`/`snatches`.
- `passkeys` — one per member, `UNIQUE` across members because an announce carries
  nothing else to say who it is from. `rotated_at` is stamped only when the key
  actually changes.
- `user_stats` — per member per torrent: `uploaded`, `downloaded`, `left_bytes`,
  `completed`, `last_seen`. `ON DELETE CASCADE` from `torrents`.

The live swarm — who is on which torrent right now — lives in **Redis**, not
Postgres: a few thousand short-lived keys rewritten every announce interval.

Three couplings were dropped in the lift, and the notes in `001_init.sql` say why:
passkeys moved out of `users`, `tracker_access` became an entitlement, and the
foreign keys to `users` are gone because a plugin schema does not reference host
tables. Note that `private_trackers` / `user_tracker_access` in the host belong to
the **offers** system — which private trackers a member can deliver *from* — and
have nothing to do with this plugin despite the name.

## Dependencies

**Core services:** `Auth` (session + role gates), `Users` (display names on the
admin page), `Entitlements` (`tracker.access`), `Redis` (the peer store),
`Storage.SchemaDB` (own schema), `Router.Engine` (routes outside `/plugin/*`, since
`/tracker` and `/api/tracker` are established URLs).

**Store:** self-contained. `PGStore` over `core.SchemaDB`, `MemStore` for tests.

**Host seams (`SetDeps`, required before `core.Boot`):**

| Field | Why the host supplies it |
|---|---|
| `RenderPage(c, title, body)` | Wraps a plugin fragment in the site chrome. |
| `CSRFToken(c)` | The double-submit token for the rotate form; a host session concern. |
| `RelativeTime(t)` | So "last seen" reads like every other timestamp on the site. |

`Provision` refuses an incomplete `Deps` rather than discovering it at first
request.

**Config (`plugins.tracker.*`):**

- `enabled` (bool, default **false**) — the switch. Checked before everything
  else, so a host that compiles the plugin in without configuring it boots
  cleanly.
- `site_url` (string, **required when enabled**, no default) — the absolute base
  baked into every `.torrent`'s announce URL. Refused rather than defaulted: a
  wrong value here does not fail loudly, it produces torrents pointing somewhere
  unable to answer, and the member finds out when their client reports the tracker
  dead.

**Redis absent ⇒ idle, not a boot failure.** `core.Redis` is optional
infrastructure by contract, so a host may legitimately have none, and taking a
site down over an opt-in feature is the wrong trade. But it logs loudly and the
admin page says "the tracker is idle" outright, because a tracker that quietly is
not there is indistinguishable from one that is broken.

## Hooks & Callbacks

- Host hooks SET: **(none)**.
- Extensions PUBLISHED (`Core.Register`): **(none)**.
- Extensions CONSUMED (`Core.Lookup`): **(none)** — the entitlement is read
  through `core.Entitlements`, not a plugin capability.
- Views registered: one `SlotAdminPage` (`tracker`, `RoleMod`, nav group
  "Operations").

`NewGate` fails **closed** on both checks: a host that wired no entitlements
service has not decided everyone may use the tracker, it has not wired the thing
that decides.

## Lifecycle

**Provision** — reads config; returns early (logging) when disabled or when Redis
is absent; refuses an empty `site_url`; builds the store, peer store and handlers;
parses templates; registers announce/scrape on the raw engine, then (web/all only)
the member routes and the admin view.

**Start / Stop** — no-ops. There is no background work: swarm counts are updated
on the announce path, and Redis expiry retires stale peers.

Migrations run in Boot **step 1, before any Provision**, so a *disabled* tracker
still gets its (empty) schema. Deliberate: enabling later is a config change and a
restart, with no migration step.

## Files

- `plugin.go` — metadata, config, Provision, the route table, the entitlement gate.
- `announce.go` / `announce_handler.go` — the announce/scrape wire format and handlers.
- `bencode.go` — scanner + outer-dict rebuild. Never re-encodes `info`.
- `sanitize.go` — strips tracker-identifying markers from a scraped `.torrent`.
- `peers.go` — the Redis peer store.
- `store.go` / `store_pg.go` / `store_mem.go` — the Store triple.
- `member_handler.go` — member pages, `.torrent` download, passkey mint/rotate.
- `handlers.go` — the `Deps` seam and shared handler plumbing.
- `views.go` + `templates/` — the admin page and the two member fragments.
- `migrations/001_init.sql` — the three tables.

## Testing

**Unit:** announce arithmetic against `MemStore` (deltas ADD, `left_bytes`
REPLACES, `completed` is sticky), passkey uniqueness and rotation, `Ratio`'s
both-zero case, the gate failing closed with no services, bencode round-trip
stability across a sanitize-and-rebuild, and both member fragments executing
against a populated `PageData`.

**Integration** (`-tags=integration`, `TRACKER_TEST_DSN`): the SQL, because
`MemStore` reproduces whatever its author believed the SQL did. Covers the
announce `ON CONFLICT` semantics, the `rotated_at` CASE, the one-hour activity
window behind seeding/leeching, `info_bytes` surviving an upsert, absence as
`nil, nil`, `ListTorrents` omitting `info_bytes`, and the `sortBy` allowlist. The
harness applies every migration inside the schema and then resets `search_path`
to `public`, so a store that lost its scoping fails instead of passing.

**Known gap:** nothing exercises a real BitTorrent client end to end. The wire
format is covered by unit tests over recorded shapes, not by a live swarm.
