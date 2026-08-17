package donations

// PGStore is the Postgres-backed implementation of
// PGStore. Extracted from *Storage in Phase 3.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

// ListSiteCosts returns every active line item, ordered first by
// category then sort_order then label. The donate page uses this to
// render the per-category groups.
func (r *PGStore) ListSiteCosts(ctx context.Context, includeInactive bool) ([]*SiteCost, error) {
	q := `SELECT id, label, category, goal_group, period, amount_usd, notes, sort_order, active, created_at, updated_at
	        FROM site_costs`
	if !includeInactive {
		q += ` WHERE active = TRUE`
	}
	q += ` ORDER BY goal_group, category, sort_order, label`
	var out []*SiteCost
	err := r.db.SelectContext(ctx, &out, q)
	return out, err
}

// CreateSiteCost inserts a new line item. updated_at defaults to now.
func (r *PGStore) CreateSiteCost(ctx context.Context, c *SiteCost) error {
	if c.GoalGroup == "" {
		c.GoalGroup = "site"
	}
	return r.db.QueryRowContext(ctx, `
		INSERT INTO site_costs (label, category, goal_group, period, amount_usd, notes, sort_order, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		c.Label, c.Category, c.GoalGroup, c.Period, c.AmountUSD, c.Notes, c.SortOrder, c.Active,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

// UpdateSiteCost overwrites every editable field. Bumps updated_at so
// a "last touched" column on the admin page stays accurate.
func (r *PGStore) UpdateSiteCost(ctx context.Context, c *SiteCost) error {
	if c.GoalGroup == "" {
		c.GoalGroup = "site"
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE site_costs
		   SET label = $1, category = $2, goal_group = $3, period = $4, amount_usd = $5,
		       notes = $6, sort_order = $7, active = $8, updated_at = NOW()
		 WHERE id = $9`,
		c.Label, c.Category, c.GoalGroup, c.Period, c.AmountUSD, c.Notes, c.SortOrder, c.Active, c.ID)
	return err
}

