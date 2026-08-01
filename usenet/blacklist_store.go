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

// filter_hits holds two unrelated populations under one table, and they have
// opposite growth characteristics:
//
//   - RULE counters (junk, blacklist): one row per configured rule. Tens of
//     rows, lifetime totals, operator-tuned.
//   - INSTRUMENT counters (ungrouped stems, merge suspects, parse drops): one
//     row per distinct observation. Unbounded — the grouping watch's first
//     DAY produced 2,260 stems against 26 rules.
//
// They were read together by one unfiltered SELECT, so the page's cost grew
// with the diagnostics and the rules were buried in the display. The reads are
// split accordingly: rules whole, instruments paged.
const filterHitCols = `kind, rule, total_count, last_sample, first_seen_at, last_seen_at`

// diagnosticKinds is the SQL predicate for "instrument, not rule". New
// instruments are additive, so it is stated as an exclusion of the two rule
// kinds — a new watch shows up in the diagnostics card without a code change.
const notRuleKinds = `kind NOT IN ('junk', 'blacklist')`

// ruleHitRows returns the rule counters, busiest first. Bounded by the rule
// count, so reading it whole on every render is fine.
func (s *PGStore) ruleHitRows(ctx context.Context) ([]filterHitRow, error) {
	var rows []filterHitRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT `+filterHitCols+` FROM filter_hits
			  WHERE kind IN ('junk', 'blacklist')
			  ORDER BY total_count DESC, rule`) // sqllint:allow column list is a package constant, no input
	})
	return rows, err
}

// diagKind is one instrument's totals: the chips above the diagnostics table,
// and the row count its pager needs.
type diagKind struct {
	Kind string `db:"kind"`
	Rows int    `db:"rows"`
	Hits int64  `db:"hits"`
}

// diagPage is one page of instrument counters plus what it did NOT show.
// Carrying the totals alongside the page is the point: a truncated list with
// no denominator reads as the whole set, which is exactly the misreading that
// made an operator count 2,257 stems as rules.
type diagPage struct {
	Rows      []filterHitRow
	Kinds     []diagKind
	TotalRows int
	TotalHits int64
}

// diagnosticHits returns one page of instrument counters. An empty kind means
// all of them.
//
// The per-kind aggregate doubles as the pager's total, so paging costs two
// queries rather than a page plus a COUNT.
func (s *PGStore) diagnosticHits(ctx context.Context, kind string, limit, offset int) (diagPage, error) {
	var out diagPage
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.SelectContext(ctx, &out.Kinds,
			`SELECT kind, count(*)::int AS rows, COALESCE(sum(total_count), 0) AS hits
			   FROM filter_hits WHERE `+notRuleKinds+`
			  GROUP BY kind ORDER BY rows DESC, kind`); err != nil { // sqllint:allow predicate is a package constant, no input
			return err
		}
		for _, k := range out.Kinds {
			if kind == "" || k.Kind == kind {
				out.TotalRows += k.Rows
				out.TotalHits += k.Hits
			}
		}
		return tx.SelectContext(ctx, &out.Rows,
			`SELECT `+filterHitCols+` FROM filter_hits
			  WHERE `+notRuleKinds+` AND ($1 = '' OR kind = $1)
			  ORDER BY total_count DESC, rule
			  LIMIT $2 OFFSET $3`, kind, limit, offset) // sqllint:allow values flow through $N
	})
	return out, err
}

// pruneFilterDiagnostics drops instrument counters that have gone quiet.
//
// Rule counters are never pruned: they are lifetime totals an operator reads
// to decide whether a rule earns its position, and a rule that stopped firing
// is precisely the one worth seeing. Instrument rows are the opposite — a stem
// last observed weeks ago describes a posting format that has since stopped,
// and keeping it forever is how a diagnostic table becomes the problem it was
// added to find.
func (s *PGStore) pruneFilterDiagnostics(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 14
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM filter_hits
			  WHERE `+notRuleKinds+` AND last_seen_at < now() - make_interval(days => $1)`,
			keepDays) // sqllint:allow predicate is a package constant, no input
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
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
