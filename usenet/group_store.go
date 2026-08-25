package usenet

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Newsgroup catalog: the group list the operator curates and the crawler walks.

func (s *PGStore) groups(ctx context.Context) ([]pluginapi.GroupInfo, error) {
	type row struct {
		Name   string `db:"name"`
		Active bool   `db:"active"`
		NZBs   int64  `db:"nzbs"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, g.active, COUNT(n.id) AS nzbs
			 FROM newsgroups g LEFT JOIN nzbs n ON n.group_name = g.name
			 WHERE g.active = TRUE
			 GROUP BY g.name, g.active ORDER BY g.name`)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.GroupInfo, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.GroupInfo{Name: r.Name, Active: r.Active, NZBs: r.NZBs}
	}
	return out, nil
}

// upsertGroups inserts each name as an inactive group, ignoring duplicates.
// Returns how many were newly added. Batched via unnest — an NNTP LIST from a
// full-feed server is 100k+ names, and one INSERT per name was 100k+ round
// trips inside one transaction.
func (s *PGStore) upsertGroups(ctx context.Context, names []string) (int, error) {
	const chunk = 5000
	added := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for i := 0; i < len(names); i += chunk {
			end := i + chunk
			if end > len(names) {
				end = len(names)
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO newsgroups (name)
				 SELECT DISTINCT unnest($1::text[])
				 ON CONFLICT (name) DO NOTHING`, pq.Array(names[i:end]))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added += int(n)
			}
		}
		return nil
	})
	return added, err
}

// allGroups returns up to limit groups, active first then alphabetical, for the
// admin picker. query filters by name substring so a 100k-group server is
// searchable instead of truncated to the first page.
// allGroups reports groups with the reset-cost figures scoped to ONE
// backbone: the one the reset buttons will actually target (the primary
// enabled provider's — see primaryBackbone). State is keyed per backbone,
// and the old cross-backbone max() reported a figure true for NO backbone:
// with A at hw 1000/range 900 and B at hw 500000/range 100, the prompt
// quoted 499100 against a click that would charge 100 or 499900. An empty
// backbone matches no state rows, so both figures come back NULL and the
// buttons hide — the same install actionResetWatermark refuses.
func (s *PGStore) allGroups(ctx context.Context, query, backbone string, limit int) ([]pluginapi.GroupInfo, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	type row struct {
		Name      string        `db:"name"`
		Active    bool          `db:"active"`
		NZBs      int64         `db:"nzbs"`
		Retention sql.NullInt64 `db:"retention_days"`
		Throttle  int           `db:"throttle_ms"`
		TierRaw   string        `db:"tier"`
		Reset     sql.NullInt64 `db:"reset_articles"`
		ResetHist sql.NullInt64 `db:"reset_history_articles"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Ordered the way the crawler will actually visit them: active first,
		// then normal before low-priority, then manual order.
		// sqllint:allow tierOrderSQL is a compile-time constant expression, no input
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, g.active, COUNT(n.id) AS nzbs,
			        g.retention_days, g.throttle_ms, g.tier,
			        -- What a watermark reset would cost, so the confirm prompt
			        -- can state it. NULL (rendered 0) when a reset is not
			        -- available: no coverage recorded, or the highest fetched
			        -- range starts at or beyond the mark, which is the
			        -- fragmented-backfill case resetWatermark refuses.
			        (SELECT max(st.high_watermark) - max(r.range_start)
			           FROM newsgroup_state st
			           JOIN newsgroup_ranges r
			             ON r.backbone = st.backbone AND r.group_name = st.group_name
			          WHERE st.group_name = g.name AND st.backbone = $3
			         HAVING max(r.range_start) < max(st.high_watermark)) AS reset_articles,
			        -- What a history re-walk would queue: everything below this
			        -- crawler's own earliest recorded fetch, down to the server's
			        -- oldest article. That span is exactly what the reset
			        -- repudiates and the backfill then re-reads. NULL when the
			        -- crawler's coverage already reaches the bottom, which is the
			        -- case resetWatermark refuses.
			        (SELECT max(r.range_start) - st.server_low
			           FROM newsgroup_state st
			           JOIN newsgroup_ranges r
			             ON r.backbone = st.backbone AND r.group_name = st.group_name
			          WHERE st.group_name = g.name AND st.backbone = $3
			          GROUP BY st.server_low
			         HAVING max(r.range_start) > st.server_low
			          LIMIT 1) AS reset_history_articles
			 FROM newsgroups g LEFT JOIN nzbs n ON n.group_name = g.name
			 WHERE ($1 = '' OR g.name ILIKE '%' || $1 || '%')
			 GROUP BY g.name, g.active, g.retention_days, g.throttle_ms, g.tier, g.sort_order
			 ORDER BY g.active DESC, `+tierOrderSQL+`, g.sort_order, g.name LIMIT $2`, query, limit, backbone) // sqllint:allow constant tier-rank expression, no input
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.GroupInfo, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.GroupInfo{
			Name: r.Name, Active: r.Active, NZBs: r.NZBs,
			RetentionDays: int(r.Retention.Int64), ThrottleMs: r.Throttle,
			Tier: string(normalizeTier(r.TierRaw)), ResetArticles: r.Reset.Int64, ResetHistoryArticles: r.ResetHist.Int64,
		}
	}
	return out, nil
}

// groupCount returns the total number of fetched groups (so the picker can show
// "showing N of M" and reassure that a big LIST was fully imported).
func (s *PGStore) groupCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM newsgroups`).Scan(&n)
	})
	return n, err
}

func (s *PGStore) setGroupActive(ctx context.Context, name string, active bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE newsgroups SET active = $2 WHERE name = $1`, name, active)
		return err
	})
}

// activeGroupNames is the configured active group list, names only — for
// callers (the pending sample) that need to address per-group Redis keys
// without discovering them by scanning the keyspace. A handful of rows off an
// indexed flag.
func (s *PGStore) activeGroupNames(ctx context.Context) ([]string, error) {
	var names []string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &names,
			`SELECT name FROM newsgroups WHERE active ORDER BY name`)
	})
	return names, err
}
