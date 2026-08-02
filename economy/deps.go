package economy

import (
	"context"
	"time"
)

// Deps are the host seams the points-economy jobs read through.
//
// Awarding is deliberately NOT here. Both rules used to credit the balance and
// write the ledger entry as two separate host calls, which is how a balance and
// its ledger drift apart — one succeeding without the other leaves a user with
// points nothing explains, or an explanation for points they do not have.
// core.Points.Award is one operation, so the plugin uses that and the seam
// disappears.
//
// What remains is genuinely host-owned: who is eligible, what has already been
// counted, and the rates an admin sets.
type Deps struct {
	// Rates are read PER RUN rather than captured at Provision, so an admin
	// changing them on the settings page takes effect on the next tick instead
	// of the next deploy. Zero disables the rule, which is the documented way
	// to turn one off.
	PointsPerGrab       func(ctx context.Context) int
	PointsTenurePerYear func(ctx context.Context) int

	// TenureEligible returns users due an anniversary award today.
	//
	// The host owns this query because it owns the idempotency: the shipped
	// implementation matches the anniversary day AND excludes anyone already
	// paid earn_tenure this calendar year. A rule that re-derived eligibility
	// here would be a second opinion about who has been paid.
	TenureEligible func(ctx context.Context) ([]TenureUser, error)

	// UploaderGrabTotals is every uploader's lifetime grab count, and
	// GrabsAlreadyCredited the high-water mark this plugin last paid to. The
	// difference is what gets awarded — a delta, so a re-run credits nothing.
	UploaderGrabTotals   func(ctx context.Context) ([]GrabTotal, error)
	GrabsAlreadyCredited func(ctx context.Context, userID int) (int, error)
}

// TenureUser is a member due an anniversary award.
type TenureUser struct {
	ID        int
	CreatedAt time.Time
}

// GrabTotal is one uploader's lifetime grabs.
type GrabTotal struct {
	UserID     int
	TotalGrabs int
}

var deps *Deps

// SetDeps hands the plugin its worker-side seams. Call from main()'s worker
// block before core.Boot; Provision fails loud otherwise.
func SetDeps(d Deps) { deps = &d }

func (d *Deps) ready() bool {
	return d != nil &&
		d.PointsPerGrab != nil && d.PointsTenurePerYear != nil &&
		d.TenureEligible != nil &&
		d.UploaderGrabTotals != nil && d.GrabsAlreadyCredited != nil
}

// Ledger reason codes. Stable strings: they are what the ledger stores and
// what GrabsAlreadyCredited filters on, so renaming one silently makes every
// past award invisible to the rule that wrote it — and the grab rule would
// then pay every uploader their entire history again.
const (
	reasonTenure = "earn_tenure"
	reasonGrabs  = "earn_grabs"
)
