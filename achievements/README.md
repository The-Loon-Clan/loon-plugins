# achievements plugin

Earnable badges, described as data: an achievement is a **criterion** — reach
N of a counter, or the moment a declared event fires — plus a look, plus an
**optional reward**. The reward is a `one_off` in the [rewards](../rewards/)
plugin, named by slug and paid through `pluginapi.RewardBySlugGranter`; an
achievement with no `reward_slug` is a **pure badge**, which is a legitimate
achievement and the change that let this plugin exist apart from rewards at
all.

The two used to be one plugin sharing one schema, so a completion and its
reward grant could land in a single transaction. Once the reward became
optional that transaction stopped being the spine — a pure badge has nothing
to be atomic with, and a paid one crosses a plugin boundary where no shared
transaction can exist. What replaced it is **idempotence instead of a
transaction**: the completion commits first as this plugin's own atomic fact
(a conditional update on `completed_at IS NULL` is the race arbiter), payment
happens after through an idempotent granter, and the crash window between the
two — a completed row whose `paid_at` is NULL — is repaired by the scoring
job calling the same granter again. At-least-once plus idempotent equals
exactly-once where it matters.

Deliberately absent: any read of the rewards tables. The old admin page could
warn eagerly that an achievement's reward was disabled or payout-less; that
warning required exactly the cross-schema read this split removed, so
payability is now reported **lazily** — the granter refuses, the scoring job
logs it, and the row's `paid_at` stays NULL (the member sees "awaiting
payout").

## Surface

| Surface | Access | Notes |
|---|---|---|
| `GET /admin/p/achievements` (SlotAdminPage view, slug `achievements`) | host admin gate, nav group "Operations" | Definition table + create form. Moved from rewards with slug and URL intact. Actions: `achievement-create`, `achievement-toggle` (POST, host CSRF chrome). |
| Profile card (SlotUserWidget, slug `achievements`, **Public**) | anyone | Earned badges are public; the in-progress list renders only on your own profile. Renders nothing when there is nothing to say, and nothing at all to a non-subject viewer when the member has opted out (below). Its title links to the page below. |
| `GET /p/achievements` (SlotSitePage view, slug `achievements`, **Public**) | anyone; the personal half needs a session | The member's own card, the visibility opt-out, and the full catalogue. Action: `visibility` (POST `/p/achievements/visibility`, host CSRF chrome) — refuses anonymously for itself, since a Public view's action is reachable without a session. |
| Content block `{{achievements}}` (`content.block.achievements`) | wherever the wiki embeds it | The public catalogue, generated per render; hidden and disabled achievements are withheld. |
| Job "Achievement Scoring" | `/admin/jobs` | Hourly: scores metric achievements from host counters, backfills new ones, repairs unpaid completions. |

No routes of its own outside the view system; no machine endpoints.

**Processes:** `web`, `worker` — the same pair rewards declares, for the same
reason: web serves the pages and completes achievements from events emitted on
web requests; worker runs the scoring job and completes from events emitted by
background work. Event subscription must live wherever events fire, and they
fire on both.

## Data

Owns three tables in its own `achievements` schema:

- `achievements` — slug, name, description, `reward_slug` (`''` = pure
  badge), the criterion (`metric`+`threshold` OR `trigger`, enforced by a
  CHECK), icon/image_path, ordinal, hidden, enabled, `backfilled_at`.
- `user_achievements` — per (achievement, member): `progress`, `times`,
  `completed_at`, `paid_at`, `updated_at`. **No** grant_id, **no** FK to any
  other schema; `user_id` is a plain BIGINT and cleanup on member deletion is
  a host-driven call.
