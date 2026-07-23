package usenet

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// ── blacklist CRUD ──────────────────────────────────────────────────

func (s *PGStore) blacklistRules(ctx context.Context) ([]blacklistRule, error) {
	type row struct {
		ID      int64  `db:"id"`
		Pattern string `db:"pattern"`
		Field   string `db:"field"`
		Enabled bool   `db:"enabled"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT id, pattern, field, enabled FROM blacklist_regexes ORDER BY id`)
	})
	if err != nil {
		return nil, err
	}
	out := make([]blacklistRule, len(rows))
	for i, r := range rows {
		out[i] = blacklistRule{ID: r.ID, Pattern: r.Pattern, Field: r.Field, Enabled: r.Enabled}
	}
	return out, nil
}

// addBlacklistRule validates before storing. Rejecting an uncompilable pattern
// here — rather than letting the loader skip it later — is the difference
// between the admin seeing "invalid regex" as they type it and a silently
// inert rule they believe is protecting them.
func (s *PGStore) addBlacklistRule(ctx context.Context, pattern, field string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	if !validBlacklistField(field) {
		return fmt.Errorf("field must be one of %s", strings.Join(blacklistFields, ", "))
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("invalid regex: %w", err)
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO blacklist_regexes (pattern, field) VALUES ($1, $2)`, pattern, field)
		return err
	})
}

func (s *PGStore) deleteBlacklistRule(ctx context.Context, id int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM blacklist_regexes WHERE id = $1`, id)
		return err
	})
}

func (s *PGStore) toggleBlacklistRule(ctx context.Context, id int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE blacklist_regexes SET enabled = NOT enabled WHERE id = $1`, id)
		return err
	})
}

// ── filter hit counters ─────────────────────────────────────────────

// filterHitRow is one row of the filter-hits page.
type filterHitRow struct {
	Kind       string    `db:"kind"`
	Rule       string    `db:"rule"`
	TotalCount int64     `db:"total_count"`
	LastSample string    `db:"last_sample"`
	FirstSeen  time.Time `db:"first_seen_at"`
	LastSeen   time.Time `db:"last_seen_at"`
}

// recordFilterHits folds a pass's accumulated counters into the table. Counts
// ADD rather than replace, so the table is a running total across restarts —
// which is what makes a rarely-firing rule visible at all.
func (s *PGStore) recordFilterHits(ctx context.Context, hits map[filterHitKey]*filterHitVal) error {
	if len(hits) == 0 {
		return nil
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, k := range sortedHitKeys(hits) {
			v := hits[k]
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO filter_hits (kind, rule, total_count, last_sample)
				 VALUES ($1,$2,$3,$4)
				 ON CONFLICT (kind, rule) DO UPDATE
				   SET total_count  = filter_hits.total_count + EXCLUDED.total_count,
				       last_sample  = CASE WHEN EXCLUDED.last_sample <> ''
				                           THEN EXCLUDED.last_sample
				                           ELSE filter_hits.last_sample END,
				       last_seen_at = now()`,
				k.kind, k.rule, v.count, v.sample); err != nil {
				return err
			}
		}
		return nil
	})
}

// filterHitRows returns every counter, busiest first.
func (s *PGStore) filterHitRows(ctx context.Context) ([]filterHitRow, error) {
	var rows []filterHitRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT kind, rule, total_count, last_sample, first_seen_at, last_seen_at
			   FROM filter_hits ORDER BY total_count DESC, rule`)
	})
	return rows, err
}

// resetFilterHits clears the counters. Needed after tuning a rule: the whole
// point of the page is "is this rule pulling its weight", and an old total from
// a pattern that has since been rewritten answers a question nobody asked.
func (s *PGStore) resetFilterHits(ctx context.Context) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM filter_hits`)
		return err
	})
}

// ── plugin-side load / flush ────────────────────────────────────────

// reloadBlacklist swaps the in-memory matcher. Called at the head of a build
// pass, so admin edits apply on the next cycle without a restart.
func (p *Plugin) reloadBlacklist(ctx context.Context) {
	rules, err := p.st.blacklistRules(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/blacklist-load", err)
		return
	}
	m, errs := newBlacklistMatcher(rules)
	for _, e := range errs {
		// Stored patterns are validated on the way in, so reaching here means a
		// row was edited directly in SQL. Report it: the rule is inert, and
		// silence would let the operator believe it is working.
		p.reportErr(ctx, "usenet/blacklist-compile", e)
	}
	activeBlacklist.Store(m)
}

// flushFilterHits persists the pass's counters. Best-effort by design: these are
// observability counters, and losing a pass of them must never fail a crawl.
func (p *Plugin) flushFilterHits(ctx context.Context) {
	hits := p.hits.drain()
	if len(hits) == 0 {
		return
	}
	if err := p.st.recordFilterHits(ctx, hits); err != nil {
		p.reportErr(ctx, "usenet/filter-hits", err)
	}
}
