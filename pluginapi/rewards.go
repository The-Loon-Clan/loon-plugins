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
