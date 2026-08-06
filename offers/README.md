# offers plugin

The offer system. Members register what they can deliver — from a private
tracker or a personal collection — other members file "request this" against a
bucket, and an external agent claims and delivers it.

The offer **tables** stay host-owned, and the reason is stronger than for most
plugins here: the host's own `/api/agent/offer/*` surface writes the same rows.
The host is a co-writer, not merely a store.

## Surface

**External agent API** (no session; Bearer or `?apikey=`, checked in
`resolveAPIUser`). Every path is unchanged from the host version — agents are
deployed in the wild and do not redeploy when we refactor.

| Route | Purpose |
|---|---|
| `POST /api/offers/hash-check` | which of these hashes do you already know |
| `POST /api/offers/register` | upsert buckets + offers for a tracker |
| `POST /api/offers/heartbeat` | re-stamp `last_seen_at` without re-uploading |
| `GET  /api/offers/requests/pending` | what could I fulfil |
| `POST /api/offers/requests/:id/claim` \| `/deliver` \| `/fail` | the claim lifecycle |
| `GET  /api/offers/buckets/:id` | one bucket, for the hash→path cache |
| `GET  /api/offers/notifications/pending` | device notifications |
| `GET  /api/nzbs/by-info-hash` | has my torrent become an NZB yet |

**Member pages** (authenticated): `GET /offers`, `/offers/search`,
`/offers/community` (the last two redirect to `/offers`), and
`POST /offers/request`.

**Admin** (mod or above): `GET /admin/trackers`, `POST /admin/trackers/save`,
`POST /admin/trackers/:id/delete`, `GET /admin/offers`.

`Metadata.Processes`: `["web", "worker", "api"]`, and each does something
different — web serves everything, **api serves only the external API** (it has
no session or template stack), worker owns the two job loops. `Provision`
checks `okAPI()` on the api process and `okWeb()` elsewhere, which is what lets
the api process boot without page seams.

## Data
Owns no tables and runs no migrations. Everything goes through `Deps`.

## Dependencies
- **Core services**: `Router`, `Auth.Authenticate`, `Auth.RequireUser(RoleMod)`,
  `schedule.RegisterJob` / `schedule.ServiceLoop`.
- **Store**: none. `SetDeps` (web/api) and `SetJobDeps` (worker) from the
  composition root before `core.Boot`.
- **Config keys / Metadata.Requires**: (none).

### Two decisions worth more than the rest

**`ComputeOfferHash` crosses as a function and is deliberately not
reimplemented here.** It defines bucket identity, and the host's agent-offer
handler computes it too. Two copies that ever drifted would silently create
duplicate buckets for identical content, with nothing to report it. Contrast
the visibility and status strings, which ARE duplicated here freely: a drift in
those fails loudly and immediately, because a value nothing matches renders
nothing and saves nothing.

**The external API payloads cross as `any`.** Agents already deployed read
those exact JSON shapes. Re-describing them with plugin structs would risk
renaming a field and breaking every agent at once, silently. The plugin
forwards them to the encoder untouched.

Staff-ness and account standing are answered by the HOST (`Viewer.IsMod`,
`Viewer.IsAdmin`, and `ResolveAPIKey` returning nil for a suspended account).
What counts as a mod, or as suspended, is the operator's decision; a plugin
that hard-coded a role ordering would have quietly taken it.

## Hooks & Callbacks
- Host hooks SET: (none).
- Extensions PUBLISHED / CONSUMED: (none).
- Notifications are optional `Deps` entries — the row state is the canonical
  event, so a missing notifier degrades the experience without losing the
  outcome.

## Lifecycle
- **Provision**: builds the job loops on worker-capable processes; registers
  the API routes everywhere; adds the member and admin routes off the api
  process.
- **Start**: launches the sweeper (60s) and pruner (daily). Both take their
  context from `Start`, so SIGTERM unblocks them.
- **Stop**: no-op.

## Files
- `plugin.go` — registration, process gating, the route table.
- `deps.go` — view types, input types, the stored vocabulary, `Deps`/`JobDeps`.
- `handlers.go` — the agent API and the member pages.
- `admin.go` — the tracker catalog and offers oversight.
- `jobs.go` — claim sweeper and stale-offer pruner.
- `views.go` — embedded templates, the three template helpers, chrome.
- `templates/` — `offers`, `admin_offers`, `admin_trackers`.

## Testing
All three fragments are EXECUTED, populated and empty. That matters more here
than in most of these plugins: this is 600 lines of markup nobody in the
package wrote, so running it is the only thing that proves the view types match
what it reads. It has already earned that — it caught `AdminRequest` missing
seven fields the admin table prints, because the row type is 21 fields long and
the first extraction was truncated. It also caught `admin_tracker_form` being a
`{{define}}` nested inside another `{{define}}`, which parses in the host (no
outer wrapper) and panics at init here.

Also covered: the stored vocabulary matches the strings the database holds.

Needs integration (live DB): the queries, which live in the host's repository
and are tested there. The agent API wants an end-to-end test against a real
agent; it has none.
