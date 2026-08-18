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

Charity is also **sold in the points store**, where members spending points
actually look. It is registered as a contributed store item type rather than
copied: create an item with reward type Charity at /admin/store, and its card
draws an amount box bounded by the same Games settings and — unless the def
pins a band in Ref — the same ratio chooser. The store debits, this plugin
distributes; the debit lands in the member's history under `spend_charity`,
the code the charity page has always written, so one gift reads the same
wherever it was made.

Admin knobs live at **Admin → Settings → Games**.

## Seams

- `core.Points` — every movement of points.
- `pluginapi.RewardBySlugGranter` (optional) — the consolation reward; absent
  means the pot pays its winner and grants nothing else.
- `pluginapi.RankStats` (optional) — the member figures charity finds need
  with; absent disables charity and the page says so.
- `games.csrf` — host token func, the rewards.csrf story.
- **publishes** `store.itemtype.charity` (`pluginapi.StoreItemType`) — charity
  as a purchasable item. Registered in **Start**, and only where `RankStats`
  answered: an item that refuses every buyer is worse than no item. The store
  resolves types per request, so registering after Provision is safe and a
  store-less host simply never calls it. GRANT-ONLY, like every granter — the
  store has already taken the points when `Grant` runs, so it pays out through
  `distribute`, which deducts nothing.
