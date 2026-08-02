package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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

// legacyStampFormat is the dated-folder name the retired archive job used.
// Kept only so its leftovers can be recognised and removed.
const legacyStampFormat = "2006-01-02_150405"

// sweepRetiredArchives deletes the dated folders the archive job left behind.
//
// Retiring that job removed its retention along with it, which orphaned every
// folder it had ever written — 54 GB on this install — with nothing left that
// would ever clean them up. Telling an operator to `rm -rf` by hand is not a
// fix: the code created the mess and the code should clear it.
//
// Deliberately narrow. It removes a directory ONLY when its name parses as the
// archive's own timestamp format, which is the exact rule the old prune used
// ("unrelated files in BackupDir are safe") and the reason db-dumps sitting
// beside them is untouched. Anything a human put there keeps its name and
// therefore keeps its life.
//
// This is a migration, not a policy: once the last install has run it, the
// function has nothing left to find and costs one ReadDir per prune pass.
func sweepRetiredArchives(dir string) (removed int, freed int64, err error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, perr := time.Parse(legacyStampFormat, e.Name()); perr != nil {
			continue // not ours; never delete what we did not create
		}
		path := filepath.Join(dir, e.Name())
		size := dirBytes(path)
		if rerr := os.RemoveAll(path); rerr != nil {
			// Report the first failure but keep going: one undeletable folder
			// must not strand the rest of the reclaim.
			if err == nil {
				err = rerr
			}
			continue
		}
		removed++
		freed += size
	}
	return removed, freed, err
}

// dirBytes totals a directory tree, best-effort — it is only used to report
// how much was reclaimed, so a walk error costs an understated number rather
// than a failed sweep.
func dirBytes(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}
