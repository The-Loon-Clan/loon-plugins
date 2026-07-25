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
func (s *PGStore) allGroups(ctx context.Context, query string, limit int) ([]pluginapi.GroupInfo, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	type row struct {
		Name      string        `db:"name"`
		Active    bool          `db:"active"`
		NZBs      int64         `db:"nzbs"`
		Retention sql.NullInt64 `db:"retention_days"`
		Throttle  int           `db:"throttle_ms"`
		LowPri    bool          `db:"low_priority"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Ordered the way the crawler will actually visit them: active first,
		// then normal before low-priority, then manual order.
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, g.active, COUNT(n.id) AS nzbs,
			        g.retention_days, g.throttle_ms, g.low_priority
			 FROM newsgroups g LEFT JOIN nzbs n ON n.group_name = g.name
			 WHERE ($1 = '' OR g.name ILIKE '%' || $1 || '%')
			 GROUP BY g.name, g.active, g.retention_days, g.throttle_ms, g.low_priority, g.sort_order
			 ORDER BY g.active DESC, g.low_priority, g.sort_order, g.name LIMIT $2`, query, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.GroupInfo, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.GroupInfo{
			Name: r.Name, Active: r.Active, NZBs: r.NZBs,
			RetentionDays: int(r.Retention.Int64), ThrottleMs: r.Throttle, LowPriority: r.LowPri,
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
