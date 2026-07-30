package usenet

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// samplePending refreshes the forming-releases readout — the dashboard's
// "which releases are still missing articles" card. Per PASS (start and end),
// never per round: even the bounded form (one O(1) sample per group plus a
// pipelined read per sampled set) is far too heavy for the render path, and it
// was previously paid once per ~500-set round.
//
// EVERY failure path returns without touching telemetry. setPending flips the
// pendingSeen sentinel, and calling it with nil after a failed group-list read
// or a Redis outage recorded "0 forming" as a fact — a false zero in exactly
// the readout an operator checks when staging is at its ceiling, which is also
// exactly when Redis is slow enough to fail the sample. Skipping leaves the
// previous sample and the -1 "not sampled" sentinel intact.
func (p *Plugin) samplePending(ctx context.Context) {
	names, err := p.st.activeGroupNames(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/incomplete-sample", err)
		return
	}
	sets, err := p.staging.incompleteSets(ctx, 15, names)
	if err != nil {
		p.reportErr(ctx, "usenet/incomplete-sample", err)
		return
	}
	p.tel.setPending(sets)
}

// runBuild assembles complete (group, base_subject) sets into NZB files. A set
// is complete when its distinct part count reaches the max total-parts seen.
func (p *Plugin) runBuild(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.buildMu.TryLock() {
		p.buildJob.Log("build already running — skipping overlap")
		return
	}
	defer p.buildMu.Unlock()
	// The builder drains shared staging, so it must run once cluster-wide.
	if !p.withLease(ctx, leaseScopeJob, jobNameBuild, p.leaseTTL(p.effective(ctx)), p.buildCatchUp) {
		p.buildJob.Log("build skipped — another worker holds this job")
		p.buildJob.SetIdle(p.nextCrawl(ctx))
	}
}

// buildCatchUp keeps assembling while the ready queue is actually shrinking.
//
// The builder had the same shape the backfill did: one bounded pass — 500 sets,
// about eight seconds — then sleep out the interval. Against a queue of 1.46
// MILLION that is 500 a minute and 48 hours to look at each entry once, while
// the machine sits idle 87% of the time. Worse, it is the backfill's release
// valve: staging fills, the backfill parks itself at the pressure gate, and the
// only thing that can unpark it drains at 500 a minute.
//
// Progress is ENTRIES DRAINED, not releases built. Most of this queue is junk
// and will never produce a release, but throwing it away still frees the Redis
// the real sets need — so a round that builds nothing and discards 500 is
// progress. Incomplete sets stay staged and do not count, which is what stops
// the loop spinning on a queue that is merely waiting for more articles.
func (p *Plugin) buildCatchUp(ctx context.Context) {
	// The pending sample is the ONLY per-pass work here. The blacklist reload
	// and the ready-queue reap look like pass work too, and a previous change
	// hoisted them — wrongly, because a catch-up pass has no round cap:
	//
	//   - the reap's budget bounds ONE call and its cursor persists, so its
	//     sweep RATE is budget × calls. Hoisted, a 7M-entry queue's full
	//     circuit went from ~20 minutes of catch-up to 12+ hours, stalling
	//     fossil removal during exactly the catch-up it was built for. At
	//     small queue depths the cursor makes the per-round call nearly free.
	//   - the blacklist matcher is process-global and prod's admin edits it in
	//     the WEB process; the worker's only refresh is the reload. Hoisted, a
	//     rule the operator just added ("applied immediately", says the flash)
	//     was ignored for the rest of a pass that can run for hours.
	//
	// Both live in buildLocked (per round) on purpose. Do not re-hoist.
	//
	// The sample runs at the START as well as the end: the census is written
	// per ROUND, so with only an end-of-pass sample every census row of a
	// catch-up pass carried the previous pass's pending figure.
	p.samplePending(ctx)
	res := runCatchUp(ctx,
		func() (int, int) { return p.buildLocked(ctx) },
		func() bool { return p.effective(ctx).BuildNoCatchup },
		// No pressure gate: this job is what RELIEVES pressure. Stopping it
		// because staging is full would deadlock the pipeline against itself.
		func() (bool, float64) { return false, 0 },
		nil)
	if res.Rounds > 1 {
		p.buildJob.Log("build catch-up: %d round(s), %s set(s) drained, %s release(s) built (%s)",
			res.Rounds, fmtComma(int64(res.Batches)), fmtComma(int64(res.Staged)), res.StoppedBy)
	}
	// The forming-releases sample, once per pass and AFTER the drain — a
	// sample taken mid-catch-up describes a queue that has since moved, and
	// taking it per round multiplied the pipeline's most expensive read by the
	// round count.
	p.samplePending(ctx)
	p.buildJob.SetIdle(p.nextCrawl(ctx))
}

