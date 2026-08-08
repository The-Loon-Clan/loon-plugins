# events plugin

Scheduled events: named spans of time other plugins hang behaviour on. A season,
a launch week, a daily reset, a one-off announcement date.

A definition is a cron expression plus an optional duration. A job materialises
concrete **windows** ahead of time, so "is event X open right now" is one indexed
lookup rather than a cron evaluation per query. Nothing user-facing lives here —
this plugin answers questions; other plugins decide what to do with the answer.

Lifted out of the `rewards` plugin, whose own schema comment admitted the concept
was *"not reward-specific in meaning even though it lives here for now"*. Rewards
gates recurring payouts on a window, news wants to publish a post when an event
opens, and a leaderboard would want to reset on one; none of them should reach
into rewards to ask.

## Surface

One admin page, registered as a `SlotAdminPage` view under slug **`events`**
("Scheduled events", nav group Operations): a **window-health banner**, the
definitions table with live open/closed state and next-opens, a windows panel per
event, and a create/update form. Actions: `event-save`, `event-toggle`,
`event-delete`.

Ported from the rewards plugin, whose own comment said why it should never have
lived there — *"Events are not reward-specific … burying them inside a Rewards
page would misrepresent what they are. It also keeps each page to one job: Events
is WHEN, Rewards is WHAT."* This is the WHEN, where the WHEN is owned.

Process kinds: `web`, `worker`. Both register the capability, because a consumer
on the worker asking "is the season open" is the same question as one on web.
Only `worker` runs the generator; the page is skipped on the worker, which has no
router.

## Data

Schema `events`, migration `001_init.sql`:

- **`events`** — the definitions: `slug`, `name`, `description`, `cron`,
  `duration_seconds`, `starts_at`, `timezone`, `enabled`.
- **`event_windows`** — materialised occurrences: `event_id`, `starts_at`,
  `ends_at`, with `UNIQUE (event_id, starts_at)` and an index on
  `(event_id, starts_at DESC, ends_at)`.

No foreign keys out of this schema. A plugin that hard-links to another's tables
cannot be uninstalled, and consumers reference events by **slug** anyway — a slug
survives the table being rebuilt, moved, or restored from a dump taken elsewhere,
and it is what an operator picks from a dropdown.

### The duration rule, which is one rule and reads like three

| definition | behaviour |
|---|---|
| `duration` set | the window closes `start + duration`. Gaps between firings. |
| `duration` NULL, `cron` set | the window runs until the **next firing** — contiguous, no gaps |
| `duration` NULL, no `cron` | it **never closes** |

The last two are the same rule: with no cron there is no next firing, so "runs
until the next firing" means forever.

`duration_seconds` is a plain **INTEGER**, not an `INTERVAL` (migration `002`).
INTERVAL came across with the lift and never paid for itself: lib/pq cannot scan
it into a `time.Duration`, so every read went through `EXTRACT(EPOCH FROM …)` and
every write built a `"%d seconds"` string for Postgres to re-parse — two grammars
to move one number. It also removes a real trap, because `'1 month'` is a legal
interval and is *not* a fixed length of time, so `start.Add(duration)` in Go
could disagree with what Postgres would have computed. NULL still means "no
duration"; zero would be a window of no length, which is a different and useless
thing, so the column stays nullable and a positive-value CHECK rejects the rest.

A never-closing window is stored with `ends_at` at the far future
(`PerpetualEnd`), **not** as NULL. Every "is this open" query is then one range
comparison with no special case, and `CHECK (ends_at > starts_at)` keeps its
meaning. A nullable end would push the case into every consumer and one of them
would forget.

### Two things rewards could not express, and now can

- **A one-off with a duration.** Rewards had
  `CHECK (duration IS NULL OR cron IS NOT NULL)`, so a duration required a
  recurrence — "launch week: starts 1 Sep, runs 7 days" was unwritable.
- **A one-off that never closes.** Its generator returned nothing for a
  cron-less event ("windows authored by hand"), and `ends_at > starts_at` forced
  a finite end, so "always after this date" had to be faked with a year-9999 row.

## Window health

Every row in this schema can be valid while the site opens nothing. An event with
no windows is a valid event; a reward or a news post gated on it is valid too.
Together they are a thing that can never happen, and the failure is **silence** —
the first report is a member asking why they never got their bonus.

So `Validate` reports what is broken and what will break soon, and the page shows
it above the table. `Store.Coverage` computes it for every event in ONE query
(`lead()` over a `LEFT JOIN`, so a zero-window event still gets a row — an inner
join would drop exactly the case worth reporting).

| finding | severity | when |
|---|---|---|
| cron event with no windows | error | the generator has never run for it |
| one-off whose start has passed, no window | error | the generator has not caught up |
| every window in the past | error | the runway is exhausted; nothing can open |
| windows run out in under a week | warn | the generator has not completed a pass in over a month |
| gaps in a contiguous event | warn | a period during which the event did not exist |
| one-off beyond the generation horizon | info | not broken — it has not happened yet |

