package rewards

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// achPlugin is a Plugin over the in-memory store with one achievement seeded on
// the given metric, plus a handle on the store to assert progress against.
//
// These tests used to inject a metricStub that answered a single read and
// recorded which key it was asked for, so they asserted "the lookup happened
// with this string". Two of them then asserted the lookup did NOT happen — and
// once the subscriber stopped using that seam those would have passed whether the
// guard existed or not. Asserting on PROGRESS instead means the guard has to
// actually hold: no progress recorded is the same answer however the code is
// arranged.
func achPlugin(t *testing.T, metric string, threshold int64) (*Plugin, *MemStore, AchievementDef) {
	t.Helper()
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{
		Slug: "a", Name: "A", Metric: metric, Threshold: threshold, Enabled: true,
	})
	return &Plugin{store: m}, m, d
}

// Only COUNTABLE events are subscribed to. The flag is where that judgement
// was already made; re-deciding it in the subscriber would put it in two
// places, and "member deleted their account" carries a UserID.
func TestOnlyCountableEventsAreSubscribed(t *testing.T) {
	c := &core.Core{}
	_ = c.DeclareEvent(core.EventDef{Name: "forum.post.created", Summary: "s",
		Emitter: "forum", Kind: core.EventMember, Countable: true})
	// A member event that is deliberately not countable — nobody should get a
	// badge for closing their account.
	_ = c.DeclareEvent(core.EventDef{Name: "account.deleted", Summary: "s",
		Emitter: "auth", Kind: core.EventMember})
	// And a SYSTEM event, which can never be countable and so can never be
	// subscribed to by the achievement path at all.
	_ = c.DeclareEvent(core.EventDef{Name: "usenet.indexed", Summary: "s",
		Emitter: "usenet", Kind: core.EventSystem})

	p := &Plugin{core: c}
	p.subscribeAchievements(c)

	if subs := c.EventSubscribers("forum.post.created"); len(subs) != 1 || subs[0] != "rewards" {
		t.Errorf("countable event subscribers = %v, want [rewards]", subs)
	}
	if subs := c.EventSubscribers("account.deleted"); len(subs) != 0 {
		t.Errorf("subscribed to a non-countable event: %v", subs)
	}
	if subs := c.EventSubscribers("usenet.indexed"); len(subs) != 0 {
		t.Errorf("subscribed to a system event: %v — there is no member to "+
			"count it against, which is why core refuses to let one be countable", subs)
	}
}

// The system did it, not a member. Crediting user 0 would build a phantom
// member holding every achievement on the site.
func TestSystemEventsAreIgnored(t *testing.T) {
	p, m, d := achPlugin(t, "usenet.release.indexed", 1)

	p.onCountableEvent(context.Background(),
		core.Event{Name: "usenet.release.indexed", UserID: 0, Count: 1})

	if v, _, _ := m.ProgressOf(d.ID, 0); v != 0 {
		t.Errorf("a system event (UserID 0) recorded progress %d against a phantom member", v)
	}
}

// A zero or negative count must not reach the store either — an event whose
// emitter set Count explicitly to 0 means "nothing happened".
func TestNonPositiveCountsAreIgnored(t *testing.T) {
	p, m, d := achPlugin(t, "forum.post.created", 1)
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: -3})
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 0 {
		t.Errorf("a negative count moved progress to %d", v)
	}
}

// The subscriber looks achievements up by the EVENT NAME. One vocabulary: an
// achievement's metric holds the event name, so there is no mapping table
// between "what happened" and "what is counted" for the two to drift about.
func TestLookupIsByEventName(t *testing.T) {
	p, m, d := achPlugin(t, "forum.post.created", 10)
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 3})
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 3 {
		t.Errorf("progress = %d, want 3 — the event name must select the achievement whose metric matches it", v)
	}

	// And an event whose name matches nothing moves nothing, which is what makes
	// the match above meaningful rather than incidental.
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.thread.created", UserID: 5, Count: 4})
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 3 {
		t.Errorf("progress = %d after an unrelated event, want 3", v)
	}
}

// A store failure must not propagate. The member's post already happened;
// failing here cannot un-happen it, and a handler that could fail the emitter
// would make the forum depend on this plugin's database being up.
func TestHandlerSwallowsStoreFailures(t *testing.T) {
	p := &Plugin{admin: failingAdmin{}}
	// The assertion is that this returns at all.
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1})
}

type failingAdmin struct{ AdminStore }

func (failingAdmin) AchievementDefsByMetric(context.Context, string) ([]AchievementDef, error) {
	return nil, context.DeadlineExceeded
}

// metricSrc is a counter with a fixed answer.
type metricSrc map[int64]int64

