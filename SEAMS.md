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
importing the other. There are **70** of them, counted from the EXPORTED `…Name` and
`…Prefix` constants in `pluginapi` whose value is a NAMESPACED key,
recounted 1 Sep 2026 (67 on 22 Aug). This number is CHECKED, not
decorative: `audit_seams` re-derives it from the source and fails when the
sentence drifts, which is how the last three additions were caught — two
of them written in this repo and left uncatalogued by their own author. They are discoverable: an
author reading `pluginapi` sees what exists, the compiler catches interface
skew, and `/admin/contracts` can report an unwired one.

> **Recount it rather than trusting that number.** It was 41 when this document
> was written and nobody noticed it drifting to 52 — eleven contracts arrived
> and none of them reached the catalogue below, which is the exact failure this
> page exists to prevent. One line does it:
>
> The line changed TWICE on 22 Aug 2026, the second time because a check
> was finally written to run it (`audit_seams.py` in loon-demo-site) and it
> disagreed with the number above by one. Three faults, partly cancelling:
>
> * `[A-Z][A-Za-z]*` excludes DIGITS, so `I18nDeclarerName` — a real,
>   exported, wired contract — was never counted. A pattern that cannot
>   spell `i18n` will not find the next `oauth2` or `sha256` either.
> * `SlotName = "name"` is a slot ATTRIBUTE and `NewznabCachePrefix =
>   "newznab:v1:"` is a cache key. Neither is on the registry; both were
>   counted. Requiring a dot in the value is what separates a registry key
>   from a constant that merely ends in `Name`.
> * `HealthReporterName = "health."` is a bare prefix with nothing after the
>   dot, and `[^"]+` requires at least one character. A pattern that cannot
>   spell the shortest possible prefix is not counting prefixes.
> * Three wrongs and two misses came to 59, which looked stable enough to
>   restate. They no longer cancel, and the number and the command agree: 58.
>
> The earlier change, also 22 Aug 2026, was because running it did not
> reproduce the number above it. The stated 63 had been counted WITH test files, so
> `pluginapi`'s own `testPrefix = "test.thing."` was in it; and the pattern
> matched any identifier, so it also counted `backupPrefix` and `statsPrefix` —
> lowercase, unexported, and unreachable from another package, which makes them
> not contracts by the definition three paragraphs up. Requiring an initial
> capital is what makes the recount and the number the same question.
>
> ```
> grep -rhoE '\b[A-Z][A-Za-z0-9]*(Name|Prefix)\s+=\s+"[a-z0-9]+\.[^"]*"' \n>   pluginapi/*.go --exclude='*_test.go' | sort -u
> ```
>
> Diff that against the tables below when you add a contract.

**Bare-string conventions** are a key agreed between one host and one plugin,
typed as a raw func, declared nowhere:

```
games.csrf                medals.csrf                magic.csrf     (deprecated)
achievements.icons        achievements.files                        (deprecated)
achievements.l10n.slugs   achievements.l10n.resolve                 (deprecated)
medals.l10n.slugs         medals.l10n.resolve                       (deprecated)
```

Nothing lists these. A plugin author cannot find them, so the tenth plugin to
need a token invents an eleventh key — or, as actually happened, ships without
one and every form 403s. **Every seam in the second tier is a seam that will be
reinvented.**

**The ones listed above are collapsed** — the csrf keys on 18 Aug 2026, the
l10n, icon and file keys on 20 Aug.

This paragraph used to end "and the tier is empty of live seams". It was
not, and saying so is the reason it stayed that way: two live bare-string
seams were never on the list, so nothing contradicted the claim.

```
notify.fanout      the host's notification channel. Registered as a bare
                   string in the host and looked up the same way, so the
                   type is agreed in a comment and nowhere else.
rewards.payout.    a PREFIX, and the worst shape of all: neither side has
                   a constant, because both COMPUTE the key --
                   "rewards.payout." + kind, in the plugin and again in
                   the host. A typo in either is a seam that silently
                   does not connect.
```

Both were found on 22 Aug 2026 by `audit_seams.py`, which is what now
keeps this section honest. Every one is
still READ, so a host that has not moved keeps working, and nothing new should
add to the list.