func (p *Plugin) buildLocked(ctx context.Context) (built, drained int) {
	p.buildJob.SetRunning()
	// One effective() read per round keeps every knob live without paying the
	// settings read per use below.
	cfg := p.effective(ctx)

	// Per-round on purpose — see buildCatchUp for why hoisting these to the
	// pass collapsed the fossil-sweep rate and let the worker ignore blacklist
	// edits for hours.
	p.reloadBlacklist(ctx)
	if scanned, removed, err := p.staging.reapReadyQueue(ctx, cfg.ReadyReapPerPass); err != nil {
		p.reportErr(ctx, "usenet/ready-reap", err)
	} else if removed > 0 {
		p.buildJob.Log("ready queue: swept %d entr(ies), removed %d dead", scanned, removed)
	}
	// The walk-past sweep rides the same round cadence: dead sets holding
	// staging memory are exactly what keeps the backfill's pressure gate
	// closed, and clearing them per round is what shortens the pause.
	p.runWalkPastSweep(ctx, cfg)

	// Make sure last pass's counters are persisted even if that pass died
	// before its own flush.
	defer p.flushFilterHits(ctx)
	defer p.flushPosterHits(ctx)
	// Every branch below names its outcome, so the reasons sum to the
	// candidates examined — see build_outcomes.go.
	defer p.flushBuildOutcomes(ctx)

	// The census records FIRST — before the sink resolve and the draw, whose
	// early returns used to skip it entirely. A sink outage is exactly when
	// fossils accumulate and Redis fills toward eviction, and it produced NO
	// census rows for its whole duration; staging_census.go's own header calls
	// a gap that reads as "nothing happened" worse than no census at all. A
	// zero draw honestly records that nothing was drawn while stagingInfo
	// still captures the memory/eviction half — the half that matters during
	// an outage. The pending figure comes from the pass-level sample
	// (samplePending, taken once per pass in buildCatchUp): -1 until one has
	// been taken, which the readout keeps distinct from "none pending".
	var drawn candidateStats
	defer func() { p.takeStagingCensus(ctx, drawn, p.tel.pendingCount()) }()

	// Resolve the sink ONCE for the pass (mirrors resolveHealthBackend): a
	// host-misconfigured pass fails here with one error instead of flooding the
	// error log with one per candidate.
	sink, err := p.resolveSink()
	if err != nil {
		p.buildJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/build-sink", err)
		return 0, 0
	}

	keys, d, err := p.staging.candidateGroups(ctx, cfg.BuildDrainPerPass)
	if err != nil {
		p.buildJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/build-scan", err)
		return 0, 0
	}
	drawn = d
	if drawn.Fossil > 0 {
		// Each fossil is a release that completed, queued itself, and then
		// expired or was evicted before the builder drew it. Nothing else
		// records these: they never reach an outcome, so build_outcomes cannot
		// see them, and the SRem that removes them is best-effort and silent.
		p.buildJob.Log("ready queue: drew %d of %d, %d already expired (completed releases lost before assembly)",
			drawn.Sampled, drawn.ReadyDepth, drawn.Fossil)
	} else if drawn.Starved() {
		p.buildJob.Log("ready queue: drew %d of %d waiting", drawn.Sampled, drawn.ReadyDepth)
	}
	skippedExt, skippedBL, demoted := 0, 0, 0

	// STAGE 1 — decide what the subject line alone can decide, before paying for
	// the articles. Skipped entirely when a poster watch is active: attribution
	// needs the articles, and the watch exists to explain why a known poster's
	// releases did not appear, so it is never traded for throughput.
	fastDropped := 0
	// candidates is the honest denominator for the pass summary: len(keys)
	// AFTER splitByTitle has removed the fast-dropped rejects made the log
	// read "built 0 of 0 — 500 junk" on a junk-heavy pass, reasons exceeding
	// the total without bound.
	candidates := len(keys)
	watchActive := p.posterWatch.active()
	if watchActive {
		// The watch disables the title fast path BY DESIGN (attribution needs
		// the articles) — but silently: one watched poster turned microsecond
		// rejects into a full ~16ms article load per junk set, collapsing the
		// drain rate exactly while an operator investigates a backlog, with
		// nothing anywhere saying why. Announce the trade where they look.
		p.buildJob.Log("poster watch active (%d pattern(s)): title fast-path off — every set takes the full article load",
			p.posterWatch.count())
	}
	kept, rejects := splitByTitle(keys, watchActive)
	keys = kept
	if len(rejects) > 0 {
		// Account for every reject first, then remove them in one go. Deleting
		// them one at a time costs a round-trip each, and with the title
		// pre-filter doing the judging in microseconds those round-trips became
		// the entire cost of the junk path — 500 of them per pass.
		doomed := make([]groupKey, 0, len(rejects))
		for _, r := range rejects {
			if r.blockedExt {
				skippedExt++
				p.outcomes.note(outcomeBlockedExt, r.key.Base)
			} else {
				p.hits.note("junk", r.junkRule, r.title)
				p.outcomes.note(outcomeJunk, r.key.Base)
			}
			doomed = append(doomed, r.key)
		}
		removed, err := p.staging.deleteStagedBatch(ctx, doomed)
		if err != nil {
			p.reportErr(ctx, "usenet/build-delete-staged", err)
		}
		// Count what actually went, not what was asked for: a partial failure
		// must not report a drain that did not happen, or the catch-up loop
		// believes it is making progress it is not.
		drained += removed
		fastDropped += removed
	}

	for _, k := range keys {
		if ctx.Err() != nil {
			break
		}
		arts, err := p.staging.groupArticles(ctx, k.Group, k.Base)
		if err != nil {
			p.outcomes.note(outcomeLoadError, k.Base)
			p.reportErr(ctx, "usenet/build-load", err)
			continue
		}
		if len(arts) == 0 {
			// Staging says there is a set here and the articles say otherwise.
			// Separated from "incomplete" because it is a consistency problem,
			// not a waiting one, and conflating them hid it.
			p.outcomes.note(outcomeEmpty, k.Base)
			continue
		}
		if !isComplete(arts) {
			// Not a drop: stays staged for the next round. Counted anyway,
			// because on a stalled site this is the majority outcome and an
			// unnamed majority is indistinguishable from a leak.
			p.outcomes.note(outcomeIncomplete, k.Base)
			// But the READY claim is withdrawn. In redis mode this candidate
			// came off nzb:ready, meaning staging's completeness check passed
			// and this verification disagrees — a disagreement no future pass
			// resolves, so leaving the entry queued re-loads and re-refuses it
			// every minute while its articles pin staging memory. Demote: the
			// articles stay, the stage-time check re-queues the set if it ever
			// truly completes, the TTL clears it if it never does.
			if ok, err := p.staging.demoteReady(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/build-demote", err)
			} else if ok {
				demoted++
			}
			continue
		}
		// Classification runs in PROD'S order: title extraction, blocked
		// extensions, the operator blacklist, then the sized junk check — which
		// an explicit category tag bypasses, exactly as prod's assembler does.
		title, cat, junkRule, blockedExt := classifyRelease(k.Base, arts)
		if blockedExt {
			// Counted, not logged per-release: a pass drains up to 500 sets, and
			// a per-set SKIP line would evict the pass summary from the 100-line
			// job ring. The count folds into that summary instead.
			skippedExt++
			p.outcomes.note(outcomeBlockedExt, k.Base)
			p.notePoster(arts, "blocked-ext", title)
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/build-delete-staged", err)
			} else {
				drained++
			}
			continue
		}
		// Operator policy, checked at build like prod does. Deliberately NOT at
		// ingest: blacklist rules are edited far more often than junk rules, and
		// filtering at build means a new rule applies to everything already
		// staged instead of only to what arrives after it.
		if pat := whichBlacklistRule(release{
			Subject: k.Base, Title: title,
			Poster: firstPoster(arts), Group: k.Group,
		}); pat != "" {
			// Attribution is already recorded per-rule in filter_hits; the
			// per-release log line is redundant with that and floods the ring.
			p.hits.note("blacklist", pat, k.Base)
			p.outcomes.note(outcomeBlacklist, k.Base)
			p.notePoster(arts, "blacklist:"+pat, title)
			skippedBL++
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/build-delete-staged", err)
			} else {
				drained++
			}
			continue
		}
		if junkRule != "" {
			p.hits.note("junk", junkRule, k.Base)
			p.outcomes.note(outcomeJunk, k.Base)
			p.notePoster(arts, junkRule, title)
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil { // drop, don't build
				p.reportErr(ctx, "usenet/build-delete-staged", err)
			} else {
				drained++
			}
			continue
		}
		xmlBytes, err := buildNZB(arts)
		if err != nil {
			p.outcomes.note(outcomeXMLError, k.Base)
			// Malformed input the sanitising didn't cover. Leave the set staged:
			// the prune horizon clears it if it never becomes buildable.
			p.reportErr(ctx, "usenet/build-xml", fmt.Errorf("%s/%s: %w", k.Group, k.Base, err))
			continue
		}
		gz, err := gzipBytes(xmlBytes)
		if err != nil {
			p.outcomes.note(outcomeGzipError, k.Base)
			p.reportErr(ctx, "usenet/build-gzip", err)
			continue
		}
		size, posted := summarize(arts)
		rel := pluginapi.AssembledRelease{
			Title: title, BaseSubject: k.Base, Group: k.Group,
			Poster:      firstPoster(arts),
			ContentHash: contentHashArticles(arts),
			SizeBytes:   size, PostedAt: posted,
			NZBGz: gz, Segments: len(arts), CategoryHint: cat,
		}
		_, created, err := sink.store(ctx, rel)
		if err != nil {
			p.outcomes.note(outcomeStoreError, k.Base)
			p.notePoster(arts, "store-error", title)
			// Storage failed — leave the set staged so a later pass retries.
			// A transient sink outage must never lose a release.
			p.reportErr(ctx, "usenet/build-store", fmt.Errorf("%s: %w", title, err))
			continue
		}
		// In redis mode this delete is the ONLY way an entry leaves nzb:ready —
		// a persistent failure re-builds the same set every pass forever.
		if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
			p.reportErr(ctx, "usenet/build-delete-staged", err)
		} else {
			drained++
		}
		if !created {
			// Assembled fine, the sink already had it. Previously invisible:
			// the pass counted creations only, so a pass that deduped every
			// set reported "built 0" and offered no reason.
			p.outcomes.note(outcomeDuplicate, k.Base)
			p.notePoster(arts, "duplicate", title)
		} else {
			// The success case matters as much as the failures: an operator
			// asking "am I getting this poster" needs to see yes, and a watch
			// that only ever shows drops reads as broken even when it is not.
			p.notePoster(arts, "built", title)
		}
		if created {
			p.outcomes.note(outcomeBuilt, k.Base)
			built++
			// Feed the "recently built" telemetry ring: with sink=host no
			// plugin table records this, and the host table mixes in agent
			// uploads — the ring is what the crawlers page shows.
			p.tel.noteBuilt(title, k.Group, size)
		}
	}
	// The summary now accounts for every candidate, not just the ones that
	// produced a file: incomplete and duplicate are usually the big two, and
	// their absence was what made a stalled pipeline look like a working one.
	if fastDropped > 0 {
		p.buildJob.Log("title pre-filter dropped %d set(s) without loading their articles", fastDropped)
	}
	if demoted > 0 {
		// Every demotion is a stage-time/build-time completeness disagreement;
		// a climbing rate means the two checks have drifted apart again.
		p.tel.noteDemoted(demoted)
		p.buildJob.Log("withdrew %d refused set(s) from the ready queue "+
			"(queued as complete, failed build verification — mixed re-posts under one subject, "+
			"or totals staged before they were monotonic); their articles stay until they complete or expire",
			demoted)
	}
	// candidates, not len(keys): splitByTitle shrank keys, and its rejects are
	// in the junk/blocked-ext totals below — the reasons must sum to the
	// denominator or the line is unreadable ("built 0 of 0 — 500 junk").
	p.buildJob.Log("built %d of %d candidate set(s) — %d incomplete, %d duplicate, "+
		"%d junk, %d blacklisted, %d blocked-ext, %d empty, %d error(s)",
		built, candidates,
		p.outcomes.total(outcomeIncomplete), p.outcomes.total(outcomeDuplicate),
		p.outcomes.total(outcomeJunk), skippedBL, skippedExt,
		p.outcomes.total(outcomeEmpty),
		p.outcomes.total(outcomeLoadError)+p.outcomes.total(outcomeXMLError)+
			p.outcomes.total(outcomeGzipError)+p.outcomes.total(outcomeStoreError))
	if built > 0 {
		// New releases changed the search surface — publish so a subscriber
		// (e.g. a cache invalidator in the worker) can react. Best-effort: no
		// host event bus => no-op.
		pluginapi.EmitEvent(p.core, ctx, pluginapi.EventIngested, built)
	}
	return built, drained
}

