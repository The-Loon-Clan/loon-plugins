package medals

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// PGStore is the Postgres store over the dedicated "medals" schema. Tables
// are schema-qualified in every statement — this holds the raw pool, whose
// search_path is the host's (the games plugin's lesson, learned once).
type PGStore struct{ db *sqlx.DB }

func NewPGStore(db *sqlx.DB) *PGStore { return &PGStore{db: db} }

// Medal is one catalogue row.
type Medal struct {
	ID              int64  `db:"id"`
	Slug            string `db:"slug"`
	Name            string `db:"name"`
	Icon            string `db:"icon"`
	Description     string `db:"description"`
	DescriptionSlug string `db:"description_slug"`
	BonusPct        int    `db:"bonus_pct"`
	Price           int64  `db:"price"`
	Ordinal         int    `db:"ordinal"`
	Enabled         bool   `db:"enabled"`
}

const medalCols = `id, slug, name, icon, description, description_slug, bonus_pct, price, ordinal, enabled`

// List returns the catalogue, enabled only or everything (the admin view).
func (s *PGStore) List(ctx context.Context, enabledOnly bool) ([]Medal, error) {
	q := `SELECT ` + medalCols + ` FROM medals.medals`
	if enabledOnly {
		q += ` WHERE enabled`
	}
	q += ` ORDER BY ordinal, name`
	var out []Medal
	err := s.db.SelectContext(ctx, &out, q)
	return out, err
}

// Get reads one medal by id.
func (s *PGStore) Get(ctx context.Context, id int64) (Medal, error) {
	var m Medal
	err := s.db.GetContext(ctx, &m,
		`SELECT `+medalCols+` FROM medals.medals WHERE id = $1`, id)
	return m, err
}

// Create inserts a medal.
func (s *PGStore) Create(ctx context.Context, m Medal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO medals.medals (slug, name, icon, description, description_slug, bonus_pct, price, ordinal, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		m.Slug, m.Name, m.Icon, m.Description, m.DescriptionSlug, m.BonusPct, m.Price, m.Ordinal, m.Enabled)
	return err
}

// SetEnabled flips one medal.
func (s *PGStore) SetEnabled(ctx context.Context, id int64, on bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE medals.medals SET enabled = $2 WHERE id = $1`, id, on)
	return err
}

// Update overwrites a medal's definition by id.
//
// The SLUG is deliberately not settable. It is the identifier a reward payout,
// an achievement and a store item all name (pluginapi.MedalGranter takes a
// slug), so renaming one would silently stop every grant that pointed at it —
// the rule ranks made explicit when it made slug the key.
func (s *PGStore) Update(ctx context.Context, m Medal) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE medals.medals
		   SET name = $2, icon = $3, description = $4, description_slug = $5,
		       bonus_pct = $6, price = $7, ordinal = $8
		 WHERE id = $1`,
		m.ID, m.Name, m.Icon, m.Description, m.DescriptionSlug, m.BonusPct, m.Price, m.Ordinal)
	return err
}

// Delete removes a medal; holders' rows cascade. The admin page warns.
func (s *PGStore) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM medals.medals WHERE id = $1`, id)
	return err
}

// Owned is one member's cabinet: medal ids they hold and whether shown.
func (s *PGStore) Owned(ctx context.Context, userID int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT medal_id, shown FROM medals.user_medals WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		var shown bool
		if err := rows.Scan(&id, &shown); err != nil {
			return nil, err
		}
		out[id] = shown
	}
	return out, rows.Err()
}

// Grant hands a medal to a member; repeat grants are the same nothing.
func (s *PGStore) Grant(ctx context.Context, userID, medalID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO medals.user_medals (medal_id, user_id) VALUES ($1, $2)
		ON CONFLICT (medal_id, user_id) DO NOTHING`, medalID, userID)
	return err
}

// GrantBySlug is the granter's path: enabled medals only, reporting whether
// the slug named anything — the caller decides how loud to be.
func (s *PGStore) GrantBySlug(ctx context.Context, userID int64, slug string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO medals.user_medals (medal_id, user_id)
		SELECT id, $2 FROM medals.medals WHERE slug = $1 AND enabled
		ON CONFLICT (medal_id, user_id) DO NOTHING`, slug, userID)
	if err != nil {
		return false, err
	}
	// RowsAffected 0 covers both "unknown slug" and "already held"; both are
	// success from the payout's seat, so the distinction is only for logs.
	var exists bool
	if err := s.db.GetContext(ctx, &exists,
		`SELECT EXISTS (SELECT 1 FROM medals.medals WHERE slug = $1 AND enabled)`, slug); err != nil {
		return false, err
	}
	_ = res
	return exists, nil
}

// SetShown flips whether one held medal renders on the profile.
func (s *PGStore) SetShown(ctx context.Context, userID, medalID int64, shown bool) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE medals.user_medals SET shown = $3
		 WHERE user_id = $1 AND medal_id = $2`, userID, medalID, shown)
	return err
}

// Worn answers the profile: the icons a member is displaying, in catalogue
// order.
func (s *PGStore) Worn(ctx context.Context, userID int64) ([]pluginapi.WornMedal, error) {
	// The SLUG comes back too, because an icon field the operator left blank
	// resolves to a sprite chosen from it — the profile must draw the same
	// face the medals page does.
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.name, m.icon, m.slug FROM medals.user_medals um
		  JOIN medals.medals m ON m.id = um.medal_id
		 WHERE um.user_id = $1 AND um.shown AND m.enabled
		 ORDER BY m.ordinal, m.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pluginapi.WornMedal
	for rows.Next() {
		var w pluginapi.WornMedal
		var icon, slug string
		if err := rows.Scan(&w.Name, &icon, &slug); err != nil {
			return nil, err
		}
		w.Sprite, w.Icon = spriteOrImage(icon, slug)
		out = append(out, w)
	}
	return out, rows.Err()
}

// WornBonusPct sums the WORN medals' bonus — the answer behind
// pluginapi.MedalBonusName, which the plugin itself never applies.
func (s *PGStore) WornBonusPct(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.GetContext(ctx, &n, `
		SELECT coalesce(sum(m.bonus_pct), 0) FROM medals.user_medals um
		  JOIN medals.medals m ON m.id = um.medal_id
		 WHERE um.user_id = $1 AND um.shown AND m.enabled`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
