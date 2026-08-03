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

An admin page at `/admin/p/rewards` (SlotAdminPage, Operations group): events
with their window counts and currently-open window, rewards with their payout
lines, the newest 50 grants, and forms to create events, author windows by
hand, create rewards, and enable or disable either. Plus a *Test on me* button
that runs a real grant against the operator's own balance.

The engine is published on the extension registry as `rewards.trigger`; the
host's login path looks it up and calls it. The configuration store is
published as `rewards.admin`, which the host's ops API serves as
`/ops/rewards` for machine callers.

`Metadata.Processes: ["web", "worker"]` — web renders the page, resolves and
settles claims; worker materialises event windows and expires lapsed grants.

## Data

Owns eight tables in the `rewards` schema (`migrations/001_init.sql`):

| table | holds |
|---|---|
| `events` | slug, name, and the window *generator*: cron + nullable duration + timezone |
| `event_windows` | concrete `[starts_at, ends_at)` rows — the truth every query reads |
| `rewards` | kind, event, trigger, delivery, expiry |
| `reward_payouts` | the ordered lines a reward hands over |
| `reward_grants` | what is owed or was paid, with `UNIQUE (reward_id, user_id, reference)` |
| `reward_grant_payouts` | those lines **frozen** at grant time, each settled independently |
| `reward_baselines` | a `per_unit` reward's starting point, so it does not pay for history |
| `reward_issuances` | deliberate retroactive grants, named cohorts only |

No foreign keys to the host's `users`: it lives in a schema this plugin does
not own, and a plugin that hard-links to host tables cannot be uninstalled.

### The three kinds, and what `reference` means

| kind | reference | means |
|---|---|---|
| `one_off` | `0` | at most once, ever |
| `recurring` | `event_windows.id` | at most once per window |
| `per_unit` | high-water mark | the delta since last paid |

For `recurring` the reference is a real row, not a computed period ordinal, so
no two subsystems can compute the period differently.

### Seasons and resets are one concept

An event's `duration` is the only difference:

- **set** — the window *closes* after it. Summer runs 64 days, then there is no
  window at all until next July. Gaps.
- **NULL** — the window runs until the next firing. Midnight to midnight,
  contiguous, forever. That is a reset.

Both produce `event_windows` rows, so nothing downstream knows which it is
looking at. A `duration` with no `cron` is refused: "closes 64 days after" has
no answer without a "starting when".

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
- Extensions CONSUMED: **`rewards.payout.<kind>`** for `role`, `medal`,
  `achievement`, `username_fx` — each a `PayoutHandler`. Optional: a host
  registering none can still run points-only rewards. Registered under the
  right key with the wrong *shape* fails Provision, because that is a wiring
  bug wearing the same face as "not registered".

A handler **must be idempotent** for the same `(userID, grantPayoutID)`. A grant
that dies between two lines is resumed, not rolled back — half its payout has
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
