// promo.go declares the contracts around torrent promotions ("magic"): the
// resolver the tracker folds into its announce crediting, and the torrent
// facts a promotion page needs to show and price a cast.
package pluginapi

import (
	"context"
	"time"
)

// The promotion resolver that briefly lived here became the generic USER
// MULTIPLIER system — see multipliers.go, where magic registers as a source
// of the upload/download dimensions and the combining rules live.

// TorrentInfoName is where the tracker publishes a name/size lookup, so a
// promotions page can show what a hash IS and price a cast by size without
// reading a schema that is not its own.
const TorrentInfoName = "tracker.torrentinfo"

// TorrentPromotionsName is the read in the other direction: what promotions
// exist on ONE torrent, so a torrent's own page can show them.
//
// DATA, not markup. The obvious alternative was a "render me a card" seam, and
// it was rejected: the card belongs on the tracker's page, drawn in the
// tracker's own vocabulary, and a fragment posted in from another plugin is
// how a page ends up with two visual languages (the wiki's index spent months
// that way). The promotions plugin knows what is cast; the torrent page knows
// what a panel looks like here.
const TorrentPromotionsName = "magic.torrentpromotions"

// TorrentPromotion is one cast, flattened for display: no ids a reader cannot
// use, no nullable times to unwrap in a template.
type TorrentPromotion struct {
	// Caster is the member's name, already resolved. Empty when the account
	// is gone — a cast outlives its caster, and the row is still true.
	Caster string
	// Scope is "private" (the caster alone), "public" (everyone) or "user"
	// (one named member). It is what decides whether a reader benefits.
	Scope string
	// The multipliers this cast applies. 1/0 is free leech.
	UpRatio, DownRatio float64
	// Until is when it lapses; zero for a cast with no end recorded.
	Until time.Time
	// Active is false for a lapsed or admin-terminated cast. Both are shown —
	// "the history of the file" is the question this answers, and a page that
	// hid the expired ones would answer a different one.
	Active bool
	// Terminated separates an admin's intervention from simple expiry, which
	// are the same absence to a member and very different to an operator.
	Terminated bool
}

// TorrentPromotionsFunc answers for one info-hash (hex), newest first.
// Registered AS this type — a bare func never survives the type assertion.
type TorrentPromotionsFunc func(ctx context.Context, infoHashHex string) ([]TorrentPromotion, error)

// TorrentInfoFunc resolves one info-hash (hex). ok=false for no such
// torrent.
type TorrentInfoFunc func(ctx context.Context, infoHashHex string) (name string, sizeBytes int64, ok bool, err error)