- `profile_visibility` (`migrations/003_profile_visibility.sql`) — one row per
  member who has made a choice about publishing their badges. **Absence is
  shown**: earned badges are public by design, so the default is the missing
  row, the column default, and the zero value all at once, and only an
  explicit opt-out is recorded. Enforced in the profile card's Render, before
  the achievements are read, and only when the viewer is not the subject — a
  member always sees their own. It lives here rather than in a host
  preferences table because it is a fact about this plugin's card, and a host
  that mounted the plugin would otherwise have to grow a column, a form field
  and a POST handler before the opt-out worked at all.

**Succession:** migration 001 lifts rows from `rewards.achievements` /
`rewards.user_achievements` when they exist (`to_regclass`-guarded,
`ON CONFLICT DO NOTHING`), mapping `reward_id` → `reward_slug` and stamping
`paid_at = completed_at` on completed rows (the old CHECK guaranteed every
completion carried a grant). The old tables are left in place; the operator
drops them once satisfied. Verified against the reference deployment before
writing: 5 achievements, 12 member rows, 0 completed rows without a grant.

What a member's row holds about them: progress counters, completion and
payment timestamps per achievement. That is everything; deleting the rows for
a `user_id` removes their entire trace.

## Dependencies

**Core services:** `Storage.SchemaDB` (own schema), `Scheduler` (the job),
the event bus (`DeclareEvent`/`On`/`Emit`), the view registry. `Auth` is used
only to decide "is this my own profile" — which is also the whole of the
visibility rule, and why the page's action re-asks it before writing.

