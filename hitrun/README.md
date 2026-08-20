# hitrun plugin

The hit-and-run framework: the rules that decide whether a member who stopped
seeding is warned, and what a site does about it.

**It sits OVER the tracker, not inside it.** The tracker keeps the accounting —
uploaded, downloaded, seedtime, last announce — and this decides what that
accounting *means*, which is a policy question every site answers differently.
A host can run the tracker with no punishment system at all by simply not
enabling this.

**This plugin never disables anything itself, and cannot.** The tracker owns the
download path and belongs to another plugin; reaching into it would put the rule
in one file and its enforcement in another that never mentions it. The host
supplies `LimitReached` and decides what losing privileges means — revoke an
entitlement, drop a rank, refuse at the edge, all three.

Defaults follow UNIT3D's `config/hitrun.php`, the nearest thing this space has
to a standard, so an operator retunes from something rather than inventing a
number.

**Deliberately absent:** appeals, and any notion of a warning being disputed. A
warning can be cleared by a moderator or by the member satisfying the
requirement afterwards, and that is all.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `/hitrun` | member | Mounted **only** when the host wired `RenderPage`. Without it the rules and enforcement still run and the page is absent — logged at boot, because a member told their downloads are disabled with nowhere to see why is the worst version of this feature. Never on `api`. |
| Widgets | member | What they currently owe and what is at risk. |
| Job **Hit and Run Sweep** | operator | Hourly, `MarkWrites()`. The thresholds are measured in days, so a finer cadence would only change which hour somebody is warned; a coarser one would leave a member unable to act on a notice not yet sent. |

`Processes: ["web"]`, `Flavours: [tracker]`.

## Data

Owns `hitrun.warnings`.

Two columns are stored rather than derived, for the same reason: **a rule
change must not rewrite history.**

- `reason` holds the words shown to the member. A warning issued under a 7-day
  rule should still say seven days after an admin moves it to three.
- `expires_at` is set at issue time from the then-current expiry setting.

Cleared warnings are **kept as rows**, not deleted, so the history survives a
moderator's decision.

## Configuration

`plugins.hitrun.*`. Everything below is UNIT3D's shipped value plus one
addition.

| Key | Default | Why |
|---|---|---|
| `enabled` | `false` | Off, like the tracker itself. A rule that disables downloads is not something a host should acquire by merely compiling the code in. |
| `seedtime_seconds` | `604800` | Seven days. The number most likely to be retuned — community trackers commonly run 24–72h, and AlphaRatio scales it with size. |
| `prewarn_days` | `1` | How long a member may be gone before the courtesy notice. |
| `grace_days` | `3` | How long *after* the notice they have to come back. |
| `max_warnings` | `3` | Active warnings before download privileges go. |
| `expire_days` | `14` | How long a warning counts for. |
| `buffer_percent` | `10` | How much of a torrent must actually have been taken before liability. **The setting that stops the rule punishing accidents**: someone who starts a 40GB torrent, takes 300MB and cancels has not hit-and-run, they changed their mind. |
| `ratio_satisfies` | `true` | Excuses a short seedtime when the member already uploaded back at least as much as they took. **Not** in UNIT3D's conditions, which key on seedtime alone — but it is what most real trackers do, and punishing somebody for doing their share quickly is a rule with no purpose behind it. |

**A zero is treated as unset, not as "none".** `normalise` replaces nonsense
with that field's default, because reading a missing config line literally
turns it into a site that warns everybody immediately.

## Dependencies

| Seam | Required | Why the host supplies it |
|---|---|---|
| `RenderPage` | for the page | Wraps a fragment in the site layout. Without it the page is not mounted at all, which is better than serving one that looks signed-out. |
| `RelativeTime` | no | Formats "last seen" the way the rest of the site does, so it does not drift from every other timestamp. |
| `Prewarn` | no | The courtesy notice — the one message that can still change the outcome, so a host should make it say what to do. |
| `Warn` | no | The punishment notice. |
| `LimitReached` | no | **The important one.** This plugin detects; the host punishes. Putting that choice here would bake one site's policy into a framework. |

Core: `Storage.SchemaDB`, `Scheduler`, `Auth`, `Router`, config.

Consumes perks' freeleech answer: a torrent the site said was free is exempt
from seeding requirements, because a site that told somebody a download was
free has already said what it owes.

## Hooks & callbacks

`Evaluate(policy, snatch, prewarnedAt, now)` returns one of three verdicts —
**Satisfied**, **Prewarn**, **Warn** — and is a pure function, which is why it
is the best-tested thing in the package.

`DownloadsBlocked`, `AtRisk` and `Owed` are the other pure answers, used by the
widgets and the host.

Declares no events — a real gap: nothing downstream can react to a warning.

## Lifecycle

`Provision` reads the policy, opens the schema DB, registers the widgets,
mounts `/hitrun` when the renderer is wired, and registers the sweep job.
`Start` runs the loop; `Stop` stops it.

## Files

```
plugin.go        lifecycle, config, job registration, the renderer check
policy.go        Policy, normalise, Evaluate, the verdicts, AtRisk, Owed
sweep.go         the hourly pass: prewarn, warn, expire
store.go         Store, PGStore
handlers.go      the member page
views.go         rendering
widgets.go       what a member owes and what is at risk
templates/
migrations/      001_init.sql
policy_test.go   the rules
sweep_test.go    the pass
```

## Testing

`go test ./hitrun/` — no database needed.

Covered: `Evaluate` across the matrix — under buffer, ratio satisfied, prewarn
threshold, grace expiry, already warned — and `normalise` turning zeroes back
into defaults, which is the one that would otherwise warn everybody.

**Not covered:** the sweep's SQL, so "which snatches does an hour's pass
actually select" is verified only through the in-memory double. The widgets'
templates are not executed in a test. And there is no event, so no test could
assert that anything downstream learns a warning happened.