func (m metricSrc) Values(context.Context) (map[int64]int64, error) {
	return map[int64]int64(m), nil
}

// A metric source is a query over the WHOLE membership. Running one on every
// tick for an achievement nobody created is pure cost, so the defs are read
// first and the counter only if something is scored on it.
func TestScoreMetricDoesNotReadTheCounterWhenNothingScoresIt(t *testing.T) {
	read := false
	src := readTrackingSrc{&read}
	// An empty store: nothing is scored on any metric.
	p := &Plugin{store: NewMemStore()}

	n, err := p.scoreMetric(context.Background(), "tenure.years", src)
	if err != nil || n != 0 {
		t.Fatalf("scoreMetric = %d, %v", n, err)
	}
	if read {
		t.Error("the counter was read for a metric no achievement uses — that is a " +
			"membership-wide query per tick to learn nothing")
	}
}

type readTrackingSrc struct{ read *bool }

func (r readTrackingSrc) Values(context.Context) (map[int64]int64, error) {
	*r.read = true
	return nil, nil
}

// ── The completion path, now reachable without a database ──────────────────
//
// Everything below was previously testable only against Postgres, because the
// completion methods lived on *PGStore and the path type-asserted for it. None of
// it is about SQL: it is which achievements move, when one completes, and what
// happens when the reward behind it cannot pay. The transaction itself stays in
// achievements_pg_test.go, where a real UNIQUE arbitrates a real race.

// payingPlugin wires a plugin whose achievement pays a working reward.
//
// Whether the member was ANNOUNCED to is read off the grant's Silent flag rather
// than a recording notifier: Silent is what the backfill decision actually sets,
// so asserting on it tests the thing that ships instead of a test-only spy.
func payingPlugin(t *testing.T, metric string, threshold int64) (*Plugin, *MemStore, AchievementDef) {
	t.Helper()
	m := NewMemStore()
	r := Reward{
		ID: 900, Slug: "badge", Name: "Badge", Kind: KindOneOff,
		Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: "points", Amount: 50}},
	}
	m.Rewards = append(m.Rewards, r)
	d := m.SeedAchievement(AchievementDef{
		Slug: "centurion", Name: "Centurion", Metric: metric, Threshold: threshold,
		RewardID: r.ID, Enabled: true,
	})
	e := NewEngine(m, nil)
	e.Handle("points", func(context.Context, Grant, Payout) error { return nil })
	p := &Plugin{store: m, engine: e}
	return p, m, d
}

// Progress accumulates and the achievement completes only on crossing.
func TestAchievementCompletesOnlyWhenTheThresholdIsCrossed(t *testing.T) {
	p, m, d := payingPlugin(t, "forum.post.created", 3)
	ev := func(n int64) {
		p.onCountableEvent(context.Background(),
			core.Event{Name: "forum.post.created", UserID: 5, Count: n})
	}

	ev(1)
	if _, _, done := m.ProgressOf(d.ID, 5); done {
		t.Fatal("completed at 1 of 3")
	}
	ev(1)
	if _, _, done := m.ProgressOf(d.ID, 5); done {
		t.Fatal("completed at 2 of 3")
	}
	ev(1)
	v, times, done := m.ProgressOf(d.ID, 5)
	if !done || v != 3 || times != 1 {
		t.Fatalf("progress=%d times=%d done=%v, want 3/1/true", v, times, done)
	}
	if len(m.Grants()) != 1 {
		t.Fatalf("got %d grants, want 1", len(m.Grants()))
	}
}

// Further events after completion must not pay again. One-off means once, and
// the reference is empty precisely so the UNIQUE says so.
func TestAchievementDoesNotPayTwice(t *testing.T) {
	p, m, d := payingPlugin(t, "forum.post.created", 1)
	for i := 0; i < 5; i++ {
		p.onCountableEvent(context.Background(),
			core.Event{Name: "forum.post.created", UserID: 5, Count: 1})
	}
	if _, times, _ := m.ProgressOf(d.ID, 5); times != 1 {
		t.Errorf("times=%d, want 1 — a one-off achievement completed repeatedly", times)
	}
	if len(m.Grants()) != 1 {
		t.Errorf("got %d grants, want 1", len(m.Grants()))
	}
}

// The first scoring pass is SILENT: everyone who already qualified earned it
// before it existed, and announcing that messages the membership about history.
func TestFirstPassIsSilentAndLaterOnesAreNot(t *testing.T) {
	p, m, d := payingPlugin(t, "forum.post.created", 1)

	// backfilled_at is nil, so this completion is part of the first pass.
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1})
	gs := m.Grants()
	if len(gs) != 1 || !gs[0].Silent {
		t.Fatalf("first-pass grant Silent=%v, want true", len(gs) == 1 && gs[0].Silent)
	}

	// Once the pass is marked done, a later member is announced normally.
	if err := m.MarkBackfilled(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 6, Count: 1})
	gs = m.Grants()
	if len(gs) != 2 {
		t.Fatalf("got %d grants, want 2", len(gs))
	}
	if gs[1].Silent {
		t.Error("a member earning it AFTER the backfill was silenced too")
	}
}

