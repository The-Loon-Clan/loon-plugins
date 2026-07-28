package usenet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Provider management for the admin UI. Distinct from providers() in
// providers.go, which returns only ENABLED servers for crawling — the UI needs
// to see disabled ones too, or you could never re-enable one.

// listServers returns every configured server, preferred first.
func (s *PGStore) listServers(ctx context.Context) ([]provider, error) {
	type row struct {
		ID          int    `db:"id"`
		Name        string `db:"name"`
		Host        string `db:"host"`
		Port        int    `db:"port"`
		TLS         bool   `db:"tls"`
		Username    string `db:"username"`
		Enabled     bool   `db:"enabled"`
		Role        string `db:"role"`
		Priority    int    `db:"priority"`
		Connections int    `db:"connections"`
		AccountCap  int    `db:"account_cap"`
		Backbone    string `db:"backbone"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Password is deliberately NOT selected: it is never sent to the browser.
		return tx.SelectContext(ctx, &rows,
			`SELECT id, name, host, port, tls, username, enabled, role, priority, connections, account_cap, backbone
			   FROM servers ORDER BY role, priority, id`)
	})
	if err != nil {
		return nil, err
	}
	out := make([]provider, len(rows))
	for i, r := range rows {
		out[i] = provider{
			ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port, TLS: r.TLS,
			Username: r.Username, Enabled: r.Enabled, Role: r.Role,
			Priority: r.Priority, Connections: r.Connections, AccountCap: r.AccountCap,
			Backbone: r.Backbone,
		}
	}
	return out, nil
}

// upsertServer creates a server (ID == 0) or updates one in place.
//
// An empty password on update KEEPS the stored one. The list view never sends
// the password to the browser, so a blank field means "unchanged", not "clear
// it" — otherwise editing a provider's priority would silently wipe its
// credentials and the next crawl would fail to authenticate.
func (s *PGStore) upsertServer(ctx context.Context, pr provider) error {
	if strings.TrimSpace(pr.Host) == "" {
		return fmt.Errorf("host is required")
	}
	if pr.Port <= 0 {
		pr.Port = 119
	}
	if pr.Role != roleBackup {
		pr.Role = roleActive
	}
	if pr.Name == "" {
		pr.Name = pr.Host
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if pr.ID == 0 {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO servers (name, host, port, tls, username, password, enabled,
				                      role, priority, connections, account_cap, backbone)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
				pr.Name, pr.Host, pr.Port, pr.TLS, pr.Username, pr.Password, pr.Enabled,
				pr.Role, pr.Priority, pr.Connections, pr.AccountCap, pr.Backbone)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE servers
			    SET name = $2, host = $3, port = $4, tls = $5, username = $6,
			        password = CASE WHEN $7 = '' THEN password ELSE $7 END,
			        enabled = $8, role = $9, priority = $10, connections = $11,
			        account_cap = $12, backbone = $13
			  WHERE id = $1`,
			pr.ID, pr.Name, pr.Host, pr.Port, pr.TLS, pr.Username, pr.Password,
			pr.Enabled, pr.Role, pr.Priority, pr.Connections, pr.AccountCap, pr.Backbone)
		return err
	})
}

// serverPassword returns one server row's stored password. Deliberately not
// part of listServers (which feeds the browser and must never carry secrets);
// this exists solely so a per-row "Test connection" can fall back to the
// stored secret when the form's password field was left blank ("unchanged").
func (s *PGStore) serverPassword(ctx context.Context, id int) (string, error) {
	var pw string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &pw, `SELECT password FROM servers WHERE id = $1`, id)
	})
	return pw, err
}

// deleteServer removes a provider. Its crawl state is left alone on purpose:
// state is keyed by BACKBONE, so it may still belong to another account on the
// same backbone — and even if not, deleting watermarks would silently re-crawl
// that history from scratch if the provider is ever added back.
func (s *PGStore) deleteServer(ctx context.Context, id int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE id = $1`, id)
		return err
	})
}

// toggleServer flips enabled, so a provider can be parked without losing its
// credentials or its place in the fleet.
func (s *PGStore) toggleServer(ctx context.Context, id int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE servers SET enabled = NOT enabled WHERE id = $1`, id)
		return err
	})
}

// ── per-group tuning (migration 013) ────────────────────────────────

