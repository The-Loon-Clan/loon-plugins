// trackercredit.go declares the contract the tracker publishes so OTHER
// plugins can sell transfer credit — the points store's "1.0 GB Uploaded"
// and "10.0 GB Downloaded" items, the classic BON spends.
//
// Credit is bookkeeping, not history: it lives in its own table beside the
// announce-fed per-torrent rows and is folded in wherever totals are
// summed, so buying upload does not invent a torrent you never seeded and
// wiping download cannot dip a real transfer row negative.
package pluginapi

import "context"

// TrackerCreditName is the Core extension-registry key. Absent means no
// tracker (or one that does not sell credit) — a store item of this kind
// then fails at purchase time with the points refunded, the same terms as
// every other absent granter.
const TrackerCreditName = "tracker.credit"

// TrackerCredit adjusts a member's transfer accounting.
type TrackerCredit interface {
	// CreditUpload adds bytes to the member's uploaded total.
	CreditUpload(ctx context.Context, userID int64, bytes int64) error
	// CreditDownload FORGIVES bytes of the member's downloaded total —
	// readers clamp the effective figure at zero, so over-forgiving is
	// generosity, not a negative download.
	CreditDownload(ctx context.Context, userID int64, bytes int64) error
}
