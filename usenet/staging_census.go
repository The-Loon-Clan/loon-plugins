package usenet

import (
	"context"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
)

// Staging census: one row per build pass recording the health of the pipeline
// between "articles are staged" and "a release exists".
//
// That stretch was completely unobservable, and it cost a day of diagnosis. A
// release could be staged, complete, queue itself, and then vanish without
// appearing in build_outcomes, the job log, the error log or any counter — the
// three mechanisms that can destroy it (Redis eviction under maxmemory, the
// staging TTL, and a ready queue deeper than the per-pass draw) all remove work
// silently and none of them were measured. Point-in-time readings could not
// separate them; a time series can, because each leaves a different signature
// in the deltas between consecutive rows.

// stagingCensus is one sample. Cumulative counters (evicted/expired) are stored
// raw so the reader takes deltas — resetting them here would lose the ability
// to detect a Redis restart, which zeroes them.
type stagingCensus struct {
	ReadyDepth      int64
	Sampled         int
	LiveCandidates  int
	FossilDropped   int
	RedisKeys       int64
	MemUsedBytes    int64
	MemMaxBytes     int64
	EvictedKeys     int64
	ExpiredKeys     int64
	MaxMemoryPolicy string
	PendingSets     int
	HopelessSeen    int
	WalkPast        int
}

// recordStagingCensus appends one sample.
//
// Best-effort by design: this is observability, and a failed INSERT must never
// fail a build pass. It is reported rather than swallowed, though — a census
// that quietly stops writing is worse than none, because the gap in the series
// reads as "nothing happened".
func (s *PGStore) recordStagingCensus(ctx context.Context, c stagingCensus) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO staging_census
			   (ready_depth, sampled, live_candidates, fossil_dropped,
			    redis_keys, mem_used_bytes, mem_max_bytes,
			    evicted_keys, expired_keys, maxmemory_policy,
			    pending_sets, hopeless_seen, walk_past)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			c.ReadyDepth, c.Sampled, c.LiveCandidates, c.FossilDropped,
			c.RedisKeys, c.MemUsedBytes, c.MemMaxBytes,
			c.EvictedKeys, c.ExpiredKeys, c.MaxMemoryPolicy,
			c.PendingSets, c.HopelessSeen, c.WalkPast)
		return err
	})
}

// censusRow is one sample as read back, with the deltas already computed
// against the PREVIOUS sample — which is the only form in which the cumulative
// counters mean anything.
type censusRow struct {
	At              time.Time `db:"at"`
	ReadyDepth      int64     `db:"ready_depth"`
	Sampled         int       `db:"sampled"`
	LiveCandidates  int       `db:"live_candidates"`
	FossilDropped   int       `db:"fossil_dropped"`
	RedisKeys       int64     `db:"redis_keys"`
	MemUsedBytes    int64     `db:"mem_used_bytes"`
	MemMaxBytes     int64     `db:"mem_max_bytes"`
	EvictedKeys     int64     `db:"evicted_keys"`
	ExpiredKeys     int64     `db:"expired_keys"`
	MaxMemoryPolicy string    `db:"maxmemory_policy"`
	PendingSets     int       `db:"pending_sets"`
	HopelessSeen    int       `db:"hopeless_seen"`
	WalkPast        int       `db:"walk_past"`

	// Deltas against the previous sample. Negative means the counter reset
	// (Redis restarted, or for hopeless a worker restart); reported as 0
	// rather than as a nonsensical negative, because a restart is not "minus
	// four million evictions".
	EvictedDelta  int64 `db:"evicted_delta"`
	ExpiredDelta  int64 `db:"expired_delta"`
	HopelessDelta int64 `db:"hopeless_delta"`
	WalkPastDelta int64 `db:"walk_past_delta"`
}

// PendingLabel renders the pending-sets sample, distinguishing "none pending"
// from "no sample taken" (recorded as -1). The sentinel is process-local: it
// means this worker has not completed a sample since it started — after a
// restart the first rows of a pass show "—" until the pass-start sample lands,
// and a failed sample preserves the previous figure rather than writing 0.
func (c censusRow) PendingLabel() string {
	if c.PendingSets < 0 {
		return "—"
	}
	return strconv.Itoa(c.PendingSets)
}

