# chat plugin

The site shoutbox: a Discord/IRC chat bridge with live SSE updates and
webhook send-back. Members read the channel on `/chat` and post into it; the
message appears in Discord as theirs, via a webhook username/avatar override.

The chat **hub** stays host-owned. It is a Redis pub/sub fan-out shared by
three processes — the web tier's SSE subscribers, and the worker's Discord and
IRC bots, which each hold their own instance and publish into the same
channel. A plugin cannot own something two sibling plugins also need, so this
plugin owns the `/chat` **surface** and the host owns the pipe.

## Surface
- `GET  /chat` — the page. A loon `SlotSitePage` view (slug `chat`); the
  markup is this plugin's (`templates/chat.html`, embedded) and the host
  supplies the chrome. The host mounts site pages at `/p/<slug>` and, via its
  alias map, keeps `/chat` at `/chat` — it has been that URL for its whole
  life, and moving it to tidy plugin code would be a cost paid by members.
- `GET  /api/chat/recent` — the backlog the page loads before the stream.
- `GET  /api/chat/stream` — Server-Sent Events: one frame per message, plus an
  `online` event every 15s.
- `GET  /api/chat/online` — current viewer count and names.
- `POST /api/chat/send` — post to the Discord webhook. Rate-limited to one
  message per user per 2 seconds, capped at 2000 chars, with
  `allowed_mentions: {parse: []}` so nobody can `@everyone` through it.

The view's `MinRole` is the zero value with `Public` false, so the host
requires a signed-in account before rendering.

`Metadata.Processes`: `["web"]`.

## Data
Owns no tables and runs no migrations. History lives in the host's hub
(a capped Redis list).

## Dependencies
- **Core services**: `Router`, `Auth.Authenticate`, `RegisterView`.
- **Store**: none. `SetDeps(Deps{...})` from the composition root before
  `core.Boot`; `Provision` rejects a partial wiring.
- **Config keys / Metadata.Requires**: (none) — the Discord webhook and
  invite URLs are read through `Deps` from wherever the host keeps them.
- **Outbound**: `loon/httpclient.NewWhitelisted`, pinned to `discord.com` and
  `discordapp.com`. The webhook URL is admin-configured, so it is treated as
  untrusted input and cannot be aimed at an internal or metadata address.

### The message envelope deliberately does not cross the seam

The obvious design was to promote the host's `ChatMessage` into a shared
contract package: `Subscribe` hands back a typed channel, and a channel's
element type cannot be adapted the way a struct can. That would have meant a
contract change rippling through the Discord and IRC bots, which publish into
the same hub.

It turned out to be unnecessary. This plugin never reads a message field —
history goes straight to the JSON encoder, and a live message goes straight
into an SSE frame. So `Recent` returns `any` and `Subscribe` yields
**already-encoded** `[]byte`. The host encodes; the plugin moves bytes. No
shared type, no churn in two sibling plugins, and no extra work: the SSE
handler used to marshal each message itself.

`Subscribe` returns a cancel that **must** be called — the SSE handler defers
it. Without it the host keeps fanning out to a channel nobody reads and the
presence list keeps counting a viewer who has left. It is safe to call twice.

## Hooks & Callbacks
- Host hooks SET: (none).
- Extensions PUBLISHED / CONSUMED: (none).

## Lifecycle
- **Provision**: validates `Deps`, registers the page view and the four API
  routes. No-op outside the `web` process.
- **Start / Stop**: no-ops. The plugin used to own a background goroutine (the
  Redis subscriber); that went back to the host with the hub.

## Files
- `plugin.go` — registration, metadata, the view and route table.
- `deps.go` — `Viewer`, `Deps`, and the note on why the envelope stays out.
- `views.go` — the embedded page and its view model.
- `templates/chat.html` — the whole page body: three-column layout, CSS, and
  the SSE client.
- `handlers.go` — recent / stream / online / send, and the rate limiter.

## Testing
The page fragment renders from a **struct** view model rather than a map, and
that is not a style preference. `IsAnon` — which chooses between the composer
and a "log in to chat" prompt — had been read by this markup since it was
written and supplied by nothing, because a map answers a missing key with the
empty value and no error. It was unreachable rather than broken, since the
host gates the page on a signed-in account, but it would have come back the
moment the page was made public. A struct makes that a render error.

The host side is expected to test its own encoding pump; the reference wiring
covers frame encoding, idempotent cancel, and — the one that matters — that
opening and closing many subscriptions does not leak goroutines, with the fake
pushing more than the pump's buffer so the pump is genuinely parked on a send
when cancel arrives.
