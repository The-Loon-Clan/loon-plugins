package achievements

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// fakeGranter stands in for the rewards plugin's RewardBySlugGranter, and is
// held to that contract's one load-bearing property: idempotence on
// (user, slug, reference). A fake looser than the real granter would let the
// repair-sweep tests pass on double payments.
type fakeGranter struct {
	mu      sync.Mutex
	fail    bool
	calls   int
	granted map[string]bool
}

func newFakeGranter() *fakeGranter { return &fakeGranter{granted: map[string]bool{}} }

// ListOneOff satisfies the widened contract. The tests that use this fake
// exercise GRANTING; none build the dropdown, so an empty shelf is the honest
// stub rather than inventing rewards the fake cannot pay.
func (g *fakeGranter) ListOneOff(ctx context.Context) ([]string, error) { return nil, nil }

func (g *fakeGranter) GrantOneOff(ctx context.Context, userID int64, slug, reference string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.fail {
		return false, errors.New("granter down")
	}
	key := fmt.Sprintf("%d/%s/%s", userID, slug, reference)
	if g.granted[key] {
		return false, nil // already held — the idempotent answer, not an error
	}
	g.granted[key] = true
	return true, nil
}

func (g *fakeGranter) grants() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.granted)
}

// achPlugin is a Plugin over the in-memory store with one metric achievement
// seeded, plus a handle on the store to assert progress against.
//
// The rewards ancestors of these tests once injected a metric stub and
// asserted "the lookup happened"; asserting on PROGRESS instead means the
// guards have to actually hold — no progress recorded is the same answer
// however the code is arranged.
func achPlugin(t *testing.T, metric string, threshold int64) (*Plugin, *MemStore, AchievementDef) {
	t.Helper()
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{
		Slug: "a", Name: "A", Metric: metric, Threshold: threshold, Enabled: true,
	})
	return &Plugin{store: m}, m, d
}

// payingPlugin wires a plugin whose achievement pays through a fake granter.
func payingPlugin(t *testing.T, metric string, threshold int64) (*Plugin, *MemStore, AchievementDef, *fakeGranter) {
	t.Helper()
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{
		Slug: "centurion", Name: "Centurion", Metric: metric, Threshold: threshold,
		RewardSlug: "badge-reward", Enabled: true,
	})
	g := newFakeGranter()
	return &Plugin{store: m, granter: g}, m, d, g
}

// ALL declared events are subscribed to now — the trigger half completes on
// any of them — where the old rewards subscriber took countable ones only.
// The countable judgement still gates the METRIC half, inside the handler,
// which the next test pins.
func TestEveryDeclaredEventIsSubscribed(t *testing.T) {
	c := &core.Core{}
	_ = c.DeclareEvent(core.EventDef{Name: "forum.post.created", Summary: "s",
		Emitter: "forum", Kind: core.EventMember, Countable: true})
	_ = c.DeclareEvent(core.EventDef{Name: "account.deleted", Summary: "s",
		Emitter: "auth", Kind: core.EventMember})
	_ = c.DeclareEvent(core.EventDef{Name: "usenet.indexed", Summary: "s",
		Emitter: "usenet", Kind: core.EventSystem})

	p := &Plugin{core: c}
	p.subscribe(c)

	for _, name := range []string{"forum.post.created", "account.deleted", "usenet.indexed"} {
		if subs := c.EventSubscribers(name); len(subs) != 1 || subs[0] != "achievements" {
			t.Errorf("%s subscribers = %v, want [achievements] — a trigger can name any declared event", name, subs)
		}
	}
}

// A non-countable event must not move progress even when a metric happens to
// share its name: Countable is where "worth totalling per member" was
// decided, and the metric half honours it. The trigger half is unaffected —
// that is the point of subscribing to everything.
func TestNonCountableEventsMoveNoProgress(t *testing.T) {
	p, m, d := achPlugin(t, "account.deleted", 1)
	p.onEvent(context.Background(),
		core.Event{Name: "account.deleted", UserID: 5, Count: 1}, false /* not countable */)
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 0 {
		t.Errorf("a non-countable event moved progress to %d — nobody should inch toward "+
			"a badge for closing their account", v)
	}
}

// The system did it, not a member. Crediting user 0 would build a phantom
// member holding every achievement on the site.
func TestSystemEventsAreIgnored(t *testing.T) {
	p, m, d := achPlugin(t, "usenet.release.indexed", 1)
	p.onEvent(context.Background(),
		core.Event{Name: "usenet.release.indexed", UserID: 0, Count: 1}, true)
	if v, _, _ := m.ProgressOf(d.ID, 0); v != 0 {
		t.Errorf("a system event (UserID 0) recorded progress %d against a phantom member", v)
	}
}

