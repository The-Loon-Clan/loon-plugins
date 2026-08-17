package achievements

import (
	"context"
	"strings"
	"testing"
)

func catalogueFixture(t *testing.T) (*Plugin, *MemStore) {
	t.Helper()
	m := NewMemStore()
	p := &Plugin{store: m}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return p, m
}

// The catalogue lists what a member can earn, with the criterion and the
// prize. The prize is the reward's SLUG now — the payout phrase required
// reading rewards' tables, which this plugin no longer does.
func TestCatalogueListsEarnableAchievements(t *testing.T) {
	p, m := catalogueFixture(t)
	m.SeedAchievement(AchievementDef{
		Slug: "centurion", Name: "Centurion", Description: "Write 100 posts",
		Metric: "forum.post.created", Threshold: 100, RewardSlug: "badge-bonus", Enabled: true,
	})
	m.SeedAchievement(AchievementDef{
		Slug: "first-login", Name: "First login", Description: "Sign in once",
		Trigger: "auth.login", Enabled: true,
	})

	out, err := p.renderCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Centurion", "Write 100 posts", "forum.post.created", "100", "badge-bonus",
		"First login", "auth.login",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("catalogue is missing %q:\n%s", want, out)
		}
	}
}

// Disabled achievements are not earnable and HIDDEN ones are secret.
// Publishing either on a help page defeats the point of the flag — this is
// the public catalogue, not the admin table.
func TestCatalogueOmitsDisabledAndHidden(t *testing.T) {
	p, m := catalogueFixture(t)
	m.SeedAchievement(AchievementDef{Slug: "live", Name: "Live", Metric: "m",
		Threshold: 1, Enabled: true})
	m.SeedAchievement(AchievementDef{Slug: "off", Name: "OffOne", Metric: "m",
		Threshold: 1, Enabled: false})
	m.SeedAchievement(AchievementDef{Slug: "secret", Name: "SecretOne", Metric: "m",
		Threshold: 1, Enabled: true, Hidden: true})

	out, err := p.renderCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Live") {
		t.Error("an earnable achievement was omitted")
	}
	for _, leak := range []string{"OffOne", "SecretOne"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("%q reached the public catalogue", leak)
		}
	}
}

// THE security property. A ContentBlock's output is inserted into a page
// AFTER sanitising, so nothing downstream will clean it up — every value it
// renders must be escaped here. Achievement names are admin-authored, which
// is not the same as safe.
func TestCatalogueEscapesAuthoredText(t *testing.T) {
	p, m := catalogueFixture(t)
	m.SeedAchievement(AchievementDef{
		Slug: "xss", Name: `<script>alert(1)</script>`,
		Description: `<img src=x onerror=alert(2)>`,
		Metric:      "m", Threshold: 1, Enabled: true,
	})

	out, err := p.renderCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// Assert on the TAG OPENERS, not on attribute text. `onerror=` appears in
	// the output legitimately, inside escaped text that renders as characters
	// and cannot execute — asserting on the substring would send someone
	// hunting for a hole that is not there.
	for _, danger := range []string{"<script", "<img"} {
		if strings.Contains(s, danger) {
			t.Errorf("%q reached the output as markup rather than text", danger)
		}
	}
	// Escaped, not dropped: the admin should still recognise what they typed.
	if !strings.Contains(s, "&lt;script&gt;") {
		t.Errorf("the name was not rendered in escaped form:\n%s", s)
	}
}

// An empty catalogue says so rather than rendering an empty table.
func TestEmptyCatalogueSaysSo(t *testing.T) {
	p, _ := catalogueFixture(t)
	out, err := p.renderCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "No achievements") {
		t.Errorf("expected an empty-state message, got:\n%s", out)
	}
}
