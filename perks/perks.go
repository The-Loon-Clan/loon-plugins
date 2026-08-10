package perks

import (
	"sync"
	"time"
)

// What a perk IS, and the lookup the announce path uses.
//
// The tracker asks for two factors per (member, torrent) on every announce —
// see tracker.Multiplier. That is a hot path: a peer announces every few
// minutes and a busy swarm is thousands of them, so the answer has to come from
// memory. Tokens are bought with points and therefore few, so the whole active
// set fits comfortably and is simply held.

// Kind is a perk's type. Text rather than an enum because a site may add one
// without a migration; an unrecognised kind does nothing, which is the safe
// direction for a perk nobody's code knows about.
type Kind string

const (
	// Freeleech: what you download on this torrent does not count against you.
	Freeleech Kind = "freeleech"
	// UploadDouble: what you upload on this torrent counts twice.
	UploadDouble Kind = "upload2x"
)

// Known reports whether a kind does anything. Used when a token is bought, so
// a typo in an admin's store item is caught at purchase rather than becoming a
// token that silently never works.
func Known(k Kind) bool { return k == Freeleech || k == UploadDouble }

// Active is one perk in force: a member, a torrent, a kind.
type Active struct {
	UserID    int64
	InfoHash  string
	Kind      Kind
	ExpiresAt time.Time
}

// key identifies the grain a perk applies at. A token is spent on ONE torrent,
// so that is the grain — not the member, and not the site.
type key struct {
	userID   int64
	infoHash string
}

// Table is the in-memory set of perks in force.
//
// Replaced wholesale on each refresh rather than mutated, so a reader never
// sees a half-applied update: the announce path takes the pointer once and
// works from a snapshot that cannot change underneath it.
type Table struct {
	mu   sync.RWMutex
	byID map[key][]Active
}

func NewTable() *Table { return &Table{byID: map[key][]Active{}} }

// Replace swaps in a freshly loaded set.
func (t *Table) Replace(all []Active) {
	next := make(map[key][]Active, len(all))
	for _, a := range all {
		k := key{a.UserID, a.InfoHash}
		next[k] = append(next[k], a)
	}
	t.mu.Lock()
	t.byID = next
	t.mu.Unlock()
}

// ActiveFor lists a member's perks that have not lapsed, across every torrent.
//
// For a member-facing surface rather than the announce path: the announce knows
// which torrent it is about and asks Factors, while a page or a widget wants
// "what do I currently hold". Scans the whole table, which is fine because it
// holds only perks IN FORCE — spent tokens leave it on the next refresh.
//
// now is a parameter for the same reason as Factors: the table is refreshed on
// a timer and can still be holding a token that lapsed a minute ago. Showing a
// member a perk they no longer have is worse than the cost of checking.
func (t *Table) ActiveFor(userID int64, now time.Time) []Active {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Active
	for k, list := range t.byID {
		if k.userID != userID {
			continue
		}
		for _, a := range list {
			if a.ExpiresAt.After(now) {
				out = append(out, a)
			}
		}
	}
	return out
}

// Factors returns the upload and download multipliers for one (member,
// torrent), in the shape tracker.Multiplier wants.
//
// now is a parameter because a perk expires, and the table is refreshed on a
// timer: between refreshes it can still be holding a token that lapsed a minute
// ago, and crediting a perk somebody no longer has is worse than the cost of
// checking.
func (t *Table) Factors(userID int64, infoHash string, now time.Time) (up, down float64) {
	up, down = 1, 1
	t.mu.RLock()
	list := t.byID[key{userID, infoHash}]
	t.mu.RUnlock()
	for _, a := range list {
		if !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt) {
			continue
		}
		switch a.Kind {
		case Freeleech:
			down = 0
		case UploadDouble:
			// Not compounded. Two upload tokens on one torrent is 2x, never 4x
			// — the unique index stops a member buying that, and this makes it
			// harmless if one ever slips through.
			up = 2
		}
	}
	return up, down
}

// HasFreeleech reports whether a download is currently free for a member.
//
// Read by the hit-and-run framework, which treats a freeleech snatch as exempt:
// a site that told somebody a download was free has already made its statement
// about what that download owes.
func (t *Table) HasFreeleech(userID int64, infoHash string, now time.Time) bool {
	_, down := t.Factors(userID, infoHash, now)
	return down == 0
}
