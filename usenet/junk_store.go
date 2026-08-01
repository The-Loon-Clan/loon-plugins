package usenet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// seedJunkRules upserts the shipped rules. It deliberately does NOT overwrite
// operator-authored rows (source='user'), and never touches `enabled` — so a
// locally disabled rule stays disabled across upgrades. Returns rows written.
func (s *PGStore) seedJunkRules(ctx context.Context, specs []junkRuleSpec) (int, error) {
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Retire seed rows superseded by the full prod port: the partial lift's
		// bare_mixed_case_token (prod has no such rule — short/mid_alnum_token
		// and short_random_token cover its ground with prod's exact gates) and
		// multi_segment_chaos (renamed to prod's multi_seg_random). Only
		// source='seed' rows: an operator-authored rule is never touched.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM junk_rules WHERE source = 'seed'
			    AND name IN ('bare_mixed_case_token', 'multi_segment_chaos')`); err != nil {
			return err
		}
		for i, sp := range specs {
			// Positions follow the shipped order; the size-band catchalls sit at
			// 900+ so operator rules (default 500) keep attribution over them.
			pos := (i + 1) * 10
			if sp.Rule == "size_catchall" {
				pos = 900 + i
			}
			params, err := json.Marshal(sp.Params)
			if err != nil {
				return fmt.Errorf("rule %q: %w", sp.Name, err)
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO junk_rules (name, kind, rule, params, enabled, source, notes, position)
				 VALUES ($1,$2,$3,$4,TRUE,'seed',$5,$6)
				 ON CONFLICT (name) DO UPDATE
				   SET kind = EXCLUDED.kind,
				       rule = EXCLUDED.rule,
				       params = EXCLUDED.params,
				       notes = EXCLUDED.notes,
				       position = EXCLUDED.position,
				       updated_at = now()
				 WHERE junk_rules.source = 'seed'`,
				sp.Name, sp.Kind, sp.Rule, string(params), sp.Notes, pos)
			if err != nil {
				return fmt.Errorf("rule %q: %w", sp.Name, err)
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n++
			}
		}
		return nil
	})
	return n, err
}

