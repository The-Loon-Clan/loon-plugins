# Plugin checklist

What a plugin must satisfy before it ships, and what an existing plugin is
graded against. Walk it in review; walk it again when a plugin grows a surface
it did not have.

Two rules about the list itself, learned the expensive way:

- **Every item names how it is VERIFIED** — a command, a test, or a concrete
  review question. This ecosystem has already shipped four working audit
  scripts wired to nothing, a `kind='earned'` rank no code evaluated, a
  donations "reward" field nothing dispatched, and an events dropdown that was
  empty because no host registered the catalogue. A checklist item nobody can
  check becomes another one of those.
- **An item whose mechanism does not exist yet says so.** It is marked
  `PENDING` with what to do today, so the list stays honest rather than
  aspirational-and-ignored.

The shape follows what the established directories converge on — WordPress's
plugin review (security, licensing, readme disclosure, i18n), Grafana's
validator-then-manual-review with sample data for the reviewer, Jenkins's
docs-as-code and CI gate — adapted to this stack's own conventions, which are
stricter in places (constant-only SQL, capability contracts) and looser in
others (one binary, so the compiler is the version check).

---

## 1. Contract & interfaces (code review)

- [ ] **MUST** — `Metadata` is complete and true: `Name`, `Version`,
      `Description` (it renders on `/admin/plugins`), `Migrations`, and
      `Processes` limited to the legs that need it, with a comment saying why
      (tracker: web AND api because announce registers on both).
- [ ] **MUST** — `Metadata.Flavours` states which half of a site the plugin
      belongs to: `core.FlavourIndexer`, `core.FlavourTracker`, or
      `core.FlavourAny` for the majority that do not care. **Say it even when
      the answer is "any"** — an empty field behaves identically, which is the
      whole problem: absence means both "belongs to both" and "nobody has
      thought about it", and only one of those is a decision. A one-sided
      declaration carries a comment saying what would run pointlessly without
      it (hitrun on an indexer: an admin page, a nightly sweep and warnings
      about a swarm that does not exist).
- [ ] **MUST** — before shipping a setting whose values are a CLOSED SET, ask:
      **can another plugin append to this?** If the answer is yes — or is "no,
      surely not", which is the answer that has been wrong every time — it is a
      registry PREFIX and a `pluginapi` contract, not a validated string. The
      host shipped three ways to join, open/invite/closed, and a fourth arrived
      in a plugin with nowhere to put it: not a value the validator accepted,
      not a radio button on the form, not a case in the handler. Opening a
      closed set later is a change to the host, its storage, its form and its
      handler, all at once, while somebody waits.
      Read the set as a question rather than a list. "Which of these three
      modes" is closed; "how may somebody join this site" is open, and so are
      *what can be bought*, *what scales what a member earns*, *what can go on a
      page* and *where a selection can be filed* — every one of which is a
      prefix here today. A genuinely closed set is one where a fourth value
      would be a different FEATURE, not another of the same thing: a site is
      normal, read-only or in maintenance, and a fourth is not something a
      plugin contributes.
      Scan it with `pluginapi.Contributions[T]` rather than hand-rolling the
      loop — it sorts, which the hand-rolled ones did not, and a dropdown whose
      options move between page loads reads as a bug in the page.
- [ ] **SHOULD** — a capability an operator might not want is declared with
      `core.RegisterFeature`, not left as a config key. Config flags are
      invisible: nothing lists them and nothing says what switching one off
      stops, so the feature ships permanently on. Name the key on the `View`
      and `Widget` it governs, check it in the view model AND the handler (a
      form outlives the page it came from), and write the `Description` for
      the operator — say what stops and what is KEPT.
- [ ] **MUST** — hard dependencies are in `Metadata.Requires`; soft ones are
      looked up defensively and DEGRADE with a message, never a panic.
      A host registration (not a sibling plugin) cannot be ordered by
      `Requires` — look it up softly and report absence on the surface it
      affects (ranks reports the missing `RankStats` on the job itself).
- [ ] **MUST** — anything another component consumes goes through
      `pluginapi`: interface + registry-name const in one file, both sides
      depending on the contract, neither importing the other. No plugin
      imports another plugin.
