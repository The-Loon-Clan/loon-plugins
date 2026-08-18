package rewards

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"sort"
)

// Lootboxes: a named, weighted set of rewards, one of which is drawn when a
// member opens the box.
//
// The box has no row of its own — it IS its slug, the set of entries carrying
// it. There is nothing to say about a box that its contents do not say, and a
// header table would let one exist with nothing in it: a box that draws
// nothing and reports no error.
//
// A box is handed over like anything else: PayoutLootbox targets a slug, so an
// achievement, a scheduled event, the pot's consolation and a store item can
// all give one without any of them learning what a box is. Opening happens at
// settle time — the draw picks an entry and the chosen reward is granted
// through the same GrantOneOff every other giver already uses.

// LootboxEntry is one line of a box: a prize and its relative chance.
type LootboxEntry struct {
	ID       int64  `db:"id"`
	BoxSlug  string `db:"box_slug"`
	RewardID int64  `db:"reward_id"`
	Weight   int    `db:"weight"`
	Ordinal  int    `db:"ordinal"`
	// RewardSlug and RewardName are joined for display; the draw needs neither.
	RewardSlug string `db:"reward_slug"`
	RewardName string `db:"reward_name"`
}

// Chance is this entry's share of the box, as a percentage of the total weight
// passed in. For the admin table, where the useful question is "how likely is
// this" and the answer is not the weight column.
//
// Rounded to one decimal rather than an integer: a 1-in-1000 rare reads as
// 0.1%, and as 0% if this rounded to whole numbers — which would describe an
// item that can be drawn as one that cannot.
func (e LootboxEntry) Chance(total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(e.Weight) * 100 / float64(total)
}

// LootboxTotalWeight sums a box. Zero for an empty box, which every caller must
// treat as "nothing to draw" rather than dividing by it.
func LootboxTotalWeight(entries []LootboxEntry) int {
	var n int
	for _, e := range entries {
		if e.Weight > 0 {
			n += e.Weight
		}
	}
	return n
}

// ErrEmptyLootbox is an open against a box with no entries — a slug nobody
// filled in, or one whose rewards were all deleted (the FK cascades). It is
// distinct from a failure because the caller must NOT retry it, and must not
// swallow it either: a member opened a box and got nothing.
var ErrEmptyLootbox = fmt.Errorf("rewards: lootbox is empty")

// DrawLootbox picks one entry, weight-proportionally.
//
// crypto/rand, not math/rand: this decides who gets the rare thing, and a
// members-can-predict-it draw is the kind of thing a private site argues about
// for a year. The cost is one read per open.
//
// Deterministic iteration — entries are sorted by id before the walk — so that
// equal randomness cannot be turned into a bias by the order rows happened to
// come back in.
func DrawLootbox(r io.Reader, entries []LootboxEntry) (LootboxEntry, error) {
	usable := make([]LootboxEntry, 0, len(entries))
	for _, e := range entries {
		if e.Weight > 0 && e.RewardID > 0 {
			usable = append(usable, e)
		}
	}
	if len(usable) == 0 {
		return LootboxEntry{}, ErrEmptyLootbox
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].ID < usable[j].ID })

	total := LootboxTotalWeight(usable)
	n, err := rand.Int(r, big.NewInt(int64(total)))
	if err != nil {
		// A failed entropy read must not fail the open — the member has spent
		// whatever the box cost by now. The FIRST entry is the deterministic
		// fallback, and it is the least valuable one on any sanely ordered
		// box, so the failure cannot become a jackpot.
		return usable[0], nil
	}
	return pickByWeight(usable, n.Int64()), nil
}

// pickByWeight is the walk, split from the entropy so it can be tested
// exhaustively: n in [0,total) maps to exactly one entry, and every entry owns
// a run of n as wide as its weight.
//
// Separate because testing the two together means asserting how crypto/rand
// consumes bytes from a reader — which is an implementation detail of the
// standard library, not of this draw, and a test built on it proves nothing
// about the odds.
func pickByWeight(sorted []LootboxEntry, n int64) LootboxEntry {
	left := n
	for _, e := range sorted {
		left -= int64(e.Weight)
		if left < 0 {
			return e
		}
	}
	// Unreachable while n < the sum of the weights walked above; returning the
	// last entry rather than panicking keeps that arithmetic from ever costing
	// a member their box.
	return sorted[len(sorted)-1]
}
