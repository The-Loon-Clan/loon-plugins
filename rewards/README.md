# rewards plugin

Earning described as **data** rather than as one Go job per rule.

A **reward** says what is earnable and on what terms. Its **payout lines** say
what it hands over. An **event** says when. The reward's *kind* decides what
"already paid" means, and a UNIQUE constraint enforces it — so a reward author
never writes idempotency logic again.

That last part is the reason this exists. Three earning rules on the host each
answered "have I already paid this?" differently — a ledger high-water mark, an
anniversary-day match plus a NOT EXISTS, and a bespoke table — and every new
rule re-answered it. Getting it wrong pays somebody twice.

## Surface

Two admin pages under Operations, because events are not reward-specific — a
season or an outage window is a site fact other systems can reference, and each
page then has one job:

- **`/admin/p/rewards-events`** (Events) — WHEN. Events with their window
  counts and currently-open window, plus a per-event panel to author windows on
  a calendar. That panel is the only way a one-off event (no cron, so nothing
  generates for it) ever becomes usable.
- **Site widget** (SlotSiteWidget, signed-in only) — a member's outstanding
  claim-delivery grants, each with what it pays and a Claim button. Renders
  **nothing** when there is nothing to claim: an empty card on every page load
  forever is how a widget gets ignored, and then the one time it matters it is
  already invisible.
- **`/admin/p/rewards`** (Rewards) — WHAT. Rewards with their payout lines, the
  newest 50 grants, and a *Test on me* button that runs a real grant against
  the operator's own balance.

Both carry the **configuration check** banner (below), because the row that
breaks a reward usually lives on the other page.

The engine is published on the extension registry as `rewards.trigger`; the
host's login path looks it up and calls it. The configuration store is
published as `rewards.admin`, which the host's ops API serves as
`/ops/rewards` for machine callers.

`Metadata.Processes: ["web", "worker"]` — web renders the page, resolves and
settles claims; worker materialises event windows and expires lapsed grants.

## Data

Owns six tables in the `rewards` schema (`migrations/001_init.sql`, plus `005`
for the events move):

| table | holds |
|---|---|
| `rewards` | kind, `scheduled_event_slug`, trigger, delivery, expiry |
| `reward_payouts` | the ordered lines a reward hands over |
| `reward_grants` | what is owed or was paid, with `UNIQUE (reward_id, user_id, reference)` |
| `reward_grant_payouts` | those lines **frozen** at grant time, each settled independently |
| `reward_baselines` | a `per_unit` reward's starting point, so it does not pay for history |
| `reward_issuances` | deliberate retroactive grants, named cohorts only |

`events` and `event_windows` were here until migration `005`. They belong to the
[events plugin](../events/) now — a season or a reset period is a site fact
several systems reference, and this one was only the first to need it. Rewards
holds a **slug** and asks through `pluginapi.ScheduledEvents`; it stores no id
belonging to another schema.

No foreign keys to the host's `users`, or to any other plugin's tables: they live
in schemas this plugin does not own, and a plugin that hard-links out cannot be
uninstalled.

### The three kinds, and what `reference` means

`reference` is TEXT and names WHICH entitlement a grant is for. It is what
`UNIQUE (reward_id, user_id, reference)` is built on.

| kind | reference | means |
|---|---|---|
| `one_off` | `''` | at most once, ever — one entitlement needs no name |
| `recurring` | the occurrence key, `summer-2026@2026-08-01T00:00:00Z` | at most once per occurrence |
| `per_unit` | `mark:0000000000000000063` | the delta since last paid |

It used to be a BIGINT meaning three unrelated things. The **high-water mark
moved to its own `high_water` column** in migration `005`, because a mark
compared as text makes `"9"` greater than `"10"` — one column doing two jobs is
why the type could not simply change. `reference` answers *which entitlement*;
`high_water` answers *how far we have paid*.

The occurrence key comes from the events plugin, slug-qualified rather than a row
id or a bare timestamp: an id couples rewards to another schema and changes if
that table is rebuilt, and a bare number tells nobody reading a grant row which
event paid.

`per_unit` pays **rate x delta**, and only countable lines scale: "2 points and
the Uploader medal" for 500 new grabs owes 1000 points and ONE medal. A mark
that moves backwards (a purge, a recount) pays nothing rather than debiting.

