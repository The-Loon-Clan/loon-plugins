# medals

The display cabinet: collectible medals a member buys with points or is
awarded, and wears — or quietly does not — on their profile.

- **/p/medals** — the catalogue and your collection in one view: buy what is
  for sale, tick what to wear, save.
- **/admin/p/medals** — define medals: slug, name, icon, description
  (plus a message-catalogue slug for per-viewer localization), optional
  bonus %, price (0 = award-only), order; enable/disable/delete. **No edit
  action** — a mistyped field is fixed by deleting and recreating, or in SQL.

## What a medal looks like

The icon field takes either an **icon name** (`star`, `shield`, `verified`,
`coin`, `check`, `clock`, `globe`, `users`) or an **image URL**
(`/uploads/medals/founder.png`). Left blank, the medal picks one from its
slug and keeps it — stable for as long as the medal exists, because a badge
that changed face between page loads would be unrecognisable.

Icon names are the HOST's sprite ids, the same coupling the store's cards
and the ranks groups widget have: a host missing a symbol draws an empty
space, never a broken page. Anything that looks like a path but is not one
this site can serve — a Windows path, a bare filename — falls back to the
sprite rather than rendering a broken image, which is what one demo medal
did for months holding `C:/Program Files/Git/uploads/...`.

`pluginapi.WornMedal` therefore carries **both** `Sprite` and `Icon`, with
exactly one set, so a host template picks with an `{{if}}` instead of
guessing from the string's shape.
- **rewards.payout.medal** — the payout kind that was declared with no
  implementation now settles: the host registers a handler that resolves
  `pluginapi.MedalGranterName` lazily, so any reward (an achievement, the
  pot's consolation, a per-unit drip) can pay a medal by slug.

## The optional bonus

A medal MAY name a bonus percentage. This plugin only ever **answers** —
`pluginapi.MedalBonusName` reports each member's summed bonus across worn
medals — and applies it to nothing. Whether medals carry mechanics is a
site-culture decision: a host that wants bonus-points medals calls the seam
where its points are earned; a host that wants medals to be nothing but
medals wires nothing and loses nothing.

## Published

- `medals.granter` — `pluginapi.MedalGranter` (idempotent; unknown slug is a
  quiet no-op, the payout tolerance)
- `medals.worn` — `pluginapi.WornMedalsFunc`, the profile's icon row
- `medals.bonus` — `pluginapi.MedalBonusFunc`, the optional mechanics

## Consumed (host-registered before Boot)

- `medals.l10n.slugs` / `medals.l10n.resolve` — the message catalogue
- `medals.csrf` — the host token every form embeds
