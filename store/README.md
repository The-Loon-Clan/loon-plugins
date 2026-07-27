# store plugin

Points store: users spend points on a catalog of items at `/store`, mods manage the catalog at `/admin/store`. The first (and currently only) reward type is **rank** — buying a rank item grants the user a rank subscription.

The interesting bit is *how* it grants a rank without importing the ranks plugin: it consumes the **`pluginapi.RankGranter`** capability the ranks plugin publishes on the core extension registry. The store is the first consumer of a cross-plugin capability contract, so it is the reference for the "publish/lookup" pattern (the provider side lives in [`../ranks/granter.go`](../ranks/granter.go)).

Self-contained data layer (owns `store.items` / `store.purchases` in its own `store` schema); the only peer dependency is the ranks capability, declared in `Metadata.Requires`.

## Surface

Routes are registered in `Provision` on the shared Gin engine:

- **Authed** (`c.Auth.Authenticate()` — any logged-in user):
  - `GET /store` — `StorePage`, lists active items + the viewer's points balance.
  - `POST /store/buy/:id` — `BuyItem`, runs the purchase transaction.
  - `GET /store/history` — `HistoryPage`, the viewer's own purchase history.
- **Admin** (`c.Auth.RequireUser(core.RoleMod)` — mod or above):
  - `GET /admin/store` — `AdminStorePage`, the full catalog (active + inactive).
  - `POST /admin/store/create` — `CreateItem`.
  - `POST /admin/store/:id/update` — `UpdateItem`.
  - `POST /admin/store/:id/delete` — `DeleteItem`.

Process kinds: `Metadata.Processes` is empty → **web/all only** (session UI). It is never booted in the worker or the bare `--mode=api` engine, so those processes never see its session/template routes.

## Data

- Owns two tables in its **own `store` Postgres schema** — the first plugin to do so. The migrations ship embedded in the plugin (`migrations/001_init.sql`, wired via `Metadata.Migrations`); loon's `core.RunPluginMigrations` creates the `store` schema at boot, applies the files with `search_path` scoped to it (so unqualified names become `store.*`), and tracks them in `core.plugin_migrations`. (The older plugins still ship host-numbered public-schema migrations pending the same conversion.)
  - `store.items` (`id, name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order, created_at`). `stock = -1` means unlimited; `reward_ref` is the target rank id for `reward_type = 'rank'`; `reward_days` is the grant duration.
  - `store.purchases` (`id, user_id, item_id, points_spent, created_at`) — the store's own audit ledger, separate from the points ledger.
- **The PGStore schema-qualifies every table** (`store.items`, not `store_items`) because the shared `c.Storage.DB()` pool runs with the default `public` search_path — never `SET search_path` on a pooled connection.

## Dependencies

