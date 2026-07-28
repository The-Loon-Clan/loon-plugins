package usenet

// Tier is a group's crawl priority. Closed set, and the crawler branches on it,
// so it is a type rather than a bare string: a mistyped literal in a comparison
// takes the wrong branch silently, and the symptom (one group quietly crawled
// last) is exactly the bug the tier exists to fix.
//
// The schema carries the same constraint (migration 019), so a bad value cannot
// reach here from the database either.
type Tier string

const (
	// TierCritical is crawled before everything else, every pass. For the one
	// or two groups the content is actually posted to.
	TierCritical Tier = "critical"
	// TierNormal is the default.
	TierNormal Tier = "normal"
	// TierLow is only crawled with whatever capacity is left after the others.
	TierLow Tier = "low"
)

// tierRank orders the tiers for sorting: lower sorts first. An unknown value
// ranks as normal rather than being dropped — a group the operator can no
// longer see is worse than one crawled at the wrong priority.
func tierRank(t Tier) int {
	switch t {
	case TierCritical:
		return 0
	case TierLow:
		return 2
	default:
		return 1
	}
}

// tierOrderSQL orders rows by crawl priority, for use in an ORDER BY.
//
// The tier column is TEXT, so a bare `ORDER BY g.tier` sorts ALPHABETICALLY —
// critical, low, normal — which puts every LOW group ahead of every NORMAL one.
// That is precisely the inversion the tier exists to prevent, and it was live in
// all three query paths: forward crawl planning, backfill group selection, and
// the admin group list. It stayed invisible because the alphabetical order
// happens to be right for critical, and prod's normal-tier groups were all
// caught up, so nothing was visibly starved.
//
// Must stay in step with tierRank; the integration test asserts they agree.
const tierOrderSQL = `CASE g.tier WHEN 'critical' THEN 0 WHEN 'low' THEN 2 ELSE 1 END`

// normalizeTier maps a stored value onto the closed set, defaulting anything
// unrecognised (or empty — a row written before migration 019) to normal.
func normalizeTier(s string) Tier {
	switch Tier(s) {
	case TierCritical:
		return TierCritical
	case TierLow:
		return TierLow
	default:
		return TierNormal
	}
}

// AllTiers is the admin UI's option list, in priority order.
var AllTiers = []Tier{TierCritical, TierNormal, TierLow}

// Label is the human name for the tier, used by the settings template.
func (t Tier) Label() string {
	switch t {
	case TierCritical:
		return "Critical — always first"
	case TierLow:
		return "Low — only with spare capacity"
	default:
		return "Normal"
	}
}