**Soft, cross-plugin:** `pluginapi.RewardBySlugGranter`
(`rewards.granter.byslug`), looked up in **Start** — Boot runs every
Provision before any Start, so the registration is visible whatever the boot
order, without a hard `Requires` a badge-only host could not satisfy. Absent:
pure badges work fully; achievements naming a `reward_slug` complete and sit
pending, and the scoring job says so out loud. Wrong shape under the key is a
boot error (a wiring bug wearing absence's face).

**Host registrations (all BEFORE Boot — the registry is snapshotted at
Provision):**

| Key | Shape | Why |
|---|---|---|
| `achievements.metrics.<metric>` | `achievements.MetricSource` | One per counter the host offers for scoring; read once per job tick for the whole membership. Wrong type = boot error; absent = the metric is inert. |
| `achievements.files` | `blob.Store` | Optional. Badge image uploads; without it the upload control is hidden. |
| `achievements.icons` | `[]string` | Optional. The sprite vocabulary for the icon picker; without it the field is free text. |

Hosts upgrading from the rewards era re-register their metric sources under
`achievements.metrics.*` (previously `rewards.metrics.*`) and rename
`rewards.files` / `rewards.icons` likewise.

**Config:** none.

## Hooks & Callbacks

- Events DECLARED: **`achievements.completed`** — member kind, countable
  (completing achievements is itself a legitimate per-member total), payload
  `achievements.Completed{Slug, Paid}`. Emitted after the completion commit,
  never on the already-completed path, and not for silently-backfilled
  history.
- Events CONSUMED: **every declared event** — countable ones move metric
  progress (the event name IS the metric name, one vocabulary); any declared
  event can be an achievement's `trigger` and completes it the moment it
  fires, once.
- Extensions PUBLISHED: **`achievements.list`** (`ListFunc` — one member's
  standing on every achievement, for a host page),
  **`achievements.granter`** (`pluginapi.AchievementGranter` — complete a
  badge directly, stamping `paid_at` and deliberately NOT running the
  achievement's own reward: a reward paying an achievement that pays a reward
  is a loop nobody configured on purpose), and the documentation placeholder
  `achievements.metrics` for the callback convention.
- Extensions CONSUMED: `rewards.granter.byslug`, `achievements.metrics.*`,
  `achievements.files`, `achievements.icons` (see above).

## Lifecycle

- **Provision** — store over `SchemaDB("achievements")`; snapshot metric
  sources; declare the event; publish `achievements.list` and the granter;
  subscribe to all declared events; register the job (worker/all); parse
  templates and register the three views (web/all).
- **Start** — look up the rewards granter (soft); worker/all: run the scoring
  loop (2 min boot delay, hourly).
- **Stop** — no-op.

Migrations run in Boot step 1, before any Provision, so the schema (and the
data lift) exists even for a process that never provisions the plugin's UI.

## The completion path, precisely

1. An event arrives (or the job reads a counter). Progress moves — events ADD
   ("one more happened"), metric reads SET the absolute total (the
   reconciling source; a dropped event self-heals on the next tick).
2. Threshold crossed, or trigger fired: `CompleteAchievement` — one
   conditional upsert, `completed_at IS NULL` arbitrates the race, losers get
   `ErrAlreadyCompleted` and stop.
3. Payment: pure badge → `paid_at` stamped with the completion (nothing
   owed). Named reward → `GrantOneOff(userID, reward_slug, achievementSlug)`
   after the commit — the reference is the achievement's slug, so two
   achievements may pay the same reward — then `paid_at`.
4. A crash or failure between 2 and 3 leaves `paid_at` NULL; the hourly job
   re-calls the granter (idempotent — `granted=false` means already held,
   which is also paid) and stamps.
5. Announcement: `achievements.completed`, unless the completion is silent
   backfill. Trigger completions are never silent — no scoring pass can
   retroactively fire an event, so every one is live.

**Silent backfill:** a metric achievement's first scoring pass awards
everyone already past the threshold without announcing it (they earned it
before it existed); `backfilled_at` stamps once when that pass ends, and
everything after is announced normally.

## Files

- `plugin.go` — lifecycle, metric-source collection, registrations.
- `achievements.go` — the model, the extension types, the per-member read row.
- `store.go` / `store_pg.go` / `store_mem.go` — the Store triple, definition CRUD included.
- `subscribe.go` — event handling: metric scoring, triggers, the completion+payment path.
- `jobs.go` — the scoring job and the payment repair sweep.
- `events.go` — the declared event and its payload.
- `granter.go` — the published `AchievementGranter`.
- `docext.go` — the documented callback convention (and the scanned-namespace guard's data).
- `views.go` / `views_admin.go` / `views_profile.go` / `block.go` + `templates/` — the three surfaces.
- `migrations/001_init.sql` — schema + the succession lift.

## Testing

**Unit, no database** (`go test ./achievements/`): the event handlers against
the MemStore (guards, one-vocabulary lookup, completes-only-on-crossing,
trigger latches once, disabled skipped, broken sibling isolation); the
payment half (pure badge stamps at completion; a failing granter leaves
`paid_at` NULL and the repair sweep pays exactly once when it recovers,
against a fake granter held to the real one's idempotence); silent backfill
as event-suppression, with a real `core.Core` capturing emissions; the
MemStore's own invariants (latch, upsert-complete, MarkPaid idempotence,
SET-vs-ADD including downward reconciliation, the read's visibility rules);
`parseNewAchievement`'s refusals; every template executed against real view
models with assertions on each page's last element; the admin page's
POST-forms-target-own-actions count; the granter contract; the
scanned-namespace and event-declaration guards.

The MemStore is stricter than its rewards ancestor in two places it had
drifted from the SQL: `RecordProgress` now SETS downward (the SQL always
replaced; the old double refused and claimed a GREATEST the SQL never had),
and the per-member read applies the query's hidden/disabled visibility rules.

**Known gaps:**

- No integration harness yet: the conditional-update race arbiter and the
  lift SQL are exercised by hand against a disposable Postgres (the lift was
  verified applied-twice against a seeded fake `rewards` schema and a fresh
  install), not by a tagged test the way rewards' UNIQUE race is. The old
  race test's subject — the shared transaction — no longer exists; a new one
  should race `CompleteAchievement` itself.
- No eager payability warning on the admin page (see the top of this file).
- Title/description are text pending the localization-slug catalogue;
  "select an existing image" pending `blob.Store` growing listing — both
  carried over from the rewards-era form, still true here.
