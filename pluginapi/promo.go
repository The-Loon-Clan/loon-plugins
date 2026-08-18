// promo.go declares the contracts around torrent promotions ("magic"): the
// resolver the tracker folds into its announce crediting, and the torrent
// facts a promotion page needs to show and price a cast.
package pluginapi

import "context"

// PromoResolverName is where the magic plugin publishes its resolver. The
// tracker looks it up in Start (softly — a site without the plugin credits
// normally) and folds it into Credit BEST-OF alongside any installed
// multiplier: the highest upload factor and the lowest download factor win,
// which is the promotion rule the genre settled on — a freeleech token and
// a 2× magic together credit 2× up and 0× down, never a compromise of
// either.
const PromoResolverName = "magic.promo"

// PromoResolver answers the effective promotion for one member on one
// torrent, across every active magic visible to them. (1, 1) means none.
//
// Called on the announce path: implementations must be one cheap indexed
// read, and errors mean "credit normally", never "fail the announce".
type PromoResolver interface {
	EffectiveRatios(ctx context.Context, infoHash string, userID int64) (up, down float64, err error)
}

// TorrentInfoName is where the tracker publishes a name/size lookup, so a
// promotions page can show what a hash IS and price a cast by size without
// reading a schema that is not its own.
const TorrentInfoName = "tracker.torrentinfo"

// TorrentInfoFunc resolves one info-hash (hex). ok=false for no such
// torrent.
type TorrentInfoFunc func(ctx context.Context, infoHashHex string) (name string, sizeBytes int64, ok bool, err error)
