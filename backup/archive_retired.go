package backup

import "fmt"

// The local archive job is gone.
//
// It staged a full copy of everything it protected onto the app server's own
// disk — every asset class zipped, plus a gzipped pg_dump — before anything
// left the box. That shape has two problems and prod hit both.
//
// It could not run. The pre-flight sized the run at ~211 GB against 136 GB
// free and refused every attempt from 2026-07-04 onward. Of the six runs that
// did start since May, four ran out of disk part-way and left truncated zips
// with `no space left on device` in the log. Meanwhile the retention that
// would have freed room sat AFTER the pre-flight, so a box too full to back up
// was also a box that never pruned: no room -> skip -> no prune -> no room.
//
// And it was never a backup. A copy on the same disk as the original dies with
// it; the most it offered was a fast local restore, which it could not
// complete anyway.
//
// Everything it did is done better by the streaming path that replaced it:
//
//	assets    index -> 64 MiB content-addressed packs -> puller -> off-site array
//	database  the Backup Database job -> backups/db-dumps -> the same pipeline
//
// Packs are assembled on demand straight into the HTTP response (writePack
// takes an io.Writer, and the caller passes the ResponseWriter), so peak disk
// on prod is one buffered write rather than one class. That is what let
// generation 37 move 143.3 GB off a box with 136.5 GB free — a transfer larger
// than the free space it ran on, which the staging design could not have done
// at any batch size.
//
// Left behind on upgrade: the dated folders the old job wrote, which nothing
// prunes now that it is gone. They are safe to delete by hand and match
// backups/<YYYY-MM-DD>_<HHMMSS>; backups/db-dumps must be kept.

// humanBytes formats a byte count for operator-facing job logs. It outlived
// the archive job because the database dump's pre-flight reports in the same
// units.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
