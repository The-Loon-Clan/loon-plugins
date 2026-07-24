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
func (s *PGStore) setGroupTuning(ctx context.Context, name string, retentionDays, throttleMs int, lowPriority bool) error {
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
			`UPDATE newsgroups SET retention_days = $2, throttle_ms = $3, low_priority = $4
			  WHERE name = $1`, name, ret, throttleMs, lowPriority)
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
			    SET host=$2, port=$3, tls=$4, username=$5, password=$6, enabled=$7, backbone=$8
			  WHERE id=$1`,
			id, srv.Host, srv.Port, srv.TLS, srv.Username, srv.Password, srv.Enabled, srv.Backbone)
		return err
	})
}
