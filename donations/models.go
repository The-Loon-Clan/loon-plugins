package donations

import "time"

// SiteCost is a single recurring expense. Donation goals are derived
// from SUM(amount_usd) per period; admin edits a row, the public donate
// page's thermometer updates next render. period is 'monthly' | 'yearly';
// category is 'server' | 'url' | 'usenet' | 'other' for grouping on the
// donate page.
type SiteCost struct {
	ID        int       `db:"id"          json:"id"`
	Label     string    `db:"label"       json:"label"`
	Category  string    `db:"category"    json:"category"`   // sub-group within a goal_group: 'server' | 'url' | 'usenet' | 'other'
	GoalGroup string    `db:"goal_group"  json:"goal_group"` // 'site' (locks when funded) | 'personal' | etc.
	Period    string    `db:"period"      json:"period"`
	AmountUSD float64   `db:"amount_usd"  json:"amount_usd"`
	Notes     string    `db:"notes"       json:"notes"`
	SortOrder int       `db:"sort_order"  json:"sort_order"`
	Active    bool      `db:"active"      json:"active"`
	CreatedAt time.Time `db:"created_at"  json:"created_at"`
	UpdatedAt time.Time `db:"updated_at"  json:"updated_at"`
}

// Donation is one recorded contribution — on-chain (BTCPay webhook drops
// it in) or manually entered (admin records a bank transfer). Asset is
// the coin symbol or 'fiat'; AmountNative is the on-chain amount in the
// asset's native unit (BTC, ETH, USD); AmountUSD is the snapshot at
// receipt time, used for goal progress and points calculation.
//
// DonorUserID NULL = unattributable donation (donor wasn't logged in
// when they hit the invoice URL). Public-list opt-out is via empty
// DonorLabel — the user_id stays so points and Donator-flag still apply.
type Donation struct {
	ID           int64     `db:"id"             json:"id"`
	Asset        string    `db:"asset"          json:"asset"`
	Txid         string    `db:"txid"           json:"txid"`
	AmountNative float64   `db:"amount_native"  json:"amount_native"`
	AmountUSD    float64   `db:"amount_usd"     json:"amount_usd"`
	DonorUserID  *int      `db:"donor_user_id"  json:"donor_user_id,omitempty"`
	DonorLabel   string    `db:"donor_label"    json:"donor_label"`
	ReceivedAt   time.Time `db:"received_at"    json:"received_at"`
	Note         string    `db:"note"           json:"note"`
	Overfunded   bool      `db:"overfunded"     json:"overfunded"`
	// PackageID links a donation to the DonationPackage slot the donor
	// claimed (migration 261). NULL for tip-jar donations, fiat
	// admin-entries without a slot, and all donations predating
	// migration 261. The BTCPay webhook reads package_id from the
	// invoice metadata it issued and writes it here on settlement.
	PackageID *int64 `db:"package_id"     json:"package_id,omitempty"`
}

// DonationPackage is one admin-defined "buy a slot" target shown on
// the public donate page. Stock is shared across the calendar year:
// each settled donation linked via Donation.PackageID consumes one
// slot until stock_used == StockTotal, at which point the package
// renders in the "Funded" subsection. See migration 261 for the
// schema rationale.
type DonationPackage struct {
	ID          int64     `db:"id"            json:"id"`
	Label       string    `db:"label"         json:"label"`
	AmountUSD   float64   `db:"amount_usd"    json:"amount_usd"`
	StockTotal  int       `db:"stock_total"   json:"stock_total"`
	Reward      string    `db:"reward"        json:"reward"`
	Description string    `db:"description"   json:"description"`
	ResetPeriod string    `db:"reset_period"  json:"reset_period"`
	SortOrder   int       `db:"sort_order"    json:"sort_order"`
	Active      bool      `db:"active"        json:"active"`
	CreatedAt   time.Time `db:"created_at"    json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"    json:"updated_at"`
}

// DonationPackageView is a DonationPackage enriched with live stock
// usage data for the current period. Built by the handler at render
// time so the storage layer doesn't have to know about "current
// year" boundaries — that decision belongs to the caller.
type DonationPackageView struct {
	DonationPackage
	StockUsed      int  // donations linked to this package within ResetPeriod
	StockRemaining int  // StockTotal - StockUsed; clamped at 0
	Funded         bool // StockRemaining == 0
	PercentRound   int  // 0-100 for the progress bar; capped at 100
}

