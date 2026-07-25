# donations plugin

The donation system: a public donate page (`/help/donate`) with per-group monthly/yearly
funding thermometers, a points-curve preview, a recent-donor leaderboard, tip-jar goal rings,
and "buy a slot" donation packages; a BTCPay Server integration (click-to-claim invoice
creation + HMAC-verified settlement webhook) that records on-chain contributions; and a unified
`/admin/donate` admin console for managing costs, the points curve, the manual donation log,
wallet addresses, BTCPay credentials, tip-jar goals, and packages.

A clean leaf — no other surface reads the donation tables.

## Surface

Routes (all registered in `plugin.go` `Provision`):

- **public / api (no auth)** — `POST /api/btcpay/webhook`: BTCPay settlement ingress. No
  session; authenticated by HMAC-SHA256 over the raw body (`BTCPay-Sig` header). The ameNZB
  host skips CSRF for `/api/*`.
- **authed** (`c.Auth.Authenticate()` group on `/`):
  - `GET  /help/donate` — the public donate page. When `Deps.IsDonateEnabled()` is false,
    non-admins get 404; admins (mod+, via `c.Auth.CurrentUser`) can still preview.
  - `POST /donate/claim-package/:id` — click-to-claim; creates a BTCPay invoice and redirects
    to the hosted checkout.
- **admin** (`c.Auth.RequireUser(core.RoleMod)` group on `/admin/donate`):
  - `GET  ""` unified admin page; legacy `GET /costs`, `/points`, `/log` redirect to it anchored.
  - `POST /costs`, `/costs/:id/del` — site-cost CRUD.
  - `POST /points` — points curve + locking groups + master enable toggle.
  - `POST /log` — record a manual/fiat donation.
  - `POST /wallet` — save BTC/ETH/XMR addresses + BTCPay credentials (secrets mask-preserving).
  - `POST /btcpay-health` — live credential check against the BTCPay store.
  - `POST /tipjar` — save the two tip-jar goal slots.
  - `POST /packages`, `/packages/:id/del` — donation-package CRUD.

Process kinds: `Metadata.Processes` is unset (empty) → **web-only**.

## Data

All tables live in the **public schema**, created by the ameNZB host's numbered migrations
(pre-PG17-consolidation; `Metadata.Migrations` is empty — an adopting host supplies them),
reached via `Core.Storage.DB()`:

- `site_costs` — recurring expense line items driving the goal thermometers (host migration
  **202**; `goal_group` column present from 202).
- `donations` — the contribution ledger, on-chain or manual (host migration **202**).
  `package_id` FK column added in host migration **261** (`ON DELETE SET NULL`).
- `donation_packages` — admin-defined limited-stock "buy a slot" targets (host migration **261**).
- `users` — read + written (not owned): `donation_count`, `donation_total_usd`, `donator`
  columns are bumped inside the `CreateDonation` transaction (SQL is host-schema-aware by
  design; prod is the source of truth).
- `site_settings` — read/written via `Deps.Settings` for wallet addresses, BTCPay credentials,
  points-curve knobs, locking groups, tip-jar slots, and the master toggle.

## Dependencies

- **Core services consumed**: `Storage` (`DB()` → the plugin-private `*PGStore`), `Router`
  (`Engine()` + the auth-gated route groups), `Auth` (`Authenticate()` / `RequireUser(RoleMod)`
  middleware chains + `CurrentUser` for the donor id and the admin-preview check),
  `Errors` (`core.ErrorReporter` — `Report` / `HandlerError`).
- **Store**: self-contained `*PGStore` built at `Provision` from `c.Storage.DB()` — the donation
  tables are plugin-private, so there is no shared storage repository for them. SQL is
  byte-identical to the pre-lift in-tree plugin.
- **`SetDeps` (required, before `core.Boot`)** — the host seams; `Provision` fails loud if any
  field is nil:
  - `BaseData` — page-chrome injector (template envelope).
  - `Settings` — the plugin's own two-method structural interface (`GetSetting`/`SetSetting`);
    the host's settings repository satisfies it directly.
  - `IsDonateEnabled` / `SetDonateEnabled` — the master-toggle pair (read the in-process state;
    flip it now + persist, no restart).
  - `LookupUsername` / `LookupUserID` — narrow user lookups wrapping the host's user
    repository; the plugin never sees the host's user model.