These checks lived in the rewards plugin until the tables moved. Rewards was
right to stop running them (the generator's owner is the authority), but they were
then checked **nowhere** for a day, which is what this file put back.

Three cases needed adapting rather than porting, and each is a false alarm the
old version would now raise. A one-off booked beyond the 45-day horizon
legitimately has no windows. A finished one-off has not "run out", it is over. And
a perpetual window's end is the year-9999 sentinel, so a runway measured against
it reports eight thousand years — true, useless, and the sort of number that makes
an operator distrust the page.

## Dependencies

- Core services: `Storage` (schema-scoped), `Scheduler` (the generator job).
- Store: self-contained `PGStore` over `Core.Storage.SchemaDB("events")`, with a
  `MemStore` for tests that enforces the same invariants the schema does.
- Config keys: none. The generator interval is a standard job default, overridable
  from `/admin/jobs` like any other.

## Hooks & Callbacks

- Host hooks SET: (none)
- Extensions PUBLISHED: **`events.scheduled`** (`pluginapi.ScheduledEventsName`)
  — `pluginapi.ScheduledEvents`: list definitions, resolve one by slug, ask which
  are open now, ask for the open window of named slugs, ask when one next opens.
- Extensions CONSUMED: (none)

`OpenNow` returns a **set** rather than offering an `IsOpen(slug)`, because the
callers are feeds and listings rendering many rows and asking per row is an N+1
across a plugin boundary. `OpenWindows` exists alongside it for callers that must
record *which occurrence* they acted on — a recurring payout keyed on "the event
is open" would pay once ever, where one keyed on the window pays once per
occurrence.

### Identifying one occurrence

`EventWindow.Key()` is `"<slug>@<start in RFC3339 UTC>"` — e.g.
`summer-2026@2026-08-01T00:00:00Z`. This is **the** cross-system identifier for
"this event, this time round": rewards needs one so a recurring payout pays once
per occurrence rather than once ever, news needs one to say which run a post
belongs to, a leaderboard needs one to scope a season.

Derived from the slug, never from a row id. An id is private to this schema, so a
consumer holding one is coupled to this table — and the id changes if the table is
rebuilt or restored from another host's dump, silently detaching every consumer.
The slug and the start do not change, because they are what the operator
configured. It is readable for the same reason it exists: the value lands in other
plugins' rows, so somebody will read it in psql while working out why a member was
or was not paid.

`NextOpen` answers from the **definition**, not the window table: the table only
reaches to the generation horizon, so reading "next" from it would report "never"
for the yearly event whose next firing is five months out — precisely the event an
operator most wants a date for.

## Lifecycle

- **Provision**: builds the store, registers the capability (before anything else,
  so a consumer's own Provision cannot race it), and on `worker` registers the
  Event Windows job.
- **Start**: on `worker`, the generator loop — 2-minute boot delay, 30-minute
  default interval.
- **Stop**: no-op.

The generator resumes from where the last window **ends** rather than from now.
Resuming from now would grow a hole every time it ran late, and a hole in a daily
reset is a day the thing does not exist. Regeneration is idempotent by
`(event_id, starts_at)`, so re-running over a covered range is a no-op — which is
what makes the resume safe. One malformed event never stops the others: a bad
cron costs that event its windows, not the site its daily reset.

## Files

- `plugin.go` — registration, capability publication, the generator job.
- `views.go` + `templates/events_admin.html` — the admin page.
- `validate.go` — window-health findings; `validateEvents` is pure so every case
  is testable without a database.
- `service.go` — the published capability, thin over the store, with an
  injectable clock so window-boundary tests are not flaky.
- `windows.go` — `GenerateWindows` and `NextStart`; the cron parsing and the
  duration rule.
- `store.go` / `store_pg.go` / `store_mem.go` — the data layer triple.
- `migrations/001_init.sql` — the two tables.

## Testing

Unit-tested: the whole duration rule (one-off bounded, one-off perpetual, past
the horizon, missing start date), recurring contiguity end-to-start, the
`from`-lands-on-a-firing off-by-one, malformed cron, disabled events, `NextStart`
past the horizon, and the MemStore honouring the schema's uniqueness and cascade.
The perpetual-window assertion was mutation-checked.

Render-tested against every event shape, including the streamed-abort signature
(a field the view model lacks truncates `html/template` mid-page, so the test
asserts on content at the END of the template). Mutation-checked by adding a
bogus field and watching it fail.

**Integration-tested against a real Postgres** (`store_pg_test.go`, build tag
`integration`, `EVENTS_TEST_DSN`). This plugin shipped without any, which meant
every line of its SQL reached production having never run. Covered: the
`duration_seconds` round trip including NULL-is-not-zero, the `unnest` batch
insert and its idempotency, the half-open boundary **in Postgres** rather than in
Go, the `Coverage` query (windows inserted out of chronological order, because
`lead()` is ordered by the window clause and not by insertion), the `ON DELETE
CASCADE`, and a perpetual window surviving the round trip.

The harness applies EVERY migration and then returns the session to `public`, so
a store that had lost its `SchemaDB` scoping fails here instead of on its first
real request.

All nine validator cases are unit-tested, and the two that guard against false
alarms were mutation-checked. One of those checks found dead code: a dedicated
perpetual guard turned out to be unreachable, since only a one-off can be
perpetual and the one-off exemption already covered it.

## Adoption status

**Rewards is wired onto this plugin** as of 2026-08-07. `rewards.rewards` holds a
`scheduled_event_slug`, its engine resolves open windows through
`ScheduledEvents.OpenWindows`, and its own `events`/`event_windows` tables are
dropped (rewards migration `005`). There is one definition of a scheduled event
on the site again.

A recurring reward keys its grant on `EventWindow.Key()`, so it pays once per
occurrence. If no events plugin is wired, rewards reads the capability as absent
and every event-gated reward becomes permanently unearnable — the safe direction,
since the alternative is paying a seasonal reward because nobody could say whether
the season was running.