For `recurring` the reference is derived by the events plugin from data the
operator configured — the slug and the window start — so no two subsystems can
compute the period differently, and it survives that plugin's table being
rebuilt.

### Seasons and resets are one concept

Still true, and now documented where it lives — see the events plugin's README
for the duration rule. From here the only thing that matters is that both produce
windows, so nothing in rewards knows which it is looking at.

If no events plugin is wired, every event-gated reward is permanently
**unearnable** rather than permanently earnable. That is the safe direction:
paying a seasonal reward because nobody could say whether the season was running
is the failure worth designing against.

## Dependencies

Core services consumed: **`Points`** — required. It is the only payout kind
this plugin implements itself, because it is the one loon already has a ledger
facade for. Provision fails without it rather than letting every points reward
be refused later at grant time.

Store: self-contained `PGStore` over `c.Storage.SchemaDB("rewards")`.

External: `github.com/robfig/cron/v3` for window generation. Configured as a
strictly **five-field** parser — a seconds-optional parser silently shifts every
field, so `0 0 1 7 *` would mean something else entirely and windows would
appear on the wrong days with no error anywhere.

## Hooks & Callbacks

- Host hooks SET: **(none)**
- Extensions PUBLISHED: **`rewards.trigger`** — the `*Engine`. The host's login
  handler calls `Fire` (auto-delivery, detached) and `Available` (claim
  delivery, synchronous, because the response has to render the button).
  **`rewards.admin`** — the `AdminStore`, consumed by the host's ops API.
  **`rewards.validator`** — the cross-table check, same consumer.
- Extensions CONSUMED: **`rewards.units.<reward slug>`** — a `UnitSource`
  supplying current counts for a `per_unit` reward. The plugin deliberately
  knows nothing about what is being counted; the host counts, and the engine
  owns "how far have I already paid". Adding a per_unit reward is a reward row
  plus one registration.
  **`rewards.payout.<kind>`** for `role`, `medal`,
  `achievement`, `username_fx` — each a `PayoutHandler`. Optional: a host
  registering none can still run points-only rewards. Registered under the
  right key with the wrong *shape* fails Provision, because that is a wiring
  bug wearing the same face as "not registered".

A `PayoutHandler` is `func(ctx, g Grant, p Payout) error`. The Grant is passed
for **attribution**, not authority: `g.UserID` says who, `g.RewardSlug` says on
whose account — a handler writing to a ledger should record both. What to hand
over comes only from `p`, the frozen line, because the reward may have been
retuned since the grant was offered.

A handler **must be idempotent** for the same `(g.UserID, p.ID)`. A grant that
dies between two lines is resumed, not rolled back — half its payout has
already left the building.

## Lifecycle

- **Provision** — builds the store, registers the points handler, adopts any
  registered payout handlers, publishes the engine.
- **Start** — worker only: a `schedule.ServiceLoop` every 30 min keeping windows
  45 days ahead and expiring lapsed grants 500 at a time. Web processes skip it;
  several containers generating the same windows is pointless contention on a
  table every login reads.
- **Stop** — no-op.

## Files

- `plugin.go` — lifecycle, handler wiring, the window-generator job.
- `models.go` — typed kinds, and the Reward/Offer split.
- `store.go` / `store_pg.go` / `store_mem.go` — the data seam, Postgres, tests.
- `engine.go` — `Defs`, `Available`, `Claim`, `GrantPerUnit`, `Settle`, `Fire`.
- `windows.go` — cron → concrete windows.
- `units.go` — the per-unit counter seam and its batch grant path.
- `validate.go` — the cross-table check.
- `views.go` / `store_admin.go` / `templates/` — the two admin pages.

## Testing

- **Unit** (`engine_test.go`, `windows_test.go`) — the idempotency taxonomy:
  once per window, again next window, the half-open boundary, claim-vs-auto
  delivery, frozen payouts surviving a retune, resume-not-replay after a failed
  line, per_unit paying only the delta, and a broken reward not blocking a
  working one. Window generation covers contiguity, seasonal gaps, weekends,
  first-of-month, timezones, a DST spring-forward, and a runaway refusal.
- **Integration** (`store_pg_test.go`, tag `integration`, `REWARDS_TEST_DSN`) —
  the two things a mock cannot prove: 25 concurrent transactions hitting the
  UNIQUE constraint (exactly one wins), and the half-open window comparison in
  SQL rather than in Go. Plus interval round-tripping, which if wrong silently
  gives every reward an expiry of zero.

