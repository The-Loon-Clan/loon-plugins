package games

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jmoiron/sqlx"
)

// PGStore is the Postgres store over the dedicated "games" schema. Tables
// are schema-qualified in every statement (the store plugin's convention)
// because this holds the RAW pool, whose search_path is the host's.
type PGStore struct{ db *sqlx.DB }

func NewPGStore(db *sqlx.DB) *PGStore { return &PGStore{db: db} }

// Config is every operator knob, already typed. Defaults are what a fresh
// site plays with; the admin section writes overrides into settings.
type Config struct {
	PotTarget   int64 // the pot closes at this many points
	PotWinPct   int   // 0–100, the winner's share
	PotDailyMax int64 // per member per day
	// PotRewardMin is the consolation threshold: give at least this across
	// the cycle and PotRewardSlug (a rewards-shelf one_off) is granted.
	PotRewardMin  int64
	PotRewardSlug string

	CharityMin       int64
	CharityMax       int64
	CharityDLFloorGB int64 // recipient must have downloaded at least this
}

func defaults() Config {
	return Config{
		PotTarget: 50000, PotWinPct: 50, PotDailyMax: 500,
		PotRewardMin: 100, PotRewardSlug: "",
		CharityMin: 1000, CharityMax: 50000, CharityDLFloorGB: 10,
	}
}

// Settings reads the config: defaults, overridden by whatever the operator
// saved. Unknown keys are ignored, unparsable values keep the default —
// a typo in the admin form must not brick both games.
func (s *PGStore) Settings(ctx context.Context) (Config, error) {
	cfg := defaults()
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM games.settings`)
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
		case "pot_target":
			setInt64(&cfg.PotTarget, v)
		case "pot_win_pct":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
				cfg.PotWinPct = n
			}
		case "pot_daily_max":
			setInt64(&cfg.PotDailyMax, v)
		case "pot_reward_min":
			setInt64(&cfg.PotRewardMin, v)
		case "pot_reward_slug":
			cfg.PotRewardSlug = v
		case "charity_min":
			setInt64(&cfg.CharityMin, v)
		case "charity_max":
			setInt64(&cfg.CharityMax, v)
		case "charity_dl_floor_gb":
			setInt64(&cfg.CharityDLFloorGB, v)
		}
	}
	return cfg, rows.Err()
}

func setInt64(dst *int64, v string) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		*dst = n
	}
}

// SaveSetting writes one knob.
func (s *PGStore) SaveSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO games.settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// Cycle is one pot.
type Cycle struct {
	ID           int64         `db:"id"`
	Target       int64         `db:"target"`
	Total        int64         `db:"total"`
	WinnerUserID sql.NullInt64 `db:"winner_user_id"`
	WinnerPoints int64         `db:"winner_points"`
	EndedAt      sql.NullTime  `db:"ended_at"`
	StartedAt    sql.NullTime  `db:"started_at"`
}

// OpenCycle returns the open cycle, creating one at the given target when
// none exists. The partial index makes the read cheap; the create races are
// harmless — two INSERTs make two open cycles only if both saw none, and
// the ORDER BY id LIMIT 1 read converges everyone onto the oldest.
func (s *PGStore) OpenCycle(ctx context.Context, target int64) (Cycle, error) {
	var c Cycle
	err := s.db.GetContext(ctx, &c, `
		SELECT id, target, total, winner_user_id, winner_points, ended_at, started_at
		  FROM games.pot_cycles WHERE ended_at IS NULL ORDER BY id LIMIT 1`)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO games.pot_cycles (target) VALUES ($1)`, target); err != nil {
		return c, err
	}
	err = s.db.GetContext(ctx, &c, `
		SELECT id, target, total, winner_user_id, winner_points, ended_at, started_at
		  FROM games.pot_cycles WHERE ended_at IS NULL ORDER BY id LIMIT 1`)
	return c, err
}

// LastClosed returns the most recently completed pot, ok=false when none.
func (s *PGStore) LastClosed(ctx context.Context) (Cycle, bool, error) {
	var c Cycle
	err := s.db.GetContext(ctx, &c, `
		SELECT id, target, total, winner_user_id, winner_points, ended_at, started_at
		  FROM games.pot_cycles WHERE ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// DonatedToday is the member's total for the current calendar day.
func (s *PGStore) DonatedToday(ctx context.Context, cycleID, userID int64) (int64, error) {
	var n int64
	err := s.db.GetContext(ctx, &n, `
		SELECT coalesce(sum(amount), 0) FROM games.pot_donations
		 WHERE cycle_id = $1 AND user_id = $2 AND day = CURRENT_DATE`, cycleID, userID)
	return n, err
}

// UserCycleTotal is the member's total for the whole cycle.
func (s *PGStore) UserCycleTotal(ctx context.Context, cycleID, userID int64) (int64, error) {
	var n int64
	err := s.db.GetContext(ctx, &n, `
		SELECT coalesce(sum(amount), 0) FROM games.pot_donations
		 WHERE cycle_id = $1 AND user_id = $2`, cycleID, userID)
	return n, err
}

// AddDonation records one donation and returns the cycle's NEW total — the
// caller's close check reads the same write it just made.
func (s *PGStore) AddDonation(ctx context.Context, cycleID, userID, amount int64) (int64, error) {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO games.pot_donations (cycle_id, user_id, day, amount)
		VALUES ($1, $2, CURRENT_DATE, $3)
		ON CONFLICT (cycle_id, user_id, day) DO UPDATE
		   SET amount = games.pot_donations.amount + $3`, cycleID, userID, amount); err != nil {
		return 0, err
	}
	var total int64
	err := s.db.GetContext(ctx, &total, `
		UPDATE games.pot_cycles SET total = total + $2 WHERE id = $1 RETURNING total`,
		cycleID, amount)
	if err != nil {
		return 0, fmt.Errorf("bump cycle total: %w", err)
	}
	return total, nil
}

// CloseCycle stamps the cycle closed and reports whether THIS caller did it
// — the closer election. Concurrent donors both filling the pot land here;
// exactly one gets true and runs the draw.
func (s *PGStore) CloseCycle(ctx context.Context, cycleID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE games.pot_cycles SET ended_at = now() WHERE id = $1 AND ended_at IS NULL`, cycleID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecordWinner stamps who the draw picked and what they took.
func (s *PGStore) RecordWinner(ctx context.Context, cycleID, winner, points int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE games.pot_cycles SET winner_user_id = $2, winner_points = $3 WHERE id = $1`,
		cycleID, winner, points)
	return err
}

// ContributorTotals sums every member's giving for the cycle — the draw's
// weights and the consolation pass in one read.
func (s *PGStore) ContributorTotals(ctx context.Context, cycleID int64) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, sum(amount) FROM games.pot_donations
		 WHERE cycle_id = $1 GROUP BY user_id`, cycleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var uid, n int64
		if err := rows.Scan(&uid, &n); err != nil {
			return nil, err
		}
		out[uid] = n
	}
	return out, rows.Err()
}

// RecordCharity writes the audit row and returns its id (the award ref).
func (s *PGStore) RecordCharity(ctx context.Context, donorID, amount int64, ratioMax float64, recipients int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO games.charity_gifts (donor_id, amount, ratio_max, recipients)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		donorID, amount, ratioMax, recipients).Scan(&id)
	return id, err
}

// CharityTotals is the page's small honesty figures: gifts and points moved.
func (s *PGStore) CharityTotals(ctx context.Context) (gifts int64, points int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(amount), 0) FROM games.charity_gifts`).Scan(&gifts, &points)
	return gifts, points, err
}