// salvageCapPerRound bounds how many salvage candidates one round assembles.
// Each costs a full article load plus an NZB build, so an unbounded burst
// (first sweep after a deploy over an aged population) would stall the round;
// the cursor brings the rest around on later rounds. A constant because it
// paces a recovery path, not a policy — the policy knob is walk_past_no_salvage.
const salvageCapPerRound = 25

// runWalkPastSweep runs one bounded walk-past round: evict staged sets whose
// whole article span has been fetched yet are still short — they can never
// complete (walkPastDead), and every hour they wait for the TTL is an hour
// the pressure gate stays closed against work that CAN complete. Dead sets
// worth keeping go to salvageSets instead of the void.
func (p *Plugin) runWalkPastSweep(ctx context.Context, cfg Config) {
	if cfg.WalkPastNoEvict {
		return
	}
	cov, err := p.walkPastCoverage(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/walk-past-coverage", err)
		return
	}
	if len(cov) == 0 {
		return
	}
	salvageCap := salvageCapPerRound
	if cfg.WalkPastNoSalvage {
		salvageCap = 0
	}
	// The margin is the crawl/backfill batch window: "walk-past" must mean the
	// walk went a full batch beyond the set on both sides, or a frontier set
	// whose missing parts are simply not fetched yet would judge as dead.
	scanned, evicted, salvage, err := p.staging.sweepWalkPast(ctx, cov,
		time.Duration(cfg.WalkPastGraceMin)*time.Minute, cfg.WalkPastSweepPerRound, salvageCap, int64(cfg.Batch))
	if err != nil {
		p.reportErr(ctx, "usenet/walk-past-sweep", err)
	}
	if evicted > 0 {
		p.tel.noteWalkPast(evicted)
		p.buildJob.Log("walk-past sweep: evicted %d of %d examined set(s) — span fully fetched, still incomplete, past grace",
			evicted, scanned)
	}
	p.salvageSets(ctx, salvage)
}