The l10n pair is the one to remember, because it shows the cost rather than the
principle. Two plugins each agreed a private pair with the host, so the host
registered **the same two closures four times**, under a comment that said "one
key per consumer, the same closures". A third plugin would have made it six. The
icon list is the other failure mode: agreed twice for one list, with two
different TYPES — `func() []string` and a plain `[]string` — so one consumer saw
sprites added later and the other went on offering the list as it stood at
Provision, with nothing to say which was right.

**A third position, which this page did not have a name for.** Nine live seams
are neither of the two tiers above: the key IS a named constant, but the
constant lives in the PROVIDER's package, not in `pluginapi`. They are spelled
`…Extension` or `…Name` and they are properly documented — at their
declaration, and nowhere a plugin author would look.

That is better than a bare string and worse than a contract. A consumer either
imports the provider — the coupling `pluginapi` exists to remove — or retypes
the string and gets no compiler help when it changes. All nine were invisible
to this document until `audit_seams.py` was written on 22 Aug 2026.

| Key | Declared as | For |
|---|---|---|
| `achievements.list` | `achievements.ListExtension` | The defined achievements. Absent means the plugin is not installed, which a host handles by not offering the page. |
| `achievements.metrics.` | `achievements.MetricSourcePrefix` | **Prefix.** A plugin contributes a metric an achievement can be scored on; achievements scans by prefix and type-asserts. |
| `catalog.registry` | `catalog.RegistryExtension` | Core publishes the catalog `Registry` here. The scraper reads it. |
| `communities.followed` | `communities.FollowedName` | The communities a member follows. Looked up after Boot and duck-typed in the template, so the plugin owes the host no shared type. |
| `dailyreward.status` | `dailyreward.StatusExtension` | Whether today's claim is available, so a stat-bar button or nav badge can show it without duplicating the once-per-day rule or reading the plugin's table. |
| `forum.spotlight` | `forum.SpotlightName` | Feeds the Community Spotlight card. A host without one simply does not look it up. |
| `news.home` | `news.HomeFeedName` | Feeds the home page's news card. Same shape as the spotlight. |
| `rewards.sources` | `rewards.SourceCatalogExtension` | The reward source catalogue. Seeded once, then owned by the operator, so a host changing its seed will not rewrite what they edited. |
| `store.flavour` | `store.FlavourExtension` | `func() (indexer, tracker bool)` — the host's answer to which half of a site this is. |

