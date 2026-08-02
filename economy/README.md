# economy plugin

Automatic points earning. Two scheduled rules, no web surface at all — they run
on the worker, credit the ledger, and are configured from the host's settings
page.

- **Points Tenure Bonus** — awards points on a member's registration
  anniversary, one payment per completed year of membership.
- **Points Grab Bonus** — pays uploaders for grabs on their releases, crediting
  only what is new since the last run.

## Surface

No routes. `Metadata.Processes: ["worker"]`, so it never provisions on web or
api. Both rules appear in `/admin/jobs` as *Points Tenure Bonus* and *Points
Grab Bonus*, and can be triggered or paused there like any other job.

## Data

Owns no tables. It reads eligibility from the host and writes through the
ledger; both are host domain, and a plugin that kept its own copy of "who has
been paid" would be a second opinion about money.

## Dependencies

Core services consumed: `Points` (the ledger — required; both rules exist to
move points, so without it they would run, log success and award nothing).
Scheduling comes from `loon/schedule` directly.

**Awarding is deliberately not a seam.** In-tree each rule credited the balance
and wrote the ledger entry as two separate host calls, which is how a balance
and its ledger drift apart: one succeeding without the other leaves a member
with points nothing explains, or an explanation for points they do not have.
`core.Points.Award` is a single operation, so the pair collapsed into it.

`Deps` (SetDeps in the worker block before `core.Boot`):

- `PointsPerGrab`, `PointsTenurePerYear` — read **per run**, not captured at
  Provision, so an admin changing a rate takes effect on the next tick rather
  than the next deploy. Zero disables that rule, which is the documented way to
  turn one off.
- `TenureEligible` — members due an anniversary award today. The host owns this
  query **because it owns the idempotency**: the shipped implementation matches
  the anniversary day and excludes anyone already paid `earn_tenure` this
  calendar year.
- `UploaderGrabTotals`, `GrabsAlreadyCredited` — lifetime grabs, and the
  high-water mark last paid to. The difference is the award, so a re-run
  credits nothing.

## Hooks & Callbacks

- Host hooks SET: **(none)**
- Extensions PUBLISHED / CONSUMED: **(none)**

## Lifecycle

- **Provision** — checks every seam and that `Core.Points` exists, then
  registers both jobs.
- **Start** — launches two `schedule.ServiceLoop` goroutines from the host root
  context, so both exit cleanly on SIGTERM rather than being killed mid-award.
  Bare `ServiceLoop`: the host installs the off-peak, interval-override and
  panic hooks globally, so the plugin carries no loop store of its own.
- **Stop** — no-op.

## Files

- `plugin.go` — lifecycle and job registration.
- `deps.go` — the host seams and the ledger reason codes.
- `jobs.go` — both rules, and the award arithmetic they share a test file with.

## Testing

- Unit-tested: the award arithmetic, which is the only place this plugin can
  lose money. `grabAward` covers a high-water mark *ahead* of the total (a
  purge, a manual ledger edit) awarding nothing rather than a negative — a
  negative award would debit a member for having fewer grabs. `tenureAward`
  covers fractional years, future `created_at`, and disabled rates. The reason
  codes are pinned too: `GrabsAlreadyCredited` filters the ledger on
  `earn_grabs`, so renaming it makes every past award invisible and re-pays
  every uploader's entire history.
- Needs integration (live DB): the eligibility queries themselves live on the
  host, so what is untested here is the seam wiring rather than the SQL.

## A known fragility, inherited

The tenure rule pays only on the exact anniversary **day** — the host query
matches month and day against `CURRENT_DATE`. If the job does not run that day
(box down, off-peak gate, a deploy landing badly), the member is not paid, and
the next opportunity is a year later. The calendar-year guard that prevents
double-paying is also what prevents catching up. This predates the extraction
and moved with it; fixing it means widening the window and having the guard
work off "paid for THIS anniversary" rather than "paid this calendar year".
