# Seams

What a plugin can reach for before writing its own, and what is still
duplicated because nothing was there to reach for.

Two documents already govern this repo: [CHECKLIST.md](CHECKLIST.md) is what a
plugin must satisfy, [GRADES.md](GRADES.md) is how each one scores. This is the
third question, the one an author asks *first* and currently answers by
grepping: **does this already exist?**

---

## The two tiers, and why the drift happens

A seam is a value on the extension registry. There are two kinds, and the
difference matters more than it looks.

**Declared contracts** live in [`pluginapi`](pluginapi/) — an interface or func
type, a `…Name` constant, both sides importing the contract and neither
importing the other. There are **41** of them. They are discoverable: an author
reading `pluginapi` sees what exists, the compiler catches interface skew, and
`/admin/contracts` can report an unwired one.

**Bare-string conventions** are a key agreed between one host and one plugin,
typed as a raw func, declared nowhere:

```
achievements.icons        achievements.files
achievements.l10n.slugs   achievements.l10n.resolve
medals.l10n.slugs         medals.l10n.resolve
games.csrf                medals.csrf                magic.csrf
```

Nothing lists these. A plugin author cannot find them, so the tenth plugin to
need a token invents an eleventh key — or, as actually happened, ships without
one and every form 403s. **Every seam in the second tier is a seam that will be
reinvented.** The csrf keys were collapsed into one declared contract on
18 Aug 2026 for exactly that reason; the l10n and icon keys are the remaining
candidates.

> **Rule of thumb.** If a second plugin could ever want it, it belongs in
> `pluginapi` with a name constant and a type. A bare string is for a seam that
> is genuinely one host talking to one plugin, and there are fewer of those than
> it looks.

---

## The catalogue

Grouped by what they are for. `Prefix` entries are scanned (many providers, one
consumer); the rest are single values.

### Identity, access and display

| Key | Contract | For |
|---|---|---|
| `csrf.token` | `CSRFTokenFunc` | The host's per-request token. **Every POST form needs it** — see CHECKLIST §3. |
| `invites.granter` | `InviteGranter` | Credit invites to a member. |
| `ranks.granter` | `RankGranter` | Grant a rank for a duration. |
| `ranks.stats` | `RankStats` | Every member's traffic figures, in one call. The one definition of "who is poor". |
| `groups.display` | — | A member's badge, resolved in batch. |
| `groups.audit` | — | Membership history. |
| `limits.boost` | `APIBoost` | A live multiplier on API allowances. |

### The economy

Points themselves are `core.Points`; everything below is what points *buy*.

| Key | Contract | For |
|---|---|---|
| `rewards.granter.byslug` | `RewardBySlugGranter` | Grant a named one-off reward, idempotent by reference. |
| `medals.granter` / `medals.worn` / `medals.bonus` | `MedalGranter`, `WornMedalsFunc`, `MedalBonusFunc` | Award, display, and the optional mechanics. |
| `perks.granter` | `PerkGranter` | Credit a perk token. |
| `pointstore.granter` | `FlairGranter` | Equip a profile flair. |
| `tracker.credit` | `TrackerCredit` | Credit or forgive transfer. |
| `multipliers.source.*` | `MultiplierSource` | **Prefix.** Anything that scales what a member earns. The combining rules live once, in `ResolveMultiplier`. |
| `store.itemtype.*` | `StoreItemType` | **Prefix.** A purchasable kind contributed by another plugin. |

### Content

| Key | Contract | For |
|---|---|---|
| `catalog.taxonomy` | `CatalogName` | The Newznab category tree. |
| `usenet.releasesink` / `.healthstore` / `.nfostore` / `.imagestore` / `.retitlestore` / `.activity` / `.catalog-stats` / `.junk-sweep` | various | The indexer's ports into a host's own domain. |
| `search.torznab` | `TorznabSearch` | Torrent search, answered by whoever has torrents. |
| `content.block.*` | — | **Prefix.** A block a page can render. |
| `content.pipeline` | — | The shared render pipeline. |
| `entity.editors` | — | Editors registered per entity kind. |
| `tracker.torrentinfo` | `TorrentInfoFunc` | Name and size for one info-hash. |
| `magic.torrentpromotions` | `TorrentPromotionsFunc` | What is cast on one torrent — **data, not markup**. |