// setGroupTuning updates one group's overrides. retentionDays <= 0 stores NULL,
// meaning "follow the plugin-wide crawl depth" — storing a copied number instead
// would silently pin the group to whatever the default was when it was set.
func (s *PGStore) setGroupTuning(ctx context.Context, name string, retentionDays, throttleMs int, tier Tier) error {
	if throttleMs < 0 {
		throttleMs = 0
	}
	if throttleMs > 60000 {
		throttleMs = 60000 // a minute between batches is already pathological
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var ret any
		if retentionDays > 0 {
			ret = retentionDays
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups SET retention_days = $2, throttle_ms = $3, tier = $4
			  WHERE name = $1`, name, ret, throttleMs, string(tier))
		return err
	})
}

// moveGroup nudges a group's manual ordering within its tier.
func (s *PGStore) moveGroup(ctx context.Context, name string, delta int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups SET sort_order = sort_order + $2 WHERE name = $1`, name, delta)
		return err
	})
}

// deleteGroup removes a group from the catalogue. Crawl state is keyed
// (backbone, group) and is left behind deliberately: re-adding the group should
// resume where it left off rather than re-crawl years of history.
func (s *PGStore) deleteGroup(ctx context.Context, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM newsgroups WHERE name = $1`, name)
		return err
	})
}

// deleteInactiveGroups clears out the long tail left by a LIST import.
func (s *PGStore) deleteInactiveGroups(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM newsgroups WHERE active = FALSE`)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

func (s *PGStore) getServer(ctx context.Context) (pluginapi.Server, bool, error) {
	var srv pluginapi.Server
	found := false
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		e := tx.QueryRowContext(ctx,
			`SELECT host, port, tls, username, password, enabled, backbone FROM servers ORDER BY id LIMIT 1`).
			Scan(&srv.Host, &srv.Port, &srv.TLS, &srv.Username, &srv.Password, &srv.Enabled, &srv.Backbone)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		found = true
		return nil
	})
	return srv, found, err
}