> **Rule of thumb.** If a second plugin could ever want it, it belongs in
> `pluginapi` with a name constant and a type. If a second plugin could ever
> want to add ANOTHER ONE, make it a `Prefix` and scan it with
> `pluginapi.Contributions[T]`. A bare string is for a seam that
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
| `admin.nav.` | `AdminNavSource` | **Prefix.** A link to a plugin's own admin routes. For a surface that is a route GROUP and so cannot be a single `SlotAdminPage` view — without it the pages are served and in no nav, findable only by URL. See CHECKLIST §7. |
| `invites.granter` | `InviteGranter` | Credit invites to a member. |
| `ranks.granter` | `RankGranter` | Grant a rank for a duration. |
| `ranks.stats` | `RankStats` | Every member's traffic figures, in one call. The one definition of "who is poor". |
| `groups.display` | — | A member's badge, resolved in batch. |
| `groups.audit` | — | Membership history. |
| `limits.boost` | `APIBoost` | A live multiplier on API allowances. |
| `auth.apikey` | `APIKeyResolver` | Turn an API key into a member. The only way a machine-facing endpoint knows who is calling. Optionally also `APIKeyIssuer`, type-asserted off the same registry entry. |
| `auth.invite.issue` | `InviteIssuer` | Open the door on a plugin's behalf. An application queue decides WHO may join; the host still decides how — same mint, same window, same email, same chain. |
| `auth.regmode.*` | `RegistrationModeInfo` | **Prefix.** A way to join, offered beside the host's built-in open/invite/closed. `AllowsSignup` governs the PAGE, not the endpoint — an approved applicant arriving with an invite must still get in. |
| `cosmetics.effects` | `CosmeticResolver` | Who is wearing which name effect, avatar frame or profile ground, and their approved title. Keyed by USERNAME, because the templates that draw a member have a name and nothing else. |

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
| `catalog.taxonomy` | `Catalog` | The Newznab category tree. |
| `usenet.releasesink` / `.healthstore` / `.nfostore` / `.imagestore` / `.retitlestore` / `.activity` / `.catalog-stats` / `.junk-sweep` | various | The indexer's ports into a host's own domain. |
| `usenet.index` / `.admin` / `.newznab` | `UsenetIndex`, various | The read surface, the operator surface, and the Newznab endpoint. What a host renders a release page from. |
| `usenet.series` | `SeriesIndex` | Every copy of an episode, grouped. What the /series pages read. |
| `tv.schedule` | `TVScheduleProvider` | What airs and when, for the shows this site carries. Filled by a six-hourly job because the upstream answers one day per call — a month view rendered live would be nineteen seconds of polite waiting per viewer. |
| `tv.gaps` | `TVGapFinder` | Aired, and we do not have it. The join between the schedule and the index, published by the **host** because only the host holds both. The detection half of the auto-request. |
| `newznab:v1:` | — | **Prefix.** Namespaces every cached Newznab response, so a worker can clear the whole search cache after an ingest (`cache.PrefixDeleter`, topic `EventIngested`). The one contract the 22 Aug recount found absent from this catalogue — which is the failure the warning at the top describes, happening again. |
| `usenet.grabs` | `DownloadGrabLookup` | Which releases a member has taken — the check that stops a download report being writable by anyone who guesses an id. |
| `usenet.recheck` | `ReleaseRecheckRequester` | Flag a release for the health sweep. A report is a signal; **the sweep decides from the articles themselves**. |
| `usenet.articleprobe` | `ArticleProbe` | Ask a provider whether specific articles are still there, and read the first bytes of one. Two verbs with a sharp rule between them: `StatMissing` reports only DEFINITIVE absences, because a socket that died is not an article that is gone, and treating "could not ask" as "missing" re-posts articles that still exist. |
| `mediainfo.summaries` | `MediaSummaries` | One line per release that carries a live media report, for a listing row. Newest report wins — two members describing one release is useful on a release page, but a row has space for one line, and the most recent is least likely to describe a file that has since been replaced. |
| `tracker.mirrors` / `tracker.mirror.make` | `TorrentMirrors`, `TorrentMirrorMaker` | Which releases also exist as torrents, and making one on demand. On-demand because an index of 160,000 releases would otherwise pre-build gigabytes of info dictionaries nobody asked for. |
| `collections.sink` | `CollectionSink` | Where a selection of releases can be filed. Deliberately narrow — name a member's own collections and take a batch. It cannot create one, read one, or touch anybody else's: a cart is a trolley, not an editor. |
| `search.torznab` | `TorznabSearch` | Torrent search, answered by whoever has torrents. |
| `trackers.search` | `TrackerSearcher` | Ask EXTERNAL trackers what they have — the plural is the distinction from every `tracker.*` key, which belongs to this site's own tracker. Third piece of the content pipeline: schedule knows it aired, gaps knows we lack it, this answers where a copy might come from. Adapters for clean interfaces only; politeness is structural (per-source spacing floored at 2s). |
| `requests.filer` | `RequestFiler` | **Proposed.** File an automated request with the board's `ScopeAutomated` origin, deduped by the board. Fourth piece of the content pipeline — the trigger that closes the loop. Dormant until a request board registers a filer (none does today); the host trigger computes what it would file and files nothing. Maps onto the board's existing `Request` fields, asks for no new column. |
| `agent.dispatch` | `GrabDispatcher` | **Proposed.** Hand a chosen torrent to the fleet to fetch and re-upload; the OUTBOUND half of the loop `content.pipeline` closes inbound. Implemented by whatever owns the agent work queue (host-side runtime, not a plugin), so dormant on this demo. Distinct from `requests.filer`: dispatch fetches a specific chosen copy now, the filer files a community request to source later. |
| `content.block.*` | — | **Prefix.** A block a page can render. |
| `content.pipeline` | `ContentPipeline` | Delivered bytes become a published release: dedup, artifact, metadata, fulfil the request, award, announce. Delivery-agnostic on purpose — Usenet feeds it today, the tracker and direct-download paths feed the same one. (It was catalogued here as "the shared render pipeline", which is a different thing entirely and describes nothing this contract does.) |
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
| `i18n.catalogue` | `MessageCatalogue` | READ the catalogue: the slug list for a definition form, and slug → text for the current viewer. One key for every consumer — it replaced four. |
| `icons.set.*` | `IconCatalogue` | **Prefix.** A CURATED icon list for a purpose (`icons.set.achievement-badge`). Falls back to the full catalogue when a host has curated nothing — a picker with too much in it beats one with nothing. |
| `icons.catalogue` | `IconCatalogue` | What icons this site can draw. Offer these in a picker instead of a free-text box. A func, not a slice: a sprite added later changes the answer. |
| `usertag.render` | `UserTag` | How this site draws a USERNAME, as finished HTML: role colour, equipped name effect, profile link. A plugin fragment does not use host templates, so without this every plugin either reinvents the chip or shows a bare name — eighteen render one and four applied the effects, by four different mechanisms, until this landed. Absent means a plain link to the profile, never nothing: a plugin that trusted the seam and got `""` would silently drop the author's name off its own page. |
| `css.stylesheet` | `StylesheetRegistrar` | A plugin's own CSS, handed over ONCE at Provision. The host gives it a URL, a content hash and the caching; the plugin never sees any of those. Absent means the plugin keeps its in-fragment `<style>`, which every browser honours — see docs/BACKLOG.md #13 in loon-demo-site for the three costs that carries. |
| `js.script` | `ScriptRegistrar` | A plugin's own JavaScript, handed over ONCE at Provision — the sibling of `css.stylesheet` and the same bargain: the host decides the URL, the caching and whether it is deferred, the plugin hands over bytes and a name. One file per plugin, served as-is: no module loader, no bundler, no dependency graph, because a plugin needing a build step would be asking every host to run its toolchain. |
| `policy.flag.*` | `PolicySource` | **Prefix.** A RESTRICTION a member is under — neutral leech today. The mirror of `multipliers.source.*` and combined by **ANY**, because a restriction does not compete: one source asserting it is enough and none can out-bid it. Modelling one as a multiplier fails silently in the generous direction — upload MAX starts at 1 and only rises, so a 0 loses to the floor and neutral becomes ordinary freeleech. |
| `tracker.swarmsnapshot` | `SeedingSnapshotter` | Who is seeding what, one row per member per torrent, so an economy can pay for seeding without reading the tracker's tables. The seeder count travels IN the row and is computed from the same result set: a pool divided by a denormalised counter but paid to separately-read rows stops summing to the pool. |
| `files.store` | `blob.Store` | Somewhere to put a plugin's uploads. One key for every plugin; the plugin picks the name it saves under. Absent means HIDE the upload control, never offer one that fails on submit. |
| *(core)* `RegisterWidget` | `core.Widget` | A placeable card. The host may also expose it as a `[widget …]` shortcode in page bodies. |

