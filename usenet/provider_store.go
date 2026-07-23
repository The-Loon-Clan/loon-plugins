package usenet

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
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
		Backbone    string `db:"backbone"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Password is deliberately NOT selected: it is never sent to the browser.
		return tx.SelectContext(ctx, &rows,
			`SELECT id, name, host, port, tls, username, enabled, role, priority, connections, backbone
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
			Priority: r.Priority, Connections: r.Connections, Backbone: r.Backbone,
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
				                      role, priority, connections, backbone)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
				pr.Name, pr.Host, pr.Port, pr.TLS, pr.Username, pr.Password, pr.Enabled,
				pr.Role, pr.Priority, pr.Connections, pr.Backbone)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE servers
			    SET name = $2, host = $3, port = $4, tls = $5, username = $6,
			        password = CASE WHEN $7 = '' THEN password ELSE $7 END,
			        enabled = $8, role = $9, priority = $10, connections = $11, backbone = $12
			  WHERE id = $1`,
			pr.ID, pr.Name, pr.Host, pr.Port, pr.TLS, pr.Username, pr.Password,
			pr.Enabled, pr.Role, pr.Priority, pr.Connections, pr.Backbone)
		return err
	})
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
