# dailyreward plugin

A home-page card where a signed-in member claims a daily reward — once per day,
behind a captcha — for points and a growing streak. Small on purpose, and one
of the better tours of composing core seams: a widget view, a plugin route for
the POST, the points ledger, schema-scoped storage, an announced event, and the
host's captcha reached as a capability.

**The captcha is looked up structurally**, off the extension registry under
`captcha`, so this plugin never imports the host's captcha package.

Two cases that look identical from here and are not, and the distinction is the
plugin's most instructive line:

- A host that registered **no** captcha has *chosen* not to gate. The claim
  runs ungated — safe enough behind auth and once-per-day — and the choice is
  logged at boot so it is a decision rather than a surprise.
- A host that registered **one this cannot use** has a wiring bug. Booting
  anyway would serve an ungated points endpoint while looking healthy, so that
  fails `Provision`. The error names the key, the offending type, and what
  swallowing it would have cost, and a test pins that it stays actionable.

**Deliberately absent:** any way to configure the ladder or the cap. The reward
curve is a function in `store.go`, not a setting, because a demo that made it
editable would need an admin surface bigger than the feature.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| View `daily-reward` | `RoleUser` | `SlotSiteWidget` — the claim card. |
| View `daily-streak` | public | `SlotUserWidget` on a profile, rendered for the profile **subject** via `core.ViewSubject`: a plugin contributing to profiles it knows nothing about. |
| `POST /plugin/dailyreward/claim` | member | A plugin route rather than a view action, because `SlotSiteWidget` ignores `Actions`. It inherits the host middleware stack. |

No jobs. `Processes: ["web"]`, any flavour.

**The claim answers in two dialects.** JSON for the widget's `fetch()`, a
redirect for a plain form POST, and it decides by `X-Requested-With` — which
the widget sets explicitly — *not* by `Accept`. A browser's form POST also
sends an `Accept` header listing several types, and matching on that would turn
the no-JS path into a screenful of JSON.

## Data

Owns `dailyreward.daily_rewards`: one row per member, `user_id` primary key,
holding `last_claim`, `streak`, `longest` and `total_claims`.

**The whole claim is one `SELECT … FOR UPDATE` and one write in a single
transaction.** Two tabs clicking at once is the ordinary case, not the exotic
one; the row lock is what makes the second see the first's result rather than
both reading "not claimed yet".

Already claimed today returns `claimed=false` with the streak unchanged — not
an error, because it is not one.

Dates are compared as `YYYY-MM-DD` strings against `today` and `yesterday`
passed in by the caller, so "what day is it" is decided in one place rather
than by the database's timezone.

## Dependencies

Core: `Storage.SchemaDB`, `Points`, `Router.Mount`, the view registry, the
extension registry, the mediator.

| Registry key | Required | Why |
|---|---|---|
| `captcha` | see above | Gates the claim. Absent = ungated by choice; present-but-wrong = boot failure. |

No `SetDeps` seam.

## Hooks & callbacks

**Publishes** `StatusExtension` — per-user claim state, for a host that wants a
compact control of its own rather than (or as well as) the card. Registered
*before* the views, so a host looking it up cannot race the widget's own
registration.

**Declares** `dailyreward.claimed` — countable, stable, member kind. The
payload carries `Streak` (after this claim) and `Reward` (points actually
paid), so a subscriber does not have to re-implement the ladder to know what
happened. Countable is right here, unlike most: a claim is rate-limited by the
calendar, so a count cannot be farmed.

**Award first, announce second.** The emit used to sit above the award, so a
failed `Award` still announced `Claimed{Reward: N}` — an event asserting points
that were never paid, which anything downstream would believe. The streak
advancing while the balance does not is the specific failure that ordering
prevents.

The announced `Reward` is the amount **paid**, after
`pluginapi.MultPoints` multipliers, which on a host with no multiplier sources
is exactly the ladder value.

## Lifecycle

`Provision` opens the schema DB, resolves and type-checks the captcha, parses
templates, mounts the claim route, registers the status extension, declares the
event, and registers the two views — in that order, and the order matters where
noted above. `Start` and `Stop` are no-ops.

## Files

```
plugin.go             lifecycle, captcha resolution, route + view registration
views.go              the widget, the profile card, the claim handler, dialects
store.go              Store, PGStore, the FOR UPDATE claim, rewardFor
events.go             the one declaration and its payload
templates/            widget + profile_streak
migrations/           001_init.sql
*_test.go             captcha wiring, claim response shape, status, store
```

## Testing

`go test ./dailyreward/` — no database needed.

Covered: the captcha wiring's three cases including the exact boot-error
wording; the claim response in both dialects; the status extension; and the
store's streak arithmetic and reward ladder through the in-memory double.

**Not covered:** the `FOR UPDATE` concurrency itself. The double cannot
reproduce a row lock, so "two tabs at once" — the reason the transaction is
shaped that way — is verified by reading rather than by a test.
`pluginapi/pgtest` makes that writable now, and it is the most valuable missing
test in this package.
