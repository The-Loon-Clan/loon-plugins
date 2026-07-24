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
	candidateGroups(ctx context.Context, limit int) ([]groupKey, error)
	groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error)
	deleteStaged(ctx context.Context, group, base string) error
	deleteJunkStaged(ctx context.Context) (int64, error)
	// prune drops stale staging. pg: DELETE WHERE added_at < now()-horizon;
	// redis (Phase B): no-op — the key TTL + inline hopeless-eviction handle it.
	prune(ctx context.Context) (int64, error)
	// pressure reports staging fullness 0.0-1.0 for back-pressure. Phase B wires
	// it into the backfill loop. pg: staged rows / maxRows; redis: used/maxmemory.
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

// stagingInfo is the Index Stats card's staging section. Mode discriminates
// which fields are meaningful (redis: key/queue/memory; pg: row count).
type stagingInfo struct {
	Mode           string // "pg" | "redis"
	StagedArticles int64  // pg only: rows in the articles table
	Keys           int64  // redis only: DBSIZE (≈2 keys per staged release set)
	ReadyGroups    int64  // redis only: LLEN nzb:ready — sets awaiting assembly
	MemUsedBytes   int64  // redis only
	MemMaxBytes    int64  // redis only; 0 = unbounded
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
func newStaging(mode StagingMode, pg *PGStore, redisSvc core.RedisService, limits func(context.Context) (int, int), onEvict func(int)) (stagingStore, error) {
	switch mode {
	case "", StagingPG:
		return newPGStaging(pg, limits), nil
	case StagingRedis:
		if redisSvc == nil || redisSvc.Client() == nil {
			return nil, fmt.Errorf("staging mode redis requires Redis, but the host has none configured (core.Redis is nil) — configure the host's Redis or use staging: pg")
		}
		return newRedisStaging(redisSvc.Client(), onEvict), nil
	default:
		return nil, fmt.Errorf("unknown staging mode %q (want pg|redis)", mode)
	}
}
