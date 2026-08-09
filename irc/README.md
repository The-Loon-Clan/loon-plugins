# irc plugin

The IRC bridge: a bot that joins one configured channel, relays chat in both
directions between IRC and the site's chat hub, links IRC nicks to site
accounts over PM, and runs a small PM command set (help, link, invite,
stats, whisper) — plus the account-link card users see on /profile. Inert
until an admin sets `irc_server` / `irc_channel` and flips `irc_enabled`.

## Surface

- `POST /profile/irc-unlink` — authed (host session stack via
  `c.Auth.Authenticate()`); drops the viewer's link and clears the verify
  token. Registered by the web leg at the host's original URL.
- Views: SlotUserWidget `irc-link` (the /profile card, self-only via
  `core.ViewSubject`).
- IRC-side PM commands: `help`, `link <token>`, `invite`, `stats`,
  `whisper <user> <body>`.
- Processes: `["web", "worker"]`. The worker runs the bot; the web leg is
  the card + unlink route and never opens a connection.

## Data

- No owned tables. Links live in the host's `irc_links` table and the verify
  token in a host `users` column — both behind the `LinkStore` seam.
  Whispers persist through the host's DM store (`DMStore`).
- Chat traffic goes to the host-owned hub behind `pluginapi.ChatHub`
  (Redis ring + pub/sub + DB write-through host-side).
- All runtime config is 12 `irc_*` host settings rows, read live through
  `SettingReader` on every connect attempt and bridged message — admin
  changes apply without a restart. There is no config-file key.

## Dependencies

- Store: none of its own. `Deps` carries `DMStore` (whisper persistence,
  crossed structurally by the host's DM repository), `LinkStore` (6 methods;
  `GetIRCLinkByNick` must be case-insensitive), `SettingReader` (one keyed
  read, crossed structurally), `UserStore` (by-id + by-username),
  `NewHub func() pluginapi.ChatHub`, `CreateInvite` (ErrNoInvites sentinel;
  nil disables the PM invite command), `Viewer`, and `BaseURL`.
- Libraries: `lrstanley/girc` (TLS 1.2+ with pinned ServerName, optional
  SASL PLAIN), `golang.org/x/net/proxy` (optional SOCKS5 egress via
  `irc_socks5_addr`).

## Hooks & Callbacks

- Extensions CONSUMED: `pluginapi.LookupGroupDisplay` at Start — optional;
  absent, chat lines carry no rank label.
- Extensions PUBLISHED: (none).
- Host hooks set: (none).

## Lifecycle

- Provision: validates Deps, registers the card + unlink route (web leg),
  builds the bot + its hub instance (worker leg).
- Start: resolves GroupDisplay, starts the hub's Redis subscriber, launches
  the hand-rolled connect/backoff loop (5s doubling to 5min, reset on clean
  exit, stops retrying when `irc_enabled` is off) and the hub→IRC bridge
  goroutine.
- Stop: `Shutdown()` — disconnects and unsubscribes the bridge (the host
  version left that goroutine blocked on the hub until process exit).
- Admin trigger on the "IRC Bot" job = reconnect (re-reads settings without
  a worker restart; the bridge subscription survives it).

## Message flow (deliberately not a mesh)

IRC ↔ site works both ways, and discord → IRC works (the discord bot
publishes into the hub this bot subscribes to). Nothing forwards IRC → 
Discord: the discord bot never subscribes, and only a site user's Send hits
the Discord webhook. Anti-loop: forwarded lines are remembered in a bounded
sent-cache and suppressed when they echo back.

## Files

- `plugin.go` — lifecycle, leg split, view + route registration.
- `deps.go` — Deps, seam interfaces, DTOs, ErrNoInvites, token mint.
- `bot.go` — the bot: connect/backoff, TLS/SASL/SOCKS5, channel bridge both
  directions, PM commands, rune-safe line capping.
- `views.go` — the /profile card + unlink handler.

## Testing

- Unit-tested: Provision guards and leg split, Metadata shape, worker-leg
  wiring, rune-safe body capping, the sent-cache echo guard.
- Needs integration (live IRCd + DB): connect/SASL/SOCKS5, the bridge loop
  against a real hub, and the PM command flows.

## Removed in the lift

`DeliverWhisperPM` — documented as "the entry point the site's
WhisperRouter calls", but no WhisperRouter exists anywhere and the method
had zero callers. Deleted rather than carried; if site→IRC whisper delivery
is built, it needs a published extension, not a dormant method.
