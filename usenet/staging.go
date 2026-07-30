package usenet

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// stagingStore is the transient article-assembly buffer — the seam that lets a
// durable Postgres backend (pgStaging, today) and a best-effort Redis backend
// (redisStaging, Phase B — a verbatim lift of prod's pipeline) be swapped by
// config (plugins.usenet.staging: pg|redis). See README.md.
//
// The durable NZB output (the nzbs table) is written by PGStore in BOTH modes;
// only this buffer differs. The method set is exactly the ingest path
// (stageArticles), the builder's read/drain path (candidateGroups / groupArticles
// / deleteStaged), maintenance (prune and the sweeps), and a back-pressure
// signal (pressure). Everything downstream of "here are a set's articles"
// (isComplete -> buildNZB -> insertNzb) stays backend-agnostic on PGStore.
type stagingStore interface {
	stageArticles(ctx context.Context, arts []stagedArticle) (int, error)
	// candidateGroups returns sets ready to assemble, PLUS what the draw
	// looked like. The stats are not decoration: with a queue that can outgrow
	// the per-pass draw, "no releases appeared" has three different causes
	// (nothing completed / the queue is deeper than the draw / entries expired
	// before being drawn) and they are indistinguishable from the returned
	// keys alone.
	candidateGroups(ctx context.Context, limit int) ([]groupKey, candidateStats, error)
	groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error)
	deleteStaged(ctx context.Context, group, base string) error
	// demoteReady withdraws a set from the ready queue while leaving its
	// staged articles in place — the builder's move when its verification
	// refuses a candidate staging queued as complete. Without it a refused
	// entry has no exit: candidateGroups peeks, deleteStaged only fires on
	// success or a permanent drop, so the entry is re-drawn and re-refused
	// forever. Returns whether an entry was actually removed, so the caller
	// counts real withdrawals only. pg: (false, nil) — there is no standing
	// queue; the next draw recomputes candidacy from rows and an incomplete
	// set simply stops qualifying.
	demoteReady(ctx context.Context, group, base string) (bool, error)
	// deleteStagedBatch removes many sets in as few round-trips as possible.
	//
	// It exists because deleting one set costs one Redis round-trip, and the
	// builder's title pre-filter turned the junk path into almost nothing BUT
	// deletes — 500 round-trips a pass to discard sets it had already judged in
	// microseconds. Returns how many were removed.
	deleteStagedBatch(ctx context.Context, keys []groupKey) (int, error)
	// prune drops stale staging. pg: DELETE WHERE added_at < now()-horizon;
	// redis: no-op — the key TTL + inline hopeless-eviction handle it.
	prune(ctx context.Context) (int64, error)
	// pressure reports staging fullness 0.0-1.0 for the backfill loop's
	// back-pressure. pg: staged rows / maxRows; redis: used/maxmemory.
	pressure(ctx context.Context) (float64, error)
	// stagingInfo is the dashboard's staging readout. Each mode fills what it
	// answers CHEAPLY and leaves the rest zero; nothing here may cost a scan.
	stagingInfo(ctx context.Context) (stagingInfo, error)
	// readyDepth is how many COMPLETE sets are waiting to be assembled, in O(1).
	//
	// The backfill's pressure gate needs it to tell two very different situations
	// apart. Staging full with a deep ready queue means the builder is behind and
	// pausing the fetcher is right. Staging full with an EMPTY ready queue means
	// the memory is incomplete sets — releases missing segments that only the
	// fetcher can supply — and pausing then deadlocks: memory cannot fall until
	// sets complete, sets cannot complete until they are fetched, and the fetcher
	// is what was stopped.
	//
	// Returns -1 when the backend cannot answer cheaply, which callers must read
	// as "assume a backlog" and fall back to the plain hysteresis.
	readyDepth(ctx context.Context) (int64, error)
	// incompleteSets lists the largest staged-but-incomplete releases — the
	// "which releases are still missing articles" readout. NOT render-path:
	// the build pass samples it into telemetry (redis mode walks the active
	// sets with pipelined reads, which is fine once per pass and unacceptable
	// per page view).
	// groups is the active group names — the caller already holds them, and
	// passing them is what keeps the redis side to one O(1) sample per group
	// instead of discovering ~28 known keys with a full keyspace SCAN
	// (~35,000 sequential round trips at the 7M-key scale this backend
	// documents itself reaching).
	incompleteSets(ctx context.Context, limit int, groups []string) ([]pendingSet, error)
	// reapReadyQueue removes entries whose staged data is already gone. Only
	// redis has a queue that can accumulate them — pg recomputes completeness
	// from durable rows every pass and has nothing to reap — so the pg
	// implementation is a no-op. Bounded per call; returns scanned, removed.
	reapReadyQueue(ctx context.Context, maxScan int) (int, int, error)
	// sweepWalkPast evicts staged sets that can NEVER complete: still short of
	// their claimed totals, idle past grace, with their whole article span
	// inside fetched coverage — the walk has offered every article the set
	// could receive, so absence is final. cov maps group name → fetched spans;
	// the caller includes only judgeable groups (exactly one backbone with
	// recorded ranges — article numbers are per-backbone). Bounded by budget
	// per call with persistent cursors. Dead sets holding at least half their
	// claimed articles are returned (up to salvageCap) instead of evicted, for
	// the caller to assemble as broken releases; salvageCap 0 evicts them all.
	// pg: no-op — the prune horizon ages pg staging out, and the memory
	// pressure this relieves is a redis phenomenon.
	// margin is the frontier guard: a set is judged dead only when coverage
	// extends a full batch window beyond BOTH ends of its seen span, so a
	// release straddling a fetch frontier is never judgeable.
	sweepWalkPast(ctx context.Context, cov map[string][]articleRange, grace time.Duration, budget, salvageCap int, margin int64) (scanned, evicted int, salvage []groupKey, err error)
	// setSpan reads a staged set's recorded article-number span (0,0 when
	// unknown). Redis folds spans into the set meta at stage time; pg staging
	// never stored article numbers, so it answers 0s — completion-distance
	// instrumentation is a redis-mode measurement, like the walk-past sweep
	// it feeds.
	setSpan(ctx context.Context, group, base string) (lo, hi int64, err error)
}

