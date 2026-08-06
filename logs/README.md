# logs plugin

Error-log search and retention. Serves `/admin/logs` — an Elasticsearch-style
search over the host's error-log sink, with a Lucene-lite query DSL,
op/severity facets, a `last_at` histogram, and a live-tail refresh — and owns
the daily **Error Log Cleanup** job.

The sink itself belongs to the host: every internal-error and service-error
call across the site writes to it, and the host reads it on its own admin
pages. This plugin is a **reader** over that data plus the prune loop. It owns
no schema and runs no migrations.

## Surface
- `GET  /admin/logs` — the search UI (host template `admin_logs.html`).
- `GET  /admin/logs/search.json` — same query semantics, JSON; backs live-tail
  auto-refresh and async pagination.
- `POST /admin/logs/:id/archive` — dismiss one row.

All three are gated at **admin**, not mod: error logs can carry internal
detail (paths, SQL fragments).

`Metadata.Processes`: `["web", "worker"]`. The web leg serves the routes; the
worker leg owns the cleanup loop. `Provision` captures the process kind and
`Start` gates on it, so a split-mode web process registers routes but never
races the worker's prune.

## Data
Owns no tables. Reads and prunes the host's error-log sink through `Deps`.

## Dependencies
- **Core services**: `Router`, `Auth.RequireUser(RoleAdmin)`,
  `schedule.RegisterJob` / `schedule.ServiceLoop`.
- **Store**: none. `SetDeps` (web) and `SetJobDeps` (worker) from the
  composition root, before `core.Boot`. Both are validated in `Provision`.
  - Web: `BaseData`, `Pagination`, `JSONOK`, `JSONError`,
    `JSONInternalError`, plus the four reads — `Search`, `Facets`,
    `Histogram`, `Archive`.
  - Worker: `Prune`, `ReportError`.
- **Config keys / Metadata.Requires**: (none). Retention is the
  `ErrorLogRetention` constant (30 days); the tick interval is daily with a
  30-minute boot delay, so the time-critical jobs get the startup window
  first.

`Search`, `Row`, `Facet` and `Bucket` are the plugin's own types. That is the
right way round for the filter in particular: `ParseQuery` is this plugin's
whole reason to exist, so it names the filters and the host translates. `Row`
is flat and snake_case-tagged because the host's record carries db tags only —
marshalling it directly would emit PascalCase keys and break the live-tail JS,
and leave pointers a Go template cannot print.

The host is expected to guard that translation; the reference wiring does it
with a reflection test asserting every `Search` field survives the crossing. A
dropped filter has no symptom — the operator's `-noise` is ignored and the page
just looks empty.

## Hooks & Callbacks
- Host hooks SET: (none).
- Extensions PUBLISHED / CONSUMED: (none).

## Lifecycle
- **Provision**: captures the process kind; builds the cleaner on
  worker-capable processes; on web, validates `Deps` and registers the three
  admin routes.
- **Start**: launches the daily prune loop, if this process has one. The
  context comes from `Start`, so SIGTERM unblocks the loop.
- **Stop**: no-op.

## Files
- `plugin.go` — registration, metadata, `Provision`, process gating.
- `deps.go` — the `Search`/`Row`/`Facet`/`Bucket` types, `Deps`, `JobDeps`.
- `query.go` — the query DSL parser. Pure and injected with `now`, so
  relative windows (`since:24h`) are reproducible in tests.
- `handlers.go` — page, JSON search, archive; histogram bar building.
- `jobs.go` — the Error Log Cleanup loop.

## Testing
Unit-tested: the query DSL in depth — terms, quoted phrases, negation,
`op:`/`severity:`/`path:`/`user:`/`count:`/`since:`/`until:`/`is:`/`sort:`,
prefix ops, unknown-key fallthrough, and the tokenizer. That is where the
logic is; the handlers are assembly over `Deps` and the store calls are the
host's.

Needs integration (live DB): the search SQL itself — filter composition,
faceting and bucketing — which lives in the host's repository implementation
and is tested there.