### Operations

| Key | Contract | For |
|---|---|---|
| `notify.ops` | `OpsNotifier` | Tell the operator something. |
| `notify.release` | `ReleaseNotifier` | Announce a release outward. |
| `backup.packs` | `BackupPacks` | Contribute to the one archive. |
| `feeds.status` | `FeedsStatus` | Feed health. |
| `metrics.source.*` | `MetricSource` | **Prefix.** What a plugin measures and the host cannot see — a staging backlog, announces, a failure rate. Counters and gauges only; the one distribution is the host's own request timing. |
| `health.*` | `HealthReporter` | **Prefix.** Whether a plugin is actually WORKING, as opposed to merely loaded. The state that earns it is `degraded`: a plugin that degrades gracefully is by construction one that can be silently useless. |
| `achievements.granter` | `AchievementGranter` | Award a badge. |
| `media.intake` | `ImageIntake` | Fetch an image a MEMBER named, and store it locally. **A plugin must never do this itself** — see the rule below. |

---

## The rules a seam lives by

Learned, each of them, by getting it wrong first.

**Register before Boot; look up siblings in Start.** All Provisions run before
any Start. A capability another *plugin* publishes is not there yet at
Provision — games missed the rewards granter and store missed `tracker.credit`
on the same afternoon, both silently degrading. Anything the *host* registers is
safe at Provision, and must be registered before `core.Boot` or it is never
seen.

