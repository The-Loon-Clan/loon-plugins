# medals

The display cabinet: collectible medals a member buys with points or is
awarded, and wears — or quietly does not — on their profile.

- **/p/medals** — the catalogue and your collection in one view: buy what is
  for sale, tick what to wear, save.
- **/admin/p/medals** — define medals: slug, name, icon URL, description
  (plus a message-catalogue slug for per-viewer localization), optional
  bonus %, price (0 = award-only), order; enable/disable/delete.
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
