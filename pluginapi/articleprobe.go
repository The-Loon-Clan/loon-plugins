package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// ArticleProbe is the READ side of a news server, exposed to whoever needs to
// ask questions about articles rather than fetch them wholesale.
//
// It exists for incremental repair. A broken release is usually barely broken
// — a handful of expired articles out of thousands — and re-posting only the
// gaps needs two answers a caller cannot get for itself:
//
//   - WHICH segments the provider no longer has. The health job already learns
//     this on every sweep and records only a COUNT, because a count is all a
//     health badge needs. A repair needs the identities.
//
//   - WHAT BYTE RANGE a missing part covers. That is not in the NZB. It is in
//     the yEnc header of an article that survived — "=ypart begin=… end=…" —
//     so answering it means reading one article's first few hundred bytes.
//
// Published by the usenet plugin, which owns the connection pool, the provider
// credentials and the retry policy. Nothing else should grow a second copy of
// those: an upload agent that also dialled the provider would be a second
// place to configure, rotate and rate-limit, for a capability the site already
// has running on a schedule.
//
// Consumers must treat absence as ordinary. A site with no news server
// configured has no probe, and that is a normal state rather than a fault.
type ArticleProbe interface {
	// Available reports whether the probe can reach a server at all. Separate
	// from a call returning nothing, for the same reason TorznabSearch splits
	// them: callers use it to decide whether to OFFER a repair, and a button
	// that always fails is worse than no button.
	Available() bool

	// StatMissing returns the subset of ids the provider does NOT have.
	//
	// Only definitive absences. A STAT that fails for any other reason — the
	// socket died, the pool was empty, the provider timed out — is NOT an
	// absence and must not appear in the result: treating "we could not ask"
	// as "it is gone" would re-post articles that are still there, which is
	// both wasted upload and a duplicate segment in the spliced NZB.
	//
	// An error means the batch is untrustworthy as a whole. Callers should
	// abandon the plan rather than repair what they managed to check, because
	// a partial answer looks exactly like a smaller outage.
	StatMissing(ctx context.Context, ids []string) ([]string, error)

	// ArticleHead returns up to maxBytes from the START of an article's body.
	//
	// The body, not the headers: yEnc's =ybegin and =ypart are the first lines
	// of the article BODY, and the NNTP headers above them say nothing about
	// part geometry. A few hundred bytes is enough; the cap exists so reading
	// the geometry of a 700 KB article costs one packet rather than the whole
	// thing.
	ArticleHead(ctx context.Context, id string, maxBytes int) ([]byte, error)
}

// ArticleProbeName is the registry key.
const ArticleProbeName = "usenet.articleprobe"

// LookupArticleProbe resolves the capability, returning nil when nothing has
// published one. A nil return is an ordinary state — see the interface doc.
func LookupArticleProbe(c *core.Core) (ArticleProbe, bool) {
	v, ok := c.Lookup(ArticleProbeName)
	if !ok {
		return nil, false
	}
	p, ok := v.(ArticleProbe)
	return p, ok
}