- **Config keys**: none. No `plugins.donations.*` namespace; all runtime config lives in
  `site_settings` via `Deps.Settings`.
- **Metadata.Requires**: none (leaf plugin; depends only on always-available core services).
- **Host templates (the template contract)** — the plugin renders the HOST's templates by name:
  `help_donate.html`, `admin_donate.html`, and `error.html` (for the claim flow's 502/503
  pages). An adopting host supplies those three in its template set.

## Hooks & Callbacks

- **Host hooks SET**: (none) — the plugin owns its own pages and feeds no host-owned card.
- **Extensions PUBLISHED** (`Core.Register`): (none) — it is a leaf; no other plugin reads its
  tables or services.
- **Extensions CONSUMED** (`Core.Lookup`): (none).

## Lifecycle

- **Provision**: validates `SetDeps` was called with every field (fails loud otherwise), builds
  the `*PGStore` from `c.Storage.DB()`, constructs `Handlers` (store + Deps + `core.AuthService`
  + `core.ErrorReporter`), and registers the public/authed/admin route groups on the router.
- **Start / Stop**: no-ops. The plugin runs no background jobs, connections, or goroutines —
  settlement arrives via the inbound webhook, not a poller.

## Files

- `plugin.go` — plugin registration, `Deps`/`SetDeps`, the structural `Settings` interface,
  `Provision` route wiring, `Metadata`.
- `models.go` — `SiteCost`, `Donation`, `DonationPackage`/`DonationPackageView` (+ `Recompute`),
  `DonationGoalGroup` (lock/percent/items helpers, `pct`), `DonationPointsConfig`
  (`PointsForDollars` curve). Pure logic lives here.
- `store.go` — the `Store` interface (per-domain repository contract).
- `store_pg.go` — `*PGStore`, the Postgres-backed `Store` implementation.
- `handlers.go` — public `DonatePage`, tip-jar loading, points-config reading, locking-group set.
- `claim.go` — `ClaimPackage` click-to-claim + BTCPay invoice creation + `absSiteURL`.
- `webhook.go` — `BTCPayWebhook` ingress, `verifyBTCPaySig` HMAC check, invoice-total fetch.
- `admin.go` — the unified admin page + all admin POST handlers (costs, points, log, wallet,
  tip-jar, packages, BTCPay health check).

Gotchas: BTCPay outbound calls (invoice create, invoice fetch, health check) all go through
`loon/httpclient.NewSafeFetch`, so the admin-configured BTCPay base URL cannot be aimed at
internal or cloud-metadata addresses. Duplicate webhook deliveries are absorbed by the
`(asset, txid)` UNIQUE constraint (txid = `btcpay-<invoiceID>`) and acked 200 so BTCPay stops
retrying. The webhook fails closed when `btcpay_webhook_secret` is unset. Package stock is
counted year-to-date, matching the yearly goal boundary, so packages auto-reopen Jan 1.

## Testing

- **Unit-tested** (`donations_test.go`, no DB): the pure logic in `models.go` and the webhook
  signature check — `DonationPointsConfig.PointsForDollars` (per-$10 compounding curve, incl.
  fractional/zero/negative and `mult<=0` guard), `DonationPackageView.Recompute` (stock/percent
  clamping, funded flag), `DonationGoalGroup` monthly/yearly lock + `FullyFunded` + percent
  (via `pct`), `Monthly/YearlyItems` period filtering, and `verifyBTCPaySig` (constant-time
  HMAC verify, bad-prefix / bad-hex / wrong-secret / tamper cases).
- **Needs integration (live DB)**: everything in `store_pg.go` — the `CreateDonation`
  transaction (donation insert + atomic `users` counter bump + Donator-flag flip + `(asset,txid)`
  dedup), site-cost/package CRUD, and the SUM/COUNT aggregation queries. These are thin SQL
  delegation with no in-memory double (there is no `MemStore` for this plugin), so a real
  Postgres is required to exercise them meaningfully. The gin handlers (webhook flow, claim
  flow, admin forms) likewise need an HTTP + DB integration harness.