// Recompute derives the View fields from StockTotal and stockUsed.
// Keeps the math in one spot so handler + tests agree.
func (v *DonationPackageView) Recompute(stockUsed int) {
	v.StockUsed = stockUsed
	v.StockRemaining = v.StockTotal - stockUsed
	if v.StockRemaining < 0 {
		v.StockRemaining = 0
	}
	v.Funded = v.StockRemaining == 0
	if v.StockTotal > 0 {
		p := stockUsed * 100 / v.StockTotal
		if p > 100 {
			p = 100
		}
		v.PercentRound = p
	}
}

// DonationGoalGroup is the per-group state the donate page renders —
// monthly/yearly thermometers for one named group. "site" group locks
// donations when fully funded; other groups (e.g. "personal") just keep
// filling. The same donations contribute to every group's progress —
// "all apply" semantics.
type DonationGoalGroup struct {
	Name             string // 'site', 'personal', etc.
	Locks            bool   // true if this group's full state stops accepting donations
	MonthlyGoalUSD   float64
	MonthlyRaisedUSD float64
	YearlyGoalUSD    float64
	YearlyRaisedUSD  float64
	Items            []*SiteCost // line items for display
	MonthlyResetAt   time.Time
	YearlyResetAt    time.Time
}

func (g *DonationGoalGroup) MonthlyLocked() bool {
	return g.MonthlyGoalUSD > 0 && g.MonthlyRaisedUSD >= g.MonthlyGoalUSD
}
func (g *DonationGoalGroup) YearlyLocked() bool {
	return g.YearlyGoalUSD > 0 && g.YearlyRaisedUSD >= g.YearlyGoalUSD
}
func (g *DonationGoalGroup) MonthlyPercent() float64 {
	return pct(g.MonthlyRaisedUSD, g.MonthlyGoalUSD)
}
func (g *DonationGoalGroup) YearlyPercent() float64 { return pct(g.YearlyRaisedUSD, g.YearlyGoalUSD) }

// FullyFunded returns true when both periods are at-or-above their
// goal. Used to decide if a "locking" group's full state should hide
// addresses on the donate page.
func (g *DonationGoalGroup) FullyFunded() bool {
	return g.MonthlyLocked() && g.YearlyLocked()
}

// MonthlyItems / YearlyItems split the display list by period for the
// per-period sub-list under each thermometer.
func (g *DonationGoalGroup) MonthlyItems() []*SiteCost {
	out := make([]*SiteCost, 0, len(g.Items))
	for _, c := range g.Items {
		if c.Period == "monthly" {
			out = append(out, c)
		}
	}
	return out
}
func (g *DonationGoalGroup) YearlyItems() []*SiteCost {
	out := make([]*SiteCost, 0, len(g.Items))
	for _, c := range g.Items {
		if c.Period == "yearly" {
			out = append(out, c)
		}
	}
	return out
}

func pct(raised, goal float64) float64 {
	if goal <= 0 {
		return 0
	}
	p := raised / goal * 100
	if p > 100 {
		p = 100
	}
	return p
}

// DonationPointsConfig holds the curve parameters the admin can tune.
//
// Per-$10 multiplier curve: each contiguous $10 brick of the donation
// is rewarded at points_per_dollar × multiplier_per_10 ^ tier, where
// tier counts from 0 ($1-10), 1 ($11-20), 2 ($21-30) and so on.
//
// $50 at (rate=1.0, mult=1.2):
//
//	$1-10  : 10 × 1 × 1.2^0  = 10.00
//	$11-20 : 10 × 1 × 1.2^1  = 12.00
//	$21-30 : 10 × 1 × 1.2^2  = 14.40
//	$31-40 : 10 × 1 × 1.2^3  = 17.28
//	$41-50 : 10 × 1 × 1.2^4  = 20.74
//	total                   = 74.42
//
// DonatorThresholdUSD is the lifetime-total at which users.donator
// flips true; permanent — never resets.
type DonationPointsConfig struct {
	PointsPerDollar     float64
	MultiplierPer10     float64 // compounding rate per $10 brick (1.0 = pure linear)
	DonatorThresholdUSD float64
}

// PointsForDollars applies the configured curve to a USD amount.
// Sub-dollar fractions land in the current tier at that tier's rate.
// Caller passes whole or fractional USD; same formula handles both.
func (c DonationPointsConfig) PointsForDollars(dollars float64) float64 {
	if dollars <= 0 {
		return 0
	}
	rate := c.PointsPerDollar
	mult := c.MultiplierPer10
	if mult <= 0 {
		mult = 1.0
	}
	const brick = 10.0
	var total float64
	remaining := dollars
	tierRate := rate
	for remaining > 0 {
		take := remaining
		if take > brick {
			take = brick
		}
		total += take * tierRate
		remaining -= take
		tierRate *= mult
	}
	return total
}
