# Providers, backbones, and why the difference matters

Read this before adding a second news server. Getting the backbone wrong is one
of the few misconfigurations here that loses articles **silently** — nothing
errors, the logs look fine, and you find out months later that a release you
should have indexed simply isn't there.

## Two identifiers, pulling opposite ways

An article on Usenet has two identities, and almost every design decision in
this plugin follows from the difference.

| | **Article number** | **Message-ID** |
|---|---|---|
| Looks like | `4812003` | `<part1of42.abc@powerpost>` |
| Assigned by | the **server** | the **poster**, at post time |
| Scope | per group, per backbone | globally unique, forever |
| Same article on another backbone? | **different number** | **same message-ID** |
| Used for | watermarks, coverage ranges, `OVER` requests | staging dedup, NZB segments, health `STAT` |

So the same article is number `4812003` on one backbone and something entirely
unrelated on another — but it carries the identical message-ID everywhere.

That single fact explains two things that otherwise look contradictory:

- **Crawl progress cannot be shared between backbones.** A watermark of
  `4812003` means "I have fetched up to here" only on the backbone that issued
  that number. On another backbone it points at a different article, or at
  nothing.
- **Staging dedup works across all providers.** Articles are deduplicated by
  message-ID, so the same post fetched from three providers is stored once.

## Why you'd run more than one provider

Two separate reasons, often confused:

**More connections.** Providers cap concurrent connections per account. A second
account gets you more parallel fetching.

**More content.** Providers genuinely differ in what they *hold* — different
retention windows, different completion, different takedowns. A release missing
a few segments on one backbone may be complete on another, and because staging
dedups by message-ID, the segments merge into one NZB. **This only happens across
different backbones.** Two accounts on the same backbone see the same articles.

## What "backbone" means here

Most providers do not run their own infrastructure; they resell one of a small
number of upstream networks. Providers reselling the same upstream see the same
articles with the same numbers.

The plugin keys crawl state — watermarks, server bounds, fetched-range coverage
— by **backbone**, not by server:

- **Same backbone, two accounts** → one shared set of watermarks. The second
  account contributes *connections*. If they were keyed separately, the second
  account would re-crawl every article the first had already fetched.
- **Different backbones** → separate state, and genuinely different content.
  This is where multi-provider pays off.

## Setting it

The `Backbone` field in the Usenet settings wizard. Free text, matched
case- and whitespace-insensitively, so `Omicron` and ` omicron ` are one
backbone.

**Leave it blank if you are not sure.** A blank backbone means "this server is
its own backbone" (`srv:<id>` internally), and the failure modes are deliberately
asymmetric:

| Mistake | Cost |
|---|---|
| Two servers wrongly marked the **same** backbone | Each treats the other's fetched ranges as covered and **skips articles it never fetched**. Silent, permanent until you reset coverage. |
| Two servers on one backbone wrongly marked **different** | Duplicate crawling. Wasteful, noisy, but nothing is lost. |

So the default errs toward duplicate work rather than data loss. Only name a
backbone when you actually know two accounts share one.

## Roles: active vs backup

Each server has a role, a priority, and an optional per-provider connection cap
(providers sell different limits, so this is not one global number).

- **active** — crawled every pass.
- **backup** — idle. Promoted only to cover an active provider that is currently
  failing, one backup per downed active.

Backups are *standby*, not extra capacity. Running them alongside healthy
actives would quietly exceed the connection budget you planned for, and because
each provider crawls independently, it would multiply the work rather than share
it.

A provider that fails to open is benched for a cooldown — long enough that a
dead server isn't re-dialled every pass, and short enough that it comes back on
its own. The pass continues with whoever is left; one dead provider never stops
the others.

## Consequences elsewhere

- **Shared range packs** (exporting coverage so another install can skip ahead)
  are only valid between installs on the **same backbone**. Applying a foreign
  pack marks ranges as covered that hold entirely different articles — the same
  silent skip as above. Any future pack import must verify backbone identity
  first, ideally by fetching a known article number and comparing message-IDs.
- **Curated newsgroup packs** have no such restriction: group *names* are the
  same everywhere. Only numbers are backbone-specific.
- **Health checking** benefits from multiple backbones: an article missing on one
  may be present on another, which turns an inconclusive result into a definite
  one.

## Known limitations

- The wizard edits **one** server. Additional providers currently need direct
  SQL against the `servers` table (`role`, `priority`, `connections`,
  `backbone`). A full provider-management UI is pending.
- Backbone is free text. A picker built from a public provider/backbone listing
  is planned; it would also give pack import something reliable to match on.
- Connections are pooled per *server*, not per backbone, so two accounts on one
  backbone don't yet combine into a single larger pool.
