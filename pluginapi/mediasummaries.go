package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// MediaSummaries answers "which copy is this?" for a whole page of releases at
// once.
//
// The question it exists for is one a listing cannot answer from filenames.
// Six releases of one episode differ in the ways that decide which to take —
// codec, bitrate, resolution, audio layout — and the only thing a row has to
// tell them apart is a name the uploader chose. Names are frequently wrong:
// the row that motivated publishing this had filename tags saying x264 while
// the measured report said HEVC. A tag is a claim; a report is a measurement,
// and the disagreement between them IS the feature.
//
// BATCH, deliberately. A listing renders fifty rows, and the per-release
// lookup that reads naturally at the call site is fifty round trips inside a
// render loop. Callers should ask once with every id on the page.
//
// The map is SPARSE: an id with no live report simply has no key, and that is
// the ordinary case rather than an error — most releases have never been
// measured by anyone. Callers must render the absence as nothing at all, not
// as "unknown", because a column of "unknown" on a page where three rows have
// data reads as a broken feature rather than an unanswered question.
//
// Published by the mediainfo plugin, which owns the reports table and the
// parsing. Absence of the capability is likewise ordinary: a site that has not
// enabled the plugin has no summaries, and the listing should simply not carry
// the column.
type MediaSummaries interface {
	// SummariesFor returns one line per release that has a live report,
	// keyed by release id. Ids with no report are absent from the map.
	//
	// The NEWEST live report per release wins. Two members describing one
	// release is useful on a release page — a re-encode and the original
	// often differ — but a listing row has space for one line, and the most
	// recent is the least likely to describe a file that has since been
	// replaced.
	//
	// An empty id list is a valid call and asks the database nothing.
	SummariesFor(ctx context.Context, releaseIDs []int64) (map[int64]string, error)
}

// MediaSummariesName is the registry key.
const MediaSummariesName = "mediainfo.summaries"

// LookupMediaSummaries resolves the capability. A false return means nothing
// published one — an ordinary state, see the interface doc.
func LookupMediaSummaries(c *core.Core) (MediaSummaries, bool) {
	v, ok := c.Lookup(MediaSummariesName)
	if !ok {
		return nil, false
	}
	m, ok := v.(MediaSummaries)
	return m, ok
}
