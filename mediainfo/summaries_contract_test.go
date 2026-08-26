package mediainfo

import (
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The published capability and the store that backs it must not drift apart.
//
// Provision registers p.st under pluginapi.MediaSummariesName, and a registry
// is an interface{} map: a signature change on SummariesFor would compile
// perfectly, register a value that no longer satisfies the contract, and fail
// at the consumer's type assertion — at runtime, on someone else's site,
// rendering as "the column silently went away".
//
// These assertions move that failure to this package's build. They are the
// whole test: there is nothing to exercise at runtime that the store's own
// integration tests (TestSummariesForIsABatch and its siblings) do not already
// cover.
var (
	_ pluginapi.MediaSummaries = (Store)(nil)
	_ pluginapi.MediaSummaries = (*PGStore)(nil)
)

// TestSummariesContractIsPublished exists so the assertions above are attached
// to a named test rather than sitting as bare package-level vars a tidy-up
// might read as unused.
func TestSummariesContractIsPublished(t *testing.T) {
	if pluginapi.MediaSummariesName != "mediainfo.summaries" {
		t.Errorf("registry key changed to %q — consumers look this up by "+
			"string, so renaming it silently unpublishes the capability",
			pluginapi.MediaSummariesName)
	}
}