func (r *PGStore) DeleteSiteCost(ctx context.Context, id int) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM site_costs WHERE id = $1`, id)
	return err
}

// SumSiteCostsByGroupPeriod is the per-group form. Drives each
// group's thermometer goal independently.
func (r *PGStore) SumSiteCostsByGroupPeriod(ctx context.Context, group, period string) (float64, error) {
	var sum sql.NullFloat64
	err := r.db.GetContext(ctx, &sum, `
		SELECT COALESCE(SUM(amount_usd), 0)
		  FROM site_costs
		 WHERE active = TRUE AND goal_group = $1 AND period = $2`, group, period)
	return sum.Float64, err
}

// DistinctActiveGoalGroups returns every goal_group with at least one
// active cost row, ordered alphabetically with 'site' pinned first.
// Drives the donate page's per-group thermometer rendering.
func (r *PGStore) DistinctActiveGoalGroups(ctx context.Context) ([]string, error) {
	var groups []string
	err := r.db.SelectContext(ctx, &groups, `
		SELECT goal_group FROM site_costs
		 WHERE active = TRUE
		 GROUP BY goal_group
		 ORDER BY (goal_group = 'site') DESC, goal_group`)
	return groups, err
}

// CreateDonation inserts a row and (when DonorUserID is set) bumps the
// user's lifetime counters and flips the Donator flag once their
// running total crosses the configured threshold. Single transaction
// so a partial failure can't leave users.donation_count out of step
// with the donations table.
//
// donatorThresholdUSD is read by the caller from settings — passing it
// in keeps storage stateless w.r.t. settings reads.
func (r *PGStore) CreateDonation(ctx context.Context, d *Donation, donatorThresholdUSD float64) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Synthetic txid for fiat / off-chain rows so the
	// (asset, txid) UNIQUE doesn't collide on the empty default.
	if d.Txid == "" {
		d.Txid = fmt.Sprintf("synth-%s-%d", d.Asset, time.Now().UnixNano())
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO donations (asset, txid, amount_native, amount_usd, donor_user_id,
		                       donor_label, received_at, note, overfunded, package_id, anonymous)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, NOW()), $8, $9, $10, $11)
		RETURNING id, received_at`,
		d.Asset, d.Txid, d.AmountNative, d.AmountUSD, d.DonorUserID,
		d.DonorLabel, donationNullTime(d.ReceivedAt), d.Note, d.Overfunded, d.PackageID, d.Anonymous,
	).Scan(&d.ID, &d.ReceivedAt)
	if err != nil {
		return err
	}

	if d.DonorUserID != nil && *d.DonorUserID > 0 {
		// Bump count + total atomically. Donator flips true the first
		// time the total crosses the threshold; never flips back.
		_, err = tx.ExecContext(ctx, `
			UPDATE users
			   SET donation_count     = donation_count + 1,
			       donation_total_usd = donation_total_usd + $2,
			       donator            = donator OR (donation_total_usd + $2) >= $3
			 WHERE id = $1`,
			*d.DonorUserID, d.AmountUSD, donatorThresholdUSD)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetDonationByTxid returns the donation with the given txid, or
// (nil, nil) when none exists (the absence contract the webhook's
// idempotency pre-check relies on).
func (r *PGStore) GetDonationByTxid(ctx context.Context, txid string) (*Donation, error) {
	var d Donation
	err := r.db.GetContext(ctx, &d, `
		SELECT id, asset, txid, amount_native, amount_usd, donor_user_id,
		       donor_label, received_at, note, overfunded, package_id
		  FROM donations
		 WHERE txid = $1`, txid)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListRecentDonations returns the N most-recent PUBLIC rows for the donate
// page. Two different opt-outs meet here and they are not the same choice:
// an empty donor_label lists the donation as "Anonymous", while anonymous=true
// keeps the row off the list entirely. Sums elsewhere never filter on either —
// hiding a donation from display must never hide it from the thermometer.
func (r *PGStore) ListRecentDonations(ctx context.Context, limit int) ([]*Donation, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var out []*Donation
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, asset, txid, amount_native, amount_usd, donor_user_id,
		       donor_label, received_at, note, overfunded, package_id
		  FROM donations
		 WHERE NOT anonymous
		 ORDER BY received_at DESC
		 LIMIT $1`, limit)
	return out, err
}

// SumDonationsSince returns the total USD raised on or after `since`.
// Used twice per page render: once with the start of the current
// month, once with the start of the current year.
func (r *PGStore) SumDonationsSince(ctx context.Context, since time.Time) (float64, error) {
	var sum sql.NullFloat64
	err := r.db.GetContext(ctx, &sum, `
		SELECT COALESCE(SUM(amount_usd), 0)
		  FROM donations
		 WHERE received_at >= $1`, since)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return sum.Float64, nil
}

// donationNullTime keeps the COALESCE($N, NOW()) trick from blowing up
// when callers pass the zero Time — sql/driver doesn't enjoy 0001-01-01.
func donationNullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

// ─── Donation packages (migration 261) ─────────────────────────────

// ListDonationPackages returns every package, ordered by sort_order
// then label. includeInactive=false hides soft-deleted rows from the
// public render path; admin pages pass true to see + restore them.
func (r *PGStore) ListDonationPackages(ctx context.Context, includeInactive bool) ([]*DonationPackage, error) {
	q := `SELECT id, label, amount_usd, stock_total, reward, description,
	             reset_period, sort_order, active, created_at, updated_at
	        FROM donation_packages`
	if !includeInactive {
		q += ` WHERE active = TRUE`
	}
	q += ` ORDER BY sort_order, amount_usd, label`
	var out []*DonationPackage
	err := r.db.SelectContext(ctx, &out, q)
	return out, err
}

// GetDonationPackage fetches one package by id. Returns sql.ErrNoRows
// when missing; callers route that to a 404 / "package retired" UX
// rather than treating it as a server error.
func (r *PGStore) GetDonationPackage(ctx context.Context, id int64) (*DonationPackage, error) {
	var p DonationPackage
	err := r.db.GetContext(ctx, &p, `
		SELECT id, label, amount_usd, stock_total, reward, description,
		       reset_period, sort_order, active, created_at, updated_at
		  FROM donation_packages
		 WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateDonationPackage inserts a new row. reset_period defaults to
// 'yearly' (the only valid value at migration 261).
func (r *PGStore) CreateDonationPackage(ctx context.Context, p *DonationPackage) error {
	if p.ResetPeriod == "" {
		p.ResetPeriod = "yearly"
	}
	return r.db.QueryRowContext(ctx, `
		INSERT INTO donation_packages (label, amount_usd, stock_total, reward,
		                               description, reset_period, sort_order, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		p.Label, p.AmountUSD, p.StockTotal, p.Reward,
		p.Description, p.ResetPeriod, p.SortOrder, p.Active,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

// UpdateDonationPackage overwrites every editable field. We keep
// historical donations linked via package_id regardless of changes
// here — admin can edit the label / amount / stock without breaking
// the ledger.
func (r *PGStore) UpdateDonationPackage(ctx context.Context, p *DonationPackage) error {
	if p.ResetPeriod == "" {
		p.ResetPeriod = "yearly"
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE donation_packages
		   SET label        = $2,
		       amount_usd   = $3,
		       stock_total  = $4,
		       reward       = $5,
		       description  = $6,
		       reset_period = $7,
		       sort_order   = $8,
		       active       = $9,
		       updated_at   = NOW()
		 WHERE id = $1`,
		p.ID, p.Label, p.AmountUSD, p.StockTotal, p.Reward,
		p.Description, p.ResetPeriod, p.SortOrder, p.Active)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("donation package id=%d not found", p.ID)
	}
	return nil
}

// DeleteDonationPackage is a hard delete. Linked donations keep
// package_id via ON DELETE SET NULL in migration 261 — the ledger
// stays intact, those donations just look like tip-jar donations
// retroactively. Prefer setting active=false in admin UI for
// reversibility; this method is for "I created it by mistake, no
// one's bought a slot yet, just remove it".
func (r *PGStore) DeleteDonationPackage(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM donation_packages WHERE id = $1`, id)
	return err
}

// CountDonationsPerPackageSince returns a {package_id: count} map of
// settled donations whose received_at >= since AND package_id IS NOT
// NULL. Packages with zero settled donations in the window are
// absent from the map (caller treats absence as 0). Used by the
// donate page to compute StockUsed for every active package in one
// round trip.
func (r *PGStore) CountDonationsPerPackageSince(ctx context.Context, since time.Time) (map[int64]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT package_id, COUNT(*)
		  FROM donations
		 WHERE package_id IS NOT NULL
		   AND received_at >= $1
		 GROUP BY package_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var pid int64
		var cnt int
		if err := rows.Scan(&pid, &cnt); err != nil {
			return nil, err
		}
		out[pid] = cnt
	}
	return out, rows.Err()
}
