# reports plugin

The member-report triage queue. Members flag a release as broken, mislabeled,
or malware from the release page; this is where staff work through what they
said.

The reports **table** stays host-owned, and must: the release page and the API
write to it whenever a member files a report, and the host's daily digest reads
it to raise findings. This plugin owns the surface over it — which was the half
nobody had looked at. When it moved there were 28 open reports, the oldest 98
days old, and not one had ever been resolved.

## Surface
- `GET  /admin/p/reports` — the queue. A loon `SlotAdminPage` view; the markup
  is this plugin's (`templates/reports.html`, embedded). `?resolved=1` switches
  to the audit trail.
- `POST /admin/p/reports/resolve` — clear one report. The id travels in the
  form body, not the path: the host mounts actions as
  `POST /admin/p/<slug>/<name>`, so an action name containing `:id` would
  register a gin path parameter, and a wildcard conflict anywhere under
  `/admin/p` is a panic at boot.
- `GET  /admin/reports` — permanent redirect to the above. That was the queue's
  address for its whole life; the admin hub and the daily digest both point at
  it.

`Metadata.Processes`: `["web"]`. Gated at admin by the host's `/admin/p` group;
`MinRole` on the view is belt-and-braces.

## Data
Owns no tables and runs no migrations. Reads and resolves through `Deps`.

## Dependencies
- **Core services**: `RegisterView`, `Router`, `Auth.RequireUser(RoleAdmin)`.
- **Store**: none. `SetDeps(Deps{...})` from the composition root before
  `core.Boot`; `Provision` rejects a partial wiring.
  - `List(ctx, resolved, limit, offset)` — a page of reports plus the unpaged
    total.
  - `Resolve(ctx, reportID, adminID)` — mark one handled.
  - `ActingAdmin(c)` — who is clearing it. Answered by the host from the
    session, never from the request body: the value of recording who resolved
    something is precisely that the acting admin cannot choose what it says.
  - `RenderPagination(...)` — the site's pager as ready HTML. A slot fragment
    is rendered by this plugin's own template set and cannot reach the host's
    partials, so the host renders its own rather than a second copy living
    here.
- **Config keys / Metadata.Requires**: (none). Page size is fixed at 50, which
  is what the host page used — an operator's sense of how deep the queue is
  should not shift underneath them.

`Report` is the plugin's own view type. `Reason` is a free string rather than a
typed enum: the plugin colours the three it knows (malware, broken,
mislabeled) and renders anything else as itself. The host page relabelled every
unrecognised reason as "Mislabeled", which on a site with different reasons
would misreport what a member actually said.

## Hooks & Callbacks
- Host hooks SET: (none).
- Extensions PUBLISHED / CONSUMED: (none).

## Lifecycle
- **Provision**: validates `Deps`, registers the view and its resolve action,
  and mounts the legacy redirect. No-op outside the `web` process.
- **Start / Stop**: no-ops — no background work.

## Files
- `plugin.go` — registration, the view, render and resolve.
- `deps.go` — the `Report` view type and `Deps`.
- `templates/reports.html` — the queue, embedded.

## Testing
The fragment is EXECUTED against representative data rather than only
compiled: html/template streams, so a field the markup reads and the view model
lacks aborts the render part way through and returns half a page with nothing
logged. Covered: all three known reasons render with their colours, an unknown
reason renders as itself rather than being relabelled, the resolved tab hides
the Resolve control, the empty state still draws, and resolving returns the
operator to the tab and page they were on — bouncing them to page one of the
other list after every action is how a 28-deep queue becomes unworkable.

Needs integration (live DB): the queries themselves, which live in the host's
repository and are tested there.

## Not here
`content_reports` is a second, parallel report system on the host — a generic
target_type/target_id table feeding an auto-hide threshold. In practice every
row in it is `target_type='nzb'`, so the two tables are reporting the same
thing by different routes. Consolidating them is a design decision, not a lift,
and has not been made.
