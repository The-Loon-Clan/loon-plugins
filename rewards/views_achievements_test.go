package rewards

import (
	"net/url"
	"strings"
	"testing"
)

// The create form's refusals. Pure over url.Values, so every one of them is
// checked here rather than through a database round trip.
func TestParseNewAchievement(t *testing.T) {
	good := func() url.Values {
		return url.Values{
			"slug": {"centurion"}, "name": {"Centurion"}, "metric": {"forum.post.created"},
			"threshold": {"100"}, "reward_id": {"7"},
		}
	}

	t.Run("a complete form parses", func(t *testing.T) {
		a, err := parseNewAchievement(good())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if a.Slug != "centurion" || a.Threshold != 100 || a.RewardID != 7 {
			t.Errorf("got %+v", a)
		}
	})

	t.Run("whitespace is trimmed", func(t *testing.T) {
		f := good()
		f.Set("slug", "  centurion  ")
		f.Set("metric", " forum.post.created ")
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.Slug != "centurion" || a.Metric != "forum.post.created" {
			t.Errorf("not trimmed: %q / %q", a.Slug, a.Metric)
		}
	})

	// A blank name is filled from the slug rather than refused: an achievement
	// with no name renders as an empty badge, and the slug is a usable label.
	t.Run("a missing name falls back to the slug", func(t *testing.T) {
		f := good()
		f.Del("name")
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.Name != "centurion" {
			t.Errorf("name = %q, want the slug", a.Name)
		}
	})

	// THE one that matters. A threshold of 0 completes for every member on the
	// first pass, and a completion is irreversible.
	t.Run("a zero threshold is refused", func(t *testing.T) {
		f := good()
		f.Set("threshold", "0")
		_, err := parseNewAchievement(f)
		if err == nil {
			t.Fatal("accepted threshold 0 — this awards the badge to the whole membership")
		}
		if !strings.Contains(err.Error(), "every member") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})

	for name, mutate := range map[string]func(url.Values){
		"no slug":            func(f url.Values) { f.Del("slug") },
		"no metric":          func(f url.Values) { f.Del("metric") },
		"no reward":          func(f url.Values) { f.Del("reward_id") },
		"reward id zero":     func(f url.Values) { f.Set("reward_id", "0") },
		"reward id garbage":  func(f url.Values) { f.Set("reward_id", "abc") },
		"threshold garbage":  func(f url.Values) { f.Set("threshold", "lots") },
		"negative threshold": func(f url.Values) { f.Set("threshold", "-5") },
		"ordinal garbage":    func(f url.Values) { f.Set("ordinal", "first") },
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			f := good()
			mutate(f)
			if _, err := parseNewAchievement(f); err == nil {
				t.Errorf("accepted a form with %s", name)
			}
		})
	}
}

// The table warns when the reward behind an achievement cannot pay. Those are
// exactly the achievements that look live and can never complete, and the
// completion path refuses them silently from an admin's point of view.
func TestAchievementRowsFlagUnpayableRewards(t *testing.T) {
	rewards := []Reward{
		{ID: 1, Slug: "ok", Kind: KindOneOff, Enabled: true, Payouts: []Payout{{Kind: "points", Amount: 5}}},
		{ID: 2, Slug: "off", Kind: KindOneOff, Enabled: false, Payouts: []Payout{{Kind: "points", Amount: 5}}},
		{ID: 3, Slug: "empty", Kind: KindOneOff, Enabled: true},
		{ID: 4, Slug: "recurring", Kind: KindRecurring, Enabled: true, Payouts: []Payout{{Kind: "points", Amount: 5}}},
	}
	defs := []AchievementDef{
		{Slug: "a", RewardID: 1}, {Slug: "b", RewardID: 2},
		{Slug: "c", RewardID: 3}, {Slug: "d", RewardID: 4},
		{Slug: "e", RewardID: 99}, // reward deleted out from under it
	}

	rows := achievementRows(defs, rewards)
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}
	if !rows[0].Payable {
		t.Errorf("a healthy reward was flagged: %s", rows[0].Why)
	}
	for i, want := range map[int]string{
		1: "disabled", 2: "no payout lines", 3: "one_off", 4: "missing",
	} {
		if rows[i].Payable {
			t.Errorf("row %d (%s) was not flagged", i, rows[i].Slug)
			continue
		}
		if !strings.Contains(rows[i].Why, want) {
			t.Errorf("row %d reason = %q, want it to mention %q", i, rows[i].Why, want)
		}
	}
	// The reward slug is resolved so the page shows a name, not a foreign key.
	if rows[0].RewardSlug != "ok" {
		t.Errorf("reward slug = %q, want \"ok\"", rows[0].RewardSlug)
	}
}

// The page renders achievements through the Store interface, so it works against
// the in-memory store with no database.
func TestAdminPageRendersAchievements(t *testing.T) {
	m := NewMemStore()
	m.Rewards = append(m.Rewards, Reward{
		ID: 1, Slug: "badge", Kind: KindOneOff, Enabled: true,
		Payouts: []Payout{{Kind: "points", Amount: 5}},
	})
	m.SeedAchievement(AchievementDef{
		Slug: "centurion", Name: "Centurion", Metric: "forum.post.created",
		Threshold: 100, RewardID: 1, Enabled: true,
	})

	defs, err := m.ListAchievementDefs(nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := achievementRows(defs, m.Rewards)
	if len(rows) != 1 || rows[0].Slug != "centurion" || !rows[0].Payable {
		t.Fatalf("got %+v", rows)
	}
}
