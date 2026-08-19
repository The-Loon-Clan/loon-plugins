// downloads.go declares what a plugin needs in order to make sense of a report
// arriving from a member's download client.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// DownloadGrabLookupName is where a host publishes its grab history. Absent
// means a caller can only act on reports that name a release outright.
const DownloadGrabLookupName = "usenet.grabs"

// GrabbedRelease is one release a member downloaded.
type GrabbedRelease struct {
	ID    int64
	Title string
}

// DownloadGrabLookup answers "what has this member downloaded lately".
//
// It exists for one problem, and it is worth stating because the fix looks
// like over-engineering until you hit it: a download client reports on a JOB,
// and a job has a name, not a release id. A member who added the NZB by URL
// gives us the id for free — the URL carries it. A member who saved the file
// and dropped it in a watch folder gives us a filename their client may have
// rewritten, and there is no id anywhere.
//
// Matching that name against what THIS member downloaded recently is what
// closes the gap, and scoping it to their own grabs is what makes matching by
// name safe: the candidate set is a handful of rows they chose themselves,
// not the whole index.
type DownloadGrabLookup interface {
	// RecentGrabs returns the member's most recent grabs, newest first.
	RecentGrabs(ctx context.Context, userID int64, limit int) ([]GrabbedRelease, error)
}

// LookupDownloadGrabLookup resolves the host-registered grab history, if any.
func LookupDownloadGrabLookup(c *core.Core) (DownloadGrabLookup, bool) {
	v, ok := c.Lookup(DownloadGrabLookupName)
	if !ok {
		return nil, false
	}
	g, ok := v.(DownloadGrabLookup)
	return g, ok
}
