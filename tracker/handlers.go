package tracker

// Handlers serves the tracker: the two BitTorrent endpoints a client talks to,
// and the member pages a browser does.
//
// One type rather than two, because they share the store and the gate and the
// split would be along a line nothing else respects — the .torrent download is a
// browser request that bakes in the same passkey an announce arrives with.
type Handlers struct {
	store   Store
	peers   *PeerStore
	gate    Gate
	siteURL string
}

// NewHandlers builds the handler set. siteURL is the absolute base (scheme +
// host, no trailing slash) used to build the announce URL baked into every
// downloaded .torrent — get it wrong and every torrent points somewhere that
// cannot answer.
func NewHandlers(store Store, peers *PeerStore, gate Gate, siteURL string) *Handlers {
	return &Handlers{store: store, peers: peers, gate: gate, siteURL: trimRightSlash(siteURL)}
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