// A zero or negative count must not reach the store either — an emitter that
// says a negative count means a retraction, which neither counts nor fires a
// trigger.
func TestNonPositiveCountsAreIgnored(t *testing.T) {
	p, m, d := achPlugin(t, "forum.post.created", 1)
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: -3}, true)
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 0 {
		t.Errorf("a negative count moved progress to %d", v)
	}

	trig := m.SeedAchievement(AchievementDef{Slug: "t", Trigger: "forum.post.created", Enabled: true})
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: -3}, true)
	if _, _, done := m.ProgressOf(trig.ID, 5); done {
		t.Error("a retraction fired a trigger completion")
	}
}

// The subscriber looks achievements up by the EVENT NAME. One vocabulary: an
// achievement's metric holds the event name, so there is no mapping table
// between "what happened" and "what is counted" for the two to drift about.
func TestLookupIsByEventName(t *testing.T) {
	p, m, d := achPlugin(t, "forum.post.created", 10)
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 3}, true)
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 3 {
		t.Errorf("progress = %d, want 3 — the event name must select the achievement whose metric matches it", v)
	}

	// And an event whose name matches nothing moves nothing, which is what
	// makes the match above meaningful rather than incidental.
	p.onEvent(context.Background(),
		core.Event{Name: "forum.thread.created", UserID: 5, Count: 4}, true)
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 3 {
		t.Errorf("progress = %d after an unrelated event, want 3", v)
	}
}

// A store failure must not propagate. The member's post already happened;
// failing here cannot un-happen it, and a handler that could fail the emitter
// would make the forum depend on this plugin's database being up.
func TestHandlerSwallowsStoreFailures(t *testing.T) {
	p := &Plugin{store: failingStore{}}
	// The assertion is that this returns at all.
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1}, true)
}

type failingStore struct{ Store }

func (failingStore) AchievementDefsByMetric(context.Context, string) ([]AchievementDef, error) {
	return nil, context.DeadlineExceeded
}
func (failingStore) AchievementDefsByTrigger(context.Context, string) ([]AchievementDef, error) {
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

// ── The completion path ─────────────────────────────────────────────────────

// Progress accumulates and the achievement completes only on crossing.
func TestAchievementCompletesOnlyWhenTheThresholdIsCrossed(t *testing.T) {
	p, m, d, g := payingPlugin(t, "forum.post.created", 3)
	ev := func(n int64) {
		p.onEvent(context.Background(),
			core.Event{Name: "forum.post.created", UserID: 5, Count: n}, true)
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
	if g.grants() != 1 {
		t.Fatalf("got %d grants, want 1", g.grants())
	}
	if !m.Paid(d.ID, 5) {
		t.Error("paid_at was not stamped after a successful grant")
	}
}

// Further events after completion must not pay again. Completion latches
// once, and the granter's reference dedup backstops it.
func TestAchievementDoesNotPayTwice(t *testing.T) {
	p, m, d, g := payingPlugin(t, "forum.post.created", 1)
	for i := 0; i < 5; i++ {
		p.onEvent(context.Background(),
			core.Event{Name: "forum.post.created", UserID: 5, Count: 1}, true)
	}
	if _, times, _ := m.ProgressOf(d.ID, 5); times != 1 {
		t.Errorf("times=%d, want 1 — completion fired repeatedly", times)
	}
	if g.grants() != 1 {
		t.Errorf("got %d grants, want 1", g.grants())
	}
}

// A pure badge (no reward_slug) stamps paid_at at completion: nothing is
// owed, so nothing may sit pending or land on the repair sweep's list.
func TestPureBadgeStampsPaidAtOnCompletion(t *testing.T) {
	p, m, d := achPlugin(t, "forum.post.created", 1)
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1}, true)

	if _, _, done := m.ProgressOf(d.ID, 5); !done {
		t.Fatal("the badge did not complete")
	}
	if !m.Paid(d.ID, 5) {
		t.Error("a pure badge's paid_at was not stamped at completion — it would show " +
			"as awaiting a payout that will never come")
	}
	if rows, _ := m.UnpaidCompletions(context.Background(), 10); len(rows) != 0 {
		t.Errorf("a pure badge landed on the repair sweep's list: %+v", rows)
	}
}

