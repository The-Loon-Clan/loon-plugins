# roadmap plugin

The planning surface: the public `/help/roadmap` + `/help/changelog` pages
(anonymous-friendly even on a closed site, so visitors can see the project
is alive), the `/flow` collaborative node-graph editor with its
propose/vote/promote workflow, mockups gallery and proposal queue, the
admin roadmap/changelog CRUD, and the periodic **Flow Snapshots** job.

Lifted from the ameNZB host's in-repo plugin, markup included, and almost
fully self-contained: the roadmap/changelog tables were already
plugin-private, and the flow domain (models + Postgres store) moved
wholesale because the host had no other caller. The dormant WebSocket hub —
never routed; the host's own comment records the reverse-proxy issues that
retired it — was left behind, which also removed the plugin's only
UserRepository dependency.

## Surface

- Public (`Auth.Optional`): `GET /help/roadmap`, `/help/roadmap/graph.json`,
  `/help/roadmap/graph-node/:id`, `/help/changelog` (301 → roadmap tab).
- Authed (`Authenticate`): `/flow` page + `GET /flow/{data, proposals,
  proposals/similar, proposals/:id/details, mockups, mockup/:id,
  node/:id/comments, node/:id/history}` and writes `POST /flow/node`,
  `PATCH|DELETE /flow/node/:id`, `POST /flow/node/:id/{tag*, status*,
  promote*, propose-change, vote, comment}`, `POST /flow/edge`,
  `DELETE /flow/edge/:id` (* = mod-gated in-handler; owner-or-mod rules on
  edit/delete). Writes are `fetch()` with the `X-CSRF-Token` header read
  from the cookie client-side.
- Admin (`RequireUser(RoleMod)`): `/admin/roadmap` + `/admin/changelog`
  CRUD.
- Job (worker): **Flow Snapshots** — 15-min JSONB graph snapshot, 7-day
  prune. Historical name; interval override carries over.
- `Metadata.Processes`: `["web", "worker"]`.

## Data

- Plugin-owned stores over the host DB handle (`c.Storage.DB()`):
  `PGStore` (roadmap_items, changelog_entries) and `PGFlowStore`
  (flow_nodes/edges/comments/votes/revisions/snapshots). Tables remain in
  the host's migrations; nothing host-side reads them.

## Dependencies

- Core services: `Auth` (three gate chains), `Errors`, `Router`, `Storage`
  (the DB handle); `loon/schedule` directly for the job.
- `SetDeps` seams — chrome and renderers only: `RenderPage`,
  `RenderPagination`, `CSRFToken` (the two admin pages' forms), `Viewer`,
  `RelativeTime`, `SanitizeForum` (the plugin's own goldmark output runs
  through the host's forum allow-list — one copy), `RenderForumMarkdown`
  (comment bodies). The worker leg needs **no host seams at all**.
- Config keys: none. `Metadata.Requires`: none.

## Hooks & Callbacks

- Host hooks set: (none). Extensions PUBLISHED / CONSUMED: (none).

## Lifecycle

- Provision: builds both stores; worker/all constructs the snapshot job;
  web validates Deps, parses templates, registers the three route groups.
- Start: launches the snapshot loop via `schedule.ServiceLoop` off the
  root context. Stop: no-op.

## Files

- `plugin.go` — lifecycle + route table (historical paths kept exactly).
- `deps.go` — the seam contract + FlowStore interface.
- `models.go` — roadmap/changelog types + the flow domain moved from the
  host (json tags are the editor's wire format).
- `store.go` / `store_pg.go` — the roadmap/changelog store.
- `store_flow_pg.go` — the flow store, moved wholesale.
- `pages.go` — public + admin pages. `flow.go` — the editor's REST surface
  and markdown pipeline. `jobs.go` — Flow Snapshots.
- `views.go` — embedded-template harness, chrome injection, exact host
  FuncMap copies. `templates/*.html` — the seven pages as fragments.

## Testing

- Unit-tested: the ported permission/parse helpers (owner-or-mod rules,
  mockup data processing, system-id parsing), all seven pages rendering
  over realistic data, the admin pages' POST-form-vs-CSRF count, and empty
  states.
- Needs integration (live DB): both PG stores — exercised by the host's
  integration suite and the running job.
