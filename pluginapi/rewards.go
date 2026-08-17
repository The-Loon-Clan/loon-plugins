// rewards.go declares the reward-granting cross-plugin capability
// contracts: the typed interfaces one plugin PUBLISHES via Core.Register
// and another CONSUMES via Core.Lookup without importing each other —
// RankGranter (published by the ranks plugin, consumed by the store) and
// InviteGranter. Both the interface and its well-known registry-name
// const live here so each side depends on the contract, not the
// implementation. See anidb.go for the package-level contract discipline.
//
// Versioning: the whole binary compiles together, so the Go compiler
// catches interface skew at build time. Adding a method is a minor
// change; a signature change is breaking. If out-of-tree plugins ever
// ship separately, version the registered value so Lookup can refuse a
// stale implementation.
package pluginapi

import (
	"context"
	"time"
)

// RankGranterName is the Core extension-registry key under which the
// ranks plugin publishes its RankGranter. Consumers Lookup this name
// and type-assert the result to RankGranter.
const RankGranterName = "ranks.granter"

// RankGranter grants a user a rank subscription — the capability the
// ranks plugin exposes so other plugins (the store, a future
// donation-reward orchestrator) can award ranks without importing the
// ranks package.
//
// It is GRANT-ONLY: the caller debits whatever currency the grant
// costs (points via core.Points for a store purchase, external money
// via the donation flow) BEFORE calling. GrantRank never touches the
// points ledger. Expiry is owned by the ranks plugin's RankExpiry job,
// so the caller only chooses the duration.
type RankGranter interface {
	// GrantRank subscribes userID to rankID for dur, recording rank
	// history and invalidating the user's limits cache. If the user is
	// already subscribed to this rank the subscription is extended.
	// Returns the rank's display name (for receipts), or an error if
	// the rank doesn't exist or the grant fails.
	GrantRank(ctx context.Context, userID, rankID int, dur time.Duration) (rankName string, err error)
}

// InviteGranterName is the Core extension-registry key under which the
// HOST publishes its InviteGranter.
//
// Unlike RankGranterName this comes from the host, not a sibling plugin:
// invites are a property of the host's own users table, so there is no
// plugin to own them. That difference matters to consumers — a plugin
// dependency can be declared in Metadata.Requires and topologically
// ordered, but a host registration cannot. Look it up defensively and
// degrade; see InviteGranter.
const InviteGranterName = "invites.granter"

// InviteGranter credits a user one or more invites — the capability the
// host exposes so the store can sell an invite item without importing
// host handlers or reaching into user storage.
//
// GRANT-ONLY, exactly like RankGranter: the caller debits the points via
// core.Points BEFORE calling, and this never touches the ledger. Keeping
// the two halves split is what lets the store run one purchase path for
// every reward type — deduct, then grant, then unwind both if the grant
// fails.
//
// Consumers must treat absence as a degraded capability rather than a
// boot failure: a host that has no invite system is a legitimate host,
// and the store only needs this when an invite item actually exists in
// its catalog. Fail the purchase loudly instead — see grantReward.
type InviteGranter interface {
	// GrantInvites credits userID with n invites and returns a short
	// human label for the receipt ("1 invite"). n must be > 0.
	GrantInvites(ctx context.Context, userID, n int) (label string, err error)
}

// RewardBySlugGranterName is the Core extension-registry key under which the
// rewards plugin publishes its RewardBySlugGranter.
const RewardBySlugGranterName = "rewards.granter.byslug"

// RewardBySlugGranter creates one grant of a named one_off reward — the
// capability the achievements plugin uses to PAY an achievement without
// importing rewards.
//
// This seam is what made splitting achievements out of rewards possible. The
// two used to share a schema so a completion and its grant could land in one
// transaction; across plugins that transaction cannot exist, so the contract
// is IDEMPOTENCE instead: the engine's UNIQUE (reward, user, reference)
// arbitrates, granted=false reports "already held", and a caller that crashed
// between its own commit and this call simply calls again. At-least-once plus
// idempotent equals exactly-once where it matters.
//
// reference is the caller's dedup key — the achievements plugin passes the
// achievement slug, so two achievements paying the SAME reward are two
// independent grants rather than a race for one. That retires the old rule
// that every achievement must own its reward.
type RewardBySlugGranter interface {
	// GrantOneOff grants the named enabled one_off reward to userID under
	// reference. granted=false with nil err means the member already holds
	// it. An unknown, disabled, payout-less or non-one_off reward is an error.
	GrantOneOff(ctx context.Context, userID int64, slug, reference string) (granted bool, err error)
}

// AchievementGranterName is the Core extension-registry key under which the
// achievements plugin publishes its AchievementGranter.
const AchievementGranterName = "achievements.granter"

// AchievementGranter marks an achievement earned directly — the reverse
// crossing, used by a REWARD whose payout line is "an achievement" (payout
// kind "achievement") and by admin tooling. It marks the badge held and does
// NOT run the achievement's own reward: a reward paying an achievement that
// pays a reward is a loop nobody configured on purpose.
type AchievementGranter interface {
	// GrantAchievement completes slug for userID if not already held.
	// Granting a held achievement is a no-op, not an error.
	GrantAchievement(ctx context.Context, userID int64, slug string) error
}

// FlairGranterName is the Core extension-registry key under which the
// pointstore plugin publishes its FlairGranter.
const FlairGranterName = "pointstore.granter"

// FlairGranter equips a profile flair — the capability the pointstore plugin
// exposes so the points store can sell flair without importing it.
//
// GRANT-ONLY, exactly like RankGranter: the caller debits the points BEFORE
// calling and unwinds them if this fails. Equipping REPLACES whatever flair
// the member wore, which is the pointstore's own semantic — one flair per
// member, a new one takes the slot.
//
// This seam exists because the alternative was a page: the pointstore used to
// register its own /p/store shop for three items, which sat in the nav one
// menu away from the points store and read as a second one. One shop, many
// granters is the shape everything else here already has.
type FlairGranter interface {
	// EquipFlair equips flairID for userID, returning the flair's display
	// name for the receipt. Unknown ids are an error, never a guess.
	EquipFlair(ctx context.Context, userID int64, flairID string) (name string, err error)
}

// PerkGranterName is the Core extension-registry key under which the perks
// plugin publishes its PerkGranter.
const PerkGranterName = "perks.granter"

// PerkGranter mints a tracker perk token — the capability the perks plugin
// exposes so the store can sell freeleech and double-upload without importing
// the perks package.
//
// GRANT-ONLY, on the same terms as RankGranter: the caller has already debited
// whatever the token cost, and this never touches the points ledger. It mints
// an UNSPENT token; choosing which torrent to spend it on is the member's, and
// happens later.
type PerkGranter interface {
	// GrantPerk credits userID one unspent token of the named kind
	// ("freeleech", "upload2x"). An unknown kind is an error rather than a
	// stored token, because points taken for an effect that will never arrive
	// is the one failure a member cannot see.
	GrantPerk(ctx context.Context, userID int64, kind string) error
}