// candidateStats describes one draw from the ready queue.
//
// ReadyDepth is the queue BEFORE the draw, so ReadyDepth > Sampled means work
// is waiting that this pass will not touch — and if that holds pass after pass
// while the queue grows, arrivals outpace the drain and entries age out before
// their turn. Fossil counts entries drawn whose set metadata was already gone:
// those completed, were queued, and then expired or were evicted. Every one is
// a release that no outcome, log line or counter would otherwise record.
type candidateStats struct {
	ReadyDepth int64
	Sampled    int
	Live       int
	Fossil     int
}

// Starved reports that the queue held more than this draw could take. One pass
// means nothing; sustained, it means arrivals outpace the drain and entries will
// expire waiting for a turn no matter how healthy each one is.
func (c candidateStats) Starved() bool { return c.ReadyDepth > int64(c.Sampled) }

// stagingInfo is the Index Stats card's staging section. Mode discriminates
// which fields are meaningful (redis: key/queue/memory; pg: row count).
type stagingInfo struct {
	Mode           string // "pg" | "redis"
	StagedArticles int64  // pg only: rows in the articles table
	Keys           int64  // redis only: DBSIZE (≈2 keys per staged release set)
	ReadyGroups    int64  // redis only: SCARD nzb:ready — sets awaiting assembly
	MemUsedBytes   int64  // redis only
	MemMaxBytes    int64  // redis only; 0 = unbounded

	// Eviction visibility. Without these, "pressure 100%" and "the release
	// vanished" are two unrelated observations; with them they are one fact.
	// Redis at maxmemory under an allkeys-* policy DELETES keys to admit
	// writes, and a forming release is exactly the kind of key it picks — cold
	// between crawl visits. The plugin previously read used/maxmemory only, so
	// this was invisible in every readout it had.
	EvictedKeys     int64  // redis only: cumulative, INFO stats
	ExpiredKeys     int64  // redis only: cumulative TTL expiries, for contrast
	MaxMemoryPolicy string // redis only: noeviction | allkeys-lru | volatile-* …
}

// EvictionRisk reports whether this Redis can silently destroy staged work.
// "noeviction" cannot — it refuses writes instead, which surfaces as an error
// the crawler reports. Every other policy discards keys quietly.
func (s stagingInfo) EvictionRisk() bool {
	return s.MaxMemoryPolicy != "" && s.MaxMemoryPolicy != "noeviction"
}

