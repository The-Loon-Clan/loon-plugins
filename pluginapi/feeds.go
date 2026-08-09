package pluginapi

import (
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The feeds importer's operational status.
//
// The importer polls public torrent sources on a schedule and files community
// requests, and until this existed its whole operational surface was a browser:
// /admin/jobs said WHEN it last ran, nothing said WHAT it found, which source
// went quiet, or why. "Did Nyaa stop working" was unanswerable without a shell.
// The plugin already knows all of this per poll; this contract is how the
// host's ops API (GET /ops/feeds) reads it.
//
// Published only in processes where the importer actually runs (the worker),
// so a lookup miss means "the job does not run here", not "the plugin is
// broken" — the ops route answers 503 with exactly that distinction.

// FeedsSource is one source's most recent poll outcome.
type FeedsSource struct {
	Source string `json:"source"`
	// Enabled is false for a source that needs configuration it does not have
	// (nekoBT without an API key). A disabled source with no polls is ordinary;
	// an ENABLED source with no recent LastOKAt is the thing to look at.
	Enabled     bool       `json:"enabled"`
	LastPollAt  *time.Time `json:"last_poll_at,omitempty"`
	LastOKAt    *time.Time `json:"last_ok_at,omitempty"`
	LastItems   int        `json:"last_items"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

// FeedsTotals are the outcome counts of the last completed run. Every fetched
// item lands in exactly one bucket; Created is the number of requests filed.
type FeedsTotals struct {
	Fetched            int `json:"fetched"`
	Created            int `json:"created"`
	CreatedAiring      int `json:"created_airing"`
	Observed           int `json:"observed"`
	SkippedOld         int `json:"skipped_old"`
	SkippedDupHash     int `json:"skipped_dup_hash"`
	SkippedDupTitle    int `json:"skipped_dup_title"`
	TopSearchedCreated int `json:"top_searched_created"`
	TopSearchedSkipped int `json:"top_searched_skipped"`
}

// FeedsSnapshot is the full status: last run outcome plus per-source health.
type FeedsSnapshot struct {
	LastRunAt    *time.Time    `json:"last_run_at,omitempty"`
	LastRunError string        `json:"last_run_error,omitempty"`
	Totals       FeedsTotals   `json:"totals"`
	Sources      []FeedsSource `json:"sources"`
}

// FeedsStatus is the read capability.
type FeedsStatus interface {
	FeedsStatus() FeedsSnapshot
}

// FeedsStatusName is the registry key.
const FeedsStatusName = "feeds.status"

// LookupFeedsStatus resolves the capability. A miss is ordinary on processes
// where the importer does not run.
func LookupFeedsStatus(c *core.Core) (FeedsStatus, bool) {
	v, ok := c.Lookup(FeedsStatusName)
	if !ok {
		return nil, false
	}
	s, ok := v.(FeedsStatus)
	return s, ok
}