- [ ] **MUST** — granters are GRANT-ONLY: the caller debits first, the granter
      never touches the ledger (`pluginapi/rewards.go` states the rule).
- [ ] **MUST** — anything the plugin scans from the registry at Provision is
      documented as BEFORE-Boot on the host side. A capability registered
      after Boot is silently never seen (achievement metrics, rank stats).
- [ ] **MUST** — `SetDeps` seams are refused incomplete at Provision, not
      discovered as a 500 at first request. Optional seams are the exception
      and say what absence costs (tracker's `ReleaseURL`: a link, not a page).
- [ ] **SHOULD** — single-writer hooks (e.g. `tracker.SetMultiplier`) are
      documented as last-writer-wins; a second feature on the same hook goes
      INSIDE the existing writer, not alongside it.

Verify: `go build ./...` (the compiler catches interface skew — pluginapi's
versioning note), `python scripts/audit_flavours.py --strict` (every plugin
declares a half), review of `plugin.go` against this list, and
`/admin/contracts` shows no unwired capability the plugin claims to consume.

## 2. Data & migrations

- [ ] **MUST** — own schema, idempotent migrations, applied by loon with
      `search_path` scoped. Safe to run with the plugin DISABLED (tracker's
      migrations run even when it is off; enabling is config + restart).
- [ ] **MUST** — no foreign keys to host tables. A plain id plus a documented
      host-driven cleanup call on member deletion (tracker migration 001 is
      the model note).
- [ ] **MUST** — demo/seed data only when the table is EMPTY, so an operator
      who deleted it never gets it back. Seeds are configuration an operator
      curates, never fabricated member activity (`demoseed_web.go` draws the
      line).
- [ ] **MUST** — one source of truth per fact. Denormalised counters are
      recomputable caches; a figure with a definition elsewhere (ratio) is
      consumed, not re-derived — two definitions is how a stats page lies.
- [ ] **SHOULD** — destructive schema changes carry the reasoning in the
      migration file, including what was verified against production before
      writing it.

Verify: `make itest` (the harness applies every migration in-schema then
resets `search_path`, so lost scoping fails); boot against an EMPTY database —
the site_settings bug shipped because nobody ever did.

## 3. Security & privacy

- [ ] **MUST** — SQL is constant-only through the typed handle; `sqlx.In` and
      allowlisted identifiers are the two sanctioned escapes, each with a
      reasoned `sqllint:allow`. The directive lives OUTSIDE the SQL string
      (a `--`-less comment inside one killed the local-link sweep for days).
- [ ] **MUST** — gates fail CLOSED. An unwired entitlements service means
      nobody may pass, not everybody. A signed-in member refused by an
      entitlement goes to `/`, not `/login` (they ARE logged in; a login
      prompt loops).
- [ ] **MUST** — CSRF tokens come from the host seam
      (`pluginapi.CSRFToken`, key `csrf.token`), never minted by the plugin,
      and **every POST form carries a hidden `_csrf`**. Destructive actions
      are POST + confirm.

      Not a formality. A host mounts CSRF middleware over the whole engine, so
      a form without the field answers 403 to every human who clicks it — and
      `audit_access.py` CANNOT see that, because it probes destructive POSTs
      WITH a valid token by design (it tests the gate, not the form). A sweep
      on 18 Aug 2026 found **58 tokenless forms across nine plugins**: every
      admin action in usenet, ranks, events, achievements, messages and lists,
      the rewards page's own toggle and create, and the discord and irc unlink
      buttons — the last two carrying a comment explaining that a "site-wide
      submit listener" made a rendered token unnecessary, which described a
      mechanism no host had. All of them had been refusing every operator and
      member who tried, for as long as they had existed.
- [ ] **MUST** — machine endpoints carry their own credential (passkey in
      path, API key) and no session; a torrent client cannot follow a login
      redirect and will parse the login page as a wire response.
- [ ] **MUST** — webhooks authenticate cryptographically (HMAC over the raw
      body — "the HMAC verification IS the auth"), dedupe on a UNIQUE
      transaction id, and answer 5xx on a failed WRITE so the sender retries
      — 200 on failure silently drops settled money.
- [ ] **MUST** — secrets live in the settings store, never in code, config
      files, or logs. Credentials are minted with `crypto/rand`.
- [ ] **MUST** — no phone-home. Any call to an external service is disclosed
      in the README: what is sent, when, and why (the WordPress rule; the
      scraper's metadata sources are the in-tree precedent).
- [ ] **MUST** — member deletion: the README says what rows this plugin holds
      about a member and how the host-driven cleanup removes them.
- [ ] **MUST** — any route outside `/plugin/*` is documented in the README's
      Surface table with its gate, and the host's access table gets a row —
      the tracker's six routes were invisible to `/admin/access` until asked.
- [ ] **SHOULD** — a `Backupable` hook (the `backups` plugin runs every
      plugin's) so the operator's one archive includes this plugin's data.

Verify: `python scripts/sqllint.py`, `python scripts/audit_access.py` and
**`make resources`** in the host — the last one scans every template AND every
Go file in both trees for a POST form with no `_csrf`, which is the blind spot
the access audit has by design. A per-plugin template test counting tokens is
still worth having where the markup is generated (releasegroups' CSRF-per-form
invariant, and rewards' since the sweep); the repo-wide scan is what catches
the plugin nobody thought to write one for.

## 4. Events & exports

Export as much as can be honestly exported — a plugin that announces nothing
cannot be built on.

- [ ] **MUST** — every notable fact is a `DeclareEvent` with the flags told
      straight: `Kind` member vs system (a donation may have no member — the
      donor rides in the payload, not `Event.UserID`), `Countable` only when
      a per-member running total is meaningful (core refuses Countable on a
      system event; both rewards subscribers drop UserID-0 events, so a
      system event can never drive a badge), `Stable` once the shape is set.
- [ ] **MUST** — emit AFTER the row commits, and not on a deduped redelivery.
      Announcing money the site never banked is the failure mode.
- [ ] **MUST** — capabilities and extensions are registered DESCRIBED, so
      `/admin/plugins` can answer "what is this and am I meant to call it or
      supply it" without reading source.
- [ ] **SHOULD** — per-member counters the plugin owns are offered as an
      `achievements.MetricSource` (one call for the whole membership), so
      operators can score achievements on them. Absent, never stubbed at
      zero: a stub is indistinguishable from a real counter for a member who
      has done nothing.
- [ ] **SHOULD** — states other plugins might hang behaviour on are expressed
      as scheduled-event windows or published reads (perks publishes
      `HasFreeleech`), not private booleans.

Verify: `go test` on the event declarations; review question — "what would a
plugin building on this one subscribe to, and is it declared?"

## 5. Background jobs ("daily review", first half)

- [ ] **MUST** — jobs register through `core.Scheduler` with a name, a
      description a stranger can read, and `MarkWrites()` when they write.
- [ ] **MUST** — **every path after `SetRunning()` reaches `SetIdle()` or
      `SetError()`.** A job that returns without either shows "running"
      forever and the scheduler never re-triggers it — the promotion sweep
      shipped this exact bug and every manual trigger returned 200 and did
      nothing.
- [ ] **MUST** — the interval constant and the `SetIdle` horizon are one
      declaration, so `/admin/jobs`'s "next run" cannot drift from reality.
- [ ] **MUST** — sweeps read the whole population in ONE call, and treat
      ABSENCE from the result as "no data, leave alone", never as zero. A
      half-returned query must not demote half the membership.
- [ ] **MUST** — the manual trigger works while the loop is idle, and pause
      is respected on both paths.
- [ ] **MUST** — a DAEMON job — a held connection (irc, discord), not a sweep
      — is the exception to the SetIdle rule: "running" IS its honest steady
      state, with `SetError` on disconnect. Grading the first sweep of this
      list nearly flagged irc for the missing-SetIdle bug before reading the
      shape; the rule above applies to work that FINISHES.
- [ ] **MUST** — `SetIdle`/`SetError` must not fight: a `defer SetIdle(...)`
      runs after an error path's `SetError` and erases it, so failures display
      as clean idle runs (found live in the tracker's cheat sweep).
- [ ] **SHOULD** — a run that changed nothing logs nothing; a run that
      changed something logs the counts ("Promoted 2, demoted 1").

Verify: trigger the job from `/admin/jobs` twice in a row — the second must
run; pause it and trigger — it must not; read the job card an hour later —
state must be idle with a future next-run.

## 6. Self-audit ("daily review", second half)

The plugin reviews ITSELF, because configuration rots and the operator finds
out from a member.

- [ ] **MUST** — the three broken-looking states are told apart out loud:
      IDLE (missing infrastructure — tracker without Redis says "cannot
      run"), UNCONFIGURED (setting absent — perks says "the wallet is NOT
      mounted, tokens cannot be spent"), and EMPTY (working, nothing in it —
      says what makes a row appear). A blank table for all three teaches an
      operator nothing.
- [ ] **SHOULD** — where an operator can configure something inert (a trigger
      naming a deleted event, a metric no source feeds), a validator reports
      it as a finding on the admin page (`events/validate.go` is the model),
      and knows which absences are NORMAL — a triggered event with no windows
      is idle, not broken, and flagging it trains operators to ignore the
      page.
- [ ] **SHOULD** — misconfiguration is refused at the form with a sentence,
      and at the schema with a constraint. "Both exist on purpose: one is the
      message, the other is the guarantee."

Verify: break it on purpose — stop Redis, blank a setting, empty the table —
and read each page; the message must name the state.

## 7. Admin surface

- [ ] **MUST** — one oversight page as a `SlotAdminPage` view: slug, Title,
      Description, `MinRole`, `NavHint` group. It appears in the hub because
      the hub ranges over registered views — never a hand-added link.
- [ ] **MUST** — `Metadata.Description` reads as the plugin's one-line README:
      it is what `/admin/plugins` shows an operator deciding what this thing
      is.
- [ ] **PENDING** — a rendered README panel inside the plugin's own admin
      page. No mechanism yet (`/admin/plugins` renders Description only).
      Until it exists: the Description carries the summary, and the README's
      Surface table stays complete so the answer is one file away.
- [ ] **SHOULD** — operator-tunable behaviour is a settings key (documented
      in the README with its default and why), not a constant needing a
      deploy — the donations `donate_*` keys are the pattern.

Verify: open `/admin` as a mod — the page is reachable from the hub;
`/admin/plugins` describes the plugin in one honest sentence.

## 8. Member-facing UI (html/css)

- [ ] **MUST** — templates speak the HOST's design vocabulary, or the host
      overrides them. Never markup that "happens to render": the tracker's
      Bootstrap `table-responsive` survived only until a phone, where it ran
      364px past the screen. If the plugin renders its own fragments, the
      README says which component names it assumes.
- [ ] **MUST** — no CDN assets, no external calls from pages: hosts ship
      `script-src 'self'`, so a CDN framework arrives as dead toggles with a
      lying `aria-expanded`. Prefer `<details>` and CSS over scripts.
- [ ] **MUST** — accessibility: every data table has a `<caption>`
      (`visually-hidden` is fine), heading levels do not skip, icons are
      `aria-hidden` beside text, controls are reachable without a mouse.
      Two unnamed tables and an h6-under-h2 shipped in this repo; the host's
      audit found them, so it will find yours.
- [ ] **MUST** — fits a 390px screen: wide tables sit in the host's scroll
      wrapper; the PAGE never scrolls sideways.
- [ ] **MUST** — the fragment does not repeat the page title the chrome
      already draws from `render()`'s title argument.
- [ ] **MUST** — states are told in words, and tags do not claim the present
      tense unless the data is live ("Snatched", not "Seeding", when the only
      fact is a sticky completed flag under tiles that DO count a live
      window).
- [ ] **MUST** — no hardcoded image path. A plugin naming a file under the
      host's `/static` is asserting the host ships it; a host that does not
      renders a broken image and says nothing. Decorative art belongs in a
      resource def — PENDING, the registry is designed and unbuilt, so until
      it lands an unavoidable one goes in `audit_resources.py`'s allowlist
      WITH its reason, and comes back out in the commit that lands the
      registry.
- [ ] **MUST** — every `<use href="#id">` resolves to a symbol the host
      ships. A missing one renders an empty box: no error, no console
      warning, nothing to notice until somebody looks at the page. Ask what
      exists rather than guessing — the host publishes `icons.catalogue`
      (`func() []string`), which is also what an admin picker should offer
      instead of a free-text box. A medal held a Windows path for months
      because its field was free text.
- [ ] **SHOULD** — empty states say what fills them; a member-facing figure
      links to the page that explains it.

Verify: host `make a11y` and `make mobile` (0 findings, every page fits);
**`make resources`** (hardcoded images, unresolvable icons — reads the plugin
tree too when it is checked out beside the host, since the sprite sheet is the
host's and that check can only be made there); `make shot NAME=x URL=/y` and
LOOK at it — two of this month's real bugs were visible only in the rendered
page, never in the CSS.

## 9. Widgets

- [ ] **SHOULD** — member-visible state the plugin owns is offered as a
      placeable widget, reading the SAME data the feature applies — perks'
      widget reads the table the multiplier consults, "so a member is shown
      the perks actually being applied rather than rows that say they should
      be".
- [ ] **SHOULD** — a per-placement operator string via `ConfigLabel` where
      the widget's meaning varies by placement.
- [ ] **MUST** (when present) — the widget's zero state is honest, and it
      renders nothing rather than an error when its capability is absent.

Verify: place it on `/admin/widgets`, view as a member with and without data.

## 10. Internationalisation (language slugs)

- [ ] **PENDING** — the plugin translation seam (`Deps.T` / a message
      catalogue with per-locale slugs) is designed but not built; the host
      currently translates its own chrome only (en, zh-Hans, ja).
- [ ] **MUST (today)** — be TRANSLATION-READY so the seam lands without a
      rewrite: every user-visible string lives in templates, not in Go
      `fmt.Sprintf` calls that would need extraction; no sentence is
      assembled from concatenated fragments (word order differs by
      language); counts are rendered so a plural form can attach.
- [ ] **MUST (today)** — identifiers are stable slugs, never display text:
      settings keys, event slugs, metric keys, group slugs. Renaming display
      text must never break a reference (`ranks` made slug the key for
      exactly this reason).
- [ ] **MUST (today)** — dates, relative times and byte counts go through
      host-supplied helpers (the `RelativeTime` seam pattern), so locale
      formatting lands in one place.

Verify: **`make resources`** counts member-facing sentences built in Go and
holds them to a RATCHET — 111 across these plugins on 18 Aug 2026, and the
number may only go down. A check that failed on all 111 would have been
switched off the day it was written; one that refuses the 112th is a check
that survives. Lower the baseline in `audit_resources.py` in the same commit
that converts one. Review question — "if `T()` arrived tomorrow, is this a
mechanical wrap or a rewrite?"

## 11. Machine surface (api / mcp)

- [ ] **MUST** (where the plugin's data is consumable by tools) — a
      documented API endpoint following the host's patterns: key-auth like
      Newznab/RSS, never session-auth; correct content type; wire formats
      with GOLDEN tests when byte-stability matters (the tracker's
      announce/scrape goldens — the info_hash is a SHA-1 of raw bytes, and a
      re-encode is invisible in psql and fatal in a client).
- [ ] **SHOULD** — admin actions worth automating are exposed as a
      `pluginapi.Proposer` (the AI/agent surface): `inspect` for reads,
      `propose` for drafts a human applies, `apply` only for actions safe to
      execute. The contract ships in `pluginapi/aisurface.go`; **no plugin
      implements it yet**, so the first adopter sets the pattern. MCP proper
      is the host's concern — plugins contribute Proposers, the host decides
      how agents reach them.
- [ ] **MUST** — API surfaces are documented in the README's Surface table
      with their auth story, and rate/size limits where they exist.

Verify: golden tests pass; the endpoint answers correctly with NO session
cookie; the README row matches the registered route.

## 12. Metrics & observability

Three audiences, and a plugin owes something different to each: a person
reading logs after the fact, an operator looking at an admin page now, and a
scraper building a graph. The first two were the whole of this section until
20 Aug 2026; the third is the one that pages somebody at 3am.

**Logs and operator figures**

- [ ] **MUST** — structured log keys come from the host's vocabulary
      (`logkeys_test.go`); a new key is added there with a meaning, in the
      same commit. A log schema nobody reviews is one nobody can query.
- [ ] **MUST** — figures shown to operators are the OUTCOME, not a proxy, and
      each fact has one computation. The goal-reward trigger reads the same
      two sums the donate page's thermometer renders "so the window opens
      exactly when the bar members are watching reaches the top".
- [ ] **SHOULD** — an `achievements.MetricSource` for countable per-member
      facts (see §4), and admin-page counts where an operator will ask "is it
      working".

**Saying whether it works**

- [ ] **SHOULD** — a `pluginapi.HealthReporter` when the plugin can be running
      and not doing its job. Every plugin here degrades gracefully because §1
      requires it, which means every plugin can be **silently useless** — a
      scraper with no API key and an IRC bot that has not connected since
      Tuesday both look exactly like healthy plugins from outside. Report
      `degraded` with a sentence an operator can act on ("no API key", not
      "config error"), and read state you already hold: a reporter that dialled
      its upstream would make the admin page as slow and as flaky as the thing
      it reports on.

**Metrics**

- [ ] **SHOULD** — a `pluginapi.MetricSource` for what the host cannot see: a
      staging backlog, announces, a per-source failure rate. Name them
      `<plugin>_<thing>[_total]`, and use BASE UNITS — seconds, not
      milliseconds. A dashboard cannot tell from the wire which you meant, and
      mixing them is how a graph ends up a thousand times wrong silently.
- [ ] **MUST** — labels are BOUNDED sets: a job name, an outcome, a source.
      Never a user id, a release id or a raw path. Every distinct combination
      is a stored series forever, so an unbounded label does not make a richer
      metric — it makes an unusable one and takes the monitoring system with
      it. The host labels request timings by ROUTE TEMPLATE (`/release/:id`)
      for exactly this reason: the path form would be 160,000 series here.
- [ ] **MUST** — `Metrics` is cheap and cannot fail a scrape. Read counters
      already in memory; a `SELECT count(*)` that runs every fifteen seconds
      forever is a monitoring system acting as a load generator, and a source
      that blocks on a dependency makes the scrape time out — which looks
      exactly like the whole process being down.
- [ ] **SHOULD** — prefer a counter and let the query do the rate. Absolute
      totals belong on an admin page; a graph wants a number that only goes up.

**What the HOST owes, stated here because a plugin author will look for it**

`/healthz` is LIVENESS and checks nothing external — a probe that touched the
database would let a thirty-second blip restart every container at once.
`/readyz` is READINESS and checks the database, so the same blip drains traffic
instead. `/versionz` is build identity for a person or a deploy script.
`/metrics` is gated: it names every job, every plugin, the member count and the
exact build, which is a reconnaissance summary of the deployment.

Verify: host `go test` (the logkeys sweep runs repo-wide), `curl /admin/metrics`
and read it — an unfamiliar metric with no HELP, or a label you could not
enumerate, is the finding. Review question: "can an operator tell this is
working without psql?"

## 13. Documentation (doc + readme)

- [ ] **MUST** — a README with the canonical sections, in this order:
      one-paragraph summary (what and why, including what is deliberately
      absent), **Surface** (routes/views/jobs table with access + notes),
      **Data** (tables owned, what is NOT here and where it went),
      **Dependencies** (core services; the `SetDeps` seam table with why the
      host supplies each; config keys with defaults and reasons),
      **Hooks & Callbacks** (set, published, consumed, views), **Lifecycle**
      (Provision/Start/Stop, migration timing), **Files**, **Testing**
      (what is covered, HOW, and the known gaps). `tracker/README.md` is the
      exemplar.
- [ ] **MUST** — the README is written for a person deciding whether to run
      this plugin, "for people, not bots" — every non-obvious default carries
      its reason, and known gaps are stated rather than omitted.
- [ ] **MUST** — external services, if any, are disclosed: what is sent,
      when, why (see §3).
- [ ] **MUST** — comments in code state constraints the code cannot show;
      the commit message carries the why. No narration.

Verify: hand the README to someone who has not read the code and ask them the
Surface table's questions; diff the Surface table against the registration
log.

## 14. Testing & coverage

- [ ] **MUST** — the interesting rules are PURE and unit-tested without a
      database (`promote.go`'s planPromotions; donations' goalRewardDue).
      The awkward inputs are the test cases: partial reads, zero thresholds,
      already-fired periods, absent members.
- [ ] **MUST** — every template executes in a test against the REAL view
      model, asserting content from the LAST element — html/template streams,
      so a missing field truncates mid-page with a 200 and no error.
- [ ] **MUST** — MemStore and PGStore stay in parity: the Mem double
      reproduces the SQL's semantics (UNIQUE no-ops, half-open windows,
      latest-wins), because "a double looser than the thing it stands for is
      a test that passes on code production rejects". Integration tags cover
      the SQL itself.
- [ ] **MUST** — a test that guards a subtle mechanism is PROVEN TO BITE:
      delete the fix, watch it fail, restore it. The OpenWindow truncation
      test passed under a frozen clock while proving nothing — only a moving
      clock made it honest.
- [ ] **MUST** — byte-stable wire formats have goldens (§11).
- [ ] **MUST** — no test target can succeed without running: no `|| true`, no
      hardcoded `-run` subsets, exit status kept. Coverage meets the repo
      floor and the floor is enforced, not printed.
- [ ] **SHOULD** — concurrency-sensitive paths run under `-race` where the
      toolchain allows, and are written so the store arbitrates races rather
      than the test hoping.
- [ ] **MUST** — a store test that needs Postgres uses `pluginapi/pgtest`
      rather than its own connection and its own environment variable:

      ```go
      //go:build integration

      func testStore(t *testing.T) *PGStore {
          return NewPGStore(pgtest.SchemaDB(t, "myplugin_store_test", migrations))
      }
      ```

      A SCRATCH schema name, not the plugin's real one, so a run cannot drop
      a schema somebody is using; and the plugin's own embedded migrations,
      so the test exercises the shipped files rather than a hand-written
      CREATE TABLE that stopped matching two migrations ago.

      The variable is `LOON_TEST_DSN` and nothing else. Thirty-one integration
      tests were written before this existed, each with its own name —
      `ACHIEVEMENTS_TEST_DSN`, `NEWS_TEST_DSN`, and eight more — and not one of
      them was set by anything, so every one skipped and the suite went green
      by doing nothing. That is the failure this rule exists to prevent, and it
      is worse than having no test: a skip reports as a pass.

Verify: `go test ./<plugin>/` and `make itest` (which starts a disposable
Postgres and runs the whole `integration`-tagged suite; CI runs the same);
`make cover` against the floor; for the bite-check, the commit message says
what was deleted to prove it.

---

## Running the mechanical half

From the reference host (`loon-site`), with the stack up:

```
make check      # fmt, build, vet, golangci, sqllint, contrast, tests, coverage floor
make release    # + access, links, a11y, mobile against the running site
make itest      # migrations + SQL semantics against a disposable Postgres/Redis
```

From THIS repo (`loon-plugins`), which has its own Makefile:

```
make check      # vet (both tags), sqllint, unit tests
make itest      # the integration-tagged suite against a disposable Postgres
make fmt        # gofmt as an error (over-reports on Windows — see the target)
```

`scripts/itest.sh` is where `make itest` actually lives, because this is a
repo people open on machines with no `make` at all — a target that only works
in one place is a check that quietly stops being run. It takes `go test`
arguments, so `bash scripts/itest.sh ./cosmetics/ -v` narrows it.

From THIS repository, which the host's targets cannot see into:

```
python scripts/audit_flavours.py            # who declares which half (section 1)
python scripts/audit_flavours.py --strict   # non-zero while any are undeclared
```

That one is a script rather than a review note for a specific reason worth
repeating: an empty `Flavours` behaves exactly like `FlavourAny`, so the
omission never surfaces as a bug and never stands out in a diff. It is only
visible by asking the whole tree at once.

Everything those commands cannot check is a review question above, and each
review question names its exemplar. When an item graduates from `PENDING`
(the i18n seam, the admin README panel, the first Proposer), edit THIS file
in the same commit — a checklist that trails the code is read once and never
trusted again.
