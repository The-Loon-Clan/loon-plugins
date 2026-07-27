# ranks plugin

The paid-rank system, mid-migration to the **groups** model (see
[ENTITLEMENTS.md](../../../ENTITLEMENTS.md) Stage 2): the catalog admin page, the rank-granting capability other plugins award through, and the
hourly expiry job. Admins (mod+) define tiers — each with a Bootstrap badge
colour, title colour, per-day download/API limits, a points cost, and a
duration. Ranks are **sold as store items** (the `store` plugin), which grant
through this plugin's published `RankGranter` capability rather than a buy route
here.

The plugin now **owns its data**: the groups model lives in its own Postgres
schema, and **nothing outside this plugin reads the legacy tables any more**.
Access decisions moved to core entitlements (the DM gate in Stage 3.1, the
download/API quotas in 3.2); the display readers moved to this plugin's
`GroupDisplay` and `GroupAudit` capabilities in 3.4. The legacy
`public.user_ranks` trio is still written as a **mirror** in the same
transaction as every groups mutation, but it now has no readers — dropping the
mirror is the next step, and it is what frees this plugin for the lift to
`loon-plugins`.

## Surface
- **admin** — a plugin-owned View (loon `SlotAdminPage`), which the host mounts
  at `GET /admin/p/groups` with actions at `POST /admin/p/groups/<action>`
  (create / update / delete) and an admin-hub card, all from the registration
  alone. The host's admin group is already mod-gated. Toggling `visible` is
  restricted to **admins** inside the action — a group that grants entitlements
  with no badge is the shape of a privilege-escalation mistake, so mods keep
  full catalog CRUD but not that lever.
- **worker** — the hourly **Rank Expiry** job (`jobs.go`): sweeps lapsed
  memberships and logs deranks to history.
- **Process kinds**: `Metadata.Processes` is `["web","worker"]` — the web/all
  leg serves the admin catalog + publishes the granter; the worker/all leg runs
  the expiry loop.

