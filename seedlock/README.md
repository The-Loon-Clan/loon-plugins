# seedlock plugin

One host per torrent per member: a claim, a lock window, and a way for the
member to clear it themselves.

The cheat it prevents is one person seeding the same torrent from several
machines to multiply their credit. The failure it tries hardest to avoid is
locking somebody out of their own torrent because they restarted a client.

Like `hitrun`, it sits **over** the tracker: the tracker owns the announce, and
this answers one question about each one.

**Deliberately absent:** any punishment. A refused announce is refused and
nothing is recorded against the member. Seeding from two machines is a
configuration mistake far more often than it is cheating, and treating it as
the latter by default would be wrong on most sites.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `GET /seedlock` | `RoleUser` | The member's claims, and where a refusal message points them. Mounted only on a non-`api` process **and** only when the host wired a renderer — a rule that tells somebody to "clear the lock on the site" while offering no such page sends them looking for something that is not there. |
| `POST /seedlock/clear` | `RoleUser` | The escape hatch, and the reason the lock window can be generous. |
| Widgets | member | Current claims. |

`Processes: ["web", "api"]`, `Flavours: [tracker]`.

## Data

**Owns no tables, and has no migrations.** A claim lives in **Redis** with a
TTL, so there is nothing to sweep and nothing to expire by hand.

**Redis is required, and its absence disarms the plugin rather than degrading
it.** An in-memory claim would be per-process and announce runs on two, so a
lock that only half applies is worse than none: it punishes the members whose
announces happen to land on the process holding the claim. Enabled without
Redis logs a warning and admits every announce.

## Configuration

`plugins.seedlock.*`:

| Key | Default | Why |
|---|---|---|
| `enabled` | `false` | Off, like every other rule here that can refuse a member something. |
| `lock_minutes` | `30` | How long a claim survives past the claiming host's last announce — about one announce interval plus slack. Too short and a client restart or brief network drop hands the torrent to another host mid-session; too long and a member who genuinely moved machines is locked out with no idea why, which is what the clear action is for. |
| `identify_by` | `ip` | What counts as "the same host". `ip` is the honest one: the cheat is one person on several machines, and machines are what have addresses. `peer` uses the client's `peer_id`, which is per torrent-session rather than per machine — a client restart mints a new one — so it locks far more aggressively and is offered only for a site that wants that. |

`normalise` replaces a non-positive `lock_minutes` with 30 and anything that is
not `peer` with `ip`, so a typo fails toward the gentler rule.

## Dependencies

Core: `Redis` (**required to arm**), `Auth`, `Router`, `Storage` (only for the
page's lookups), config.

Imports `loon-plugins/tracker` to hook the announce.

| Seam | Required | Why |
|---|---|---|
| host page renderer | for the page | Same rule as hitrun: no renderer, no page, and a log line saying so. |

## Hooks & callbacks

**Publishes** itself under `seedlock`, so the host can offer the clear action
without importing this package.

`Decide(policy, held, host, event, now)` returns **Allow**, **Refuse** or
**Release**, and *the order of its checks is the design* — every one before the
refusal is a reason not to lock somebody out:

1. the rule is off;
2. the member is **stopping** — that releases rather than refuses, or a member
   who stops on host A could never start on host B;
3. nobody holds it;
4. this **is** the holder — the overwhelmingly common case, and it has to be
   cheap;
5. the previous holder has been quiet for longer than the window.

The refusal message names a **masked** host, not the full address: enough for
the member to recognise their own other machine, not enough to hand out an IP.

Declares no events.

## Lifecycle

`Provision` reads the policy and returns early — armed at nothing — when
disabled or when Redis is absent. Otherwise it builds the Redis store,
registers the extension, and mounts the page where a human can see it. `Start`
and `Stop` are no-ops; the TTL does the expiry.

## Files

```
plugin.go        lifecycle, the disabled/no-Redis paths, registration
policy.go        Policy, normalise, Decide, the verdicts, masking
store.go         the Redis claim store
views.go         claims page and clear action
widgets.go
templates/
policy_test.go   the decision order and the window
admit_test.go    admission end to end
```

## Testing

`go test ./seedlock/` — no Redis needed; the tests use an in-memory store.

Covered: every branch of `Decide` including the ordering above, the boundary of
the lock window, both `identify_by` modes, and that a stop event releases
rather than refuses.

**Not covered:** Redis itself — the TTL, and the two-process sharing that is
the entire reason Redis is required, are verified by reading. `REDIS_TEST_ADDR`
exists in the host's `make itest` and nothing here uses it yet. The page
templates are not executed in a test.
