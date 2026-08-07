package rewards

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// metricStub answers only the read the subscriber makes.
type metricStub struct {
	AdminStore
	byMetric map[string][]AchievementDef
	asked    []string
}

func (m *metricStub) AchievementDefsByMetric(_ context.Context, metric string) ([]AchievementDef, error) {
	m.asked = append(m.asked, metric)
	return m.byMetric[metric], nil
}

// Only COUNTABLE events are subscribed to. The flag is where that judgement
// was already made; re-deciding it in the subscriber would put it in two
// places, and "member deleted their account" carries a UserID.
func TestOnlyCountableEventsAreSubscribed(t *testing.T) {
	c := &core.Core{}
	_ = c.DeclareEvent(core.EventDef{Name: "forum.post.created", Summary: "s",
		Emitter: "forum", Countable: true})
	_ = c.DeclareEvent(core.EventDef{Name: "account.deleted", Summary: "s",
		Emitter: "auth"})

	p := &Plugin{core: c}
	p.subscribeAchievements(c)

	if subs := c.EventSubscribers("forum.post.created"); len(subs) != 1 || subs[0] != "rewards" {
		t.Errorf("countable event subscribers = %v, want [rewards]", subs)
	}
	if subs := c.EventSubscribers("account.deleted"); len(subs) != 0 {
		t.Errorf("subscribed to a non-countable event: %v", subs)
	}
}

// The system did it, not a member. Crediting user 0 would build a phantom
// member holding every achievement on the site.
func TestSystemEventsAreIgnored(t *testing.T) {
	st := &metricStub{byMetric: map[string][]AchievementDef{
		"usenet.release.indexed": {{ID: 1, Slug: "a", Metric: "usenet.release.indexed"}},
	}}
	p := &Plugin{admin: st}

	p.onCountableEvent(context.Background(),
		core.Event{Name: "usenet.release.indexed", UserID: 0, Count: 1})

	if len(st.asked) != 0 {
		t.Errorf("a system event (UserID 0) reached the achievement lookup: %v", st.asked)
	}
}

// A zero or negative count must not reach the store either — an event whose
// emitter set Count explicitly to 0 means "nothing happened".
func TestNonPositiveCountsAreIgnored(t *testing.T) {
	st := &metricStub{}
	p := &Plugin{admin: st}
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: -3})
	if len(st.asked) != 0 {
		t.Errorf("a negative count reached the lookup: %v", st.asked)
	}
}

// The subscriber looks achievements up by the EVENT NAME. One vocabulary: an
// achievement's metric holds the event name, so there is no mapping table
// between "what happened" and "what is counted" for the two to drift about.
func TestLookupIsByEventName(t *testing.T) {
	st := &metricStub{byMetric: map[string][]AchievementDef{}}
	p := &Plugin{admin: st}
	p.onCountableEvent(context.Background(),
		core.Event{Name: "forum.post.created", UserID: 5, Count: 1})

	if len(st.asked) != 1 || st.asked[0] != "forum.post.created" {
		t.Errorf("looked up %v, want the event name itself", st.asked)
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
	p := &Plugin{admin: &metricStub{byMetric: map[string][]AchievementDef{}}}

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
