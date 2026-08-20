# Plugin grades — 2026-08-16 snapshot

Every plugin graded against [CHECKLIST.md](CHECKLIST.md), the day the list
landed. A snapshot, not a scoreboard to keep green by editing this file: regrade
after real work and replace it wholesale.

**Method.** Six parallel reviewers, one batch of plugins each, grading all 14
sections from the code with file:line evidence for FAILs; plus mechanical
sweeps run once across the whole tree (coverage, README presence, caption and
Bootstrap-vocabulary greps, CDN references, `SetRunning`/`SetIdle` pairing).
Where a reviewer claim was checkable against the running demo it was checked —
two were corrected that way (irc's "running" state is daemon-correct, and the
news/wiki token counts belonged to host chrome, which made the finding
*sharper*: every plugin-owned form on those pages lacks a token). Reviewer
grades are one pass of judgment, not gospel; the CONFIRMED section below is
the part verified beyond the reviewer's word.

**Legend.** ✓ pass · ~ partial · ✗ fail · — not applicable.
Columns: 1 contract · 2 data · 3 security · 4 events · 5 jobs · 6 self-audit ·
7 admin · 8 ui · 9 widgets · 10 i18n-ready · 11 machine · 12 observability ·
13 docs · 14 tests.

| plugin | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| agent | ✓ | — | ✓ | — | — | ~ | ✓ | ✗ | ✓ | ~ | — | — | ✓ | ✓ |
| anidbscraper | ✓ | — | ~ | ✗ | ~ | ✗ | — | — | — | ✓ | — | ✓ | ✗ | ✗ |
| backup | ✓ | ✓ | ✓ | ~ | ✓ | ✓ | ✓ | ~ | — | ~ | ✓ | ✓ | ✓ | ✓ |
| backups | ✓ | — | ✓ | ~ | ~ | ~ | ~ | — | — | — | — | ~ | ✗ | ✓ |
| catalog | ✓ | ✓ | ✓ | ~ | — | — | ~ | — | — | ~ | — | — | ✗ | ~ |
| chat | ✓ | — | ✓ | ✓ | — | ✓ | — | ✗ | ~ | ~ | — | ~ | ✓ | ~ |
| communities | ✓ | ✗ | ~ | ✗ | — | ✓ | ✗ | ✗ | ~ | ~ | — | ~ | ✓ | ~ |
| curation | ✓ | — | ✓ | ~ | ✓ | ✓ | ✓ | — | — | ~ | — | ✓ | ✓ | ✓ |
| dailyreward | ✓ | ✓ | ~ | ~ | — | ~ | ✗ | ✗ | ~ | ~ | — | ✓ | ✗ | ~ |
| dbmaint | ✓ | — | ~ | — | ~ | ✓ | ~ | — | — | — | — | ✓ | ~ | ✓ |
| discord | ✓ | — | ~ | ~ | ✓ | ✓ | ✓ | ✗ | ✓ | ~ | — | ✓ | ✓ | ~ |
| donations | ✓ | ✗ | ~ | ✓ | — | ~ | ✗ | ✗ | ~ | ~ | ✓ | ~ | ✓ | ~ |
| economy | ✓ | — | ✓ | ~ | ~ | ✓ | — | — | — | ~ | — | ~ | ~ | ✓ |
| events | ✓ | ✓ | ✓ | ✓ | ~ | ✓ | ✓ | — | — | ~ | — | ✓ | ✓ | ✓ |
| feeds | ✓ | — | ~ | ~ | ~ | ✓ | ~ | — | — | ~ | — | ✓ | ✓ | ✓ |
| forum | ✓ | ~ | ~ | ✓ | — | ✓ | ✗ | ✗ | ~ | ~ | — | ~ | ✓ | ~ |
| hitrun | ✓ | ~ | ~ | ✗ | ✓ | ~ | ✗ | ~ | ✗ | ~ | — | ~ | ✗ | ~ |
| irc | ✓ | — | ✓ | ✗ | ✓ | ✓ | ✗ | ✗ | ✓ | ~ | — | ✓ | ~ | ~ |
| lists | ✓ | — | ~ | ✗ | — | — | — | ✗ | ~ | ~ | — | ~ | ~ | ✓ |
| logs | ✓ | — | ✓ | — | ~ | ✓ | ✓ | ~ | — | ~ | — | ✓ | ✓ | ✓ |
| messages | ✓ | ✗ | ~ | ✓ | — | ✓ | ✗ | ✗ | ~ | ✓ | — | ~ | ~ | ~ |
| news | ✓ | ~ | ✗ | ✗ | — | ✓ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ~ |
| offers | ✓ | — | ✓ | ✓ | ~ | ~ | ✗ | ✗ | ~ | ~ | ✓ | ✓ | ✓ | ~ |
| perks | ✓ | ~ | ~ | ✗ | ~ | ✓ | ✗ | ~ | ✓ | ~ | — | ~ | ✗ | ~ |
| playlists | ✓ | ✓ | ~ | ✓ | — | ~ | ✗ | ~ | ~ | ~ | — | ✗ | ✗ | ~ |
| pointstore | ✓ | ~ | ~ | ✗ | — | ~ | ✗ | ✗ | ✓ | ~ | — | ~ | ✗ | ~ |
| ranks | ✓ | ✓ | ~ | ~ | ✓ | ✓ | ~ | ~ | ✓ | ~ | — | ✓ | ~ | ✓ |
| releasegroups | ✓ | — | ✓ | ✗ | ✓ | ✓ | ~ | ✗ | ~ | ~ | — | ~ | ✓ | ✓ |
| reports | ✓ | — | ~ | ~ | — | ~ | ✓ | ✗ | — | ~ | — | ✓ | ✓ | ✓ |
| requests | ✓ | — | ✓ | ✗ | ~ | ~ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ✓ |
| rewards | ✓ | ✓ | ~ | ~ | ~ | ✓ | ✓ | ✗ | ✓ | ~ | ~ | ✓ | ✓ | ✓ |
| roadmap | ~ | ✗ | ~ | ✗ | ✓ | ✓ | ✗ | ✗ | ~ | ~ | ~ | ~ | ~ | ✓ |
| scraper | ✓ | — | ✗ | ~ | ✓ | ~ | — | — | — | ✓ | — | ✓ | ✗ | ~ |
| seedlock | ✓ | ~ | ~ | ✗ | — | ~ | ✗ | ~ | ✓ | ~ | — | ~ | ✗ | ~ |
| stats | ✓ | — | ✓ | — | ✓ | ~ | — | ✗ | ✓ | ~ | — | ~ | ✗ | ✓ |
| store | ✓ | ✓ | ~ | ✓ | — | ~ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ✓ |
| tickets | ~ | ~ | ✗ | ✓ | — | ~ | ✗ | ✗ | ~ | ~ | — | ~ | ✗ | ~ |
| tracker | ✓ | ✓ | ✓ | ✗ | ~ | ✓ | ✓ | ✓ | ✓ | ~ | ✓ | ~ | ✓ | ✓ |
| uploads | ✓ | — | ✓ | — | — | ✓ | — | ✗ | ~ | ~ | — | ✓ | ~ | ✓ |
| usenet | ✓ | ~ | ✓ | ~ | ✓ | ✓ | ✓ | — | — | ~ | ✓ | ✓ | ✓ | ✓ |
| wiki | ✓ | ~ | ✗ | ✓ | — | ✓ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ~ |

