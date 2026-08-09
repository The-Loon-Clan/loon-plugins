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
- **admin — mod-editable** (`c.Auth.RequireUser(core.RoleMod)` group on `/admin/donate`): the
  page and everything that shapes the public view but touches no credentials and can't move money.
  - `GET  ""` unified admin page; legacy `GET /costs`, `/points`, `/log` redirect to it anchored.
    Secrets render presence-only, so the page is safe for mods to view.
  - `POST /costs`, `/costs/:id/del` — site-cost CRUD.
  - `POST /points` — points curve + locking groups + master enable toggle.
  - `POST /tipjar` — save the two tip-jar goal slots.
  - `POST /packages`, `/packages/:id/del` — donation-package CRUD.
- **admin-only** (`c.Auth.RequireUser(core.RoleAdmin)` group on `/admin/donate`): the credential
  and money-moving writes a moderator must not reach.
  - `POST /wallet` — save BTC/ETH/XMR addresses + BTCPay credentials (secrets mask-preserving).
  - `POST /btcpay-health` — live credential check against the BTCPay store (sends the stored API
    key to the configured URL; admin-gated so a mod can't repoint the URL and exfiltrate it).
  - `POST /log` — record a manual/fiat donation (credits lifetime totals + the Donator flag).

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
- **`SetDeps` (required, before `core.Boot`)** — the host seams; `Provision` fails loud when a
  data seam is missing or neither render contract is complete:
  - Data seams, required on both contracts:
    - `Settings` — the plugin's own two-method structural interface (`GetSetting`/`SetSetting`);
      the host's settings repository satisfies it directly.
    - `IsDonateEnabled` / `SetDonateEnabled` — the master-toggle pair (read the in-process state;
      flip it now + persist, no restart).
    - `LookupUsername` / `LookupUserID` — narrow user lookups wrapping the host's user
      repository; the plugin never sees the host's user model.
  - Current render contract (the plugin renders its own embedded fragments):
    - `RenderPage` — wraps a finished fragment in the site chrome.
    - `RenderError` — shows the host's error page (see the error.html note below).
    - `CSRFToken` — minted by host middleware; feeds the pages' inline token inputs.
    - `RelativeTime` — the site's time wording.
  - Previous render contract, kept working while `loon-demo-site` still wires it (remove once
    the demo moves to `RenderPage`):
    - `BaseData` — page-chrome injector; the plugin then renders the host's *own* copies of
      `help_donate.html` / `admin_donate.html` / `error.html` by name.
- **Config keys**: none. No `plugins.donations.*` namespace; all runtime config lives in
  `site_settings` via `Deps.Settings`.
- **Metadata.Requires**: none (leaf plugin; depends only on always-available core services).
- **Template ownership** — the plugin owns `help_donate.html` and `admin_donate.html`
  (`templates/`, embedded fragments, struct view models). `error.html` deliberately did NOT
  move: it is the host's site-wide error surface (the global 404, the download-limit page, the
  page behind other plugins' `RenderError` seams) with a dozen host render sites against two
  here — moving a copy in would fork the page every visitor sees when anything breaks. The
  claim flow reaches it through `Deps.RenderError` instead, the same arrangement offers/lists
  use, with a title parameter added because the BTCPay-unconfigured 503 carries custom copy.
  The lift also fixed a latent streamed-render abort: the public page's Recent Donors carousel
  read `$d.CreatedAt`, a field `Donation` lost in a rename — it reads `ReceivedAt` now, and a
  render test pins it.

## Hooks & Callbacks

- **Host hooks SET**: (none) — the plugin owns its own pages and feeds no host-owned card.
- **Extensions PUBLISHED** (`Core.Register`): (none) — it is a leaf; no other plugin reads its
  tables or services.
- **Extensions CONSUMED** (`Core.Lookup`): (none).

## Lifecycle

- **Provision**: validates the data seams and a complete render contract (fails loud
  otherwise), parses the embedded fragments on the current contract (a parse failure fails
  boot, not the first page view), builds the `*PGStore` from `c.Storage.DB()`, constructs
  `Handlers` (store + Deps + `core.AuthService` + `core.ErrorReporter`), and registers the
  public/authed/admin route groups on the router.
- **Start / Stop**: no-ops. The plugin runs no background jobs, connections, or goroutines —
  settlement arrives via the inbound webhook, not a poller.

## Files

- `plugin.go` — plugin registration, `Deps`/`SetDeps` (+ `renderContractOK`), the structural
  `Settings` interface, `Provision` route wiring, `Metadata`.
- `views.go` — embedded fragment set, FuncMap, struct view models (`donatePageVM`,
  `adminDonateVM`), the dual-contract `render`, and `renderError`.
- `templates/*.html` — the two page fragments, lifted verbatim from the origin host (chrome
  stripped; the Recent Donors field corrected as noted above).
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
internal or cloud-metadata addresses. Webhook settlement is idempotent by the stable txid
(`btcpay-<invoiceID>`): a pre-check dedups redeliveries independent of the invoice's asset
label, a settled row is acked 200 so BTCPay stops retrying, and a genuine record failure
returns 5xx so BTCPay retries rather than silently dropping the donation. Invoice metadata is
read tolerantly (numbers or strings) because the click-to-claim flow writes numeric ids while
the manual/operator flow writes strings, and BTCPay echoes either verbatim. The webhook fails
closed when `btcpay_webhook_secret` is unset. Package stock is counted year-to-date, matching
the yearly goal boundary, so packages auto-reopen Jan 1.

## Testing

- **Unit-tested** (`donations_test.go`, no DB): the pure logic in `models.go` and the webhook
  signature check — `DonationPointsConfig.PointsForDollars` (per-$10 compounding curve, incl.
  fractional/zero/negative and `mult<=0` guard), `DonationPackageView.Recompute` (stock/percent
  clamping, funded flag), `DonationGoalGroup` monthly/yearly lock + `FullyFunded` + percent
  (via `pct`), `Monthly/YearlyItems` period filtering, and `verifyBTCPaySig` (constant-time
  HMAC verify, bad-prefix / bad-hex / wrong-secret / tamper cases).
- **Render-tested** (`views_test.go`): both fragments execute over realistic fixture data with
  content markers and a chrome-leak check; the Recent Donors timestamp regression is pinned
  (the `ReceivedAt` fix, plus content *after* the carousel proving no mid-stream abort); the
  POST-form inventory and the two inline CSRF tokens are counted (submit-time CSRF for the
  rest is the host chrome's csrf-js, documented in the fragment); the admin page's
  tab-structure invariants moved here from the host's `admin_donate_tabs_test.go` with the
  markup they pin (every tab has a pane and vice versa, exactly one active pane, hash-to-tab
  handling, house chrome, guarded tab counts); the fragment set parses in one pass; and both
  render contracts are checked (`renderContractOK`, `renderError` seam routing).
- **Needs integration (live DB)**: everything in `store_pg.go` — the `CreateDonation`
  transaction (donation insert + atomic `users` counter bump + Donator-flag flip + `(asset,txid)`
  dedup), site-cost/package CRUD, and the SUM/COUNT aggregation queries. These are thin SQL
  delegation with no in-memory double (there is no `MemStore` for this plugin), so a real
  Postgres is required to exercise them meaningfully. The gin handlers (webhook flow, claim
  flow, admin forms) likewise need an HTTP + DB integration harness.
