// apikey.go declares how a plugin identifies the member behind a MACHINE
// request — one made by a download client, a script or a cron job rather than
// by a browser with a session.
//
// The host owns keys: it mints them, shows them on an account page, rotates
// them, and meters what they are allowed to do. A plugin that exposes an
// endpoint for a member's own tooling needs exactly one thing from all of
// that — which member is this — and asking for it through a seam is what stops
// every such plugin from growing its own key table.
package pluginapi

import "context"

// APIKeyResolverName is the Core extension-registry key.
//
// Absent means the host mints no API keys, and a plugin that needs one must
// refuse its machine endpoints rather than mount them unauthenticated. That
// refusal is the whole point of looking it up: an endpoint that accepts a key
// nobody can validate accepts every key.
const APIKeyResolverName = "auth.apikey"

// APIKeyResolver turns a key into the member it belongs to.
type APIKeyResolver interface {
	// ResolveAPIKey returns the member's id for a key, or ok=false when the
	// key is unknown.
	//
	// An unknown key is NOT an error: keys arrive from the open internet, and
	// a typo, a rotated key and a probe are all ordinary events a caller
	// answers with 401. An error means the lookup itself failed — the
	// database is down — which is a 500 and a different thing.
	ResolveAPIKey(ctx context.Context, key string) (userID int64, ok bool, err error)
}

// APIKeyIssuer is the richer capability a host MAY publish under the same
// registry key: not just "whose key is this" but "what is this member's key".
//
// Optional and type-asserted rather than a second registry entry, the same way
// a Catalog may also be a CatalogSink. A plugin that only authenticates
// requests wants the narrow interface and should not have to care; a plugin
// that hands a member a preconfigured script needs the key itself, and asking
// them to copy-paste it is precisely the step that stops people finishing a
// setup.
type APIKeyIssuer interface {
	APIKeyResolver
	// APIKeyFor returns the member's current key, minting one if they have
	// none — a member who has never opened the API page still gets a script
	// that works.
	APIKeyFor(ctx context.Context, userID int64) (string, error)
}