// saveServer updates the FIRST configured server, or creates one if there are
// none. It deliberately does not touch any other row: the wizard edits a single
// server, but the fleet may hold several (providers.go), and this used to
// DELETE FROM servers first — so saving the wizard would have silently wiped
// every additional provider an operator had added.
func (s *PGStore) saveServer(ctx context.Context, srv pluginapi.Server) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var id int
		err := tx.GetContext(ctx, &id, `SELECT id FROM servers ORDER BY id LIMIT 1`)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			_, err = tx.ExecContext(ctx,
				`INSERT INTO servers (host, port, tls, username, password, enabled, backbone, name)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$1)`,
				srv.Host, srv.Port, srv.TLS, srv.Username, srv.Password, srv.Enabled, srv.Backbone)
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE servers
			    SET host=$2, port=$3, tls=$4, username=$5,
			        password = CASE WHEN $6 = '' THEN password ELSE $6 END,
			        enabled=$7, backbone=$8
			  WHERE id=$1`,
			id, srv.Host, srv.Port, srv.TLS, srv.Username, srv.Password, srv.Enabled, srv.Backbone)
		return err
	})
}

// resetScope selects which half of a group's crawl state to rewind. They are
// independent because they repair different failures and cost different orders
// of magnitude, and an operator should be able to buy one without the other.
type resetScope string

const (
	// resetForward re-reads the span THIS crawler fetched. Repairs a parser
	// bug in the plugin era. Cheap: ~10M articles on prod's busiest group.
	resetForward resetScope = "forward"
	// resetHistory reopens the backfill so it re-walks everything below the
	// forward mark that is not already recorded as fetched. Repairs a blind
	// spot inherited from a previous crawler. Expensive: ~793M articles on the
	// same group, though bounded per pass so it trickles rather than blocks.
	resetHistory resetScope = "history"
)

// watermarkReset describes a completed rewind.
type watermarkReset struct {
	Group    string
	Scope    resetScope
	OldMark  int64
	NewMark  int64
	Frags    int
	Articles int64 // articles the crawler will re-read as a result
}

// resetWatermark rewinds a group's FORWARD watermark to the start of the span
// this crawler actually fetched, so a pass re-reads it. The use case is a
// parsing bug: articles were read, mis-assembled, and discarded, and they sit
// behind the mark where nothing will ever look at them again.
//
// Re-reading is safe. Dedup is on content_hash over the sorted message-ids, so
// a release already stored collides and is skipped; only genuinely new
// assemblies are written.
//
// resetForward leaves backfill_done alone: it moves the forward mark only, so
// the backfill does not wake up and start walking through years of history
// nobody asked for. resetHistory is the deliberate opposite.
//
// The target is max(range_start), NOT min. Adoption seeds a coverage range from
// the legacy crawler's watermarks (migration 020/021), and on a real group that
// inherited span is enormous — 793M articles on prod's busiest — but it was
// indexed by the OLD crawler and never touched by this one. Rewinding into it
// would re-read hundreds of millions of articles to no purpose.
func (s *PGStore) resetWatermark(ctx context.Context, backbone, group string, scope resetScope) (watermarkReset, error) {
	var out watermarkReset
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var cur struct {
			HighWatermark int64         `db:"high_watermark"`
			ServerLow     int64         `db:"server_low"`
			Target        sql.NullInt64 `db:"target"`
			Frags         int           `db:"frags"`
		}
		err := tx.GetContext(ctx, &cur, `
			SELECT s.high_watermark, s.server_low,
			       (SELECT max(range_start) FROM newsgroup_ranges r
			         WHERE r.backbone = s.backbone AND r.group_name = s.group_name) AS target,
			       (SELECT count(*) FROM newsgroup_ranges r
			         WHERE r.backbone = s.backbone AND r.group_name = s.group_name) AS frags
			  FROM newsgroup_state s
			 WHERE s.backbone = $1 AND s.group_name = $2`, backbone, group)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s has no crawl state on backbone %s — nothing to reset", group, backbone)
		}
		if err != nil {
			return err
		}

		if scope == resetHistory {
			if !cur.Target.Valid {
				return fmt.Errorf("%s has no recorded coverage — there is nothing to re-walk below", group)
			}
			// Everything below the crawler's own most recent run. On an adopted
			// install this is the span a PREVIOUS crawler claimed, which is the
			// claim being repudiated.
			below := cur.Target.Int64 - cur.ServerLow
			if below <= 0 {
				return fmt.Errorf(
					"%s: this crawler's coverage already starts at the server's oldest article (%d) — "+
						"there is no earlier history to re-walk",
					group, cur.ServerLow)
			}

			// DROP the claimed coverage below that run, rather than trusting a
			// migration to have done it.
			//
			// The backfill plans from gaps in this table, so a claim of coverage
			// IS the thing preventing a re-walk — deleting it is the operation,
			// not a tidy-up before the operation. Doing it here also avoids the
			// mistake migration 022 made: it identified the rows to remove by
			// recomputing GREATEST(back_watermark, server_low), and server_low
			// drifts upward as the provider expires articles, so by the time it
			// ran the value no longer matched anything and it deleted nothing.
			// Here the boundary is the crawler's own max(range_start), which is
			// a recorded fact rather than a re-derived one.
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM newsgroup_ranges
				 WHERE backbone = $1 AND group_name = $2 AND range_start < $3`,
				backbone, group, cur.Target.Int64); err != nil {
				return err
			}
			// back_watermark is where the backfill walks DOWN from. The run at
			// or above Target stays recorded, so the backfill skips it and
			// queues only what was just repudiated.
			if _, err := tx.ExecContext(ctx, `
				UPDATE newsgroup_state
				   SET back_watermark = high_watermark, back_watermark_date = NULL,
				       backfill_done  = FALSE
				 WHERE backbone = $1 AND group_name = $2`, backbone, group); err != nil {
				return err
			}
			out = watermarkReset{
				Group: group, Scope: scope, OldMark: cur.HighWatermark,
				NewMark: cur.ServerLow, Frags: cur.Frags, Articles: below,
			}
			return nil
		}

		if !cur.Target.Valid {
			return fmt.Errorf("%s has no recorded coverage — nothing to rewind to", group)
		}
		// Refuses the heavily-fragmented case. A group mid-backfill has
		// thousands of runs and its highest range_start can sit ABOVE the
		// forward mark, which would move the watermark FORWARD and skip
		// everything in between — the opposite of the intent, and silent.
		if cur.Target.Int64 >= cur.HighWatermark {
			return fmt.Errorf(
				"%s: computed target %d is not behind the current watermark %d (%d coverage fragments) — "+
					"this group's coverage is too fragmented for an automatic reset; pick a target by hand",
				group, cur.Target.Int64, cur.HighWatermark, cur.Frags)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE newsgroup_state
			   SET high_watermark = $3, high_watermark_date = NULL
			 WHERE backbone = $1 AND group_name = $2`,
			backbone, group, cur.Target.Int64); err != nil {
			return err
		}
		out = watermarkReset{
			Group: group, Scope: scope, OldMark: cur.HighWatermark, NewMark: cur.Target.Int64,
			Frags: cur.Frags, Articles: cur.HighWatermark - cur.Target.Int64,
		}
		return nil
	})
	return out, err
}
