package achievements

import (
	"context"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// The reverse crossing: a reward's payout line (or admin tooling) awarding a
// badge directly through pluginapi.AchievementGranter.

// A direct grant completes and stamps paid_at — nothing further is owed, by
// design: running the achievement's own reward from here is the loop the
// contract exists to avoid.
func TestGrantAchievementCompletesAndSettles(t *testing.T) {
	m := NewMemStore()
	// Deliberately an achievement WITH a reward_slug: the contract says the
	// reward is not run, and paid_at stamped anyway is what keeps the repair
	// sweep from running it later either.
	d := m.SeedAchievement(AchievementDef{Slug: "founder", RewardSlug: "founder-reward",
		Trigger: "x", Enabled: true})
	g := newFakeGranter()
	p := &Plugin{store: m, granter: g}

	if err := p.GrantAchievement(context.Background(), 5, "founder"); err != nil {
		t.Fatalf("GrantAchievement: %v", err)
	}
	if _, times, done := m.ProgressOf(d.ID, 5); !done || times != 1 {
		t.Fatalf("times=%d done=%v, want 1/true", times, done)
	}
	if !m.Paid(d.ID, 5) {
		t.Error("paid_at was not stamped — the repair sweep would pay the reward this call avoids")
	}
	if g.grants() != 0 {
		t.Error("the achievement's reward was paid — that is the loop the contract forbids")
	}
	if rows, _ := m.UnpaidCompletions(context.Background(), 10); len(rows) != 0 {
		t.Errorf("a directly-granted achievement landed on the repair list: %+v", rows)
	}
}

// Granting a held achievement is a no-op, not an error — an idempotent payout
// handler depends on it.
func TestGrantAchievementIsIdempotent(t *testing.T) {
	c := &core.Core{}
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{Slug: "founder", Trigger: "x", Enabled: true})
	p := &Plugin{core: c, store: m}

	announced := 0
	c.On(EventCompleted, "test", func(ctx context.Context, e core.Event) { announced++ })

	for i := 0; i < 3; i++ {
		if err := p.GrantAchievement(context.Background(), 5, "founder"); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}
	if _, times, _ := m.ProgressOf(d.ID, 5); times != 1 {
		t.Errorf("times = %d, want 1", times)
	}
	// Announced once — the already-completed path is not a completion.
	if announced != 1 {
		t.Errorf("announced %d times, want 1", announced)
	}
}

// An unknown slug is an error, never a guess: a payout line naming a deleted
// achievement should fail its settle loudly rather than mint nothing in
// silence.
func TestGrantAchievementRefusesUnknownSlug(t *testing.T) {
	p := &Plugin{store: NewMemStore()}
	err := p.GrantAchievement(context.Background(), 5, "no-such-badge")
	if err == nil || !strings.Contains(err.Error(), "no-such-badge") {
		t.Errorf("unknown slug: %v, want an error naming it", err)
	}
}

// User 0 is the system, and the system does not hold badges.
func TestGrantAchievementRefusesTheSystemUser(t *testing.T) {
	p := &Plugin{store: NewMemStore()}
	if err := p.GrantAchievement(context.Background(), 0, "founder"); err == nil {
		t.Error("granted an achievement to user 0")
	}
}
