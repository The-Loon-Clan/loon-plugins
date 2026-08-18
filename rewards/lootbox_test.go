package rewards

import (
	"bytes"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"testing"
)

func box(entries ...LootboxEntry) []LootboxEntry { return entries }

// The walk is the whole feature: it decides who gets the rare thing, and every
// failure mode is silent. A biased range, a dropped last entry, an off-by-one
// at a boundary — none of them error, they just quietly hand out the wrong
// prize at the wrong rate.
//
// Tested EXHAUSTIVELY over the range rather than by sampling a random draw: a
// probabilistic test of randomness is a flaky test, and the property that
// actually matters is that each entry owns a run exactly as wide as its weight.
func TestPickByWeightGivesEachEntryItsExactShare(t *testing.T) {
	entries := []LootboxEntry{
		{ID: 1, RewardID: 10, Weight: 1},
		{ID: 2, RewardID: 20, Weight: 3},
		{ID: 3, RewardID: 30, Weight: 6},
	}
	total := LootboxTotalWeight(entries)
	if total != 10 {
		t.Fatalf("total = %d, want 10", total)
	}
	hits := map[int64]int{}
	for n := int64(0); n < int64(total); n++ {
		hits[pickByWeight(entries, n).ID]++
	}
	for _, e := range entries {
		if hits[e.ID] != e.Weight {
			t.Errorf("entry %d owns %d of the range, want %d — the odds are not the weights",
				e.ID, hits[e.ID], e.Weight)
		}
	}
	// The boundaries: the first n and the last n in the range must both land,
	// and on the entries at either end of the walk.
	if got := pickByWeight(entries, 0).ID; got != 1 {
		t.Errorf("n=0 picked %d, want the first entry", got)
	}
	if got := pickByWeight(entries, int64(total)-1).ID; got != 3 {
		t.Errorf("n=total-1 picked %d, want the last entry", got)
	}
}

// Row order must not change the odds: the draw sorts by id first, so equal
// randomness cannot be turned into a bias by whatever order the rows arrived.
func TestDrawLootboxIsOrderIndependent(t *testing.T) {
	forward := []LootboxEntry{
		{ID: 1, RewardID: 10, Weight: 1},
		{ID: 2, RewardID: 20, Weight: 1},
	}
	reversed := []LootboxEntry{forward[1], forward[0]}
	// crypto/rand is the real source here — this asserts only that both
	// orderings answer from the same set, not which one comes back.
	for i := 0; i < 20; i++ {
		a, err := DrawLootbox(rand.Reader, forward)
		if err != nil {
			t.Fatalf("draw: %v", err)
		}
		b, err := DrawLootbox(rand.Reader, reversed)
		if err != nil {
			t.Fatalf("draw: %v", err)
		}
		if a.ID != 1 && a.ID != 2 {
			t.Fatalf("draw returned an entry that is not in the box: %+v", a)
		}
		if b.ID != 1 && b.ID != 2 {
			t.Fatalf("reversed draw returned an entry that is not in the box: %+v", b)
		}
	}
}

// A box with nothing in it must SAY so. It is a configuration fault — a slug
// nobody filled in, or one whose rewards were all deleted (the FK cascades) —
// and a member has just opened it, so silence would mean paying them nothing
// and reporting success.
func TestDrawLootboxRefusesAnEmptyBox(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []LootboxEntry
	}{
		{"no entries at all", nil},
		{"every weight zero", box(LootboxEntry{ID: 1, RewardID: 10, Weight: 0})},
		{"an entry pointing at no reward", box(LootboxEntry{ID: 1, Weight: 5})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DrawLootbox(bytes.NewReader(make([]byte, 64)), tc.entries); !errors.Is(err, ErrEmptyLootbox) {
				t.Errorf("err = %v, want ErrEmptyLootbox", err)
			}
		})
	}
}

// A failed entropy read must still hand over a prize — the member has already
// paid for the box by the time this runs — and it must be the FIRST entry, so
// a broken reader cannot become a jackpot.
func TestDrawLootboxSurvivesAFailedReader(t *testing.T) {
	entries := box(
		LootboxEntry{ID: 1, RewardID: 10, Weight: 1},
		LootboxEntry{ID: 2, RewardID: 20, Weight: 999},
	)
	got, err := DrawLootbox(failReader{}, entries)
	if err != nil {
		t.Fatalf("a failed reader lost the box: %v", err)
	}
	if got.ID != 1 {
		t.Errorf("entropy failure paid out entry %d — the fallback must not be the rare one", got.ID)
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// The odds an operator reads have to be the odds the draw uses.
func TestLootboxChance(t *testing.T) {
	entries := box(
		LootboxEntry{ID: 1, Weight: 1},
		LootboxEntry{ID: 2, Weight: 3},
		LootboxEntry{ID: 3, Weight: 996},
	)
	total := LootboxTotalWeight(entries)
	if total != 1000 {
		t.Fatalf("total = %d, want 1000", total)
	}
	// One decimal, deliberately: a 1-in-1000 rare reads as 0.1%, and as 0% if
	// this rounded to whole numbers — describing a prize that CAN be drawn as
	// one that cannot.
	if got := entries[0].Chance(total); got != 0.1 {
		t.Errorf("chance = %v, want 0.1", got)
	}
	if got := entries[2].Chance(total); got != 99.6 {
		t.Errorf("chance = %v, want 99.6", got)
	}
	// An empty box divides by nothing rather than panicking.
	if got := entries[0].Chance(0); got != 0 {
		t.Errorf("chance against an empty box = %v, want 0", got)
	}
}

// EVERY POST form on this page must carry a token.
//
// Not hypothetical: this page shipped with five POST forms and NO tokens, and
// the host gates every POST globally — so toggling a reward, creating one and
// test-granting each answered 403 for every operator who ever tried. The
// access audit cannot see it, because it probes destructive POSTs WITH a valid
// token by design; only counting the tokens in the rendered markup catches it.
func TestEveryAdminFormCarriesACSRFToken(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_admin.html", adminVM{
		CSRFToken: "test-csrf",
		Rewards:   []Reward{{ID: 1, Slug: "welcome", Name: "Welcome", Enabled: true}},
		PickedBox: "daily-crate",
		BoxEntries: []LootboxEntry{
			{ID: 1, BoxSlug: "daily-crate", RewardID: 1, Weight: 3, RewardSlug: "welcome", RewardName: "Welcome"},
		},
		BoxTotal: 3,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()

	forms := regexp.MustCompile(`(?s)<form[^>]*method="post"[^>]*>.*?</form>`).FindAllString(out, -1)
	if len(forms) == 0 {
		t.Fatal("no POST forms found — the scan is broken, not the page")
	}
	for i, f := range forms {
		if !strings.Contains(f, `name="_csrf"`) {
			t.Errorf("form %d posts with no token, so it 403s for every operator: %s", i, f)
		}
		if strings.Contains(f, `name="_csrf" value=""`) {
			t.Errorf("form %d carries an EMPTY token, which 403s like a missing one: %s", i, f)
		}
	}
	// And the token that reached them is the one the view model carried.
	if !strings.Contains(out, "test-csrf") {
		t.Error("the page rendered no token at all")
	}
}
