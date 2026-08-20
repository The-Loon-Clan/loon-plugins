# perks plugin

The tracker economy: **freeleech** and **double-upload** tokens, bought with
points and spent on one torrent at a time.

**It exists apart from the tracker on purpose.** A private site's economy
changes constantly — new perks, promotions, seasonal rates — and none of that is
the announce protocol. The tracker exposes two numbers per (member, torrent);
this decides what they are.

It also answers the hit-and-run framework. A site that told somebody a download
was free has already said what that download owes, so a freeleech torrent is
**exempt from seeding requirements**. That is one rule living in one place
rather than two plugins agreeing by coincidence.

**Tracker flavour only.** Both perks are discounts on *transfer*, and an
indexer-only site has no transfer to discount — so on one, these would be shop
items that take points and grant nothing. `Flavours: [tracker]` is what stops
that.

**Deliberately absent:** perk kinds beyond the two. `kind` is TEXT rather than
an enum so a site can add one without a migration, and an unrecognised kind is
**ignored** by the multiplier rather than treated as an error — the safe
direction, since an unknown perk should do nothing, not everything.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `GET /perks` | `RoleUser` | The wallet. Registered only when the host wired a page renderer, and never on the `api` process — that serves torrent clients, which have no wallet. |
| `POST /perks/spend` | `RoleUser` | Spend a held token on one torrent. |
| Widgets | member | Show what is **applied to their traffic**, not rows that say it should be. |

`Processes: ["web", "api"]` — the announce endpoints are registered on both,
and a perk that applied on one process and not the other would make a member's
ratio depend on which one served them.

When the host wired no page renderer, tokens can still be bought and still
apply; there is simply nowhere to spend one. The plugin logs that at boot,
because a store selling something unusable should not be silent about it.

No jobs; the in-memory table refreshes on a timer instead.

## Data

Owns `perks.tokens`. One row per token: who holds it, its `kind`, when it was
acquired, and — once spent — the `info_hash`, `spent_at` and `expires_at`.

**`expires_at` is set at SPEND time from the then-current setting.** Changing
the site's token duration therefore never shortens a perk somebody has already
paid for and started using. An unspent token has all three columns NULL and
does not expire.

## Configuration

`plugins.perks.*`:

| Key | Default | Why |
|---|---|---|
| `token_hours` | `168` | How long a perk lasts once spent; zero means forever. Seven days, **matching the hit-and-run seedtime requirement**, so a freeleech download is free for exactly as long as a member is required to seed it. Two numbers that mean different things and should move together — a site changing one should look at the other. |

`RefreshInterval` is 30 seconds, and is a constant rather than a setting. A
member who spends a token expects it to work on their next announce, which is
minutes away; `Spend` refreshes immediately anyway, so the timer only covers
perks that expired and other processes' writes.

## Dependencies

Core: `Storage.SchemaDB`, `Auth`, `Router`, config, the extension registry.

Imports `loon-plugins/tracker` for `tracker.SetMultiplier` — the one place this
plugin reaches into a sibling rather than through the registry, because the
multiplier is a hot-path function pointer on the announce.

## Hooks & callbacks

**Publishes** two, both satisfied structurally so consumers need no import of
this package:

| Key | What it answers |
|---|---|
| `ExtensionName` | Was this snatch free? The hit-and-run framework asks. |
| `pluginapi.PerkGranterName` | Grant a token. Lets the points store sell perks without importing this package. |

**Installs** `tracker.SetMultiplier`, which the announce calls per (member,
torrent) for the up/down factors.

### The Factors rule, which is the whole plugin

`Table.Factors` returns `(up, down)`, both 1 by default, and two details in it
are load-bearing:

- **Site-wide freeleech is read under the SAME lock as the token list.** Two
  lock acquisitions on the announce path could see the window close between
  them and credit a member at 1:1 for traffic the first half of the function
  had already called free.
- **Site freeleech does not short-circuit.** An upload-double token still
  doubles during a freeleech week. The two perks answer different questions,
  and a member who paid points for the upload multiplier should not silently
  lose it because the site got generous.

Declares no events.

## Lifecycle

`Provision` opens the schema DB, loads the perk table, registers the widgets,
installs the tracker multiplier, registers the two extensions, and — on a
non-`api` process with a renderer wired — parses templates and mounts `/perks`.
`Start` runs the refresh loop. `Stop` stops it.

## Files

```
plugin.go            lifecycle, Config, registration, the process/flavour rules
perks.go             Table, Factors, site freeleech, Spend
store.go             Store, PGStore
views.go             wallet page and spend action
widgets.go           what is applied to a member's traffic
templates/           wallet
migrations/          001_init.sql
perks_test.go        the table and its factors
sitefreeleech_test.go the site-wide window
```

## Testing

`go test ./perks/` — no database needed.

Covered: `Factors` across held, spent, expired and unknown kinds; that an
unknown kind does nothing; the site-freeleech window including its boundary;
and that a site-wide window does not cancel an upload-double token.

**Not covered:** the store's SQL — `Spend`'s write and the reload query are
only exercised against a real Postgres, and there is no integration test here
yet. Nothing tests the two-lock race described above; it is prevented by
construction and verified by reading. The wallet template is not executed in a
test.
