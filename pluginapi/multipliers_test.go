package pluginapi

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

type fakeSource struct {
	factors map[string]float64
	err     error
}

func (f fakeSource) Factor(_ context.Context, dim string, _ MultiplierContext) (float64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	v, ok := f.factors[dim]
	return v, ok, nil
}

// The combining rules are the whole point of the file; these hold them
// still per dimension, across sources, with the hostile inputs guarded.
func TestResolveMultiplierCombiningRules(t *testing.T) {
	c := &core.Core{}
	must := func(name string, s MultiplierSource) {
		if err := c.Register(MultiplierSourcePrefix+name, s); err != nil {
			t.Fatal(err)
		}
	}
	must("magic", fakeSource{factors: map[string]float64{MultUpload: 2, MultDownload: 0}})
	must("event", fakeSource{factors: map[string]float64{MultUpload: 1.5, MultDownload: 0.5, MultPoints: 1.10}})
	must("medals", fakeSource{factors: map[string]float64{MultPoints: 1.05}})
	must("broken", fakeSource{err: errors.New("db down")})
	must("hostile", fakeSource{factors: map[string]float64{MultUpload: -3, MultPoints: -1}})

	mc := MultiplierContext{UserID: 1}
	if got := ResolveMultiplier(context.Background(), c, MultUpload, mc); got != 2 {
		t.Errorf("upload = %v, want the MAX 2 (best promotion wins)", got)
	}
	if got := ResolveMultiplier(context.Background(), c, MultDownload, mc); got != 0 {
		t.Errorf("download = %v, want the MIN 0 (freeleech wins)", got)
	}
	if got := ResolveMultiplier(context.Background(), c, MultPoints, mc); math.Abs(got-1.15) > 1e-9 {
		t.Errorf("points = %v, want 1.15 (bonuses SUM: +10%% and +5%%)", got)
	}
}

// No sources, nil core, unknown dimensions: all neutral, never a panic —
// a consumer must be able to call this unconditionally.
func TestResolveMultiplierNeutralDefaults(t *testing.T) {
	if got := ResolveMultiplier(context.Background(), nil, MultPoints, MultiplierContext{}); got != 1 {
		t.Errorf("nil core = %v, want 1", got)
	}
	c := &core.Core{}
	for _, dim := range []string{MultUpload, MultDownload, MultPoints, "someday"} {
		if got := ResolveMultiplier(context.Background(), c, dim, MultiplierContext{}); got != 1 {
			t.Errorf("%s with no sources = %v, want 1", dim, got)
		}
	}
}