// salvageSets assembles walk-past-dead sets that still hold most of their
// articles — the operator's "save them as broken NZB files" path. Each
// candidate is scored with the SAME rule the health job applies to stored
// releases (healthVerdict): data gaps covered by surviving par2 make a BROKEN
// release worth serving (a downloader can repair it); gaps beyond the par2
// are dead and evict. par2-only gaps score healthy — all the data exists —
// and store as a normal release the completeness check alone was holding.
//
// Broken releases are marked through the health backend, the same seam the
// health job writes, so they land in the existing health UI in both sink
// modes with no contract change. When a later re-walk completes the release
// for real, the broken NZB's segment set is a strict subset of the new one —
// exactly what nzb-heal purges.
//
// A resolve or store failure leaves candidates staged: the sweep cursor
// brings them around again, and the TTL is the backstop — never evict a
// salvageable set just because a backend was briefly down.
func (p *Plugin) salvageSets(ctx context.Context, keys []groupKey) {
	if len(keys) == 0 {
		return
	}
	sink, err := p.resolveSink()
	if err != nil {
		p.reportErr(ctx, "usenet/salvage-sink", err)
		return
	}
	hb, err := p.resolveHealthBackend()
	if err != nil {
		p.reportErr(ctx, "usenet/salvage-health", err)
		return
	}
	salvaged, dead, junked := 0, 0, 0
	for _, k := range keys {
		if ctx.Err() != nil {
			break
		}
		arts, err := p.staging.groupArticles(ctx, k.Group, k.Base)
		if err != nil {
			p.reportErr(ctx, "usenet/salvage-load", err)
			continue
		}
		if len(arts) == 0 {
			continue
		}
		// The same gates every built release passes — salvage must never
		// resurrect what the build path would drop.
		title, cat, junkRule, blockedExt := classifyRelease(k.Base, arts)
		blPattern := whichBlacklistRule(release{
			Subject: k.Base, Title: title,
			Poster: firstPoster(arts), Group: k.Group,
		})
		if blockedExt || junkRule != "" || blPattern != "" {
			if junkRule != "" {
				p.hits.note("junk", junkRule, k.Base)
			}
			if blPattern != "" {
				p.hits.note("blacklist", blPattern, k.Base)
			}
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/salvage-delete", err)
			} else {
				junked++
			}
			continue
		}
		total, missData, par2Claimed, par2Missing := salvageTally(arts)
		verdict := healthVerdict(missData, par2Claimed, par2Missing)
		if verdict == healthDead {
			// Gaps beyond what the surviving par2 can rebuild: not worth a row.
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/salvage-delete", err)
			} else {
				dead++
				p.tel.noteWalkPast(1)
			}
			continue
		}
		xmlBytes, err := buildNZB(arts)
		if err != nil {
			p.reportErr(ctx, "usenet/salvage-xml", fmt.Errorf("%s/%s: %w", k.Group, k.Base, err))
			continue
		}
		gz, err := gzipBytes(xmlBytes)
		if err != nil {
			p.reportErr(ctx, "usenet/salvage-gzip", err)
			continue
		}
		size, posted := summarize(arts)
		id, created, err := sink.store(ctx, pluginapi.AssembledRelease{
			Title: title, BaseSubject: k.Base, Group: k.Group,
			Poster:      firstPoster(arts),
			ContentHash: contentHashArticles(arts),
			SizeBytes:   size, PostedAt: posted,
			NZBGz: gz, Segments: len(arts), CategoryHint: cat,
		})
		if err != nil {
			// Transient sink failure: stay staged, the cursor retries.
			p.reportErr(ctx, "usenet/salvage-store", fmt.Errorf("%s: %w", title, err))
			continue
		}
		if created && verdict == healthBroken {
			// The verdict write must be loud on failure: a broken release
			// stored unmarked serves as complete. (The healthy case — par2-only
			// gaps — stays unmarked on purpose: all data is present, and the
			// health job will confirm from the live server on its cadence.)
			if err := hb.setVerdict(ctx, id, healthBroken, total, missData, par2Claimed); err != nil {
				p.reportErr(ctx, "usenet/salvage-verdict", fmt.Errorf("nzb %d stored but not marked broken: %w", id, err))
			}
		}
		if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
			p.reportErr(ctx, "usenet/salvage-delete", err)
		}
		p.outcomes.note(outcomeSalvaged, k.Base)
		if created {
			salvaged++
			p.tel.noteSalvaged(1)
			p.tel.noteBuilt(title, k.Group, size)
		}
	}
	if salvaged+dead+junked > 0 {
		p.buildJob.Log("salvage: %d broken/partial release(s) stored, %d beyond par2 repair evicted, %d junk dropped",
			salvaged, dead, junked)
	}
}