// A trigger-based achievement completes the moment its event fires — once,
// and only once, however often the event repeats.
func TestTriggerAchievementCompletesOnceAndOnlyOnce(t *testing.T) {
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{
		Slug: "first-login", Name: "First login", Trigger: "auth.login", Enabled: true,
	})
	p := &Plugin{store: m}

	for i := 0; i < 3; i++ {
		p.onEvent(context.Background(),
			core.Event{Name: "auth.login", UserID: 5, Count: 1}, true)
	}
	v, times, done := m.ProgressOf(d.ID, 5)
	if !done || times != 1 {
		t.Fatalf("times=%d done=%v, want 1/true — a trigger latches once", times, done)
	}
	if v != 0 {
		t.Errorf("progress = %d on a trigger achievement, want 0 — there is no counter", v)
	}
	// And an unrelated member is untouched.
	if _, _, done := m.ProgressOf(d.ID, 6); done {
		t.Error("a different member was completed by someone else's event")
	}
}

// The crash-window repair: a completion whose granter failed leaves paid_at
// NULL, and the sweep pays it when the granter recovers — exactly once,
// because the granter is idempotent.
func TestFailingGranterLeavesUnpaidAndSweepRepairs(t *testing.T) {
	p, m, d, g := payingPlugin(t, "forum.post.created", 1)
	g.fail = true

	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1}, true)

	// The badge is the member's — a broken payer must not block the earning —
	// but the payment is owed.
	if _, _, done := m.ProgressOf(d.ID, 5); !done {
		t.Fatal("the completion was blocked by a failing granter")
	}
	if m.Paid(d.ID, 5) {
		t.Fatal("paid_at was stamped although the grant failed")
	}
	rows, err := m.UnpaidCompletions(context.Background(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("UnpaidCompletions = %+v, %v — the sweep cannot see the debt", rows, err)
	}

	// The sweep runs while the granter is still down: nothing changes, and
	// nothing errors the whole pass.
	if n, err := p.repayUnpaid(context.Background()); err != nil || n != 0 {
		t.Fatalf("sweep with granter down: repaired=%d err=%v", n, err)
	}

	// The granter recovers; the sweep pays exactly once and stamps.
	g.fail = false
	n, err := p.repayUnpaid(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sweep after recovery: repaired=%d err=%v, want 1/nil", n, err)
	}
	if !m.Paid(d.ID, 5) {
		t.Error("the sweep paid but did not stamp paid_at")
	}
	if g.grants() != 1 {
		t.Errorf("%d grants after repair, want 1", g.grants())
	}
	// And a second sweep finds nothing left to do.
	if n, _ := p.repayUnpaid(context.Background()); n != 0 {
		t.Errorf("a second sweep repaid %d rows on a settled table", n)
	}
}

// The first scoring pass is SILENT: everyone who already qualified earned it
// before it existed, and announcing that messages the membership about
// history. Silence now means "no achievements.completed event" — the
// announcement IS the event.
func TestFirstPassIsSilentAndLaterOnesAreNot(t *testing.T) {
	c := &core.Core{}
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{
		Slug: "centurion", Metric: "forum.post.created", Threshold: 1, Enabled: true,
	})
	p := &Plugin{core: c, store: m}

	var announced []Completed
	c.On(EventCompleted, "test", func(ctx context.Context, e core.Event) {
		announced = append(announced, e.Data.(Completed))
	})

	// backfilled_at is nil, so this completion is part of the first pass.
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1}, true)
	if _, _, done := m.ProgressOf(d.ID, 5); !done {
		t.Fatal("the backfill completion did not happen")
	}
	if len(announced) != 0 {
		t.Fatalf("a backfill completion was announced: %+v", announced)
	}

	// Once the pass is marked done, a later member is announced normally.
	if err := m.MarkBackfilled(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 6, Count: 1}, true)
	if len(announced) != 1 {
		t.Fatalf("got %d announcements, want 1", len(announced))
	}
	if announced[0].Slug != "centurion" || !announced[0].Paid {
		t.Errorf("payload = %+v, want {centurion true} — a pure badge announces as paid", announced[0])
	}
}

