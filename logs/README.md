# logs plugin

Error-log search and retention. Serves `/admin/p/logs` — an Elasticsearch-style
search over the host's error-log sink, with a Lucene-lite query DSL,
op/severity facets, a `last_at` histogram, and a live-tail refresh — and owns
the daily **Error Log Cleanup** job.

The sink itself belongs to the host: every internal-error and service-error
call across the site writes to it, and the host reads it on its own admin
pages. This plugin is a **reader** over that data plus the prune loop. It owns
no schema and runs no migrations.

## Surface
- `GET  /admin/p/logs` — the search UI. A loon `SlotAdminPage` view; the
  markup is this plugin's (`templates/logs.html`, embedded) and the host
  supplies only the chrome.
- `GET  /admin/logs` — permanent redirect to the above, query preserved. That
  was the page's address for its whole life and operators have it bookmarked.
- `GET  /admin/logs/search.json` — same query semantics, JSON; backs live-tail
  auto-refresh and async pagination.
- `POST /admin/logs/:id/archive` — dismiss one row.

The two JSON endpoints deliberately did **not** move under `/admin/p`: they
are not pages, and the live-tail client that calls them ships inside this
plugin's own fragment, so one URL changing is enough.

All of it is gated at **admin**, not mod: error logs can carry internal
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
  - Web: `RenderPagination`, `JSONOK`, `JSONError`, `JSONInternalError`,
    plus the four reads — `Search`, `Facets`, `Histogram`, `Archive`.
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
  worker-capable processes; on web, validates `Deps`, registers the page view
  and the JSON routes.
- **Start**: launches the daily prune loop, if this process has one. The
  context comes from `Start`, so SIGTERM unblocks the loop.
- **Stop**: no-op.

## Files
- `plugin.go` — registration, metadata, `Provision`, process gating.
- `deps.go` — the `Search`/`Row`/`Facet`/`Bucket` types, `Deps`, `JobDeps`.
- `query.go` — the query DSL parser. Pure and injected with `now`, so
  relative windows (`since:24h`) are reproducible in tests.
- `views.go` — the page view: embeds the fragment and renders it.
- `templates/logs.html` — the whole page body, CSS and live-tail client
  included. Embedded, so a missing template is a build error here rather than
  a 500 at runtime in the host.
- `handlers.go` — JSON search, archive, the shared query/run path, histogram
  bar building.
- `jobs.go` — the Error Log Cleanup loop.

## Testing
Unit-tested: the query DSL in depth — terms, quoted phrases, negation,
`op:`/`severity:`/`path:`/`user:`/`count:`/`since:`/`until:`/`is:`/`sort:`,
prefix ops, unknown-key fallthrough, and the tokenizer.

Also: the fragment is EXECUTED against representative data, with two rows.
html/template streams, so a field the markup reads and the data does not carry
aborts the render part way through — the page returns 200 carrying its own top
half and nothing says why. Compiling the plugin proves nothing about that. The
test asserts the fragment reaches its closing tag, that the second row is
present, and that no host chrome (`<html>`, the navbar) leaked into what is
supposed to be body content.

Needs integration (live DB): the search SQL itself — filter composition,
faceting and bucketing — which lives in the host's repository implementation
and is tested there.

The host is also expected to resolve `RenderPagination` at BOOT and fail hard
if the partial is missing; a pager that silently renders nothing is
indistinguishable from a single page of results.