### Time

| Key | Contract | For |
|---|---|---|
| `events.scheduled` | `ScheduledEvents` | Named windows other systems gate on. A season is a site fact, not a rewards fact — which is why it was lifted out of rewards. |

### Presentation

| Key | Contract | For |
|---|---|---|
| `i18n.declare` | `I18nDeclarer` | Seed a plugin's default strings into the catalogue. Seed-only. |
| `icons.catalogue` | `func() []string` | What icons this site can draw. Offer these in a picker instead of a free-text box. |
| *(core)* `RegisterWidget` | `core.Widget` | A placeable card. The host may also expose it as a `[widget …]` shortcode in page bodies. |

### Operations

| Key | Contract | For |
|---|---|---|
| `notify.ops` | `OpsNotifier` | Tell the operator something. |
| `notify.release` | `ReleaseNotifier` | Announce a release outward. |
| `backup.packs` | `BackupPacks` | Contribute to the one archive. |
| `feeds.status` | `FeedsStatus` | Feed health. |
| `achievements.granter` | `AchievementGranter` | Award a badge. |

---

## The rules a seam lives by

Learned, each of them, by getting it wrong first.

**Register before Boot; look up siblings in Start.** All Provisions run before
any Start. A capability another *plugin* publishes is not there yet at
Provision — games missed the rewards granter and store missed `tracker.credit`
on the same afternoon, both silently degrading. Anything the *host* registers is
safe at Provision, and must be registered before `core.Boot` or it is never
seen.

**Register AS the declared type.** The registry asserts on the exact type, so a
bare `func(...)` never satisfies `pluginapi.SomethingFunc`. Wrong type under the
right name is a wiring bug, and the consumer should fail loudly rather than
behave like a host that wired nothing.

**Prefer per-request resolution for prefix scans.** `store.itemtype.*` is looked
up per request, which is what lets a provider register in Start and what lets a
plugin be absent without the consumer caching a nil. `ResolveMultiplier` does
the same on the announce path — one map read is cheaper than a cache that can
be wrong.

**Granters are grant-only.** The caller debits, the granter hands over. A
granter that touches the ledger double-charges, because the points are already
gone by the time it runs.

**Cross data, not markup.** A plugin contributing a *fragment* to another
plugin's page gives that page two visual languages and splits ownership of its
accessibility. `magic.torrentpromotions` returns rows and the tracker draws
them; `StoreItemType` declares *fields* and the store renders them.

**Absence is a normal state.** A soft seam that is missing means the feature is
not on this site — hide the surface rather than showing one that fails. An item
whose provider is gone is hidden, exactly as an off-flavour item is.

---

## Still duplicated

A review of all 49 plugins on 18 Aug 2026. Ranked by duplication removed per
unit of work; the plugins named are the evidence.

### 1. Runtime settings — *three unrelated answers*

`core.ConfigService` is a boot-time read-only snapshot from the config file, so
an operator cannot edit it at all. Everything else split two ways:

- **Own key/value table** in the plugin schema: `games`, `magic`, `usenet`
- **Host `site_settings`** via a per-plugin `Deps.Settings` seam: `donations`,
  `irc`, `communities`
- **Own settings page** (`SlotAdminSettings`): `agent`, `catalog`, `discord`,
  `games`, `magic`

Each private implementation repeats the same four moves — a typed `Config`, a
`defaults()`, a read that overlays saved rows and keeps the default on a parse
failure, and a save that writes every form key blind. `games` and `magic` are
near line-for-line.

**Seam:** declare knobs once — key, type, default, one line of help — and get
storage, typed reads, validation and a generated admin section. A knob then
cannot exist without documentation, and five settings pages become one.

### 2. Definition catalogues — *nine tables that are one table*

`slug`, `name`, `icon`, `enabled`, `ordinal`, plus hand-written CRUD:
`achievements`, `events`, `magic` (buff_defs), `medals`, `playlists`, `ranks`
(groups), `rewards`, `rewards` (achievements), `backup` (inventory).

The gaps are the evidence: **medals** had no edit action until 18 Aug — a
mistyped icon could only be fixed in SQL — and **achievements** still has none.
The slug pattern is re-typed per plugin; ordering, delete confirmation and the
picker that offers a catalogue to another plugin are each re-implemented.

