package downloads

import (
	"context"
	"time"
)

// Report is one member's standing opinion of one release, as their download
// client reported it.
type Report struct {
	UserID    int64     `db:"user_id"`
	ReleaseID int64     `db:"release_id"`
	Status    string    `db:"status"`
	Detail    string    `db:"detail"`
	Client    string    `db:"client"`
	Reports   int       `db:"reports"`
	FirstAt   time.Time `db:"first_at"`
	LastAt    time.Time `db:"last_at"`
}

// ReleaseTally is what a release looks like to the people who downloaded it.
type ReleaseTally struct {
	ReleaseID int64 `db:"release_id"`
	// Failed and OK count MEMBERS, not reports: one member retrying a broken
	// download six times is one opinion, and a tally that said six would send
	// staff chasing a release nobody but them had touched.
	Failed int       `db:"failed"`
	OK     int       `db:"ok"`
	LastAt time.Time `db:"last_at"`
}

// RecentReport is a staff-view row: a report with the member's name resolved
// by the caller, since this plugin owns no users table.
type RecentReport struct {
	Report
	Username string
}

type Store interface {
	// Record upserts one member's opinion of one release and returns the row
	// as it now stands.
	//
	// Upsert rather than insert, for the reason on the migration: a client
	// re-runs post-processing on every retry, and those are repetitions of one
	// opinion. A member whose retry finally SUCCEEDS overwrites their own
	// failure, which is correct — the release worked for them in the end.
	Record(ctx context.Context, r Report) (Report, error)

	// Tally reports how a set of releases looks to the members who downloaded
	// them. Releases nobody reported on are absent from the map, never
	// present-and-zero: "nobody has said anything" and "everybody said it was
	// fine" are different answers and a page renders them differently.
	Tally(ctx context.Context, releaseIDs []int64) (map[int64]ReleaseTally, error)

	// Recent lists the newest reports for the staff view, worst first within
	// the window — a failure is what somebody needs to look at.
	Recent(ctx context.Context, limit int) ([]Report, error)

	// Counts totals the table for the admin page's summary.
	Counts(ctx context.Context) (failed, ok int, err error)
}
