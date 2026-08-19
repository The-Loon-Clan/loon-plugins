# downloads plugin

The loop closed the other way: a member's **download client telling the site
what happened**.

An indexer publishes an NZB and never learns whether it worked. The health
sweep STATs articles on a schedule — it catches expiry eventually, and it
cannot see a bad PAR2 set, a truncated post, or an unpack that dies on a
corrupt volume. The member's downloader knows within minutes and has had
nowhere to say so.

This ships the endpoint **and the script**, because the request it answers was
specifically for a prebuilt one:

> "Does anyone of you have a call back script for SABnzbd with the indexer?
> if there is a prebuilt one, it will be very helpful."

## A report is a signal, not a verdict

Nothing here marks a release broken. A failure flags the row for the health
sweep through `pluginapi.ReleaseRecheckRequester`, and **the sweep decides from
the articles themselves**.

That is the load-bearing decision in the whole plugin. "It failed for me" has a
dozen causes on the member's side — thin retention at their provider, a full
disk, an unpack password, the wrong file — and exactly one of them is the
release being bad. Writing a verdict from a report would let one broken seedbox
condemn a healthy release, and nothing downstream could tell that apart from a
real failure.

## Surface

| Route | Who | What |
|---|---|---|
| `POST /api/downloads/report` | **API key** (no session) | One job's outcome. Form-encoded or JSON. |
| `GET /p/downloads/script` | member | `report.py` with this site's URL and the member's key substituted in. |
| `GET /p/downloads` | member | The setup page: three steps and the download link. |
| `GET /admin/p/downloads` | `RoleMod` | What clients have reported, failures first. |

### The report

Takes `apikey` (or `Authorization: Bearer`), `status`, and whichever of `id`,
`url`, `name`, `filename` the client knows, plus optional `detail` and
`client`. Answers JSON with a `message` written to be read by a person — that
message is the only feedback a member ever sees, because it lands in their
client's history log.

`status` is parsed leniently and **anything unrecognised is a failure**. Clients
have one word for "fine" and a dozen for the ways a job can end badly, and the
two mistakes do not cost the same: a wrong `failed` asks for one re-check
nobody needed, a wrong `ok` silently discards the report the feature exists for.

## Matching a job to a release

The hard part, and worth stating plainly: **a download client reports on a JOB**.
A job has a name, a category and a directory. It does not have a release id,
because the id is ours and the client never saw it. Three routes back, in
falling order of trust:

1. **The script sent an id.** Certain.
2. **The script sent the URL it fetched from**, and our download links carry
   `?id=` (`/api?t=get&id=N`) or the id as a path segment (`/nzb/N`). Also
   certain — we are reading our own link back.
3. **Nothing but a name.** Folded to letters and digits (so a client's renaming
   does not defeat it) and matched **against what this member recently
   grabbed** — never against the index at large. That scoping is what makes
   matching by name defensible: the candidate set is a handful of rows they
   chose themselves.

Route 3 can still miss, and a miss is answered honestly as unmatched rather
than guessed at. A wrong match attaches one member's failure to somebody else's
release.

## Dependencies

Looked up in `Start` (every `Provision` runs first, so an earlier lookup finds
nothing on a correctly wired host). Each absence is logged loudly, because a
capability that quietly is not there is indistinguishable from a bug and the
member-visible symptom of both is "my reports do nothing".

| Extension | Required? | Without it |
|---|---|---|
| `auth.apikey` (`pluginapi.APIKeyResolver`) | **yes** | the endpoint refuses every report — one that cannot validate a key accepts every key, and this one can ask for real NNTP work |
| `usenet.recheck` (`pluginapi.ReleaseRecheckRequester`) | no | failures are recorded but queue no health check |
| `usenet.grabs` (`pluginapi.DownloadGrabLookup`) | no | only reports carrying an id or a URL can be matched |

The host owns the recheck **rate limit** for the same reason it owns the key: a
member's client can retry a failed job in a loop, and a re-check costs real
NNTP work. `accepted=false` inside the cooldown is an ordinary answer, not an
error, and the member is told so rather than ignored.

## Data

One table, `download_reports`, keyed `(user_id, release_id)` — **one row per
member per release, not one per report**. A post-processing script runs on
every retry, and a member who retried four times has said one thing four times.
The upsert takes the newest status and counts up `reports`, so a member who
failed three times and succeeded on the fourth ends as `ok` with `reports = 4`:
it worked, but it took four goes.

That makes the read that matters — how many *distinct* members hit this — a
plain count rather than a count of distinct user ids over a growing log. The
cost is that one member's changing opinion is not kept as history, which is the
right trade: the useful question is what they think now.

## The script

`scripts/report.py`, one file for **both** SABnzbd and NZBGet, served
pre-filled from `/p/downloads/script`. Generated rather than shipped with
instructions to edit it, because "download this, then open it in a text editor
and paste two values" is where most people stop. The placeholders keep it valid
Python either way, so a copy taken from this repo runs and explains itself.

It reads SABnzbd's `SAB_*` environment (falling back to the positional
arguments for older builds) or NZBGet's `NZBPP_*`, and **never fails the
download**: every error is caught and printed, and it exits successfully
whatever happens. A post-processing script that can mark a good job as failed is
worse than no script.

## Files

- `plugin.go` — metadata, provisioning, the route table, capability resolution
- `report.go` — the endpoint, and the status vocabulary every client is folded into
- `resolve.go` — job → release, and why each route is trusted as much as it is
- `store.go` / `store_pg.go` / `migrations/` — the one table
- `views.go` + `templates/` — the member setup page and the staff view
- `scripts/report.py` — the thing members were asking for
