# economy plugin

Automatic points earning. One scheduled rule, no web surface at all — it runs
on the worker, credits the ledger, and is configured from the host's settings
page.

- **Points Grab Bonus** — pays uploaders for grabs on their releases, crediting
  only what is new since the last run.

**Points Tenure Bonus moved out.** It is now a `per_unit` reward over completed
years of membership in the rewards plugin, which retires the anniversary-DAY
query that made a missed run cost a member a whole year. See docs/REWARDS.md.

## Surface

No routes. `Metadata.Processes: ["worker"]`, so it never provisions on web or
api. The rule appears in `/admin/jobs` as *Points Grab Bonus*, and can be
triggered or paused there like any other job.

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

- `PointsPerGrab` — read **per run**, not captured at Provision, so an admin
  changing the rate takes effect on the next tick rather than the next deploy.
  Zero disables the rule, which is the documented way to turn it off.
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
  negative award would debit a member for having fewer grabs. The reason code
  is pinned too: `GrabsAlreadyCredited` filters the ledger on `earn_grabs`, so
  renaming it makes every past award invisible and re-pays every uploader's
  entire history.
- Needs integration (live DB): the eligibility queries themselves live on the
  host, so what is untested here is the seam wiring rather than the SQL.

## Why tenure left

The tenure rule paid only on the exact anniversary **day** — the host query
matched month and day against `CURRENT_DATE`. A run missed that day (box down,
off-peak gate, a deploy landing badly) meant the member was not paid, and the
next opportunity was a year later. The calendar-year guard that prevented
double-paying was also what prevented catching up.

That is not fixable by widening the window: the guard and the bug are the same
mechanism. As a `per_unit` reward over completed years, there is no day to miss
— the reference is the year count, and "you have reached year 4 and been paid
for year 3" is answerable whenever the job next looks. It moved rather than
being patched.
