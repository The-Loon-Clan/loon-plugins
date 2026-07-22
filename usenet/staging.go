package usenet

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

// stagingStore is the transient article-assembly buffer — the seam that lets a
// durable Postgres backend (pgStaging, today) and a best-effort Redis backend
// (redisStaging, Phase B — a verbatim lift of prod's pipeline) be swapped by
// config (plugins.usenet.staging: pg|redis). See USENET-STAGING-MODES.md.
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
// mode/behavior mismatch is worse than a boot error (USENET-STAGING-MODES.md).
func newStaging(mode string, pg *PGStore, redisSvc core.RedisService, limits func(context.Context) (int, int)) (stagingStore, error) {
	switch mode {
	case "", "pg":
		return newPGStaging(pg, limits), nil
	case "redis":
		if redisSvc == nil || redisSvc.Client() == nil {
			return nil, fmt.Errorf("staging mode redis requires Redis, but the host has none configured (core.Redis is nil) — configure the host's Redis or use staging: pg")
		}
		return newRedisStaging(redisSvc.Client()), nil
	default:
		return nil, fmt.Errorf("unknown staging mode %q (want pg|redis)", mode)
	}
}