**Seam:** declare a catalogue and its extra columns; receive the table, the
admin page (create/edit/toggle/delete/reorder), slug validation, an icon picker
wired to `icons.catalogue`, and a slug picker other plugins consume.

### 3. Naming a member — *three ways, one of them forbidden*

- `public.user_display`, the sanctioned view: `communities`, `forum`, `ranks`
- **Direct `JOIN users`** from a plugin schema: `messages`, `roadmap`, `wiki`
- `core.Users.GetByID` per row: `games`, `magic`, `medals`, `tracker`

The middle group reads the host's own table. The last is an N+1 — fine for one
caster on a magic page, quietly quadratic on a history table.

**Seam:** `core.Users.DisplayBatch(ctx, ids)` → name, avatar, role, one query,
over the view. Then delete the direct joins.

### 4. Spend, grant, unwind

Eight plugins debit points and hand something back: `communities`, `games`,
`magic`, `medals`, `offers`, `pointstore`, `requests`, `store`. Seven also
refund. `store.purchase()` is the careful version — claim stock first so nobody
is charged for a sold-out item, debit, grant, unwind both on failure — and the
others re-derive subsets of it.

**Seam:** `core.Points.Spend(ctx, user, n, reason, func() error)` that refunds
when the closure errors. One place holds the rule that a failed grant never
costs a member points.

### 5. Terminal job state

22 plugins register scheduled jobs. CHECKLIST §5 already carries a MUST about
this because the bug keeps landing: a path that returns without reaching
`SetIdle` or `SetError` shows "running" forever and never re-triggers. Two
recorded instances — the promotion sweep, and the tracker's cheat sweep where a
`defer SetIdle` erased an error path's `SetError`.

**Seam:** `schedule.Sweep(job, func(ctx) error)` — marks running, runs, sets
exactly one terminal state. Daemon jobs keep the raw API, since "running" is
their honest steady state.

### 6. Pagination

Thirteen plugins carry `Paginate` / `PageOffset` / `RenderPagination` in their
own `Deps`; ten also compute `(page-1)×size` by hand somewhere. Pure wiring
duplication — the code already lives on the host.

**Seam:** one registry key, resolved the way `csrf.token` now is.

### 7. Schema store boilerplate

`achievements`, `events`, `rewards` and `tracker` each define the identical
`sel` / `get` / `exec` helpers over `SchemaDB.WithTx`, because `SET LOCAL
search_path` is the only way into a plugin schema. Plugins holding the raw pool
must schema-qualify every statement instead — a trap learned twice, by `games`
and by `store`.

**Seam:** put `Select` / `Get` / `Exec` on `core.SchemaDB`.

### 8. Uploads and their allowlist

`achievements`, `communities`, `roadmap` and `wiki` each wire `blob.Store` with
their own cap, sniff and naming. `blob.ImageExts` is already shared; the rest is
not. This is also where a **resource registry** for named site assets lands, so
the two want designing together.

### 9. Submit → triage → resolve — *lowest confidence*

`reports`, `tickets`, `requests`, `roadmap`, `offers`, `curation`, `uploads` are
the same story about different nouns. `tickets` constrains status in the schema;
`roadmap` keeps an allowlist in Go.

Read all seven before committing to anything. A torrent request and a support
ticket may differ enough in their middle states that a shared engine becomes
configuration soup — and this is worth saying no to if so.

---

## What not to extract

**Event declaration.** Thirteen plugins call `DeclareEvent` and they look alike
because the contract is working. Nothing is shared underneath.

**Admin CRUD in general.** Only the *definition* catalogues in §2 share a shape.
The rest edit genuinely different things, and a generic CRUD framework would fit
none of them. The line is the table shape, not the fact of editing.

**A plugin's own domain logic.** Two plugins computing a ratio is not
duplication when one is announce crediting and the other is a charity band —
they already meet at `ranks.stats`, which is the right amount of sharing.

---

## If you do three

**Settings, definitions, member-naming**, in that order. They are the three
where an author currently has to *choose* between existing approaches, and
choosing is what produces drift — the same mechanism that left 58 POST forms
without a CSRF token and pointed a medal at an icon the sprite sheet does not
have.

Each also removes a class of *missing feature* rather than only lines: the edit
action nobody writes, the settings knob with no documentation, the N+1 nobody
measured.
