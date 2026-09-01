// swarm.go publishes WHO IS SEEDING WHAT, right now, so an economy plugin can
// pay for it without reading the tracker's tables.
//
// This exists because a host built seeding rewards by querying
// tracker.user_stats and tracker.torrents directly. That works, and it was
// flagged rather than hidden — but it makes the tracker's schema an API, so a
// column rename becomes somebody else's outage, and it only works at all on a
// host where the tracker plugin owns those tables.
//
// THE SEEDER COUNT IS PART OF THE ROW, AND THAT IS THE WHOLE POINT. A pool
// economy mints a per-torrent pool, divides it by the number of seeders and
// pays the rows it just read. If the count came from the denormalised
// torrents.seeders column while the payees came from a separate query, the
// two would disagree the moment a peer announced between them, and the shares
// would stop summing to the pool — minting or destroying points every tick, in
// a direction nobody could see, because each half looks right on its own. So
// the count is computed from the SAME rows the call returns and travels with
// them.
package pluginapi

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// SeedingSnapshotName is where the tracker publishes the snapshot.
const SeedingSnapshotName = "tracker.swarmsnapshot"

// SeedRow is one member seeding one torrent at the moment of the call.
type SeedRow struct {
	UserID   int64
	InfoHash string
	// SizeBytes is the torrent's size. Both known economies scale by it:
	// holding 40GB is worth more than holding 40MB.
	SizeBytes int64
	// Seedtime is CUMULATIVE SECONDS this member has seeded this torrent,
	// for a loyalty term. Seconds rather than a duration because it is a
	// stored total, not an interval measured here.
	Seedtime int64
	// Seeders is how many members are seeding THIS torrent, counted from the
	// same rows this call returns — never from a denormalised counter. See
	// the file comment: a pool divided by one number and paid to a different
	// set of rows does not sum to the pool.
	Seeders int
}

// SeedingSnapshotter answers "who is seeding what" for one instant.
type SeedingSnapshotter interface {
	// SeedingSnapshot returns one row per member per torrent they are
	// seeding: left_bytes = 0 and an announce inside freshFor.
	//
	// freshFor is the caller's, not the tracker's, and that is deliberate.
	// "Has the whole file" and "is currently serving it" are different
	// questions, and an economy paying for the second has to choose its own
	// tolerance against its own tick — the tracker's listing counters use a
	// fixed hour, which is the right answer for a display and the wrong one
	// for a payout.
	//
	// Whole-swarm read, so a caller runs it on a tick, not per request.
	SeedingSnapshot(ctx context.Context, freshFor time.Duration) ([]SeedRow, error)
}

// SeedingSnapshots resolves the publisher. Absent is normal: a host with no
// tracker has no swarm, and a caller must degrade rather than fail.
func SeedingSnapshots(c *core.Core) (SeedingSnapshotter, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(SeedingSnapshotName)
	if !ok {
		return nil, false
	}
	s, ok := v.(SeedingSnapshotter)
	return s, ok
}
