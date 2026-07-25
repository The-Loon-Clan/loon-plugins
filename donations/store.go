package donations

import (
	"context"
	"time"
)

// DonationRepository is the per-domain interface for the public
// donate page + admin oversight surfaces — site_costs goal lines
// and the donations ledger that drives both the per-period
// thermometers and the lifetime Donator promotion. Phase 3
// extraction.
type Store interface {
	ListSiteCosts(ctx context.Context, includeInactive bool) ([]*SiteCost, error)
	CreateSiteCost(ctx context.Context, c *SiteCost) error
	UpdateSiteCost(ctx context.Context, c *SiteCost) error
	DeleteSiteCost(ctx context.Context, id int) error
	SumSiteCostsByGroupPeriod(ctx context.Context, group, period string) (float64, error)
	DistinctActiveGoalGroups(ctx context.Context) ([]string, error)
	CreateDonation(ctx context.Context, d *Donation, donatorThresholdUSD float64) error
	// GetDonationByTxid returns the donation with the given txid, or
	// (nil, nil) when none exists. The webhook uses it to make BTCPay
	// settlement idempotent by the stable txid (btcpay-<invoiceID>),
	// independent of the invoice's asset/currency.
	GetDonationByTxid(ctx context.Context, txid string) (*Donation, error)
	ListRecentDonations(ctx context.Context, limit int) ([]*Donation, error)
	SumDonationsSince(ctx context.Context, since time.Time) (float64, error)

	// Donation packages (migration 261). Each package is a fixed-amount,
	// limited-stock contribution slot the admin defines; donors claim a
	// slot via the BTCPay click-to-claim flow which sets
	// donations.package_id on settlement.
	ListDonationPackages(ctx context.Context, includeInactive bool) ([]*DonationPackage, error)
	GetDonationPackage(ctx context.Context, id int64) (*DonationPackage, error)
	CreateDonationPackage(ctx context.Context, p *DonationPackage) error
	UpdateDonationPackage(ctx context.Context, p *DonationPackage) error
	DeleteDonationPackage(ctx context.Context, id int64) error
	// CountDonationsPerPackageSince returns a map of package_id →
	// settled donation count since `since`. The caller (donate page
	// handler) computes "current year start" and uses the result to
	// derive each package's StockUsed / StockRemaining.
	CountDonationsPerPackageSince(ctx context.Context, since time.Time) (map[int64]int, error)
}
