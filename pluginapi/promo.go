// promo.go declares the contracts around torrent promotions ("magic"): the
// resolver the tracker folds into its announce crediting, and the torrent
// facts a promotion page needs to show and price a cast.
package pluginapi

import "context"

// The promotion resolver that briefly lived here became the generic USER
// MULTIPLIER system — see multipliers.go, where magic registers as a source
// of the upload/download dimensions and the combining rules live.

// TorrentInfoName is where the tracker publishes a name/size lookup, so a
// promotions page can show what a hash IS and price a cast by size without
// reading a schema that is not its own.
const TorrentInfoName = "tracker.torrentinfo"

// TorrentInfoFunc resolves one info-hash (hex). ok=false for no such
// torrent.
type TorrentInfoFunc func(ctx context.Context, infoHashHex string) (name string, sizeBytes int64, ok bool, err error)
