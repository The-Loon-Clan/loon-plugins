package usenet

import (
	"context"
	"sort"
	"sync"

	"github.com/jmoiron/sqlx"
)

// Accounting for what the build pass does with each staged release set.
//
// The pass used to report built / blocked-ext / blacklisted and nothing else,
// which hid the two buckets that matter most when assembly stalls: sets that
// are not complete yet, and sets the sink rejects as already-known. A site
// staging 146 million articles and producing 260 NZBs looked, from the log,
// like a pass doing its job.
//
// Every branch of buildLocked now names its outcome, so the reasons add up to
// the candidates examined. That invariant is the point — if they ever fail to,
// a branch has been added without accounting for it, and the test asserts it.

// buildOutcome is the closed set of things that can happen to one candidate
// set. Typed rather than bare strings because these drive both a stored row and
// a log line: a mistyped literal would silently mint a new bucket that looks
// like real data.
type buildOutcome string

const (
	// outcomeBuilt — assembled and accepted by the sink.
	outcomeBuilt buildOutcome = "built"
	// outcomeIncomplete — still missing articles. NOT a drop: the set stays
	// staged and is reconsidered next pass. Expected to dominate on a healthy
	// site with an active crawl, which is exactly why it needs a name — an
	// unnamed majority is indistinguishable from a leak.
	outcomeIncomplete buildOutcome = "incomplete"
	// outcomeEmpty — the candidate had no articles at all when loaded. Distinct
	// from incomplete: it means the staging entry and its articles disagree,
	// which is a consistency problem rather than a waiting one.
	outcomeEmpty buildOutcome = "empty"
	// outcomeDuplicate — assembled fine, but the sink already had it, so
	// nothing was created. Dropped from staging. Was previously invisible: the
	// pass counted only creations, so a pass that deduped every set reported
	// "built 0" with no explanation.
	outcomeDuplicate buildOutcome = "duplicate"
	// outcomeBlockedExt / outcomeBlacklist / outcomeJunk — policy drops. The
	// rule-level attribution stays in filter_hits; these are the totals.
	outcomeBlockedExt buildOutcome = "blocked_ext"
	outcomeBlacklist  buildOutcome = "blacklist"
	outcomeJunk       buildOutcome = "junk"
	// The error outcomes. Each leaves the set staged for a later pass except
	// where noted in buildLocked, so a persistent one means a set that will be
	// retried forever — worth seeing as a rising count rather than as a stream
	// of individually-forgettable error rows.
	outcomeLoadError  buildOutcome = "load_error"
	outcomeXMLError   buildOutcome = "xml_error"
	outcomeGzipError  buildOutcome = "gzip_error"
	outcomeStoreError buildOutcome = "store_error"
)

// buildOutcomes accumulates one pass's outcomes in memory, flushed once at the
// end. Deliberately the same shape as filterHits — accumulate, drain, upsert —
// because it is the same problem: per-set writes on a 500-set drain would cost
// more than the work being measured.
type buildOutcomes struct {
	mu  sync.Mutex
	out map[buildOutcome]*outcomeVal
}

type outcomeVal struct {
	count  int64
	sample string
}

func newBuildOutcomes() *buildOutcomes {
	return &buildOutcomes{out: make(map[buildOutcome]*outcomeVal)}
}

// note records one candidate's outcome. sample is its base subject; the FIRST
// of a batch is kept, so the sample stays stable while a long pass runs rather
// than moving under an admin who is reading it.
//
// nil-safe: the build path takes this optionally so pure-function tests need no
// plugin, and accounting must never change what the pass actually does.
func (b *buildOutcomes) note(reason buildOutcome, sample string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.out[reason]
	if !ok {
		v = &outcomeVal{}
		b.out[reason] = v
	}
	v.count++
	if v.sample == "" {
		v.sample = sample
	}
}

// total returns the count for one reason. Test/log helper.
func (b *buildOutcomes) total(reason buildOutcome) int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.out[reason]; ok {
		return v.count
	}
	return 0
}

// drain takes the accumulated counts and resets, so a flush failure loses one
// pass's numbers rather than double-counting them on the next.
func (b *buildOutcomes) drain() map[buildOutcome]*outcomeVal {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.out
	b.out = make(map[buildOutcome]*outcomeVal)
	return out
}

// sortedOutcomeKeys gives the flush a deterministic order, which keeps the
// upserts in a stable sequence and makes a failing test readable.
func sortedOutcomeKeys(m map[buildOutcome]*outcomeVal) []buildOutcome {
	keys := make([]buildOutcome, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// recordBuildOutcomes upserts one pass's counts into today's row per reason.
func (s *PGStore) recordBuildOutcomes(ctx context.Context, out map[buildOutcome]*outcomeVal) error {
	if len(out) == 0 {
		return nil
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, k := range sortedOutcomeKeys(out) {
			v := out[k]
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO build_outcomes (day, reason, total_count, last_sample)
				 VALUES (CURRENT_DATE, $1, $2, $3)
				 ON CONFLICT (day, reason) DO UPDATE
				   SET total_count  = build_outcomes.total_count + EXCLUDED.total_count,
				       last_sample  = CASE WHEN EXCLUDED.last_sample <> ''
				                           THEN EXCLUDED.last_sample
				                           ELSE build_outcomes.last_sample END,
				       last_seen_at = now()`,
				string(k), v.count, v.sample); err != nil {
				return err
			}
		}
		return nil
	})
}

// flushBuildOutcomes persists the pass's accounting. Best-effort by design: the
// releases are already built, and losing a diagnostic counter must not fail a
// build pass — but it is reported, because a silently broken diagnostic is
// worse than none.
func (p *Plugin) flushBuildOutcomes(ctx context.Context) {
	out := p.outcomes.drain()
	if len(out) == 0 {
		return
	}
	if err := p.st.recordBuildOutcomes(ctx, out); err != nil {
		p.reportErr(ctx, "usenet/build-outcomes", err)
	}
}