## Data
**Owned** (this plugin's Postgres schema, `migrations/001_groups.sql`, applied by
loon's plugin-migration runner):
- `groups` — the catalog. Carries `visible` (grants entitlements but shows no
  badge) and `parent_id` (inherits the parent's entitlements). Cycles are
  structurally impossible: `child.depth = parent.depth + 1` with
  `CHECK (depth BETWEEN 0 AND 3)`, since a cycle would need a node to be its own
  strict ancestor.
- `group_entitlements` — the keys a group confers (`download.daily`,
  `api.daily`, `dm.initiate`).
- `group_members` — membership. `expires_at NULL` means permanent, which
  `assigned` staff groups need and the legacy NOT NULL column could not express.
- `group_member_history` — the audit log.

IDs are **copied from the legacy catalog, never renumbered**:
`store.items.reward_ref` holds rank ids as unvalidated free text, so renumbering
would silently grant the wrong group.

**Mirrored** (host `public` schema, still authoritative for every reader):
`user_ranks` (migrations 86/88/89), `user_rank_subscriptions`, and
`user_rank_history` (migration 111). Only `visible AND kind='paid'` groups
mirror, so a hidden or earned group can never surface in a legacy reader.

## Dependencies
- **Core services** (off `*core.Core`):
  - `c.Storage` — `SchemaDB("ranks")`, which the plugin's `PGStore` is built over.
  - `c.Auth` — `CurrentUser`, for the admin-only visibility gate.
  - `c.Errors` (`core.ErrorReporter`) — handler failures and, importantly, a
    failed reconcile.
- **Store**: self-contained (`PGStore` over the plugin schema, `MemStore` for
  tests).
- **`SetDeps` (web/all): gone.** The web leg's only shared-domain dependency was
  the host's user-limits cache, which the granter invalidated after a purchase.
  Stage 3.2 moved the download/API quotas themselves onto core entitlements, so
  granting through `c.Entitlements` invalidates the reader's cache and there is
  nothing left to hand in. `Provision` no longer gates on it.
- **`SetJobDeps` (worker/all)** still runs from `cmd/main.go` before `core.Boot`,
  and `Provision` hard-fails on the worker leg without it:
  - `JobDeps.Loop services.ServiceLoopStore` — the schedule loop.

  This is the one remaining host import (`jobs.go`), and it is a re-export
  rather than app data: `ServiceLoop`, `RegisterJob` and `RootContext` are
  loon's `schedule` package under historical names. The lift swaps the import
  path; it does not need a seam.
- **Config keys**: (none).
- **Metadata.Requires**: (none).
- No `web/handlers` import: the View renders its own embedded template, so the
  last host-handler dependency is gone.

## Hooks & Callbacks
- **Host hooks SET** (`web/handlers/plugin_hooks.go`): (none) — and
  deliberately so. `handlers.GroupBadges` is fed from this plugin's
  `GroupDisplay`, but `cmd/groups_wiring.go` assigns it after `core.Boot`
  rather than this plugin assigning it during `Provision`: doing it here would
  re-add the `web/handlers` import the plugin shed, re-blocking the lift.
- **Extensions PUBLISHED** (`Core.Register`), on **every** leg including the
  headless worker — the discord and irc bots run there and read badges, so a
  web-only publish left them resolving capabilities absent from their process:
  `pluginapi.RankGranterName` — a
  `rankGranter` other plugins (the `store`, a donation-reward orchestrator) call
  to award a rank without importing this package. Its `(userID, rankID)`
  contract is unchanged by the groups move because ids were preserved.
  Also `pluginapi.GroupAuditName` — a `groupAudit` (see `audit.go`) serving the
  admin user page's Rank History card. A separate capability from the display
  one on purpose: reading membership history is an administrative concern, and
  `GroupDisplay` stays narrow so that wanting a badge does not come with the
  ability to audit.

  Also `pluginapi.GroupDisplayName` — a `groupDisplay` (see `display.go`)
  answering "what badge does this user wear?", the display counterpart to the
  access decisions that go through `core.Entitlements`. Stage 2.4 wrote the
  implementation and the contract but never called `Register`, so it resolved
  as not-found until Stage 3.2; both consumers of that contract treat absence
  as a cosmetic degrade, which is why nothing complained.
- **Extensions CONSUMED** (`Core.Lookup`): (none).

## Lifecycle
- **Provision**: builds the `PGStore` over `SchemaDB("ranks")`; the web/all leg
  publishes `RankGranter` and registers the admin View; the worker/all leg
  validates `JobDeps` and constructs the expiry job. No network at Provision.
- **Start**: runs **Reconcile** (below), then the worker/all leg starts the
  hourly Rank Expiry loop. **Stop**: no-op.

### Reconcile
Makes the two models agree, and the ORDER is the point. A rolling deploy leaves
a window where an old pod writes only the legacy tables, so a purchase — or a
renewal — can land there with no groups counterpart. Reconcile therefore
**imports first** (adopting a legacy expiry that is ahead of ours, not just
first purchases) and only then rebuilds the mirror; the reverse order would
delete money-adjacent data silently. It runs at `REPEATABLE READ`, because under
the default `READ COMMITTED` every statement takes a fresh snapshot and a row
committed between the import and the prune would be invisible to the first and
visible to the second. Idempotent: a settled pass reports all zeros.

## Files
- `plugin.go` — `core.Plugin` impl: registration, `Metadata` (incl. the embedded
  migrations), `Provision`, `Start` (reconcile + expiry loop).
- `store.go` — the `Group`/`Member` types, the `Store` interface, `MemStore`.
- `store_pg.go` — `PGStore`: the plugin-schema queries, **the dual-write**, and
  `Reconcile`.
- `handlers.go` — the shared admin-form parsing (no deps left to carry).
- `views.go` — the `SlotAdminPage` View: render + the create/update/delete
  actions, including the admin-only visibility gate.
- `templates/groups.html` — the embedded catalog fragment.
- `granter.go` — the `pluginapi.RankGranter` implementation.
- `audit.go` — the `pluginapi.GroupAudit` implementation (membership history).
- `display.go` — the `pluginapi.GroupDisplay` implementation (badge resolution,
  the visibility filter, the prominence sort).
- `jobs.go` — `JobDeps`/`SetJobDeps`, the `rankExpiry` worker job.
- `migrations/001_groups.sql` — the owned schema + the ID-preserving seed.

## Testing
- **Unit-tested** (`provision_test.go`): that `Provision` actually puts both
  capabilities on the extension registry, and that the worker leg still refuses
  to boot without `SetJobDeps`. No database — `sqlx.Open` does not dial, and
  `Provision` only needs a non-nil handle — so a registry regression fails on a
  plain `go test`. This exists because `GroupDisplay` shipped unregistered and
  nothing noticed.
- **Unit-tested** (`ranks_test.go`, over `MemStore`): `GrantRank` duration
  semantics (fallback to the group's duration, the 30-day default, an explicit
  duration winning, unknown-group error); admin form normalization (numeric
  clamps, name-required, the hidden-cost round-trip); and that an edit through
  the legacy form preserves `kind`/`visible`/`parent_id`/`icon` rather than
  resetting a hidden staff group to a visible paid tier; and that a mod cannot
  flip visibility while an admin can.
- **Integration-tested** (`badge_data_integration_test.go`): that `BadgeData`
  agrees exactly with the two readers it replaced, that it does **not** load
  grants while `Groups` still does, and `BadgesForBatch` end-to-end over
  Postgres including the hidden-group filter. The parity case is the point: the
  method exists only to be cheaper, so the risk is that it silently answers
  differently.
- **Needs integration** (live Postgres, `-tags=integration`,
  `INDEXER_TEST_DB_DSN`): everything that is a property of the database rather
  than of Go — the migration (schema placement, ID-preserving seed,
  idempotency, the depth/cycle guards), the **dual-write** (mirroring on create
  and on edit, hidden/non-paid groups never reaching legacy, the
  hidden↔visible round trip, membership extension stacking, permanent
  memberships surviving both `AddMember` and the expiry sweep, history reaching
  the legacy audit table with a NULL rank for non-mirrored groups), and
  **reconcile** (adopting a legacy-only renewal, importing legacy-only catalog
  rows, not rewriting limits a group has no opinion on, keeping hidden groups
  out of the mirror, sweeping orphaned memberships, idempotency), and
  **re-parenting** (building a chain, re-stamping a moved subtree's depths,
  and rejecting self-parents, descendant-parents and moves that would push a
  descendant past the four-level limit).

  These use a scratch schema for BOTH the plugin tables and the legacy fixture,
  because `go test ./...` runs packages in parallel against one database and
  writing `public.user_ranks` here races `pkg/storage/postgres`. The trade-off:
  the literal `public` default is never exercised, only the mirror mechanism.
