// requestfiler.go is a PROPOSED seam: the host asks the request board to file
// an automated request, so the content pipeline can close its own loop.
//
// WHY THIS IS A PROPOSAL AND NOT YET LIVE. The request board (loon-plugins/
// requests) is a host-data plugin: nzb_requests is a HOST-owned table, and the
// board reads and writes it through a RequestStore the host implements. So the
// host that owns the table can already file a row -- but the board owns the
// meaning of a well-formed automated request (which Origin, which dedup, which
// fields an agent can act on), and a trigger that hand-rolled an INSERT would
// drift from the board the first time either side changed. This contract is
// where that meaning crosses once.
//
// It maps onto the board's EXISTING model rather than inventing one. requests.
// Request already carries Origin (with ScopeAutomated meaning "feed importer,
// resurrector, bulk import"), Season, Episodes, ImdbID/TvdbID/TmdbID, InfoHash,
// SeedCount and Notes -- every field a TV-gap request needs. The board grew
// those for the production feed importer; a gap trigger is the same shape of
// caller. Nothing here asks the board for a new column.
//
// The host consumer (the TV-gap trigger) is built against this and no-ops
// cleanly when nobody publishes a filer -- which is every host, including this
// demo, until the board registers one. See the host's tvgaps request trigger.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// RequestFilerName is where a request board publishes its automated-filing
// entry point. `requests.filer`, in the board's own `requests.*` namespace.
const RequestFilerName = "requests.filer"

// FiledRequest is one automated request, in the fields the board already
// stores. Everything is optional except Title and Category: an agent can act
// on a title and a category alone, and the ids and candidate only sharpen it.
type FiledRequest struct {
	// Title is what a person would read: "All American S08E07".
	Title string
	// Category is the board's category vocabulary; "tv" for a gap.
	Category string

	// Season and Episodes are the board's own string fields -- "8", "7" --
	// left as strings because a season pack files as "8" with no episode and
	// the board already stores the distinction that way.
	Season   string
	Episodes string

	// External ids, empty when unknown. These are the whole reason a gap
	// request beats a hand-typed one: the show is identified precisely, so an
	// agent searches by id rather than by a title that may not match.
	ImdbID string
	TvdbID string
	TmdbID string

	// A candidate already located, when the trigger searched before filing.
	// Optional: filing the ids alone is a valid request the agent can source
	// itself. When present, InfoHash gives the agent a head start and
	// SeedCount tells the board the swarm was alive when the request was made.
	InfoHash  string
	SourceURL string
	SeedCount int

	// Reason is why this was filed automatically, for the board's Notes and
	// for a member reading an automated row: "aired 2026-08-18, no release in
	// the index". Not machine-read.
	Reason string
}

// RequestFileResult reports what the board did, so a trigger that runs every
// few hours can tell a new request from one it already filed.
type RequestFileResult struct {
	// ID is the request row, created or pre-existing.
	ID int64
	// Created is false when the board deduped this against a request it
	// already held. The trigger relies on this: without a dedup promise a
	// six-hourly pass would file the same gap forty times before an agent
	// touched it.
	Created bool
}

// RequestFiler is the board's automated-filing surface.
type RequestFiler interface {
	// FileAutomated files a request with the board's automated Origin, or
	// returns the existing one when the board already holds a match.
	//
	// DEDUP IS THE BOARD'S, on whatever identity it considers "the same
	// request" -- infohash when one is given, else title-and-episode. The
	// trigger cannot dedup for it: two hosts, two definitions of same, and
	// the board is the one that has to answer a member asking "why are there
	// three of these". The contract is only that calling twice for one gap
	// does not make two open rows.
	FileAutomated(ctx context.Context, req FiledRequest) (RequestFileResult, error)
}

// LookupRequestFiler resolves the filer, or false when no board publishes one
// -- which is the ordinary state on a host without the request board, and the
// state the trigger must handle rather than assume away.
func LookupRequestFiler(c *core.Core) (RequestFiler, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(RequestFilerName)
	if !ok {
		return nil, false
	}
	f, ok := v.(RequestFiler)
	return f, ok
}
