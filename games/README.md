# games

Community point games: **the pot** and **charity**, both modelled on
MyAnonaMouse traditions.

**The pot** (`/p/pot`): members drop points in, capped per member per day.
At the target one contributor wins a configured percentage — drawn with odds
proportional to contribution — everyone at or above the consolation
threshold is granted a configured rewards-shelf `one_off` (idempotent by
`pot:<cycle>` reference), and a new pot opens. The remainder leaves the
economy: the house keeps it, deliberately.

**Charity** (`/p/charity`): pick a ratio ceiling and an amount
(admin-bounded), and the points are split evenly across every member at or
under that ratio who has downloaded at least the configured floor — need,
not inactivity. Poorest first for the uneven remainder. Anonymous both ways.

Admin knobs live at **Admin → Settings → Games**.

## Seams

- `core.Points` — every movement of points.
- `pluginapi.RewardBySlugGranter` (optional) — the consolation reward; absent
  means the pot pays its winner and grants nothing else.
- `pluginapi.RankStats` (optional) — the member figures charity finds need
  with; absent disables charity and the page says so.
- `games.csrf` — host token func, the rewards.csrf story.