// salvageTally mirrors isComplete's per-file grouping to count what a dead
// set actually holds versus what it claims, split data/par2 the way the
// health job splits an NZB (isPar2Subject) — so healthVerdict scores staged
// sets and stored releases by the same rule.
//
// ENTIRELY-missing files count too, and skipping them was a confirmed bug:
// whole-file absence is the canonical walk-past shape (the last volumes never
// crawled), and a tally built only from seen files scored such a release
// HEALTHY when its in-span gaps happened to be par2-only — an unmarked
// release missing whole data files, served as complete. An unseen file's
// segment count is unknowable (we never saw a subject claiming it), so it is
// estimated at the average seen data file and counted as DATA — the
// conservative direction, since demanding par2 for it can only push the
// verdict toward broken/dead, never toward healthy.
func salvageTally(arts []stagedArticle) (total, missingData, par2Claimed, par2Missing int) {
	type fileState struct {
		parts    map[int]bool
		segTotal int
		par2Seen bool
		nonPar2  bool
	}
	files := map[int]*fileState{}
	totalFiles := 0
	for _, a := range arts {
		f := files[a.FileNum]
		if f == nil {
			f = &fileState{parts: map[int]bool{}}
			files[a.FileNum] = f
		}
		f.parts[a.PartNum] = true
		if a.SegTotal > f.segTotal {
			f.segTotal = a.SegTotal
		}
		if isPar2Subject(a.Subject) {
			f.par2Seen = true
		} else {
			f.nonPar2 = true
		}
		if a.FileParts && a.TotalFiles > totalFiles {
			totalFiles = a.TotalFiles
		}
	}
	seenData, seenDataSegs := 0, 0
	for _, f := range files {
		claimed := f.segTotal
		if claimed < len(f.parts) {
			// A claim below what we hold (unparsed totals): trust the articles.
			claimed = len(f.parts)
		}
		total += claimed
		missing := claimed - len(f.parts)
		// A bucket counts as par2 only when EVERY held article is par2. A
		// mixed bucket — a single-file release (FileNum 0) whose par2
		// companion shares its bucket — is judged as data, so its gaps count
		// as missingData. Pessimistic on purpose, matching parseNzbSegments'
		// stated bias: attributing a mixed bucket's gaps to par2 scored
		// incomplete single-file releases healthy and stored them unmarked.
		if f.par2Seen && !f.nonPar2 {
			par2Claimed += claimed
			par2Missing += missing
		} else {
			missingData += missing
			seenData++
			seenDataSegs += claimed
		}
	}
	if totalFiles > 0 {
		absent := 0
		for fn := 1; fn <= totalFiles; fn++ {
			if files[fn] == nil {
				absent++
			}
		}
		if absent > 0 {
			est := 1
			if seenData > 0 && seenDataSegs/seenData > 1 {
				est = seenDataSegs / seenData
			}
			total += absent * est
			missingData += absent * est
		}
	}
	return total, missingData, par2Claimed, par2Missing
}

// walkPastCoverage assembles the coverage map the sweep judges against:
// active groups whose fetched ranges live on exactly ONE backbone. Article
// numbers are per-backbone, and a set's span checked against another
// backbone's number space is meaningless — multi-backbone groups are skipped
// (their dead sets fall to the TTL, as everything did before the sweep).
func (p *Plugin) walkPastCoverage(ctx context.Context) (map[string][]articleRange, error) {
	ranges, err := p.st.allCoveredRanges(ctx)
	if err != nil {
		return nil, err
	}
	return judgeableCoverage(ranges), nil
}

// judgeableCoverage flattens per-(backbone, group) ranges to per-group,
// dropping any group covered on MORE than one backbone — its sets' spans mix
// two article-number spaces and cannot be judged.
func judgeableCoverage(ranges map[coverKey][]articleRange) map[string][]articleRange {
	byGroup := make(map[string][]articleRange, len(ranges))
	backbones := map[string]int{}
	for k, rs := range ranges {
		backbones[k.group]++
		byGroup[k.group] = rs
	}
	for g, n := range backbones {
		if n > 1 {
			delete(byGroup, g)
		}
	}
	return byGroup
}

// isComplete decides whether a staged (group, base_subject) set is ready to
// assemble. Multi-file releases (file_parts) are complete when every file has
// all its segments and the release has all its files; single-file releases when
// the distinct part count reaches total_parts.
func isComplete(arts []stagedArticle) bool {
	multi := false
	totalFiles := 0
	for _, a := range arts {
		if a.FileParts && a.TotalFiles > 0 {
			multi = true
			if a.TotalFiles > totalFiles {
				totalFiles = a.TotalFiles
			}
		}
	}
	if multi {
		type fileState struct {
			parts    map[int]bool
			segTotal int
		}
		files := map[int]*fileState{}
		for _, a := range arts {
			f := files[a.FileNum]
			if f == nil {
				f = &fileState{parts: map[int]bool{}}
				files[a.FileNum] = f
			}
			f.parts[a.PartNum] = true
			if a.SegTotal > f.segTotal {
				f.segTotal = a.SegTotal
			}
		}
		// Two rules, and the range check is the load-bearing one. Only files
		// numbered 1..totalFiles count toward the poster's own total: a
		// "[00/14]" header post or an unnumbered par2 companion lands in the
		// file-0 bucket, and counting it meant complete = 14 the moment files
		// 0..13 were in — the NZB was emitted, the set deleted, and the
		// watermark moved on while file 14 was still being crawled. The last
		// volume of the release was permanently lost and the broken NZB served
		// as "completed". The forward crawl stages in posting order with a
		// build after every pass, so the window was hit routinely.
		//
		// And EVERY seen file must have all its segments, whatever its number:
		// buildNZB emits every bucket it has, so assembling around a
		// half-fetched companion would ship a truncated file. A set held back
		// here stays staged and is reconsidered once the missing articles
		// arrive; if they never do, staging's age horizon sheds it.
		complete := 0
		for fn, f := range files {
			if f.segTotal <= 0 || len(f.parts) < f.segTotal {
				return false
			}
			if fn >= 1 && fn <= totalFiles {
				complete++
			}
		}
		return complete >= totalFiles
	}
	parts := map[int]bool{}
	total := 0
	for _, a := range arts {
		parts[a.PartNum] = true
		if a.TotalParts > total {
			total = a.TotalParts
		}
	}
	return total > 0 && len(parts) >= total
}