// junkRules loads the live rule set, ordered so the reported rule name is
// stable. Cheap by design: this table has a handful of rows and is read once per
// crawl pass to detect edits, never per article.
func (s *PGStore) junkRules(ctx context.Context) ([]junkRuleSpec, error) {
	type row struct {
		Name    string `db:"name"`
		Kind    string `db:"kind"`
		Rule    string `db:"rule"`
		Params  string `db:"params"`
		Enabled bool   `db:"enabled"`
		Notes   string `db:"notes"`
		Source  string `db:"source"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT name, kind, rule, params, enabled, notes, source
			   FROM junk_rules ORDER BY position, name`)
	})
	if err != nil {
		return nil, err
	}
	out := make([]junkRuleSpec, 0, len(rows))
	for _, r := range rows {
		spec := junkRuleSpec{
			Name: r.Name, Kind: r.Kind, Rule: r.Rule,
			Notes: r.Notes, Enabled: r.Enabled,
		}
		if strings.TrimSpace(r.Params) != "" {
			if err := json.Unmarshal([]byte(r.Params), &spec.Params); err != nil {
				return nil, fmt.Errorf("rule %q params: %w", r.Name, err)
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

// junkFingerprint is a cheap change-detector so we only recompile the regex set
// when the rules actually changed. ORDER-SENSITIVE on purpose: evaluation order
// is part of the semantics now (attribution, and the catchalls running last),
// so a reorder must count as a change.
func junkFingerprint(specs []junkRuleSpec) string {
	parts := make([]string, 0, len(specs))
	for i, s := range specs {
		p, _ := json.Marshal(s.Params)
		parts = append(parts, fmt.Sprintf("%d|%s|%s|%s|%s|%t", i, s.Name, s.Kind, s.Rule, p, s.Enabled))
	}
	return strings.Join(parts, "\n")
}

// seedAndLoadJunkRules seeds the shipped defaults, then loads the live set into
// memory. Failure is non-fatal: the embedded defaults compiled at init stay
// active, so ingest is never left without a filter.
func (p *Plugin) seedAndLoadJunkRules(ctx context.Context) {
	specs, err := embeddedJunkRules()
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/junk-seed-parse", err)
		return
	}
	if _, err := p.st.seedJunkRules(ctx, specs); err != nil {
		p.core.Errors.Report(ctx, "usenet/junk-seed", err)
		// keep going — we can still load whatever is already there
	}
	p.reloadJunkRules(ctx)
}

// reloadJunkRules swaps the in-memory matcher if the stored rules changed. Runs
// at the head of a crawl pass, so admin edits apply on the next cycle without a
// restart; the per-article hot path only ever reads the compiled matcher.
func (p *Plugin) reloadJunkRules(ctx context.Context) {
	specs, err := p.st.junkRules(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/junk-load", err)
		return
	}
	if len(specs) == 0 {
		return // nothing stored yet; embedded defaults remain active
	}
	fp := junkFingerprint(specs)
	p.junkMu.Lock()
	unchanged := fp == p.junkFP
	if !unchanged {
		p.junkFP = fp
	}
	p.junkMu.Unlock()
	if unchanged {
		return
	}
	m, err := newJunkMatcher(specs)
	if err != nil {
		// A bad operator-authored rule must not disable junk filtering entirely.
		p.core.Errors.Report(ctx, "usenet/junk-compile", err)
		return
	}
	setJunkMatcher(m)
	p.crawlJob.Log("junk rules reloaded: %d active", len(m.rules))
}

// junkRuleStat is one rule as the order editor shows it: where it sits, what
// it has caught, and the sample that proves what "caught" means here.
type junkRuleStat struct {
	Position   int       `db:"position"`
	Name       string    `db:"name"`
	Kind       string    `db:"kind"`
	Enabled    bool      `db:"enabled"`
	Source     string    `db:"source"`
	Hits       int64     `db:"hits"`
	LastSample string    `db:"last_sample"`
	LastSeen   time.Time `db:"last_seen_at"`
}

// junkRuleStats lists every rule in EVALUATION order with its lifetime hit
// count joined from filter_hits.
//
// Evaluation order is the whole point of the readout. `match` returns on the
// first rule that fires, and on this corpus ~96% of ingested articles are
// junk — so a high-volume rule sitting late means almost every article pays
// for everything above it first. That was measurably the case: a rule catching
// 3.5 billion articles ran thirteenth, behind one costing 81% of the engine's
// CPU for 0.3% of the catches.
func (s *PGStore) junkRuleStats(ctx context.Context) ([]junkRuleStat, error) {
	var out []junkRuleStat
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT r.position, r.name, r.kind, r.enabled, r.source,
			       COALESCE(f.total_count, 0)                  AS hits,
			       COALESCE(f.last_sample, '')                 AS last_sample,
			       COALESCE(f.last_seen_at, 'epoch'::timestamptz) AS last_seen_at
			  FROM junk_rules r
			  LEFT JOIN filter_hits f ON f.kind = 'junk' AND f.rule = r.name
			 ORDER BY r.position, r.name`)
	})
	return out, err
}

// setJunkRulePositions rewrites the evaluation order in one transaction.
//
// All-or-nothing on purpose: a partial reorder can leave two rules sharing a
// position, and the loader's tie-break is then the rule NAME — an ordering
// nobody chose and nobody can see. The caller passes the complete desired
// order, so a concurrent edit loses cleanly rather than interleaving.
func (s *PGStore) setJunkRulePositions(ctx context.Context, order map[string]int) error {
	if len(order) == 0 {
		return nil
	}
	names := make([]string, 0, len(order))
	positions := make([]int, 0, len(order))
	for n, p := range order {
		names = append(names, n)
		positions = append(positions, p)
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE junk_rules AS r
			   SET position = v.position, updated_at = now()
			  FROM (SELECT unnest($1::text[]) AS name, unnest($2::int[]) AS position) AS v
			 WHERE r.name = v.name`,
			pq.Array(names), pq.Array(positions))
		return err
	})
}

// setJunkRuleEnabled toggles one rule. Disabling is how an operator retires a
// rule that has stopped earning its cost without deleting the row and losing
// its hit history.
func (s *PGStore) setJunkRuleEnabled(ctx context.Context, name string, enabled bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE junk_rules SET enabled = $2, updated_at = now() WHERE name = $1`, name, enabled)
		return err
	})
}