- **Core services**: `Storage` (`c.Storage.DB()` → builds the `*PGStore`), `Router` (`c.Router.Engine()`), `Auth` (`Authenticate()` + `RequireUser(RoleMod)` chains), `Points` (`c.Points` — `Balance` on the store page, `Deduct`/`Refund` in the buy transaction), `Errors` (`c.Errors` → `Report` on the redirect-based handlers; `HandlerError` is not used because these paths redirect rather than return JSON).
- **Store**: self-contained `*PGStore` built at `Provision` from `c.Storage.DB()` (nil DB → boot error). No `SetDeps`. `Store` (`store.go`) is the plugin's own interface; `PGStore` (`store_pg.go`) is the production impl and `memStore` (in `store_test.go`) the in-memory test double.
- **Config**: none.
- **Metadata.Requires**: `["ranks"]` — guarantees ranks is provisioned first, so its `RankGranter` is on the registry before this plugin's `Lookup` runs.
- **Host seams** (`Deps` + `SetDeps`, called from the host's `main()` before `core.Boot`): the plugin renders the **host's** templates, so the host supplies the two things a template needs and the Core does not carry.
  - `BaseData(c, extra) gin.H` — merges the host's page chrome (viewer, nav, CSRF, notification counts) into the template data map.
  - `Paginate(page, pageSize, totalItems, baseURL) any` — builds the view-model the host's pagination partial consumes. Returned as `any` so the plugin never learns the host's type; the template reads it by field name.
  - `PageOffset(page, pageSize) int` — the SQL offset. Separate from `Paginate` because it is needed *before* the query, while the view-model cannot be built until the total is known.

  `Provision` fails loud if `SetDeps` was skipped. The **viewer is deliberately not a dep**: it comes off `core.Auth.CurrentUser(c)`, which every plugin already has — taking it would mean exporting a host type (`*models.User`) when all three call sites want an id.
- **Templates**: `store.html`, `store_history.html` and `admin_store.html` are the host's, not the plugin's. An adopting site must provide them; see "Template contract" below.

## Hooks & Callbacks

- **Host hooks SET** (`web/handlers/plugin_hooks.go`): (none).
- **Extensions PUBLISHED** (`Core.Register`): (none).
- **Extensions CONSUMED** (`Core.Lookup`):
  - **`pluginapi.RankGranterName` (`"ranks.granter"`)** → asserted to `pluginapi.RankGranter`. **Hard** dependency: declared in `Requires`, so a missing/wrong-typed value aborts boot. The reference example of consuming a cross-plugin capability.
  - **`pluginapi.InviteGranterName`** → asserted to `pluginapi.InviteGranter`. **Soft**: looked up without being in `Requires` (a host capability, not a plugin, so it can't order boot), and absent means the invite reward type is simply unavailable rather than a boot failure.

## Lifecycle

- **Provision**: validates `SetDeps` was called and `Storage.DB()`/`Router.Engine()` are non-nil, looks up + type-asserts the required `RankGranter` and the optional `InviteGranter`, builds the `*PGStore` + `Handlers` (store + points + rank granter + invites + errors), and registers the `/store` (incl. `/store/history`) + `/admin/store` route groups.
- **Start / Stop**: no-op (no background work).

## The buy transaction

`BuyItem` is thin HTTP glue; the integrity lives in `purchase(ctx, userID, item)`. Points and the reward grant are separate services with no shared DB transaction, so the steps run in an order that stays consistent under partial failure, unwinding prior steps on any error:

1. **Claim stock** (`ClaimStock`) — atomic `UPDATE … WHERE stock < 0 OR stock > 0`; 0 rows affected = sold out. Done first so we never charge for an item that's gone. No-op success for unlimited stock.
2. **Debit points** (`Points.Deduct`) — on failure, **restore the claimed unit**. `ErrInsufficientPoints` is surfaced as a normal outcome (not logged as an error).
3. **Grant the reward** (`grantReward` → `RankGranter.GrantRank`) — on failure, **refund the points and restore the unit**, so the user is made whole.
4. **Record the sale** (`RecordPurchase`) — a failure here is logged (`Report`) but **not** rolled back: the economic transaction already completed, and undoing a granted rank would be worse than a missing audit row.

`grantReward` switches on `reward_type`; adding a reward kind (points bonus, freeleech, invites) is one new `case` here plus its capability `Lookup` in `Provision`.

## Files

- `plugin.go` — `init()` registration, `Metadata` (`Requires: ["ranks"]`, `Migrations: storeMigrations`), the `//go:embed migrations/*.sql`, `Provision` (capability lookup + routes), `Start`/`Stop`.
- `migrations/001_init.sql` — the `store` schema (applied by `core.RunPluginMigrations`; unqualified names → `store.*`).
- `migrations/002_seed_from_profile.sql` — **a no-op that keeps its filename** so already-recorded history resolves. It used to seed the catalog from the operator's `public.user_ranks`/`public.site_settings`; that SQL moved to the host's `deploy/import/store_from_profile.sql`. A plugin's migrations create its own empty schema and nothing else — importing an existing site's data is a separate, deliberate operation (ADOPTION-MIGRATIONS.md).
- `models.go` — `Item` / `Purchase` structs, `RewardType` enum, `Item.InStock()`.
- `store.go` — `Store` interface (list / get / CRUD / claim / restore / record).
- `store_pg.go` — `PGStore`, the sqlx-backed production impl (schema-qualified; atomic stock claim/restore).
- `handlers.go` — `StorePage`, `BuyItem` + `purchase` (the transaction), `grantReward`, admin CRUD, `itemFromForm`/`validItem`.
- `store_test.go` — `memStore` + fakes; validation, `InStock`, reward dispatch, and every path of `purchase`.
- `schema_pg_test.go` (`//go:build integration`) — applies the embedded migrations + round-trips the `PGStore` against real Postgres.

## Testing

- **Unit-tested** (`store_test.go`): `validItem` (name/cost/reward-type/rank-ref gates), `InStock`, `grantReward` dispatch, and the full `purchase` transaction against an in-memory `memStore` — happy path, unlimited stock (not decremented), out-of-stock (no debit), insufficient points (unit restored, nothing granted), grant failure (points refunded net-zero, unit restored), and audit-ledger failure (economic tx still succeeds, no wrongful refund). The `memStore`'s `ClaimStock`/`RestoreStock` mirror the SQL guards in `store_pg.go` so the compensation logic is exercised against the same semantics.
- **Integration-tested** (`schema_pg_test.go`, `//go:build integration`): applies the plugin's embedded migrations into a fresh `store` schema exactly as `core.RunPluginMigrations` does, then round-trips the schema-qualified `PGStore` (create / get / active-only list / claim-until-sold-out / restore / record-purchase / delete) against real Postgres. This also proves the embedded migration SQL is valid and the `store` schema is usable. (It reproduces the one-plugin apply itself rather than calling `RunPluginMigrations`, which needs the whole registry incl. `ranks` — a sibling import the boundary lint forbids in committed tests.)

## Template contract

The plugin ships no templates — it renders the **host's**, by name, with the keys
below merged over whatever `Deps.BaseData` returns. A site adopting this plugin
supplies three templates:

| Template | Keys |
|---|---|
| `store.html` | `Items` (`[]Item`, active only), `Balance` (`int`), `Error` (string, from `?error=`), `Ok` (string, from `?ok=`) |
| `store_history.html` | `Entries` (`[]core.LedgerEntry`), `Total` (`int`), `Balance` (`int`), `Pagination` |
| `admin_store.html` | `Items` (`[]Item`, **incl. inactive**), `Error` (string), `Ok` (**bool** — `?ok=1`) |

Three things that are easy to get wrong:

- **`Ok` is a string on `/store` and a bool on `/admin/store`.** Not an
  oversight worth a breaking change, but a template that does `{{if .Ok}}` on
  the store page is true for *any* non-empty value, including `?ok=0`.
- **Only `/store/history` paginates.** The other two render their full list, so
  they get no `Pagination` key; a template that unconditionally calls the
  pagination partial will fail there.
- **`store_history.html` also renders on the error path with `Error` alone** —
  no `Entries`, `Total`, `Balance`, or `Pagination`. It must tolerate their
  absence. html/template streams, so a field the template reads but the data
  does not carry aborts the render mid-page with a 200 already sent, and the
  reader sees a page that just stops.

`Pagination` is whatever `Deps.Paginate` returned, so its field names are the
host's, not the plugin's.

## Notes / gotchas

- The admin catalog references ranks by **numeric rank id** typed into `reward_ref` (see `/admin/ranks` for ids) rather than a dropdown — the store deliberately does not import the ranks package, and a rank-list capability was not worth adding for a mod-only field.
- CSRF: the buy/admin forms carry an explicit `{{.CSRFToken}}` hidden input *and* are covered by the site-wide form-token injector in `_partials.html`; either alone suffices.