// buildNZB serializes a complete set into NZB XML. A multi-file release gets one
// <file> element per file number; a single-file release gets one <file>.
//
// The error return exists because xml.Marshal REFUSES invalid UTF-8, and Usenet
// subjects carry arbitrary bytes. The old form returned nil on error, which
// gzipped fine and produced a "completed" release whose NZB downloads as zero
// bytes — invisible until a user tries it. Attributes are sanitised so the
// error path is nearly unreachable, but if it fires the release is skipped
// loudly rather than stored empty.
func buildNZB(arts []stagedArticle) ([]byte, error) {
	multi := false
	for _, a := range arts {
		if a.FileParts {
			multi = true
			break
		}
	}
	doc := nzbDoc{Xmlns: "http://www.newzbin.com/DTD/2003/nzb"}
	if multi {
		byFile := map[int][]stagedArticle{}
		order := []int{}
		for _, a := range arts {
			if _, ok := byFile[a.FileNum]; !ok {
				order = append(order, a.FileNum)
			}
			byFile[a.FileNum] = append(byFile[a.FileNum], a)
		}
		sort.Ints(order)
		for _, fn := range order {
			doc.Files = append(doc.Files, makeFile(byFile[fn]))
		}
	} else {
		doc.Files = []nzbFile{makeFile(arts)}
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// makeFile builds one <file> from the articles of a single file, segments
// ordered as loaded and de-duped by part number.
func makeFile(arts []stagedArticle) nzbFile {
	first := arts[0]
	f := nzbFile{
		Poster:  strings.ToValidUTF8(first.Poster, "\uFFFD"),
		Date:    first.Posted.Unix(),
		Subject: strings.ToValidUTF8(first.Subject, "\uFFFD"),
		Groups:  nzbGroups{Group: []string{first.Group}},
	}
	seen := make(map[int]bool, len(arts))
	for _, a := range arts {
		if seen[a.PartNum] {
			continue
		}
		seen[a.PartNum] = true
		f.Segments.Segment = append(f.Segments.Segment, nzbSegment{
			Bytes: a.Bytes, Number: a.PartNum, Value: strings.Trim(a.MessageID, "<>"),
		})
	}
	return f
}

type nzbDoc struct {
	XMLName xml.Name  `xml:"nzb"`
	Xmlns   string    `xml:"xmlns,attr"`
	Files   []nzbFile `xml:"file"`
}

type nzbFile struct {
	Poster   string      `xml:"poster,attr"`
	Date     int64       `xml:"date,attr"`
	Subject  string      `xml:"subject,attr"`
	Groups   nzbGroups   `xml:"groups"`
	Segments nzbSegments `xml:"segments"`
}

type nzbGroups struct {
	Group []string `xml:"group"`
}

type nzbSegments struct {
	Segment []nzbSegment `xml:"segment"`
}

type nzbSegment struct {
	Bytes  int64  `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	Value  string `xml:",chardata"`
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// maxNZBBytes bounds NZB decompression. NZBs are text; even a 4000-file
// release's XML stays far under this — but in sink=host mode the health sweep
// gunzips the HOST catalogue's blobs, which include agent uploads, and an
// unbounded ReadAll turns one crafted tiny gzip into an OOM'd worker.
const maxNZBBytes = 128 << 20

func gunzipBytes(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxNZBBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxNZBBytes {
		return nil, fmt.Errorf("nzb decompresses past %d bytes — refusing", maxNZBBytes)
	}
	return out, nil
}

func summarize(arts []stagedArticle) (size int64, posted time.Time) {
	for _, a := range arts {
		size += a.Bytes
		if !a.Posted.IsZero() && (posted.IsZero() || a.Posted.Before(posted)) {
			posted = a.Posted
		}
	}
	return size, posted
}

// contentHashArticles is prod's content identity: sha256 over the SORTED
// segment message-ids, first 16 bytes as hex. It identifies the ARTICLES, not
// the name — a re-post of the same title with fresh articles hashes new (and
// can be indexed), while the same articles always collide (and dedup). The old
// hash-of-(group|base) meant two different releases sharing a subject collided
// forever and a re-post could never be indexed again.
func contentHashArticles(arts []stagedArticle) string {
	ids := make([]string, 0, len(arts))
	for _, a := range arts {
		if a.MessageID != "" {
			ids = append(ids, a.MessageID)
		}
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func safeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r', '\t':
			return '_'
		}
		return r
	}, s)
	if len(s) > 180 {
		s = s[:180]
	}
	return strings.TrimSpace(s)
}

// ── store methods for assembly ──────────────────────────────────────

type groupKey struct {
	Group string
	Base  string
}

// candidateGroups pre-filters likely-complete releases in SQL: single-file when
// distinct parts reach total_parts, multi-file when all file numbers are
// present. runBuild re-verifies each with isComplete (which checks per-file
// segment counts the SQL can't cheaply express).
func (s *PGStore) candidateGroups(ctx context.Context, limit int) ([]groupKey, candidateStats, error) {
	type row struct {
		Group string `db:"group_name"`
		Base  string `db:"base_subject"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// The multi-file arm counts only file numbers >= 1: a "[00/N]" header
		// post or an unnumbered companion sits at file_num 0 and counting it
		// satisfied MAX(total_files) one real file early. This is a cheap
		// PRE-filter — isComplete re-verifies per file on the loaded articles
		// — so it may stay optimistic (a set passed here and refused there
		// just waits), but it must not be optimistic in a way that admits
		// every [0/N]-companioned set the moment its last file starts.
		return tx.SelectContext(ctx, &rows,
			`SELECT group_name, base_subject FROM articles
			 GROUP BY group_name, base_subject
			 HAVING (bool_or(file_parts) = FALSE AND COUNT(DISTINCT part_num) >= MAX(total_parts))
			     OR (bool_or(file_parts) = TRUE  AND COUNT(DISTINCT file_num) FILTER (WHERE file_num >= 1) >= MAX(total_files))
			 LIMIT $1`, limit)
	})
	if err != nil {
		return nil, candidateStats{}, err
	}
	out := make([]groupKey, len(rows))
	for i, r := range rows {
		out[i] = groupKey{Group: r.Group, Base: r.Base}
	}
	// pg has no queue to be starved by: the SELECT recomputes completeness from
	// durable rows every pass, so nothing can be "waiting behind the draw" and
	// nothing can expire before its turn. Depth and Live equal what was found;
	// Fossil is structurally impossible here. Reporting them keeps the readout
	// meaningful in both modes rather than blank in one.
	st := candidateStats{ReadyDepth: int64(len(out)), Sampled: len(out), Live: len(out)}
	return out, st, nil
}

func (s *PGStore) groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error) {
	type row struct {
		MessageID  string       `db:"message_id"`
		Subject    string       `db:"subject"`
		Poster     string       `db:"poster"`
		Bytes      int64        `db:"bytes"`
		Posted     sql.NullTime `db:"posted"`
		Group      string       `db:"group_name"`
		PartNum    int          `db:"part_num"`
		TotalParts int          `db:"total_parts"`
		SegTotal   int          `db:"seg_total"`
		FileNum    int          `db:"file_num"`
		TotalFiles int          `db:"total_files"`
		FileParts  bool         `db:"file_parts"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT message_id, subject, poster, bytes, posted, group_name, part_num,
			        total_parts, seg_total, file_num, total_files, file_parts
			 FROM articles WHERE group_name = $1 AND base_subject = $2
			 ORDER BY file_num, part_num`, group, base)
	})
	if err != nil {
		return nil, err
	}
	out := make([]stagedArticle, len(rows))
	for i, r := range rows {
		out[i] = stagedArticle{
			MessageID: r.MessageID, Subject: r.Subject, Poster: r.Poster,
			Bytes: r.Bytes, Group: r.Group, PartNum: r.PartNum,
			TotalParts: r.TotalParts, SegTotal: r.SegTotal,
			FileNum: r.FileNum, TotalFiles: r.TotalFiles, FileParts: r.FileParts,
		}
		if r.Posted.Valid {
			out[i].Posted = r.Posted.Time
		}
	}
	return out, nil
}

