package magic

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/jmoiron/sqlx"
)

// PGStore is the Postgres store over the dedicated "magic" schema. Every
// statement schema-qualifies its tables — this holds the raw pool.
type PGStore struct{ db *sqlx.DB }

func NewPGStore(db *sqlx.DB) *PGStore { return &PGStore{db: db} }

// Config is the operator's pricing knobs.
type Config struct {
	BaseAll   int64 // scope base: everyone
	BaseSelf  int64 // scope base: the caster alone
	BaseUser  int64 // scope base: one named member
	AvgSizeGB int64 // the size a torrent is priced against
	// CustomUpMax / CustomDownMax bound custom ratio pairs. Down's LOWER
	// bound is always 0 (full forgiveness); Up's lower bound is always 1
	// (a promotion, not a penalty).
	CustomUpMax   float64
	CustomDownMax float64
}

func defaults() Config {
	return Config{
		BaseAll: 1200, BaseSelf: 350, BaseUser: 500,
		AvgSizeGB: 5, CustomUpMax: 2.33, CustomDownMax: 1,
	}
}

// Settings reads the config: defaults under operator overrides; bad values
// keep the default.
func (s *PGStore) Settings(ctx context.Context) (Config, error) {
	cfg := defaults()
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM magic.settings`)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return cfg, err
		}
		switch k {
		case "base_all":
			setPosInt64(&cfg.BaseAll, v)
		case "base_self":
			setPosInt64(&cfg.BaseSelf, v)
		case "base_user":
			setPosInt64(&cfg.BaseUser, v)
		case "avg_size_gb":
			setPosInt64(&cfg.AvgSizeGB, v)
		case "custom_up_max":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 1 {
				cfg.CustomUpMax = f
			}
		case "custom_down_max":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
				cfg.CustomDownMax = f
			}
		}
	}
	return cfg, rows.Err()
}

func setPosInt64(dst *int64, v string) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		*dst = n
	}
}

// SaveSetting writes one knob.
func (s *PGStore) SaveSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO magic.settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// BuffDef is one preset promotion.
type BuffDef struct {
	ID        int64   `db:"id"`
	Slug      string  `db:"slug"`
	Name      string  `db:"name"`
	UpRatio   float64 `db:"up_ratio"`
	DownRatio float64 `db:"down_ratio"`
	Ordinal   int     `db:"ordinal"`
	Enabled   bool    `db:"enabled"`
}

// EnsureBuffDefs inserts what is missing by slug, touching nothing that
// exists — the builtin reconciliation every catalogue in this tree uses.
func (s *PGStore) EnsureBuffDefs(ctx context.Context, defs []BuffDef) error {
	for _, d := range defs {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO magic.buff_defs (slug, name, up_ratio, down_ratio, ordinal)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (slug) DO NOTHING`,
			d.Slug, d.Name, d.UpRatio, d.DownRatio, d.Ordinal); err != nil {
			return err
		}
	}
	return nil
}

// BuffDefs lists the enabled presets, in order.
func (s *PGStore) BuffDefs(ctx context.Context) ([]BuffDef, error) {
	var out []BuffDef
	err := s.db.SelectContext(ctx, &out, `
		SELECT id, slug, name, up_ratio, down_ratio, ordinal, enabled
		  FROM magic.buff_defs WHERE enabled ORDER BY ordinal, slug`)
	return out, err
}

// BuffBySlug reads one preset.
func (s *PGStore) BuffBySlug(ctx context.Context, slug string) (BuffDef, bool, error) {
	var d BuffDef
	err := s.db.GetContext(ctx, &d, `
		SELECT id, slug, name, up_ratio, down_ratio, ordinal, enabled
		  FROM magic.buff_defs WHERE slug = $1 AND enabled`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

// Magic is one cast.
type Magic struct {
	ID           int64         `db:"id"`
	CasterID     int64         `db:"caster_id"`
	InfoHash     string        `db:"info_hash"`
	Scope        string        `db:"scope"`
	TargetUserID sql.NullInt64 `db:"target_user_id"`
	UpRatio      float64       `db:"up_ratio"`
	DownRatio    float64       `db:"down_ratio"`
	StartsAt     sql.NullTime  `db:"starts_at"`
	EndsAt       sql.NullTime  `db:"ends_at"`
	Hours        int           `db:"hours"`
	Cost         int64         `db:"cost"`
	Comment      string        `db:"comment"`
	TerminatedAt sql.NullTime  `db:"terminated_at"`
	TerminatedBy sql.NullInt64 `db:"terminated_by"`
	CreatedAt    sql.NullTime  `db:"created_at"`
}

// Cast records one magic.
func (s *PGStore) Cast(ctx context.Context, m Magic) (int64, error) {
	var id int64
	var target any
	if m.TargetUserID.Valid {
		target = m.TargetUserID.Int64
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO magic.magics
		    (caster_id, info_hash, scope, target_user_id, up_ratio, down_ratio,
		     starts_at, ends_at, hours, cost, comment)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now() + ($7 * INTERVAL '1 hour'), $7, $8, $9)
		RETURNING id`,
		m.CasterID, m.InfoHash, m.Scope, target, m.UpRatio, m.DownRatio,
		m.Hours, m.Cost, m.Comment).Scan(&id)
	return id, err
}

// EffectiveRatios is the resolver's read: best-of across active magics
// visible to this member on this torrent. (1, 1) when none apply.
func (s *PGStore) EffectiveRatios(ctx context.Context, infoHash string, userID int64) (float64, float64, error) {
	var up, down sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT max(up_ratio), min(down_ratio) FROM magic.magics
		 WHERE info_hash = $1 AND terminated_at IS NULL
		   AND starts_at <= now() AND ends_at > now()
		   AND (scope = 'all'
		        OR (scope = 'self' AND caster_id = $2)
		        OR (scope = 'user' AND target_user_id = $2))`,
		infoHash, userID).Scan(&up, &down)
	if err != nil {
		return 1, 1, err
	}
	u, d := 1.0, 1.0
	if up.Valid && up.Float64 > 1 {
		u = up.Float64
	}
	if down.Valid && down.Float64 < 1 {
		d = down.Float64
	}
	return u, d, nil
}

// History lists recent casts, newest first.
func (s *PGStore) History(ctx context.Context, limit int) ([]Magic, error) {
	var out []Magic
	err := s.db.SelectContext(ctx, &out, `
		SELECT id, caster_id, info_hash, scope, target_user_id, up_ratio, down_ratio,
		       starts_at, ends_at, hours, cost, comment, terminated_at, terminated_by, created_at
		  FROM magic.magics ORDER BY id DESC LIMIT $1`, limit)
	return out, err
}

// Terminate stamps a magic dead. Idempotent; reports whether this call did it.
func (s *PGStore) Terminate(ctx context.Context, id, adminID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE magic.magics SET terminated_at = now(), terminated_by = $2
		 WHERE id = $1 AND terminated_at IS NULL`, id, adminID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// XP reads a member's casting experience.
func (s *PGStore) XP(ctx context.Context, userID int64) (int64, error) {
	var xp int64
	err := s.db.GetContext(ctx, &xp,
		`SELECT xp FROM magic.magic_xp WHERE user_id = $1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return xp, err
}

// AddXP credits casting experience.
func (s *PGStore) AddXP(ctx context.Context, userID, n int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO magic.magic_xp (user_id, xp) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET xp = magic.magic_xp.xp + $2`, userID, n)
	return err
}