// stagingCensusRows returns the most recent samples, newest first.
func (s *PGStore) stagingCensusRows(ctx context.Context, limit int) ([]censusRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 60
	}
	var out []censusRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT at, ready_depth, sampled, live_candidates, fossil_dropped,
			        redis_keys, mem_used_bytes, mem_max_bytes,
			        evicted_keys, expired_keys, maxmemory_policy,
			        pending_sets, hopeless_seen, walk_past,
			        GREATEST(evicted_keys - LAG(evicted_keys) OVER (ORDER BY at), 0) AS evicted_delta,
			        GREATEST(expired_keys - LAG(expired_keys) OVER (ORDER BY at), 0) AS expired_delta,
			        GREATEST(hopeless_seen - LAG(hopeless_seen) OVER (ORDER BY at), 0) AS hopeless_delta,
			        GREATEST(walk_past - LAG(walk_past) OVER (ORDER BY at), 0) AS walk_past_delta
			   FROM staging_census
			  ORDER BY at DESC
			  LIMIT $1`, limit)
	})
	return out, err
}

// pruneStagingCensus keeps the series bounded. One row per build pass is ~5/hour
// per worker, so a week is a few hundred rows — small, but unbounded growth in a
// diagnostic table is how diagnostics become the problem they were added to
// find.
func (s *PGStore) pruneStagingCensus(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 14
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// make_interval rather than a formatted literal: the value is a clamped
		// int and could not be hostile, but a parameterised query needs no such
		// argument to be safe, and the lint should not have to be told.
		res, err := tx.ExecContext(ctx,
			`DELETE FROM staging_census WHERE at < now() - make_interval(days => $1)`, keepDays)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// MemPct renders memory use as a percentage, or -1 when Redis has no ceiling
// (maxmemory 0). Zero would read as "empty", which is the opposite of
// "unbounded".
func (c censusRow) MemPct() float64 {
	if c.MemMaxBytes <= 0 {
		return -1
	}
	return 100 * float64(c.MemUsedBytes) / float64(c.MemMaxBytes)
}

// EvictionRisk reports that this Redis can silently destroy staged work.
// "noeviction" refuses writes instead, which surfaces as a reported error;
// every other policy discards keys quietly, which is what made a day of
// vanishing releases leave no trace.
func (c censusRow) EvictionRisk() bool {
	return c.MaxMemoryPolicy != "" && c.MaxMemoryPolicy != "noeviction"
}

// Starved reports whether the ready queue holds more than this pass could draw.
// Sustained across passes, that means arrivals outpace the drain and entries
// will age out before their turn regardless of how healthy each one is.
func (c censusRow) Starved() bool { return c.ReadyDepth > int64(c.Sampled) }

// takeStagingCensus samples staging health for one build pass and appends it to
// the series.
//
// Best-effort throughout: every failure degrades to a partial row rather than
// costing the pass, because a build that fails on account of its own telemetry
// would be a strictly worse bug than the one this exists to find.
func (p *Plugin) takeStagingCensus(ctx context.Context, drawn candidateStats, pendingSeen int) {
	if p.staging == nil || p.st == nil {
		return
	}
	c := stagingCensus{
		ReadyDepth:     drawn.ReadyDepth,
		Sampled:        drawn.Sampled,
		LiveCandidates: drawn.Live,
		FossilDropped:  drawn.Fossil,
		// The plugin's own deliberate shedding, cumulative like the Redis
		// counters so the reader takes deltas. The column existed from the
		// start but was never assigned — every row read "shed nothing", which
		// is a factual-looking claim, not an unmeasured one, and it also
		// masked the inline eviction being dead code for its entire life.
		HopelessSeen: int(p.tel.evictedCount()),
		WalkPast:     int(p.tel.walkPastCount()),
	}
	if info, err := p.staging.stagingInfo(ctx); err == nil {
		c.RedisKeys = info.Keys
		c.MemUsedBytes = info.MemUsedBytes
		c.MemMaxBytes = info.MemMaxBytes
		c.EvictedKeys = info.EvictedKeys
		c.ExpiredKeys = info.ExpiredKeys
		c.MaxMemoryPolicy = info.MaxMemoryPolicy
	} else {
		// An INFO failure must not write an all-zero row: zeros render as
		// "unbounded, never evicting" — indistinguishable from healthy, which
		// is the exact shape of the Redis 6.2 multi-section-INFO bug that hid
		// the 97M-key eviction, re-entered through the error path. Keep the
		// fields stagingInfo filled before it failed, stamp the policy with a
		// sentinel so the row reads as UNREADABLE rather than unbounded, and
		// say so where the operator looks for failures. This probe failing is
		// most likely exactly when the server is at its ceiling and slow.
		c.RedisKeys = info.Keys
		c.MaxMemoryPolicy = "(unavailable)"
		p.reportErr(ctx, "usenet/staging-census-info", err)
	}
	// The pass's own sample, passed in — walking the active sets a second time
	// doubled the cost of the most expensive read in the pipeline. The comment
	// here used to claim this reused the pass's list; it did not, and at three
	// million staged sets that gap was the difference between an observation
	// and an outage.
	c.PendingSets = pendingSeen
	if err := p.st.recordStagingCensus(ctx, c); err != nil {
		p.reportErr(ctx, "usenet/staging-census", err)
	}
}

// newestMigration reports the highest-numbered migration this binary embeds.
//
// A deploy marker for the PLUGIN. app_versions carries the site's git SHA and
// the plugin lives in a different repository, so "did my plugin change actually
// ship" was answerable only by comparing commit timestamps against container
// restart times — inference, and wrong at least once. The embedded migration
// list travels with the binary, so this cannot disagree with the code around it.
func newestMigration() string {
	entries, err := migrations.ReadDir("migrations")
	if err != nil || len(entries) == 0 {
		return ""
	}
	newest := ""
	for _, e := range entries {
		if n := e.Name(); n > newest {
			newest = n
		}
	}
	return newest
}
