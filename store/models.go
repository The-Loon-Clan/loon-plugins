package store

import (
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

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

	// RewardPerk credits a tracker perk token: RewardRef = the kind
	// ("freeleech", "upload2x"). RewardDays is unused — a token does not
	// expire while it is held, and the perks plugin owns how long it lasts
	// once SPENT, which is a different clock and not the store's to set.
	// Invokes pluginapi.PerkGranter.
	RewardPerk RewardType = "perk"

	// RewardFlair equips a profile flair: RewardRef = the flair id
	// ("supporter", "vip", ...). RewardDays is unused — a flair is worn until
	// replaced, not until a date. Invokes pluginapi.FlairGranter.
	RewardFlair RewardType = "flair"

	// RewardUpload credits purchased upload: RewardRef = whole GB (as
	// string). Needs the tracker's pluginapi.TrackerCredit; absent, the
	// purchase fails and the points come back.
	RewardUpload RewardType = "upload_gb"
	// RewardDownload FORGIVES downloaded GB: RewardRef = whole GB. Readers
	// clamp at zero, so over-buying is generosity rather than a negative.
	RewardDownload RewardType = "download_gb"

	// RewardMedalGrant hands over a medal: RewardRef = the medal slug.
	// Grants are idempotent (the granter's contract), so re-buying an owned
	// medal spends the points for nothing — the medals page's own shop
	// refuses that; this type exists so bundles and store-first sites can
	// sell medals beside everything else.
	RewardMedalGrant RewardType = "medal"
)

// builtinTypes describes the reward types the store grants itself, in the
// order the def editor offers them.
//
// The same facts the switches above encode, said as data — because the def
// editor's dropdown is now built from this list PLUS whatever plugins
// contribute under pluginapi.StoreItemTypePrefix, and two hand-written
// <option> lists in a template were free to disagree with the code that
// grants (the invite type was in the switch and missing from validItem for
// exactly that long).
//
// Icons match Item.Icon, which stays the store card's answer: an item row is
// enough to draw a card, and a card should not need a registry lookup to know
// what picture to use.
var builtinTypes = []pluginapi.StoreItemTypeInfo{
	{Kind: string(RewardRank), Label: "Rank", RefLabel: "rank id", Icon: "shield"},
	{Kind: string(RewardInvite), Label: "Invites", RefLabel: "how many", Icon: "envelope"},
	{Kind: string(RewardPerk), Label: "Perk token", RefLabel: "perk kind", Icon: "bolt"},
	{Kind: string(RewardFlair), Label: "Flair", RefLabel: "flair id", Icon: "star"},
	{Kind: string(RewardUpload), Label: "GB uploaded", RefLabel: "whole GB", Icon: "tag"},
	{Kind: string(RewardDownload), Label: "GB downloaded (wipe)", RefLabel: "whole GB", Icon: "tag"},
	{Kind: string(RewardMedalGrant), Label: "Medal", RefLabel: "medal slug", Icon: "tag"},
}

// builtin reports whether a reward type is one the store grants itself —
// anything else is a contributed type and belongs to a provider.
func builtin(kind string) bool {
	for _, t := range builtinTypes {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

// Item is a purchasable catalog entry priced in points. PointsCost is
// int to line up with the core PointsService (Deduct takes n int).
type Item struct {
	ID          int    `db:"id"`
	Name        string `db:"name"`
	Description string `db:"description"`
	PointsCost  int    `db:"points_cost"`
	RewardType  string `db:"reward_type"`
	RewardRef   string `db:"reward_ref"`
	RewardDays  int    `db:"reward_days"`
	Stock       int    `db:"stock"` // remaining count; -1 = unlimited
	Active      bool   `db:"active"`
	SortOrder   int    `db:"sort_order"`
	// Flavour is which half of the site sells this: "tracker", "indexer" or
	// "both" (the default). The shop hides — and purchase refuses — items
	// whose half is off, read through the store.flavour host seam.
	Flavour   string    `db:"flavour"`
	CreatedAt time.Time `db:"created_at"`
}

// InStock reports whether the item can still be bought.
func (i *Item) InStock() bool { return i.Stock < 0 || i.Stock > 0 }

// Icon is the sprite id the store card draws beside an item's name.
//
// DERIVED from RewardType rather than stored. What an item is is already
// recorded in the row, and an icon column would be a second, hand-maintained
// answer to the same question — free to disagree with the first, and one more
// field an admin has to get right on every item they add.
//
// The ids are the HOST's sprite symbols. That is the same coupling this
// plugin's markup already has to --text-muted and --bs-warning: it renders
// inside host chrome through RenderPage, so it draws with the host's assets.
// A host missing a symbol renders an empty <use>, which is why the card does
// not reserve space around it.
func (i *Item) Icon() string {
	switch RewardType(i.RewardType) {
	case RewardRank:
		return "shield"
	case RewardInvite:
		return "envelope"
	case RewardPerk:
		return "bolt"
	case RewardFlair:
		return "star"
	}
	// Deliberately generic. The migration comments name freeleech and points
	// bonuses as the next reward types; until they exist, a neutral tag reads
	// better than a specific icon that turns out to mean the wrong thing.
	return "tag"
}

// Purchase is one row of the buy ledger.
type Purchase struct {
	ID          int       `db:"id"`
	UserID      int       `db:"user_id"`
	ItemID      int       `db:"item_id"`
	PointsSpent int       `db:"points_spent"`
	CreatedAt   time.Time `db:"created_at"`
}
