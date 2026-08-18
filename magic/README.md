# magic

Torrent promotions, the NexusPHP tradition: a member spends points to cast a
**buff** on a torrent — free leech, double upload, one of the six classics,
or (once practised) a custom ratio pair — **private** (themselves), **public**
(everyone), or for one named member, lasting 24–360 hours.

- **/p/magic** — the cast form (reached with `?hash=` from a torrent page)
  and the full history: every cast, its ratios, its window, its status.
  A cast cannot be edited; an admin can terminate it, and the row stays.
- **Resolution**: the highest upload factor and lowest download factor across
  every active magic visible to a member win — a public promotion never
  overrides a private one with better rules, and promotions stack without
  limit. Published as `pluginapi.PromoResolver`; the tracker folds it into
  announce crediting best-of with its other economies (perks tokens).
- **Levels**: points spent casting are experience. Levels discount casts
  (2%/level, cap 20%), extend the duration reach (48h + 48h/level, cap
  360h), and unlock custom ratios (level 1).
- **Cost** = scope base × √(size ÷ typical) × ratio strength × √hours ÷ 40,
  then the discount. Knobs at **Admin → Settings → Magic**.

## Seams

- registers as a USER MULTIPLIER source (`multipliers.source.magic`,
  upload/download dimensions) — one indexed read on the announce path;
  errors are no-opinion. The combining rules live in pluginapi/multipliers.go.
- consumes `tracker.torrentinfo` (`pluginapi.TorrentInfoFunc`, in Start) for
  names, sizes and existence checks — absent, casts are priced size-1 and
  unnamed.
- consumes `magic.csrf` — the host token every form embeds.
- `core.Points` for the spend; refund on a failed cast.