// pgStaging is the durable, never-lost backend: every staged article is a
// committed Postgres row, so a crash loses nothing (slower, unbounded until the
// prune sweep, but safe). It reuses PGStore's staging methods and adds the
// config-driven prune horizon + a row-count pressure signal. `limits` is read
// per-call so the admin knobs apply live without a restart.
type pgStaging struct {
	*PGStore
	limits func(context.Context) (maxRows, pruneHours int)
}

func newPGStaging(pg *PGStore, limits func(context.Context) (int, int)) *pgStaging {
	return &pgStaging{PGStore: pg, limits: limits}
}

func (s *pgStaging) prune(ctx context.Context) (int64, error) {
	_, hours := s.limits(ctx)
	return s.PGStore.pruneStagedOlderThan(ctx, hours)
}

// readyDepth is unanswerable cheaply in pg mode: there is no standing ready
// queue, completeness is computed per draw, and counting it means the same
// aggregate the draw itself runs. -1 tells the caller to assume a backlog and
// keep the plain hysteresis, which is exactly the behaviour this mode had before
// the probe existed.
func (s *pgStaging) readyDepth(ctx context.Context) (int64, error) { return -1, nil }

func (s *pgStaging) stagingInfo(ctx context.Context) (stagingInfo, error) {
	n, err := s.PGStore.stagedCount(ctx)
	return stagingInfo{Mode: "pg", StagedArticles: int64(n)}, err
}

func (s *pgStaging) incompleteSets(ctx context.Context, limit int, _ []string) ([]pendingSet, error) {
	bi, err := s.PGStore.builderInfo(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]pendingSet, len(bi.Pending))
	for i, pr := range bi.Pending {
		out[i] = pendingSet{
			Base: pr.Base, Have: pr.Have, Need: pr.Need,
			Segments: pr.Segments, Multi: pr.Multi,
		}
	}
	return out, nil
}

func (s *pgStaging) pressure(ctx context.Context) (float64, error) {
	maxRows, _ := s.limits(ctx)
	if maxRows <= 0 {
		return 0, nil
	}
	n, err := s.PGStore.stagedCount(ctx)
	if err != nil {
		return 0, err
	}
	r := float64(n) / float64(maxRows)
	if r > 1 {
		r = 1
	}
	return r, nil
}

var _ stagingStore = (*pgStaging)(nil)

// newStaging selects the staging backend by config mode. `redis` fails fast when
// the host has no Redis (core.Redis nil) rather than silently running pg — a
// mode/behavior mismatch is worse than a boot error (README.md).
func newStaging(mode StagingMode, pg *PGStore, redisSvc core.RedisService, limits func(context.Context) (int, int), ttlHours func(context.Context) int, onEvict func(int), report func(context.Context, string, error)) (stagingStore, error) {
	switch mode {
	case "", StagingPG:
		return newPGStaging(pg, limits), nil
	case StagingRedis:
		if redisSvc == nil || redisSvc.Client() == nil {
			return nil, fmt.Errorf("staging mode redis requires Redis, but the host has none configured (core.Redis is nil) — configure the host's Redis or use staging: pg")
		}
		return newRedisStaging(redisSvc.Client(), ttlHours, onEvict, report), nil
	default:
		return nil, fmt.Errorf("unknown staging mode %q (want pg|redis)", mode)
	}
}

// reapReadyQueue is a no-op for pg staging: candidateGroups recomputes
// completeness from durable rows on every pass, so there is no queue to hold
// stale entries and nothing to sweep.
func (s *pgStaging) reapReadyQueue(ctx context.Context, maxScan int) (int, int, error) {
	return 0, 0, nil
}

// demoteReady is a no-op for pg staging for the same reason as the reaper:
// candidacy is recomputed from rows every draw, so an incomplete set stops
// qualifying by itself. false so the caller does not count a withdrawal that
// never happened.
func (s *pgStaging) demoteReady(ctx context.Context, group, base string) (bool, error) {
	return false, nil
}

// sweepWalkPast is a no-op for pg staging: rows age out on the prune horizon,
// and the memory ceiling the sweep protects is a redis phenomenon.
func (s *pgStaging) sweepWalkPast(ctx context.Context, cov map[string][]articleRange, grace time.Duration, budget, salvageCap int, margin int64) (int, int, []groupKey, error) {
	return 0, 0, nil, nil
}

// setSpan is unknowable in pg staging: article numbers were never stored.
func (s *pgStaging) setSpan(ctx context.Context, group, base string) (int64, int64, error) {
	return 0, 0, nil
}