// A reward that cannot pay must not consume the member's one-off entitlement.
// A completion is irreversible: completed_at stamps once, so an achievement that
// completed against a broken reward can never be re-earned once it is fixed.
func TestBrokenRewardDoesNotConsumeTheEntitlement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Reward)
	}{
		{"disabled", func(r *Reward) { r.Enabled = false }},
		{"no payout lines", func(r *Reward) { r.Payouts = nil }},
		{"wrong kind", func(r *Reward) { r.Kind = KindRecurring }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, m, d := payingPlugin(t, "forum.post.created", 1)
			tc.mutate(&m.Rewards[0])

			p.onCountableEvent(context.Background(),
				core.Event{Name: "forum.post.created", UserID: 5, Count: 1})

			if _, _, done := m.ProgressOf(d.ID, 5); done {
				t.Error("the achievement completed against a reward that cannot pay; " +
					"the member now holds it, was paid nothing, and can never re-earn it")
			}
			if len(m.Grants()) != 0 {
				t.Errorf("got %d grants from an unpayable reward", len(m.Grants()))
			}
		})
	}
}

// One broken achievement must not abandon the others in the same event.
func TestABrokenAchievementDoesNotBlockItsSiblings(t *testing.T) {
	m := NewMemStore()
	good := Reward{ID: 901, Slug: "good", Kind: KindOneOff, Delivery: DeliveryAuto,
		Enabled: true, Payouts: []Payout{{Kind: "points", Amount: 5}}}
	broken := Reward{ID: 902, Slug: "broken", Kind: KindOneOff, Delivery: DeliveryAuto,
		Enabled: true} // no payout lines
	m.Rewards = append(m.Rewards, broken, good)
	// Ordinal puts the broken one FIRST, so a path that aborted on error would
	// never reach the healthy one.
	bad := m.SeedAchievement(AchievementDef{Slug: "bad", Metric: "m", Threshold: 1,
		RewardID: broken.ID, Enabled: true, Ordinal: 1})
	ok := m.SeedAchievement(AchievementDef{Slug: "ok", Metric: "m", Threshold: 1,
		RewardID: good.ID, Enabled: true, Ordinal: 2})

	e := NewEngine(m, nil)
	e.Handle("points", func(context.Context, Grant, Payout) error { return nil })
	p := &Plugin{store: m, engine: e}
	p.onCountableEvent(context.Background(), core.Event{Name: "m", UserID: 5, Count: 1})

	if _, _, done := m.ProgressOf(bad.ID, 5); done {
		t.Error("the broken achievement completed")
	}
	if _, _, done := m.ProgressOf(ok.ID, 5); !done {
		t.Error("the healthy achievement was skipped because a sibling failed first")
	}
}

// A disabled achievement is not evaluated at all.
func TestDisabledAchievementsAreNotEvaluated(t *testing.T) {
	p, m, d := payingPlugin(t, "forum.post.created", 1)
	m.SeedAchievement(AchievementDef{ID: d.ID, Slug: d.Slug, Metric: d.Metric,
		Threshold: d.Threshold, RewardID: d.RewardID, Enabled: false})

	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 9})

	if v, _, _ := m.ProgressOf(d.ID, 5); v != 0 {
		t.Errorf("progress=%d on a disabled achievement", v)
	}
}

// The metric path SETS an absolute total; the event path ADDS. Using either where
// the other belongs multiplies progress by the tick count or flatlines it.
func TestMetricPathSetsWhileEventPathAdds(t *testing.T) {
	p, m, d := payingPlugin(t, "tenure.years", 5)
	ctx := context.Background()

	// Three ticks of the same absolute value must leave progress at 3, not 9.
	for i := 0; i < 3; i++ {
		if _, err := p.scoreMetric(ctx, "tenure.years", metricSrc{5: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if v, _, done := m.ProgressOf(d.ID, 5); v != 3 || done {
		t.Fatalf("progress=%d done=%v after three identical ticks, want 3/false", v, done)
	}

	// And the counter reaching the threshold completes it.
	if _, err := p.scoreMetric(ctx, "tenure.years", metricSrc{5: 5}); err != nil {
		t.Fatal(err)
	}
	if v, _, done := m.ProgressOf(d.ID, 5); v != 5 || !done {
		t.Errorf("progress=%d done=%v, want 5/true", v, done)
	}
}
