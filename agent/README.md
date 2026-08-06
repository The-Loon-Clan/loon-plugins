# agent plugin

The fleet agent's read-only surfaces: the **Agent Fleet card** a member sees on
their own profile, and the **Agent Dispatch** overview on admin settings. An
"agent" here is a fleet worker the member runs, which polls the host for upload
work; this plugin shows what those agents are doing and never talks to them.

It is surfaces-only on purpose. The agent tables, the `/api/agent/*` runtime
agents poll, and the lock-expiry job all stay with the host: agents write those
rows continuously, and a half-moved runtime is a worse place to stop than
either end.

## Surface
- **Profile fleet card** — loon `SlotUserWidget`, slug `agent-fleet`.
  Owner-only: renders nothing unless the viewer owns the profile, because an
  agent roster (names, activity, last-seen) is not public. loon's
  `Public`/`MinRole` cannot express "only the subject", so the gate is the
  view's own, checking `core.ViewSubject` against `Deps.Viewer`.
- **Admin dispatch panel** — loon `SlotAdminSettings`, slug `agent-dispatch`,
  `MinRole: RoleAdmin`. Read-only: online/total, the per-agent concurrency cap,
  and jump links to `/admin/agents`, `/admin/agent-groups`, `/admin/dispatch`.
  Editing the cap stays in the host's "Agent Defaults" form on the same page.
- `Metadata.Processes`: `["web"]`.

## Data
Owns no tables and runs no migrations. Every read goes through `Deps`.

## Dependencies
- **Core services**: `RegisterView`, `ViewSubject`.
- **Store**: none — `SetDeps(Deps{...})` from the composition root, before
  `core.Boot`. The five adapters are function-typed rather than an interface
  set because each needs translating at the boundary anyway:
  - `Viewer(c) (userID, ok)` — who is signed in.
  - `AgentsForUser(ctx, userID) ([]Agent, error)` — one member's agents.
  - `ActiveTask(ctx, agentID) (*Task, error)` — current work, nil for idle.
  - `CountAgents(ctx, onlineSince) (online, total, error)` — admin overview.
  - `MaxConcurrent(ctx) int` — the host's dispatch cap, displayed read-only.
- **Config keys / Metadata.Requires**: (none).

`Agent` and `Task` are plugin-side view types holding only the fields the two
templates draw. That is the narrowing, not just translation: a host's agent
record carries the token hash and revocation state next to the name, and a type
that cannot hold them cannot leak them into a template.

`Provision` fails when any adapter is missing. A partial wiring used to be
survivable — the card rendered empty forever, which is indistinguishable from
"this member has no agents" and would ship unnoticed.

## Hooks & Callbacks
- Host hooks SET: (none).
- Extensions PUBLISHED / CONSUMED: (none).

## Lifecycle
- **Provision**: validates `Deps`, registers the two views. No-op outside the
  `web` process.
- **Start / Stop**: no-ops — no background work.

## Files
- `plugin.go` — registration, metadata, `Provision` and its deps check.
- `deps.go` — the `Agent`/`Task` view types and the `Deps` adapters.
- `views.go` — the profile fleet card, its owner gate, `shortDuration`.
- `admin.go` — the admin dispatch panel.

## Testing
Unit-tested with stub adapters: the owner gate (no subject / anonymous /
non-owner all render empty, seeded with an agent so a leak would show),
the owner's populated card, the empty-roster case, the dispatch panel's
counts and links, `Provision`'s rejection of a partial `Deps`, and
`shortDuration`. No integration gap — the plugin issues no queries of its own.

## Not here yet
The `/api/agent/*` runtime, the account-management pages, and the
`ExpireStaleLocks` job. As they move, `Processes` grows to `web/worker/api` and
the dispatch panel gains an editable form (which needs setters across the
settings seam).