*Post-snapshot: the achievements half of rewards was extracted into its own
`achievements` plugin (2026-08-16). Neither row is regraded here — this file
is a snapshot; regrade after real work and replace it wholesale.*

## Confirmed defects — verified beyond the reviewer's word

Most severe first. These are bugs, not style.

> **Status, same day (a42b37d + host a49239e):** 1–6, 8 (bootstrap tags — 17
> found, not 11) and 10 are FIXED and verified live; the crawler now counts
> tokens per form so class 1 cannot ship silently again. Still open: 7
> (anidbscraper — finish or retire is an owner decision), 9 (scraper's
> disclosure/SSRF rework), and roadmap's cytoscape, which needs local
> bundling rather than deletion.

1. **news + wiki: every plugin-owned admin POST form lacks a CSRF token and
   403s.** Verified against the running site, form by form: news create and
   all four deletes, wiki topic create and both deletes — the only tokens on
   those pages belong to host chrome (theme, logout). Neither plugin wires a
   CSRFToken seam at all, so those admin features **cannot work today**. The
   access audit could not see it because it probes with a valid token by
   design; the checklist's §3 verify note now records the blind spot.
2. **pointstore loses a member's points on failure** — `Points.Deduct`
   succeeds, then `SetFlair` can fail, and nothing refunds
   (views.go:83-93). Compare store/handlers.go:186, which unwinds.
