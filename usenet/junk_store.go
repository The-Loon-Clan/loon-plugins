package usenet

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

// seedJunkRules upserts the shipped rules. It deliberately does NOT overwrite
// operator-authored rows (source='user'), and never touches `enabled` — so a
// locally disabled rule stays disabled across upgrades. Returns rows written.
func (s *PGStore) seedJunkRules(ctx context.Context, specs []junkRuleSpec) (int, error) {
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, sp := range specs {
			params, err := json.Marshal(sp.Params)
			if err != nil {
				return fmt.Errorf("rule %q: %w", sp.Name, err)
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO junk_rules (name, kind, rule, params, enabled, source, notes)
				 VALUES ($1,$2,$3,$4,TRUE,'seed',$5)
				 ON CONFLICT (name) DO UPDATE
				   SET kind = EXCLUDED.kind,
				       rule = EXCLUDED.rule,
				       params = EXCLUDED.params,
				       notes = EXCLUDED.notes,
				       updated_at = now()
				 WHERE junk_rules.source = 'seed'`,
				sp.Name, sp.Kind, sp.Rule, string(params), sp.Notes)
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
			   FROM junk_rules ORDER BY source, name`)
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
// when the rules actually changed.
func junkFingerprint(specs []junkRuleSpec) string {
	parts := make([]string, 0, len(specs))
	for _, s := range specs {
		p, _ := json.Marshal(s.Params)
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%s|%t", s.Name, s.Kind, s.Rule, p, s.Enabled))
	}
	sort.Strings(parts)
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
