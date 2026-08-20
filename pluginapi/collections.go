// collections.go declares where a SELECTION of releases can be sent.
//
// The host lets a member tick rows across a listing and accumulate them — a
// cart. What a cart is for is emptying somewhere, and one of the places is a
// collection: a playlist, a watchlist, whatever the site's plugin calls the
// thing a member curates. The host must not know which.
//
// So this is deliberately the narrowest contract that makes "add these to that"
// work: name the member's own collections, and take a batch. It cannot create
// one, cannot read what is in one, and cannot touch anybody else's — a cart is
// a shopping trolley, not an editor.
//
// See anidb.go for the package-level contract discipline this follows.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// CollectionSinkName is the Core extension-registry key under which a
// collections plugin publishes its CollectionSink.
const CollectionSinkName = "collections.sink"

// Collection is one place a member can put releases.
type Collection struct {
	// Slug is the stable key a form posts back. Never an id: the host renders
	// this into a select and reads it out of a POST, and an integer id from an
	// untrusted form is one typo away from naming somebody else's row.
	Slug string
	// Name is what the member called it.
	Name string
	// Count is how many releases are in it already, for a chooser that would
	// otherwise be a list of indistinguishable names.
	Count int
}

// CollectionSink is implemented by whichever plugin owns collections.
type CollectionSink interface {
	// CollectionsOf lists the collections this member may ADD TO — their own,
	// and nobody else's. The host renders these as choices, so a collection
	// that appears here is one the member is being invited to write to.
	CollectionsOf(ctx context.Context, userID int64) ([]Collection, error)

	// AddToCollection puts releases into one, and returns how many were
	// actually added.
	//
	// A BATCH rather than one call per release, because the caller is a cart
	// and the whole point of a cart is that it holds forty things. Duplicates
	// are the implementation's to skip — a member ticking a release they
	// already saved should get no error and no second copy, which is why the
	// count returned is what was added rather than what was asked for.
	//
	// The userID is checked against the collection's owner by the
	// implementation. The host has no way to know who owns what.
	AddToCollection(ctx context.Context, userID int64, slug string, releaseIDs []int64) (added int, err error)
}

// Collections resolves the registered implementation. Absent is normal — a site
// with no collections plugin simply does not offer the choice.
func Collections(c *core.Core) (CollectionSink, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(CollectionSinkName)
	if !ok {
		return nil, false
	}
	s, ok := v.(CollectionSink)
	return s, ok
}
