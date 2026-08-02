package pluginapi

import (
	"context"
	"testing"
)

// The tier gate is the whole design, so it is tested as a gate rather than as
// a label. A propose action must never be appliable — an agent that could
// apply one would have the apply tier by a longer route.
func TestTiersGrantOnlyWhatTheyName(t *testing.T) {
	for _, c := range []struct {
		tier                 Tier
		appliable, draftable bool
	}{
		{TierInspect, false, false},
		{TierApply, true, false},
		{TierPropose, false, true},
		{Tier("anything-else"), false, false}, // unknown grants nothing
	} {
		if got := c.tier.Appliable(); got != c.appliable {
			t.Errorf("%s.Appliable() = %v, want %v", c.tier, got, c.appliable)
		}
		if got := c.tier.Draftable(); got != c.draftable {
			t.Errorf("%s.Draftable() = %v, want %v", c.tier, got, c.draftable)
		}
	}
}

// The listing is what an agent reads before deciding anything, and it is read
// top-down. Least authority first means everything it can do freely appears
// before anything it must ask a human about.
func TestActionsListLeastAuthorityFirst(t *testing.T) {
	ps := map[string]Proposer{"x" + ProposerSuffix: stubProposer{actions: []Action{
		{ID: "x.publish", Tier: TierPropose},
		{ID: "x.read", Tier: TierInspect},
		{ID: "x.tune", Tier: TierApply},
		{ID: "x.another-read", Tier: TierInspect},
	}}}
	got, err := AllActions(t.Context(), ps)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x.another-read", "x.read", "x.tune", "x.publish"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d = %s, want %s (order: %v)", i, got[i].ID, id, ids(got))
		}
	}
}

// A broken plugin must not blank the listing: an empty list looks healthy, and
// that is the worse failure.
func TestOnePluginFailingDoesNotHideTheRest(t *testing.T) {
	ps := map[string]Proposer{
		"good" + ProposerSuffix: stubProposer{actions: []Action{{ID: "good.a", Tier: TierInspect}}},
		"bad" + ProposerSuffix:  stubProposer{err: errBoom},
	}
	got, err := AllActions(t.Context(), ps)
	if err == nil {
		t.Error("the failure was swallowed — it must be reported alongside the partial list")
	}
	if len(got) != 1 || got[0].ID != "good.a" {
		t.Errorf("working plugin's actions lost: %v", ids(got))
	}
}

func TestProposerForRoutesOnThePluginPrefix(t *testing.T) {
	ps := map[string]Proposer{"news" + ProposerSuffix: stubProposer{}}
	if _, ok := ProposerFor(ps, "news.weekly-post"); !ok {
		t.Error("news.weekly-post did not route to the news proposer")
	}
	for _, bad := range []string{"", "news", "other.thing", ".leading"} {
		if _, ok := ProposerFor(ps, bad); ok {
			t.Errorf("%q routed somewhere", bad)
		}
	}
}

var errBoom = errBoomType{}

type errBoomType struct{}

func (errBoomType) Error() string { return "boom" }

type stubProposer struct {
	actions []Action
	err     error
}

func (s stubProposer) Actions(ctx context.Context) ([]Action, error) { return s.actions, s.err }
func (s stubProposer) Inspect(ctx context.Context, id string, in map[string]string) ([]Metric, error) {
	return nil, nil
}
func (s stubProposer) Draft(ctx context.Context, id string, in map[string]string) (Proposal, error) {
	return Proposal{}, nil
}

func ids(as []Action) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.ID
	}
	return out
}
