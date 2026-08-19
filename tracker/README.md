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
and the storage layer. The protocol code came across near-verbatim — the peer
store, the announce parsing — because the `info_hash` is the SHA-1 of raw `info`
dict bytes and any re-encoding changes it.

The bencode scanner and encoder are **not** here: they live in `loon/bencode`,
shared with the feeds importer. This plugin carried a second, identical copy
until it was folded in. That is a correctness constraint, not housekeeping —
two encoders that disagree on dict key order produce different SHA-1s for the
same torrent, and the same missing "is `info` actually a dict" guard had to be
found and fixed twice, once per copy. Byte-level parity across the move is
pinned by goldens in `wire_golden_test.go` (announce/scrape bodies) and
`sanitize_test.go` (info-hash survives a rebuild).

## Surface

| Route | Access | Notes |
|---|---|---|
| `GET /api/tracker/announce/:passkey` | **public** (passkey IS the auth) | No session middleware by design: the caller is a torrent client, has no cookie, cannot follow a login redirect, and would parse a login page as a bencoded response. |
| `GET /api/tracker/scrape/:passkey` | **public** (as announce) | |
| `GET /tracker` | member + `tracker.access` | The torrent listing, titled **Torrents** — the name the site nav has always used for it. Just the list: the viewer's own uploaded/downloaded/ratio tiles moved off it to `/tracker/my`, which one quiet link reaches, because a member opening a torrent list came to find a torrent. |
| `GET /tracker/my` | member + `tracker.access` | Per-torrent counters, announce URL, rotate button. |
| `GET /tracker/t/:info_hash` | member + `tracker.access` | **One torrent's own page** — facts, the `.torrent`, the info-hash, the release cross-link, and every promotion ever cast on it. A static `/t/` prefix so it cannot collide with `/my` or `/download` in Gin's tree. Reached from the list's torrent name. |
| `GET /tracker/download/:info_hash` | member + `tracker.access` | Splices the member's passkey into the announce URL; `info_bytes` are spliced **unchanged**. |
| `POST /tracker/passkey/rotate` | member + `tracker.access` | Returns the new key with a warning: rotating invalidates every `.torrent` already downloaded. |
| `GET /admin/p/tracker` | `RoleMod` (host admin gate) | One page — torrents plus every member's accounting. |

The torrent page exists because there was no such thing: the tracker was a flat
list, so any surface wanting to act on ONE torrent had to be reached by pasting
a 40-character info-hash into a form. Casting magic was the case that made it
obvious. It consumes `pluginapi.TorrentPromotionsName` (looked up in **Start** —
sibling registration order is nobody's promise), and a host without the magic
plugin gets the page with no promotions panel and no cast button, rather than a
link to a route it does not serve.

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
| `ReleaseURL(nzbID)` | **Optional.** Where the release a torrent was made from is browsable. `torrents.nzb_id` is an id in the HOST's index, and the route that renders it is the host's to move — so the listing asks rather than hardcoding `/release/%d`. Unwired, or a torrent with no `nzb_id`, renders the name as text instead of a link. |

`Provision` refuses an incomplete `Deps` rather than discovering it at first
request. `ReleaseURL` is exempt: a nil seam there costs a link, not a page.

**Markup is the host's design vocabulary, not Bootstrap's.** The three templates
were lifted carrying `card` / `row` / `col-md-4` / `btn btn-outline-primary` /
`table-responsive`, which rendered on the reference host only because its CSS
happens to define some of those names — and came apart where it does not: the
listing's table sat in a `.table-responsive` rather than a `.data-table-wrapper`
and ran 364px past a 390px screen. They now use `panelV2`, `data-table`,
`stat-tile`, `tag--seed` and `notice`, the same components the host's own
listings use, so a torrent row and a release row describe a swarm identically.
A host with different component names should override the templates rather than
expect these to be neutral — they are not, and markup that pretends to be is
markup that renders wrong somewhere without saying so.

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
- Extensions PUBLISHED (`Core.Register`) — all four on a **running** tracker
  only, because a capability offered by a tracker that idled for want of Redis
  can only ever fail at the moment somebody uses it:

  | Key | Contract | What it is for |
  |---|---|---|
  | `tracker.info` | `pluginapi.TorrentInfoFunc` | Name and size by info-hash, so a magic cast can show and price what it is enchanting without reading this schema. |
  | `tracker.credit` | `pluginapi.TrackerCredit` | Selling transfer credit — the points store's "1.0 GB Uploaded". Credit is bookkeeping in its own table, so buying upload does not invent a torrent nobody seeded. |
  | `tracker.mirrors` | `pluginapi.TorrentMirrors` | **Read:** which of these release ids the tracker also carries. Batched, because the caller is a listing with fifty rows and the per-row question would be fifty round trips for a badge. Ids with no torrent are ABSENT from the map — 0 seeders is a dead torrent and must not read the same as no torrent. |
  | `tracker.mirror.make` | `pluginapi.TorrentMirrorMaker` | **Write:** turn a release into a torrent. Idempotent — it is reachable from a button, so a second click finds the first torrent. |

  Both mirror structs carry `Href`, filled from `TorrentPath`: the route is this
  plugin's to own and to move, so a foreign page links without hardcoding
  `/tracker/t/%s`. That is the mirror image of `ReleaseURL` below.

  **What a mirrored torrent's piece hashes are.** Piece hashes are SHA-1 of the
  file bytes, and an *index* holds pointers to Usenet articles rather than
  bytes. So `MirrorRequest` takes optional real `Pieces` from a caller that has
  hashed the content, and falls back to a deterministic placeholder chain for
  one that has not. A placeholder torrent announces, downloads as a `.torrent`,
  and would fail a client's verification the moment it fetched a piece — which
  is the honest state of a mirror of content nobody holds, and is why the
  fallback is deterministic rather than random: the same release mirrored twice
  is one torrent, not two. The file **list** is real either way when the caller
  passes one.

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
- `sanitize.go` — strips tracker-identifying markers from a scraped `.torrent`, and rebuilds one per user with `loon/bencode`. Never re-encodes `info`.
- `peers.go` — the Redis peer store.
- `store.go` / `store_pg.go` / `store_mem.go` — the Store triple.
- `member_handler.go` — member pages, `.torrent` download, passkey mint/rotate.
- `handlers.go` — the `Deps` seam and shared handler plumbing.
- `mirrors.go` — the mirror seams, read and write, plus `TorrentPath`.
- `torrentbuild.go` — `BuildTorrent`: a described release becomes an info dict. Exported because two callers build torrents (the mirror button and a host seeding demo data) and a second bencode encoder to keep byte-identical is a bug nobody finds by reading either copy.
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