`MemStore` enforces the UNIQUE constraint and mirrors the half-open comparison
itself. That is deliberate: a mock that let a double-book through would hide the
one thing the whole model rests on.

## The catalogue — why the pickers are dropdowns

`rewards.trigger` and `achievements.metric` name what the site does. They used
to be free text, and the picker was assembled from whatever was **already
configured** plus a hardcoded `"login"` — a list that is empty exactly when it
is most needed, setting up the first one. A typo then produced a reward that
looked perfectly healthy and could never fire, because nothing anywhere knew
what the valid names were.

So the catalogue is a **table** — `reward_sources` — and every picker reads it
live. A host registers a SEED under `rewards.sources`:

```go
c.Register(rewards.SourceCatalogExtension, rewards.StockSources())
```

which is written **once, into an empty table, and never again**. Code proposes,
configuration disposes: after the first boot the operator owns the list, adds
what this site has and disables what it does not, and a host changing its seed
cannot silently rewrite their edits or resurrect rows they deleted. That is the
difference between a default and a declaration — the previous version of this
was a Go slice, so adding a countable thing meant a deploy.

Each `SourceDef` carries a stable `Key`, the dropdown `Label`, a `Group` to
bucket it, and two independent flags:

- **`Fires`** — a surface announces it the moment it happens, so it can be a
  reward's trigger.
- **`Counts`** — a running total exists, so it can score an achievement
  threshold. A counting source must also register a `MetricSource` under
  `rewards.metrics.<key>`; the configuration check reports one that does not.

They are separate because most things are both and some are only one: a post
announces itself AND is counted for a lifetime, "days registered" only counts,
"password changed" only fires. One flag could not say that.

`Unit`/`Units` exist so achievements name themselves — `SuggestName` gives
**"First post"** at a threshold of one and **"100 posts"** above. A suggestion,
not a rule: the field stays editable, because "Centurion" beats "100 posts" and
no generator will think of it. But an empty name field is how a catalogue fills
up with achievements called `achievement-3`.

`Schedules()` is the same idea for cadence: an operator picks **Hourly** rather
than typing a cron expression, whose mistakes are silent — a stray field in
`0 0 * * *` does not fail, it runs at the wrong time.

A host that declares nothing keeps the old derived picker, so an install
predating the catalogue still works. Keys already in use are always merged into
the options, so renaming one in the catalogue cannot make an existing reward's
own trigger vanish from the dropdown that edits it.

## Achievements

An achievement is a **criterion attached to a reward**. That split is the whole
design, and the reason there is not a second engine here.

The engine already owns definitions, repeatability, triggers, jobs, callbacks
and pay-once-as-a-constraint. The one thing it cannot express is "reach N of
X": `per_unit` counts a number and pays per delta, with no threshold that
latches. So `achievements` owns the criterion — a `metric` and a `threshold` —
and a `reward_id` says what it pays. Payment then goes through the ordinary
grant path, which means an achievement cannot pay twice even under a race
without any new locking or idempotency scheme.

**There is deliberately no `repeatable` column.** `rewards.kind` already
declares repeatability and the engine enforces it through the `reference` it
computes per grant; a second copy here could disagree, and an achievement
marked repeatable whose reward is `one_off` would complete over and over while
paying exactly once, with nothing reporting it. Validation restricts
achievements to `one_off` rewards today. Allowing `recurring` (a seasonal
achievement) is coherent and needs a validation change, not a migration.
`per_unit` is refused outright: a criterion latches, and `per_unit` keeps
paying on every later unit.

**Completion is one transaction.** Stamping `user_achievements.completed_at`
and creating the `reward_grants` row happen together or not at all — a
completion with no grant paid nothing, and a grant with no completion pays
again on the next evaluation because nothing recorded that it fired. The schema
carries the same rule as a `CHECK ((completed_at IS NULL) = (grant_id IS
NULL))`, so a future writer that sets one without the other is rejected rather
than merely wrong.

**Creating an achievement BACKFILLS it — silently.** The first scoring pass
after an achievement is created awards it to everyone already past the
threshold and tells none of them: they earned it before the thing existed, and
announcing to three thousand people at once about something they did months ago
is a mailshot rather than a notification. The pass then stamps
`achievements.backfilled_at`, and every completion after that is announced
normally. The badge is awarded either way — only the telling is suppressed.

