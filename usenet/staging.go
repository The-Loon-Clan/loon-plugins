package usenet

import (
	"context"
	"fmt"

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
// / deleteStaged), maintenance (deleteJunkStaged / prune), and a back-pressure
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
	deleteJunkStaged(ctx context.Context) (int64, error)
	// prune drops stale staging. pg: DELETE WHERE added_at < now()-horizon;
	// redis: no-op — the key TTL + inline hopeless-eviction handle it.
	prune(ctx context.Context) (int64, error)
	// pressure reports staging fullness 0.0-1.0 for the backfill loop's
	// back-pressure. pg: staged rows / maxRows; redis: used/maxmemory.
	pressure(ctx context.Context) (float64, error)
	// stagingInfo is the dashboard's staging readout. Each mode fills what it
	// answers CHEAPLY and leaves the rest zero; nothing here may cost a scan.
	stagingInfo(ctx context.Context) (stagingInfo, error)
	// incompleteSets lists the largest staged-but-incomplete releases — the
	// "which releases are still missing articles" readout. NOT render-path:
	// the build pass samples it into telemetry (redis mode walks the active
	// sets with pipelined reads, which is fine once per pass and unacceptable
	// per page view).
	incompleteSets(ctx context.Context, limit int) ([]pendingSet, error)
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

func (s *pgStaging) stagingInfo(ctx context.Context) (stagingInfo, error) {
	n, err := s.PGStore.stagedCount(ctx)
	return stagingInfo{Mode: "pg", StagedArticles: int64(n)}, err
}

func (s *pgStaging) incompleteSets(ctx context.Context, limit int) ([]pendingSet, error) {
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