type nzbRow struct {
	Title       string
	Filename    string
	Size        int64
	Group       string
	ContentHash string
	Posted      time.Time
	Data        []byte
	Tags        Tags
	CategoryID  int
}

func (s *PGStore) insertNzb(ctx context.Context, n nzbRow) (int64, bool, error) {
	var id int64
	inserted := false
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var posted sql.NullTime
		if !n.Posted.IsZero() {
			posted = sql.NullTime{Time: n.Posted, Valid: true}
		}
		// RETURNING id emits no row on a conflict, so ErrNoRows IS the
		// duplicate signal — the salvage path needs the id to hand the health
		// backend its verdict.
		err := tx.QueryRowContext(ctx,
			`INSERT INTO nzbs (title, filename, size, status, group_name, content_hash, posted_at,
			                   nzb_data, nzb_data_bytes, resolution, source, video_codec, audio, language, category_id)
			 VALUES ($1,$2,$3,'completed',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			 ON CONFLICT (content_hash) DO NOTHING
			 RETURNING id`,
			n.Title, n.Filename, n.Size, n.Group, n.ContentHash, posted, n.Data, len(n.Data),
			n.Tags.Resolution, n.Tags.Source, n.Tags.Codec, n.Tags.Audio, n.Tags.Language, n.CategoryID).Scan(&id)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	return id, inserted, err
}

// deleteStagedBatch removes many sets in one statement rather than one
// transaction each — the same reasoning as the redis path, where the cost is a
// round-trip per set.
func (s *PGStore) deleteStagedBatch(ctx context.Context, keys []groupKey) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	groups := make([]string, len(keys))
	bases := make([]string, len(keys))
	for i, k := range keys {
		groups[i] = k.Group
		bases[i] = k.Base
	}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// unnest pairs the two arrays positionally, so this deletes exactly the
		// (group, base) combinations given and never a cross product.
		_, err := tx.ExecContext(ctx,
			`DELETE FROM articles a
			  USING unnest($1::text[], $2::text[]) AS t(group_name, base_subject)
			  WHERE a.group_name = t.group_name AND a.base_subject = t.base_subject`,
			pq.Array(groups), pq.Array(bases))
		return err
	})
	if err != nil {
		return 0, err
	}
	// The count is SETS, not rows: RowsAffected here counts articles, and every
	// caller is measuring how much of the queue it drained.
	return len(keys), nil
}