The mechanism is `reward_grants.silent`, not an achievement concept:
`reward_issuances` (deliberate retroactive grants to a cohort) wants exactly
the same thing, and an operator back-paying six months of a reward should not
fire six months of notifications either.

**The award itself is the point, so it is never suppressed.** One scored on a counter is awarded
retroactively to everyone who already meets the threshold, on the next job
tick, and each of them is notified. That is the point of an absolute counter —
"100 posts" should recognise people who already wrote a hundred — but it means
creating a row is an action with an immediate, member-visible blast radius.
Count the qualifying set before creating it; there is no way to suppress the
backfill's notifications yet. On 2026-08-07 a threshold-1 achievement awarded
23 members and sent 23 notifications within seconds of the INSERT. See
`docs/ACHIEVEMENTS.md` in the ameNZB repo.

**Metrics** are host-registered under `rewards.metrics.<name>`, the same shape
as `rewards.units.<slug>`, and a metric with no source is **inert** rather than
a boot error — the configuration check reports it instead, so a half-deployed
site says so on the admin page rather than refusing to start.

Built so far: schema, the per-member read, progress recording, the completion
transaction, and validation. **Not yet wired: the trigger path and the job that
call `CompleteAchievement`** — so nothing completes an achievement in
production yet. Design: `docs/ACHIEVEMENTS.md` in the ameNZB repo.

## The configuration check

Every table enforces its own shape, and every one of them can be satisfied by a
configuration that pays nobody. An event with no windows is a valid event. A
reward pointing at it is a valid reward. Together they are a reward that can
never be earned, and nothing else says so — the failure is silence, and the
first report is a member asking where their bonus went.

`Validate` cross-checks relationships, and grades what will break SOON as well
as what is broken now:

| severity | means | examples |
|---|---|---|
| error | cannot pay right now | event with no windows; reward gated by a disabled event; enabled reward with no payout lines, or a payout kind nothing can execute; a reset with no open window |
| warn | works today, will stop | windows run out inside a week; a contiguous reset with gaps; pending grants past expiry (the sweep is not running) |
| info | legal, probably not meant | expiry on a `delivery=auto` reward, which settles immediately so it never applies; a `per_unit` reward gated by an event |

A healthy configuration produces **nothing** — a validator that always finds
something is one an operator learns to ignore, and there is a test pinning that.
Findings carry a fix, not just a complaint. Published as `rewards.validator`
and served at `GET /ops/rewards/validate`, where `healthy` is about the
configuration and `ok` is about the call.

## Claiming

`delivery='claim'` writes a pending grant, notifies the member (`reward_claim`
via `core.Notifications`), and shows it on the site widget until collected.
POST `/plugin/rewards/claim` settles one.

The member id comes from the **session**, never the form. `ClaimGrant` refuses
a grant belonging to anybody else, and refuses it *identically* to one that
does not exist — grant ids are sequential integers, so without both halves the
endpoint is an IDOR that pays an attacker from someone else's pending grants,
and with only the first it is an oracle for which ids are real.

Notification is a nudge, not the delivery mechanism: the grant is durable and
the widget shows it whether or not `core.Notifications` is wired.

## Editing, and what it deliberately refuses

Rewards are created **disabled**, through both the page and the API. One that
starts paying the moment it is typed leaves no chance to check the payout line
first, and un-paying is not a thing.

Deleting a window is refused when any grant is keyed on it. `reference` holds
the window id for a recurring reward but cannot be a foreign key — the same
column means a high-water mark for a `per_unit` one — so nothing at the schema
level stops it, and removing the window a grant was issued against would let
that member be paid again for a period they were already paid for.

There is **no way to pay a named member**, from the page or the API. Rewards
are rules; a per-member payment typed into a form is the ad-hoc thing this
model replaces.

## Not built yet



The design (`docs/REWARDS.md` in the private site repo) also specifies
achievements (a counter crossing a threshold) and retroactive issuances. The
schema for issuances is here; the logic is not. Achievements have neither yet.

Editing is create-and-toggle only — no in-place edit of an existing event or
reward, and no adding a payout line after creation. Deleting and recreating
works, and for a reward with grants against it that is arguably the honest
operation anyway.
