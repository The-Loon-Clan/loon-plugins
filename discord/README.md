# discord plugin

The Discord bridge: a gateway bot that links Discord accounts to site
accounts, syncs Discord roles from site authority and paid ranks, relays the
configured Discord channel into the site's chat hub, mints site invites over
a slash command, and announces new releases — plus the account-link card
users see on /profile and the bot's own section of /admin/settings.

## Surface

- `POST /profile/discord-unlink` — authed (host session stack via
  `c.Auth.Authenticate()`); drops the viewer's link. Registered by the web
  leg at the host's original URL.
- `POST /admin/settings/discord/save` — NOT registered here; the host mounts
  the SlotAdminSettings action inside its admin group (admin-gated).
- Views: SlotUserWidget `discord-link` (the /profile card, self-only via
  `core.ViewSubject`); SlotAdminSettings `discord` (MinRole RoleAdmin) with a
  `save` action.
- Discord-side: `/setup-verify` (ManageServer only) posts the Verify
  button+modal flow; `/invite` mints a site invite for a linked user.
- Processes: `["web", "worker"]`. The worker runs the bot; the web leg is
  views + the unlink route and never opens a gateway connection.

## Data

- No owned tables. Links live in the host's `discord_links` table and the
  verify token in a host `users` column — both behind the `LinkStore` seam,
  because the host reads `discord_links` independently (admin user list).
- Chat traffic goes to the host-owned hub: a Redis ring (`chat:messages`) +
  pub/sub channel (`chat:broadcast`) + best-effort DB write-through, all
  behind `pluginapi.ChatHub`. The `pluginapi.ChatMessage` json tags are the
  wire format.
- All runtime config is host settings rows (`discord_*`), via the typed
  `Settings` seam.

## Dependencies

- Store: none of its own. `Deps` carries `LinkStore` (7 methods over
  discord_links + the verify token), `UserStore` (GetUserByID), `Settings`
  (typed get/set pairs — the host's SettingsService satisfies it
  structurally, which keeps the host's invite-URL page cache refreshed),
  `NewHub func() pluginapi.ChatHub` (the bot's own hub instance),
  `CreateInvite` (returns `ErrNoInvites` when the balance is spent; nil
  disables the /invite command), `Viewer` (session resolution for the web
  leg), and `BaseURL`.
- Libraries: `bwmarrin/discordgo`. Intents include the privileged
  MessageContent — it must be enabled in the Discord Developer Portal or the
  bot cannot connect.

## Hooks & Callbacks

- Extensions PUBLISHED: `pluginapi.ReleaseNotifierName` → the bot
  (`NotifyRelease`); the host's agent handler looks it up after Boot for
  upload-completion announcements.
- Extensions CONSUMED: `pluginapi.LookupGroupDisplay` at Start — optional;
  absent, account roles still sync and chat lines carry no rank label.
- Host hooks set: (none).

## Lifecycle

- Provision: validates Deps, registers the two views + unlink route (web
  leg), builds the bot + its hub instance and publishes ReleaseNotifier
  (worker leg).
- Start: resolves GroupDisplay, starts the hub's Redis subscriber, connects
  the bot, and launches the role-sync loop (5-minute ticker owned by the
  service, not the connection — an admin-triggered reconnect does not leak
  or duplicate it).
- Stop: `Shutdown()` — disconnects and ends the role-sync loop.
- Admin trigger on the "Discord Bot" job = reconnect (re-reads settings
  without a worker restart).

## Files

- `plugin.go` — lifecycle, leg split, view + route registration.
- `deps.go` — Deps, seam interfaces, DTOs, ErrNoInvites, token mint.
- `bot.go` — the gateway bot: connect, slash commands, verify button+modal,
  chat bridge + backfill, invite, release notify, two-axis role sync.
- `views.go` — the /profile card + unlink handler.
- `settings_view.go` — the /admin/settings section + save action.

## Testing

- Unit-tested: Provision guards and leg split, Metadata shape, rank-role map
  catalog/slug keying and failure safety, batch badge resolution, the
  ErrNoInvites sentinel.
- Needs integration (live Discord + DB): everything behind the gateway —
  connect/reconnect, interactions, role reconciliation against a real guild,
  and the Redis chat bridge.