// A trigger completion always announces: no scoring pass can retroactively
// fire an event, so there is no "history" cohort to silence, and reading the
// never-stamped backfilled_at as "silence me" would mute every trigger
// achievement forever.
func TestTriggerCompletionsAnnounceDespiteNilBackfill(t *testing.T) {
	c := &core.Core{}
	m := NewMemStore()
	m.SeedAchievement(AchievementDef{Slug: "first-login", Trigger: "auth.login", Enabled: true})
	p := &Plugin{core: c, store: m}

	announced := 0
	c.On(EventCompleted, "test", func(ctx context.Context, e core.Event) { announced++ })

	p.onEvent(context.Background(), core.Event{Name: "auth.login", UserID: 5, Count: 1}, false)
	if announced != 1 {
		t.Fatalf("got %d announcements, want 1", announced)
	}
	// The already-completed path announces nothing — the completion did not
	// happen this time.
	p.onEvent(context.Background(), core.Event{Name: "auth.login", UserID: 5, Count: 1}, false)
	if announced != 1 {
		t.Errorf("a repeat firing announced again (%d total)", announced)
	}
}

// One broken achievement must not abandon the others in the same event.
func TestABrokenAchievementDoesNotBlockItsSiblings(t *testing.T) {
	m := NewMemStore()
	// Ordinal puts the broken one FIRST, so a path that aborted on error
	// would never reach the healthy one. "Broken" now means the store
	// refuses its completion — simulated with a def the mock does not know.
	bad := m.SeedAchievement(AchievementDef{Slug: "bad", Metric: "m", Threshold: 1, Enabled: true, Ordinal: 1})
	ok := m.SeedAchievement(AchievementDef{Slug: "ok", Metric: "m", Threshold: 1, Enabled: true, Ordinal: 2})
	p := &Plugin{store: brokenCompleter{Store: m, brokenID: bad.ID}}

	p.onEvent(context.Background(), core.Event{Name: "m", UserID: 5, Count: 1}, true)

	if _, _, done := m.ProgressOf(bad.ID, 5); done {
		t.Error("the broken achievement completed")
	}
	if _, _, done := m.ProgressOf(ok.ID, 5); !done {
		t.Error("the healthy achievement was skipped because a sibling failed first")
	}
}

// brokenCompleter refuses completion for one achievement id.
type brokenCompleter struct {
	Store
	brokenID int64
}

func (b brokenCompleter) CompleteAchievement(ctx context.Context, achievementID, userID int64, paid bool) error {
	if achievementID == b.brokenID {
		return errors.New("simulated store failure")
	}
	return b.Store.CompleteAchievement(ctx, achievementID, userID, paid)
}

// A disabled achievement is not evaluated at all.
func TestDisabledAchievementsAreNotEvaluated(t *testing.T) {
	p, m, d := achPlugin(t, "forum.post.created", 1)
	m.SeedAchievement(AchievementDef{ID: d.ID, Slug: d.Slug, Metric: d.Metric,
		Threshold: d.Threshold, Enabled: false})

	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 9}, true)

	if v, _, _ := m.ProgressOf(d.ID, 5); v != 0 {
		t.Errorf("progress=%d on a disabled achievement", v)
	}
}

// The metric path SETS an absolute total; the event path ADDS. Using either
// where the other belongs multiplies progress by the tick count or flatlines
// it.
func TestMetricPathSetsWhileEventPathAdds(t *testing.T) {
	p, m, d, _ := payingPlugin(t, "tenure.years", 5)
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

// A member ABSENT from the counter's map is left alone — absence is "no
// data", never zero. A half-returned counter must not stall or reset anyone.
func TestScoreMetricLeavesAbsentMembersAlone(t *testing.T) {
	p, m, d, _ := payingPlugin(t, "uploads", 100)
	ctx := context.Background()

	if _, err := p.scoreMetric(ctx, "uploads", metricSrc{5: 40, 6: 90}); err != nil {
		t.Fatal(err)
	}
	// The next read only names member 6.
	if _, err := p.scoreMetric(ctx, "uploads", metricSrc{6: 95}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 40 {
		t.Errorf("member absent from the read had progress moved to %d, want the untouched 40", v)
	}
	if v, _, _ := m.ProgressOf(d.ID, 6); v != 95 {
		t.Errorf("named member's progress = %d, want 95", v)
	}
}

// The repair sweep bites: prove the failing-granter test is testing the
// mechanism by checking a healthy pipeline never enters the sweep.
func TestHealthySweepIsANoOp(t *testing.T) {
	p, _, _, g := payingPlugin(t, "forum.post.created", 1)
	p.onEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1}, true)
	callsBefore := g.calls
	if n, err := p.repayUnpaid(context.Background()); err != nil || n != 0 {
		t.Fatalf("sweep on a healthy table: repaired=%d err=%v", n, err)
	}
	if g.calls != callsBefore {
		t.Error("the sweep called the granter with nothing owed")
	}
}
