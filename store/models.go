package store

import "time"

// RewardType enumerates what a store item grants on purchase. Each
// value maps to a capability the plugin invokes after debiting points.
type RewardType string

const (
	// RewardRank grants a rank: RewardRef = target rank id (as string),
	// RewardDays = subscription duration. Invokes pluginapi.RankGranter.
	RewardRank RewardType = "rank"

	// RewardInvite credits invites: RewardRef = how many (as string;
	// empty or unparseable means 1, since one invite is the only shape
	// this has ever been sold in). RewardDays is unused — an invite does
	// not expire. Invokes pluginapi.InviteGranter, published by the host
	// rather than a sibling plugin because invites live on users.
	RewardInvite RewardType = "invite"
)

// Item is a purchasable catalog entry priced in points. PointsCost is
// int to line up with the core PointsService (Deduct takes n int).
type Item struct {
	ID          int       `db:"id"`
	Name        string    `db:"name"`
	Description string    `db:"description"`
	PointsCost  int       `db:"points_cost"`
	RewardType  string    `db:"reward_type"`
	RewardRef   string    `db:"reward_ref"`
	RewardDays  int       `db:"reward_days"`
	Stock       int       `db:"stock"` // remaining count; -1 = unlimited
	Active      bool      `db:"active"`
	SortOrder   int       `db:"sort_order"`
	CreatedAt   time.Time `db:"created_at"`
}

// InStock reports whether the item can still be bought.
func (i *Item) InStock() bool { return i.Stock < 0 || i.Stock > 0 }

// Purchase is one row of the buy ledger.
type Purchase struct {
	ID          int       `db:"id"`
	UserID      int       `db:"user_id"`
	ItemID      int       `db:"item_id"`
	PointsSpent int       `db:"points_spent"`
	CreatedAt   time.Time `db:"created_at"`
}