**A closed set of options is a PREFIX, not a validated string.** Before
shipping a setting whose values are a fixed list, ask *can another plugin append
to this?* — and treat "no, surely not" as the answer that has been wrong every
time. The site's three ways to join were as closed as a set gets until
`applications` needed a fourth, and by then opening it meant changing the host's
validator, its storage, its form and its handler at once.

The tell is whether the set reads as a question or a list. "Which of these three
modes" is closed; "how may somebody join this site" is open, and so is *what can
be bought*, *what scales what a member earns*, *what goes on a page*, *where a
selection can be filed*. A genuinely closed set is one where a fourth value
would be a different FEATURE rather than another of the same thing — a site is
normal, read-only or in maintenance, and nothing a plugin writes belongs beside
those.

Scan with `pluginapi.Contributions[T]`, which exists because five domains wrote
the loop themselves and it drifted: two sorted their results and three did not,
so one dropdown was stable and another reshuffled between page loads, and only
one of the five checked that a value's own key matched the key it was registered
under — the disagreement that surfaces to an operator as "the site quietly
reverted my choice".

**A capability an operator might not want is a FEATURE, not a config key.**
`core.RegisterFeature` in Provision, and name the key on the `View` and `Widget`
it governs — core then hides both from every nav and every placement, and an
existing placement comes back still placed when it is switched on. Three things
that are easy to get wrong:

* **Check it twice.** In the view model, so the control stops being drawn and
  nothing is queried for it; and in the handler, because a form already open in
  somebody's browser outlives the page it came from. A feature that only hid its
  button is one anybody can still use by keeping a tab open.
* **The Description is for the person deciding.** "Thanks" tells an operator
  nothing. Say what stops AND what is kept — the fear that stops anybody trying
  a switch is that it deletes something.
* **A host mounts routes from `AllViews`, not `Views`.** Route mounting happens
  once at boot; a feature off at boot would otherwise never mount its route and
  could never be switched back on without a restart. Mount everything, refuse
  per request.

Fails ON everywhere — no service, an unregistered key, an unreadable store. A
flag that fails closed is worse than no flag: the capability vanishes and
nothing says why.

**A plugin says which half of a site it belongs to; the host does not keep a
list.** `core.Metadata.Flavours` is `[]string{core.FlavourTracker}` or
`{core.FlavourIndexer}`, empty for the majority that belong to both, and
`core.Boot` skips what does not match — exactly as it does for `Processes`. A
host that keeps the list keeps it wrong: it has to know that hitrun, seedlock
and perks are tracker plugins, and the day somebody writes a fourth it does not
know about it. Two things follow that are easy to get wrong:

* `Core.Flavours` is the SET of halves that are ON, not a mode. "Both" is the
  two-element set and never a value, because the moment it is one, every
  consumer downstream grows a three-way switch.
* **The tables outlive the plugin.** A site that ran as "both" and moved to
  indexer-only still has the tracker's rows, so anything reading them directly
  goes on reporting a swarm for a tracker that is not running. Three host pages
  did exactly that — a stats panel, a listing badge and a release page's
  download button, all linking to routes that no longer existed. If you read
  another half's tables, check the flavour too.

**A plugin never fetches a URL a member typed.** It asks `media.intake`.
Fetching a typed URL is a request the SERVER makes, from inside the network, to
an address the poster chose: a cloud metadata endpoint, a port scan of a private
subnet, or on a host whose egress goes through a VPN, a way to make the site
reveal its real address. The address rules, the redirect re-check, the size cap
and the content sniffing are one thing to get right rather than one per plugin —
and the way to get it wrong is subtle enough to be worth naming: the guard
belongs in `net.Dialer.Control`, which runs after resolution and is handed the
real `ip:port`. In `Transport.DialContext` it is handed the HOSTNAME, cannot
parse it as an address, and refuses every named host on earth while passing
every test you wrote for it.

**A widget returns BODY content; the host draws the frame.** A host wraps a
placed widget in its own section and heading, so a widget that renders its own
panel lands inside that one, heading under heading. `ranks`, `perks`, `hitrun`
and the tracker's swarm get this right; `comments` and `cosmetics` both got it
wrong and it stayed invisible until a poll landed in a 250px sidebar where the
two borders sit inches apart. State that belongs in a header — a count, a
"closed" tag — goes beside the first line of the body instead.

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
