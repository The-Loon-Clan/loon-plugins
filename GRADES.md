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
| backups | ✓ | — | ✓ | ~ | ~ | ~ | ~ | — | — | — | — | ~ | ✗ | ✗ |
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
| playlists | ✓ | ✓ | ~ | ✓ | — | ~ | ✗ | ~ | ~ | ~ | — | ✗ | ✗ | ✗ |
| pointstore | ✓ | ~ | ~ | ✗ | — | ~ | ✗ | ✗ | ✓ | ~ | — | ~ | ✗ | ~ |
| ranks | ✓ | ✓ | ~ | ~ | ✓ | ✓ | ~ | ~ | ✓ | ~ | — | ✓ | ~ | ✓ |
| releasegroups | ✓ | — | ✓ | ✗ | ✓ | ✓ | ~ | ✗ | ~ | ~ | — | ~ | ✓ | ✓ |
| reports | ✓ | — | ~ | ~ | — | ~ | ✓ | ✗ | — | ~ | — | ✓ | ✓ | ✓ |
| requests | ✓ | — | ✓ | ✗ | ~ | ~ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ✓ |
| rewards | ✓ | ✓ | ~ | ~ | ~ | ✓ | ✓ | ✗ | ✓ | ~ | ~ | ✓ | ✓ | ✓ |
| roadmap | ~ | ✗ | ~ | ✗ | ✓ | ✓ | ✗ | ✗ | ~ | ~ | ~ | ~ | ~ | ✓ |
| scraper | ✓ | — | ✗ | ~ | ✓ | ~ | — | — | — | ✓ | — | ✓ | ✗ | ~ |
| seedlock | ✓ | ~ | ~ | ✗ | — | ~ | ✗ | ~ | ✓ | ~ | — | ~ | ✗ | ~ |
| stats | ✓ | — | ✓ | — | ✓ | ~ | — | ✗ | ✓ | ~ | — | ~ | ✗ | ✗ |
| store | ✓ | ✓ | ~ | ✓ | — | ~ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ✓ |
| tickets | ~ | ~ | ✗ | ✓ | — | ~ | ✗ | ✗ | ~ | ~ | — | ~ | ✗ | ~ |
| tracker | ✓ | ✓ | ✓ | ✗ | ~ | ✓ | ✓ | ✓ | ✓ | ~ | ✓ | ~ | ✓ | ✓ |
| uploads | ✓ | — | ✓ | — | — | ✓ | — | ✗ | ~ | ~ | — | ✓ | ~ | ✓ |
| usenet | ✓ | ~ | ✓ | ~ | ✓ | ✓ | ✓ | — | — | ~ | ✓ | ✓ | ✓ | ✓ |
| wiki | ✓ | ~ | ✗ | ✓ | — | ✓ | ✗ | ✗ | ~ | ~ | — | ~ | ~ | ~ |

## Confirmed defects — verified beyond the reviewer's word

Most severe first. These are bugs, not style.

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
| 6 | **Zero-test plugins** — 4 ✗ | anidbscraper, backups, playlists, stats (then chat 0.8%, scraper 3%, releasegroups 5.8%, pointstore 6.4%) | §14; the template-execution test is the highest-value first test for any of them. |
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
- **anidbscraper** — finish or retire the extraction: the fetch is a stub; then tests + README.
- **backup** — declare sealed-generation/ack events; captions.
- **backups** — README and first tests; log succeeded vs failed entries.
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
- **playlists** — README, first tests, stop swallowing lookup errors; oversight page.
- **pointstore** — refund on SetFlair failure (points are lost today); host vocabulary; operator-editable catalog.
- **ranks** — refresh the stale README (SetJobDeps/mirror both gone); declare promote/derank events; NavHint.
- **releasegroups** — recut five templates; declare claim/news/follow events; followed-groups widget.
- **reports** — CSRF token on the resolve form; drop the CDN script; report-resolved event.
- **requests** — declare created/fulfilled/boosted events; admin oversight page; fix stale README lifecycle.
- **rewards** — maintain() SetError + real horizon; declare grant/claim events; host vocabulary.
- **roadmap** — bundle cytoscape locally; move eight tables into plugin migrations; SlotAdminPage CRUD.
- **scraper** — README disclosing all seven services; host SSRF-safe client; keys out of URLs; test theporndb.
- **seedlock** — reasoned sqllint:allow on views.go:143; admin view of held claims; README.
- **stats** — README + first tests; read the snapshot back from shared cache on web processes.
- **store** — drop two CDN scripts; SlotAdminPage; fix README's "ships no templates" claim.
- **tickets** — rewrite the README (Surface contradicts the code); CSRF on every form; SlotAdminPage; stop swallowing update/delete errors.
- **tracker** — fix the deferred-SetIdle clobber; declare snatch/cheat events; MetricSource for up/down/snatches.
- **uploads** — fix the README RequireUser contradiction; caption + vocabulary.
- **usenet** — describe the six plain-Registered capabilities; dismissed-marker for deleted curated newsgroups.
- **wiki** — wire the CSRF seam (forms 403 today); SlotAdminPage; rewrite README's template/extension claims.

## Mechanical appendix

Coverage (statement %, `go test -cover`, 2026-08-16): agent 72 · reports 62 ·
logs 61 · catalog 51 · events 50 · ranks 49 · predb 47 · feeds 45 · rewards 44 ·
forum 43 · wiki 40 · backup 39 · usenet 36 · hitrun 32 · news 30 · messages 28 ·
curation 28 · tracker 27 · seedlock 25 · requests 22 · dailyreward 19 · store 17 ·
discord 14 · donations 13 · uploads 13 · economy 12 · offers 12 · lists 12 ·
irc 12 · roadmap 9 · tickets 9 · dbmaint 8 · pointstore 6 · releasegroups 6 ·
scraper 3 · chat 1 · anidbscraper 0 · backups 0 · playlists 0 · stats 0.
(scraper's sources average ~77 — the shell is what is bare.)

Tables without captions: 38 templates. Bootstrap vocabulary: ~75 template
files. CDN scripts: 11 tags in 7 plugins. Plugins declaring events: 12 of 41.
Widget registrants: 5. Backupable providers: 0. Proposer implementers: 0.