func (s *PGStore) deleteStaged(ctx context.Context, group, base string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM articles WHERE group_name = $1 AND base_subject = $2`, group, base)
		return err
	})
}

// totalBytes sums a staged set's payload — the release size the sized junk
// rules are banded on.
func totalBytes(arts []stagedArticle) int64 {
	var n int64
	for _, a := range arts {
		n += a.Bytes
	}
	return n
}

// firstPoster returns the poster of the first article in a set. Every part of a
// release comes from one poster in practice, and prod matches on the same single
// value; scanning them all would let one spoofed part veto a whole release.
func firstPoster(arts []stagedArticle) string {
	if len(arts) == 0 {
		return ""
	}
	return arts[0].Poster
}

// classifyRelease runs the title-domain checks in prod's order and returns the
// decision inputs the build loop acts on: the extracted sanitised title, the
// category hint (explicit tag, else comic-archive sniff), the junk rule that
// fired (empty when clean OR when an explicit category tag vouched for the
// release — prod's bypass), and whether the title names a blocked file type.
// preClassify is everything decidable from the base subject alone — no
// articles, no Redis read, microseconds.
//
// It exists because the builder had the order backwards: it loaded every article
// in a set (HGETALL plus a JSON unmarshal per segment, measured at 16.2 ms) and
// only THEN asked whether the title was obviously junk. On this install the
// answer is usually yes — one sampled pass rejected 500 of 500 — so nearly all
// of that I/O bought a verdict the subject line had already given for free.
//
// The junk check runs with an UNKNOWN size deliberately: whichJunkRule passes 0,
// and the matcher skips every size-banded rule when the size is unknown. So this
// can only fire a title-shape rule, and can never reach a verdict the full sized
// check would disagree with.
func preClassify(base string) (title, junkRule string, blockedExt bool) {

	title = strings.TrimSpace(strings.ToValidUTF8(extractTitle(base), "\uFFFD"))
	if title == "" {
		title = "release"
	}
	if hasBlockedExtension(title) {
		return title, "", true
	}
	if parseCategoryTag(title) != "" {
		// An explicit category tag bypasses the junk engine, as prod's
		// assembler does.
		return title, "", false
	}
	return title, whichJunkRule(title), false
}

func classifyRelease(base string, arts []stagedArticle) (title, cat, junkRule string, blockedExt bool) {
	title, _, blockedExt = preClassify(base)
	if blockedExt {
		return title, "", "", true
	}
	cat = parseCategoryTag(title)
	if cat == "" {
		// Payload-free: every file in the set is PAR recovery data. Checked
		// here rather than by name, because the name no longer says — the
		// volume suffix is stripped so recovery files group with the release
		// they protect, and only the assembled set knows whether a release is
		// actually there. Reported under the historical rule name so existing
		// filter_hits attribution and the operator's mental model both hold.
		if allRecoveryVolumes(arts) {
			return title, "", "par2_volume", false
		}
		if junkRule = whichJunkRuleSized(title, totalBytes(arts)); junkRule != "" {
			return title, "", junkRule, false
		}
		if articlesContainComicArchive(arts) {
			cat = "Manga"
		}
	}
	return title, cat, "", false
}

// releaseSink stores one assembled release. Internal mode is the plugin's own
// minimal nzbs table (standalone installs, the demo); host mode is the
// ReleaseSink capability, so a rich host owns storage. Sibling of healthBackend
// — same two-implementation shape, and resolved ONCE per build pass (see
// resolveSink) rather than per release, so a host-misconfigured pass fails with
// a single error instead of one per candidate.
type releaseSink interface {
	// store returns the stored release's id so the salvage path can hand it
	// to the health backend; the normal build path ignores it. id is 0 when
	// created is false (duplicate).
	store(ctx context.Context, rel pluginapi.AssembledRelease) (id int64, created bool, err error)
}

type internalSink struct{ p *Plugin }

func (s internalSink) store(ctx context.Context, rel pluginapi.AssembledRelease) (int64, bool, error) {
	return s.p.st.insertNzb(ctx, nzbRow{
		Title: rel.Title, Filename: safeFilename(rel.Title) + ".nzb",
		Size: rel.SizeBytes, Group: rel.Group, ContentHash: rel.ContentHash,
		Posted: rel.PostedAt, Data: rel.NZBGz, Tags: parseTags(rel.Title),
		CategoryID: s.p.categoryFor(rel.Group, rel.Title),
	})
}

type hostSink struct{ sink pluginapi.ReleaseSink }

func (s hostSink) store(ctx context.Context, rel pluginapi.AssembledRelease) (int64, bool, error) {
	return s.sink.IngestAssembled(ctx, rel)
}

// resolveSink mirrors resolveHealthBackend: host mode without the capability
// refuses loudly — silently self-storing splits the catalogue across two tables,
// far worse than a visible stall that retries once the host build is deployed.
func (p *Plugin) resolveSink() (releaseSink, error) {
	if p.cfg.Sink == SinkHost {
		sink, ok := pluginapi.LookupReleaseSink(p.core)
		if !ok {
			return nil, fmt.Errorf(
				"sink=host but this host registered no ReleaseSink — deploy a host build that wires the release sink, or set plugins.usenet.sink=internal for a standalone catalogue")
		}
		return hostSink{sink: sink}, nil
	}
	return internalSink{p: p}, nil
}

// BuilderInfo is the NZB Builder's view of staging: how many articles are
// staged, how many distinct releases they form, how many are ready to assemble,
// and the largest still-incomplete releases (with unit progress) — so an admin
// can see WHY nothing is building (usually huge multi-file releases only
// partly crawled).
type BuilderInfo struct {
	StagedArticles int
	Releases       int
	Ready          int
	Pending        []PendingRelease
}

// PendingRelease is one incomplete staged release. Units are files for
// multi-file releases, else segments.
type PendingRelease struct {
	Base     string
	Have     int
	Need     int
	Segments int
	Multi    bool
}

// Pct is the unit-completion percentage (0-100).
func (p PendingRelease) Pct() int {
	if p.Need <= 0 {
		return 0
	}
	v := p.Have * 100 / p.Need
	if v > 100 {
		v = 100
	}
	return v
}

func (s *PGStore) builderInfo(ctx context.Context, limit int) (BuilderInfo, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var bi BuilderInfo
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &bi.StagedArticles, `SELECT COUNT(*) FROM articles`); err != nil {
			return err
		}
		// One GROUP BY over staging; the derived per-release "have/need units"
		// mirrors candidateGroups (files for multi-file, parts otherwise).
		// have mirrors candidateGroups' pre-filter, including its file_num >= 1
		// clause — the file-0 bucket ([00/N] headers, unnumbered companions)
		// is not one of the poster's N files, and counting it showed sets as
		// ready one file before the builder would take them.
		const setsCTE = `
			WITH sets AS (
			  SELECT bool_or(file_parts) AS multi,
			         CASE WHEN bool_or(file_parts) THEN COUNT(DISTINCT file_num) FILTER (WHERE file_num >= 1) ELSE COUNT(DISTINCT part_num) END AS have,
			         CASE WHEN bool_or(file_parts) THEN MAX(total_files)          ELSE MAX(total_parts)          END AS need,
			         base_subject, COUNT(*) AS segs
			  FROM articles GROUP BY group_name, base_subject
			)`
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.GetContext(ctx, &bi.Releases, setsCTE+` SELECT COUNT(*) FROM sets`); err != nil {
			return err
		}
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.GetContext(ctx, &bi.Ready, setsCTE+` SELECT COUNT(*) FROM sets WHERE need > 0 AND have >= need`); err != nil {
			return err
		}
		var rows []struct {
			Base  string `db:"base_subject"`
			Have  int    `db:"have"`
			Need  int    `db:"need"`
			Segs  int    `db:"segs"`
			Multi bool   `db:"multi"`
		}
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.SelectContext(ctx, &rows, setsCTE+`
			SELECT base_subject, have, need, segs, multi FROM sets
			WHERE NOT (need > 0 AND have >= need)
			ORDER BY segs DESC LIMIT $1`, limit); err != nil {
			return err
		}
		for _, r := range rows {
			bi.Pending = append(bi.Pending, PendingRelease{
				Base: r.Base, Have: r.Have, Need: r.Need, Segments: r.Segs, Multi: r.Multi,
			})
		}
		return nil
	})
	return bi, err
}

// notePoster records a build-stage outcome against a watched poster.
//
// Uses firstPoster — the same value the sink stores — so the trace names
// whoever the catalogue would have credited, not some other article's header.
func (p *Plugin) notePoster(arts []stagedArticle, reason, sample string) {
	who, ok := p.posterWatch.watched(firstPoster(arts))
	if !ok {
		return
	}
	p.posterHits.note(who, "build", reason, sample)
}

// titleReject is one set the pre-filter turned away, with the verdict needed to
// account for it.
type titleReject struct {
	key        groupKey
	title      string
	junkRule   string
	blockedExt bool
}

// splitByTitle partitions a draw into sets worth loading and sets already
// decided, using nothing but their subject lines.
//
// Extracted rather than inlined because the two judgements here are the ones
// worth getting wrong quietly: rejecting a set deletes it from staging without
// assembling it, and honouring the poster watch is what keeps attribution
// working when an operator is trying to find out why a known poster's releases
// never appeared. Neither was reachable by a test while it lived in the middle
// of a pass that needs Redis, a fleet and a lease to run at all.
//
// watchActive short-circuits the whole thing: attribution needs the articles, so
// when a watch is on, every set takes the slow path and nothing is traded for
// throughput.
func splitByTitle(keys []groupKey, watchActive bool) ([]groupKey, []titleReject) {
	if watchActive {
		return keys, nil
	}
	kept := keys[:0]
	var rejects []titleReject
	for _, k := range keys {
		title, junkRule, blockedExt := preClassify(k.Base)
		if !blockedExt && junkRule == "" {
			kept = append(kept, k)
			continue
		}
		rejects = append(rejects, titleReject{
			key: k, title: title, junkRule: junkRule, blockedExt: blockedExt,
		})
	}
	return kept, rejects
}
