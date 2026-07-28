package usenet

import (
	"context"
	"sort"

	"github.com/jmoiron/sqlx"
)

// Storage for the poster watch. Mirrors blacklist_store.go's arrangement
// deliberately: same accumulate-in-memory, flush-once-per-pass shape, same
// best-effort error handling, so there is one pattern to understand rather than
// two.

// posterWatchPatterns returns the enabled watch patterns.
func (s *PGStore) posterWatchPatterns(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT pattern FROM poster_watch WHERE enabled = TRUE ORDER BY pattern`)
	})
	return out, err
}

// setPosterWatch adds or updates one watched poster.
func (s *PGStore) setPosterWatch(ctx context.Context, pattern, note string, enabled bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO poster_watch (pattern, note, enabled) VALUES ($1,$2,$3)
			 ON CONFLICT (pattern) DO UPDATE SET note = EXCLUDED.note, enabled = EXCLUDED.enabled`,
			pattern, note, enabled)
		return err
	})
}

func (s *PGStore) deletePosterWatch(ctx context.Context, pattern string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM poster_watch WHERE pattern = $1`, pattern)
		return err
	})
}

// posterHitRow is one (poster, stage, reason) tally for the admin view.
type posterHitRow struct {
	Poster  string `db:"poster"`
	Stage   string `db:"stage"`
	Reason  string `db:"reason"`
	Count   int64  `db:"total_count"`
	Sample  string `db:"last_sample"`
	LastAt  string `db:"last_at"`
	FirstAt string `db:"first_at"`
}

// posterHitRows returns the tallies newest-activity first.
func (s *PGStore) posterHitRows(ctx context.Context, limit int) ([]posterHitRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []posterHitRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT poster, stage, reason, total_count, last_sample,
			        to_char(last_seen_at,  'YYYY-MM-DD HH24:MI') AS last_at,
			        to_char(first_seen_at, 'YYYY-MM-DD HH24:MI') AS first_at
			   FROM poster_hits
			  ORDER BY poster, total_count DESC
			  LIMIT $1`, limit)
	})
	return out, err
}

// recordPosterHits folds a pass's tallies in. Counts ADD, so the table is a
// running total across restarts — a poster whose releases fail once an hour is
// invisible in any single pass.
func (s *PGStore) recordPosterHits(ctx context.Context, hits map[posterHitKey]*posterHitVal) error {
	if len(hits) == 0 {
		return nil
	}
	keys := make([]posterHitKey, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	// Sorted so concurrent workers touch rows in the same order and cannot
	// deadlock against each other — same reasoning as sortedHitKeys.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].poster != keys[j].poster {
			return keys[i].poster < keys[j].poster
		}
		if keys[i].stage != keys[j].stage {
			return keys[i].stage < keys[j].stage
		}
		return keys[i].reason < keys[j].reason
	})
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, k := range keys {
			v := hits[k]
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO poster_hits (poster, stage, reason, total_count, last_sample)
				 VALUES ($1,$2,$3,$4,$5)
				 ON CONFLICT (poster, stage, reason) DO UPDATE
				   SET total_count  = poster_hits.total_count + EXCLUDED.total_count,
				       last_sample  = CASE WHEN EXCLUDED.last_sample <> ''
				                           THEN EXCLUDED.last_sample
				                           ELSE poster_hits.last_sample END,
				       last_seen_at = now()`,
				k.poster, k.stage, k.reason, v.count, v.sample); err != nil {
				return err
			}
		}
		return nil
	})
}

// loadPosterWatch refreshes the in-memory matcher for this pass. Read once per
// pass rather than per article, so adding a pattern takes effect on the next
// pass and costs nothing in between.
func (p *Plugin) loadPosterWatch(ctx context.Context) {
	pats, err := p.st.posterWatchPatterns(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/poster-watch-load", err)
		return
	}
	p.posterWatch = newPosterWatch(pats)
}

// flushPosterHits persists the pass's tallies. Best-effort: these are
// observability counters and losing a pass of them must never fail a crawl.
func (p *Plugin) flushPosterHits(ctx context.Context) {
	hits := p.posterHits.drain()
	if len(hits) == 0 {
		return
	}
	if err := p.st.recordPosterHits(ctx, hits); err != nil {
		p.reportErr(ctx, "usenet/poster-hits", err)
	}
}