3. **tracker's cheat sweep hides its failures** — a deferred `SetIdle`
   (cheat_job.go:39) runs after any `SetError` and erases it, so a failed
   sweep displays as a clean idle run. Inverse of the promotion-sweep bug;
   now a checklist §5 item.
4. **dbmaint's verify job can hang the display** — the verifyAborted path
   after `SetRunning` reaches neither `SetIdle` nor `SetError`
   (jobs.go:338-340): the named critical bug, live in one more place.
5. **hitrun's "At risk" widget is permanently zero** — it renders
   `Standing.AtRisk` (widgets.go:48) which `PGStore.Standing`
   (store.go:252-276) never populates.
6. **lists declares `lists.created` and never emits it**, and discards the
   store error from Create (handlers.go:33) — a declared event that has never
   fired, and a write whose failure is invisible.
7. **anidbscraper is a stub shipped as a working job** — `fetchMetadata`
   errors on every id and the run logs completion anyway (plugin.go:249-251).
   Zero tests, no README to say so.
8. **Eleven CDN `<script>` tags across seven plugins** (lists ×4, messages,
   store ×2, offers ×3, roadmap's cytoscape, tickets ×2, reports) — all dead
   under the host's `script-src 'self'`, so every feature behind them is
   broken for members, and each is an undisclosed external call besides.
9. **scraper's seven external APIs are undisclosed and un-proxied** — bare
   `http.Client` instead of the host's SSRF-safe client, API key in the URL
   (sources/tmdb/tmdb.go:107), and no README to disclose any of it.
10. **dailyreward emits its reward event before `Points.Award` can fail**
    (views.go:127-134) — the event can announce points that were never paid.

## Systemic gaps, ranked by how many plugins fail

| # | Gap | Failing | The fix pattern |
|---|---|---|---|
| 1 | **UI speaks Bootstrap, not the host** — 23 ✗ + 6 ~; ~75 template files; 38 uncaptioned tables | nearly everyone with templates | tracker's three templates are the worked example: host vocabulary, captions, no repeated titles. Only tracker passes clean. |
| 2 | **Admin surfaces invisible to the hub** — 18 ✗ | forum, communities, messages, news, wiki, playlists, tickets, store, donations, offers, perks, hitrun, seedlock, dailyreward, pointstore, requests, irc, roadmap | register a `SlotAdminPage`/`SlotAdminSettings` view instead of hand-mounting `/admin/*` routes; the hub ranges over views. |
| 3 | **Nothing to build on: no events** — 13 ✗ + 11 ~ | tracker, perks, hitrun, seedlock, pointstore, communities, news, releasegroups, requests, lists, irc, roadmap, anidbscraper | `DeclareEvent` the notable facts with honest flags; the ecosystem's achievements/stats can only see what is announced. |
| 4 | **No README** — 12 ✗ | backups, stats, tickets*, catalog, scraper, anidbscraper, playlists, perks, hitrun, seedlock, dailyreward, pointstore (*tickets has one that contradicts the code, which is worse) | tracker/README.md is the exemplar; §13 lists the sections. |
| 5 | **i18n-readiness** — 0 ✓ passes among plugins with member strings | everyone | strings into templates, host helpers for dates/bytes; §10 is written so the seam lands as a wrap, not a rewrite. |
| 6 | **Zero-test plugins** — 1 ✗ | anidbscraper (then chat 0.8%, scraper 3%, releasegroups 5.8%, pointstore 6.4%) | §14; the template-execution test is the highest-value first test for any of them. **20 Aug:** backups, playlists and stats were paid off along with cosmetics, polls and applications; anidbscraper is left deliberately, being a stub whose bodies are not extracted yet. |
| 7 | **Job hygiene** — repeated interval literals, pause skipped on manual triggers, `SetIdle(time.Time{})` | backups, offers, requests, economy, feeds, logs, rewards, events, stats | one interval const; check `IsPaused` on the trigger path; idle with a real horizon. |

Unadopted contracts, for whoever wants a first-mover slot: `pluginapi.Proposer`
(0 implementers; dbmaint, curation, rewards, reports and roadmap were each
named a natural fit) and `Backupable` packs (only backup/backups themselves).
29 of 41 plugins declare no events at all.

## Where each plugin should start

The three highest-value fixes per plugin, from the graders, deduplicated
against the systemic table (a plugin listed under a systemic gap does not
repeat it here unless it is the worst instance).

- **agent** — restyle the two cards; honest empty state for a zero-agent owner.
- **anidbscraper** — finish or retire the extraction: the fetch is a stub; then tests + README. Left out of the 20 Aug test sweep on purpose: a test over a placeholder pins the placeholder.
- **backup** — declare sealed-generation/ack events; captions.
- **backups** — README; log succeeded vs failed entries. *(Tests done 20 Aug: every path out of `run` ends the job, and one failing hook does not cost the rest of the archive.)*
- **catalog** — README; execute settings.html in a test; describe the capability.
- **chat** — move the SSE client to a host-served asset (inline script is CSP-dead); handler tests.
- **communities** — declare events (join/thread/reply); drop host-table FKs from schema.sql; admin oversight page.
- **curation** — caption the worklist table; candidate first Proposer.
- **dailyreward** — retire the CSP-dead inline script; emit after the award; ladder into settings.
- **dbmaint** — fix jobs.go:338; allowlist configured table names like backup's pgIdentifier.
- **discord** — execute both inline templates in tests; longer verify token, check rand error.
- **donations** — own schema + migrations instead of host tables; SlotAdminPage; member-deletion note.
- **economy** — core.Scheduler not schedule.RegisterJob; fix stale two-jobs README.
- **events** — SetError on generator failure; real SetIdle horizon.
- **feeds** — pause check on manual trigger; nekoBT key into the settings store; emit on auto-created requests.
- **forum** — SlotAdminPage for categories; PG integration tags for the LATERAL rollups.
- **hitrun** — fix the AtRisk widget; admin page with a ClearWarning path; README.
- **irc** — SlotAdminSettings for the twelve irc_* keys; declare nick-link events.
- **lists** — emit lists.created and stop discarding Create's error; CSRF fields via seam.
- **logs** — pause check on manual trigger; drop the duplicated heading.
- **messages** — delete the CDN script; SlotAdminPage for the broadcast composer; fix README's no-MemStore claim.
- **news** — wire the CSRF seam (forms 403 today); declare publish events; adopt host RelativeTime.
- **offers** — drop three CDN scripts; SlotAdminPage ×2; pause + one interval const.
- **perks** — README (the tracker cross-schema read is undisclosed); admin oversight for tokens; grant/spend events.
- **playlists** — README, stop swallowing lookup errors; oversight page. *(Tests done 20 Aug — and they found that `owned()` treated an anonymous viewer id of 0 as an owner id; hardened.)*
- **pointstore** — refund on SetFlair failure (points are lost today); host vocabulary; operator-editable catalog.
- **ranks** — refresh the stale README (SetJobDeps/mirror both gone); declare promote/derank events; NavHint.
- **releasegroups** — recut five templates; declare claim/news/follow events; followed-groups widget.
- **reports** — CSRF token on the resolve form; drop the CDN script; report-resolved event.
- **requests** — declare created/fulfilled/boosted events; admin oversight page; fix stale README lifecycle.
- **rewards** — maintain() SetError + real horizon; declare grant/claim events; host vocabulary.
- **roadmap** — bundle cytoscape locally; move eight tables into plugin migrations; SlotAdminPage CRUD.
- **scraper** — README disclosing all seven services; host SSRF-safe client; keys out of URLs. *(theporndb tested 20 Aug: the JAV routing regex, the cover-preference ladder, and the mapping.)*
- **seedlock** — reasoned sqllint:allow on views.go:143; admin view of held claims; README.
- **stats** — README; read the snapshot back from shared cache on web processes. *(Tests done 20 Aug: the job-never-left-running rule, and that a cache failure costs the persisted copy but not the page.)*
- **store** — drop two CDN scripts; SlotAdminPage; fix README's "ships no templates" claim.
- **tickets** — rewrite the README (Surface contradicts the code); CSRF on every form; SlotAdminPage; stop swallowing update/delete errors.
- **tracker** — fix the deferred-SetIdle clobber; declare snatch/cheat events; MetricSource for up/down/snatches.
- **uploads** — fix the README RequireUser contradiction; caption + vocabulary.
- **usenet** — describe the six plain-Registered capabilities; dismissed-marker for deleted curated newsgroups.
- **wiki** — wire the CSRF seam (forms 403 today); SlotAdminPage; rewrite README's template/extension claims.

## Not in the table above — NOT GRADED

Ten plugins are missing from the grading table. They are listed here rather
than given rows, because this document's own rule is that it is *"a snapshot,
not a scoreboard to keep green by editing this file: regrade after real work and
replace it wholesale"* — and a self-assessment by the author of the code is not
the six-independent-reviewer method the rest of the table was produced by.
Grading my own work into a table that does not say so would make the whole page
less trustworthy, not more.

So this holds only what a command can check. Dates are the first commit that
added the directory; README and test-file counts are from the tree on
20 Aug 2026.

| plugin | added | README | test files |
|---|---|---|---|
| achievements | 16 Aug | ✓ | 10 |
| games | 17 Aug | ✓ | 2 |
| magic | 17 Aug | ✓ | 1 |
| medals | 17 Aug | ✓ | 2 |
| downloads | 18 Aug | ✓ | 1 |
| applications | 19 Aug | ✓ *(20 Aug)* | 1 |
| comments | 19 Aug | ✓ *(20 Aug)* | 1 |
| cosmetics | 19 Aug | ✓ *(20 Aug)* | 1 |
| mediainfo | 19 Aug | ✓ *(20 Aug)* | 1 |
| polls | 19 Aug | ✓ *(20 Aug)* | 1 |

The five marked *(20 Aug)* had no README until that day; writing them was how
this section came to exist.

**The pattern, and it is not flattering.** Everything shipped in the last week
was verified against a running site — by transcript, and for anything visual by
screenshot, which repeatedly caught faults no test would have. Almost none of it
was verified by a test that runs in CI. Hand-verification proves it worked once,
on one machine, on one afternoon; it does not survive the next refactor and a
contributor cannot run it.

Three of the ten had **no test file at all** when this was written, and each
had at least one pure function that was exactly the shape a table test wants:

- `cosmetics.cleanTitle` — strips bidi overrides and stacked combining marks
  from text published beside somebody's name. Security-adjacent and untested.
- `applications.looksLikeEmail` / `hashIP`, plus the enumeration rule that a
  known address and an unknown one must answer identically — the kind of
  property that regresses silently when somebody adds a helpful error message.
- `polls.showResults` / `percent` / `slugify` / `Poll.Closed` — a
  three-policy × voted × closed matrix, which is a table test and nothing more.

**All three are done (20 Aug)**, and the sweep carried on into every other
package that had no test file: `stats`, `backups`, `playlists`, the
`theporndb` source, and `scripts/lint-sql`. Only `anidbscraper` is still bare,
deliberately — its bodies are stubs awaiting extraction, so a test would pin a
placeholder.

**20 Aug, later: the store-level rules too.** `pluginapi/pgtest` (scratch
schema + the plugin's own migrations + one variable, `LOON_TEST_DSN`),
`scripts/itest.sh`, a Makefile and a CI job with a Postgres service — then the
three store test files (`cosmetics.Equip`, the `comments` delete/edit rules,
`mediainfo.SummariesFor`/`RemoveReport`). 61 packages green.

The finding underneath it is the one worth keeping: **thirty-one integration
tests already existed in this repo and none of them had ever run.** Each read
its own environment variable and not one was set by anything, so they all
skipped and the suite reported green — which is worse than having no test,
because a skip reports as a pass. They run now.

Three things the tests found on the way, which is the argument for having
written them at all:

- `playlists.owned()` compared the stored owner against a viewer id that is
  **0 for anonymous**, so a `user_id = 0` row would have been owned by every
  signed-out visitor. Unreachable today — every write route sits behind
  `RequireUser` — but the check claims to stand on its own and did not.
- `lint-sql`, the guard that keeps SQL in this repo constant-only, could be
  stepped around by **naming your string**: it skips bare identifier arguments,
  so `q := fmt.Sprintf(...)` followed by `tx.ExecContext(ctx, q)` passed in
  silence. Closed, with the rule narrowed to strings that both are built
  dynamically and open with a SQL verb, so a Sprint'ed value bound as `$1` is
  not mistaken for a query.
- `comments.Delete` expressed "staff" by passing **0** in place of the caller's
  id, so the sentinel meaning *staff* and the id meaning *nobody is signed in*
  were the same value — a non-staff call with user id 0 removed anybody's
  comment. Found on the harness's first run. Now a boolean parameter, which is
  the shape `mediainfo.RemoveReport` already used and why that one was never
  wrong.

Note the pattern in all three: each is a check that was correct in the code
around it and wrong in the one input nobody passes by hand. `0`, twice.

A first correction to this list was written from memory and got three rows
wrong — it claimed `games`, `magic` and `medals` had neither README nor tests,
and all three have both. They are here because the 16 Aug batch missed them, not
because anything is wrong with them. Check the tree, not your recollection.

## Mechanical appendix

Coverage (statement %, `go test -cover`, 2026-08-16): agent 72 · reports 62 ·
logs 61 · catalog 51 · events 50 · ranks 49 · predb 47 · feeds 45 · rewards 44 ·
forum 43 · wiki 40 · backup 39 · usenet 36 · hitrun 32 · news 30 · messages 28 ·
curation 28 · tracker 27 · seedlock 25 · requests 22 · dailyreward 19 · store 17 ·
discord 14 · donations 13 · uploads 13 · economy 12 · offers 12 · lists 12 ·
irc 12 · roadmap 9 · tickets 9 · dbmaint 8 · pointstore 6 · releasegroups 6 ·
scraper 3 · chat 1 · anidbscraper 0 · backups 0 · playlists 0 · stats 0.
(scraper's sources average ~77 — the shell is what is bare.)

Re-measured for the packages touched on 20 Aug: backups 62.9 · stats 43.1 ·
applications 20.5 · playlists 13.0 · polls 10.4 · cosmetics 8.2 · anidbscraper
still 0. Plus two that were not in the original sweep at all: lint-sql 69.1 and
scraper/sources/theporndb 41.7. The low numbers are honest — these files test
the decisions (an ownership gate, an enumeration rule, a results policy, a job
that must never be left running), not the SQL around them, and a store method
that only a database can exercise is not reachable from a unit test here.

Tables without captions: 38 templates. Bootstrap vocabulary: ~75 template
files. CDN scripts: 11 tags in 7 plugins. Plugins declaring events: 12 of 41.
Widget registrants: 5. Backupable providers: 0. Proposer implementers: 0.
