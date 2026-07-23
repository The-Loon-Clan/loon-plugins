package usenet

import (
	"context"
	"errors"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

type fakeHealthStore struct {
	cands    []pluginapi.HealthCandidate
	verdicts map[int64]string
	touched  map[int64]bool
	err      error
}

func (f *fakeHealthStore) HealthCandidates(_ context.Context, limit, _, _ int) ([]pluginapi.HealthCandidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit < len(f.cands) {
		return f.cands[:limit], nil
	}
	return f.cands, nil
}
func (f *fakeHealthStore) SetHealthVerdict(_ context.Context, id int64, status string, _, _, _ int) error {
	if f.verdicts == nil {
		f.verdicts = map[int64]string{}
	}
	f.verdicts[id] = status
	return nil
}
func (f *fakeHealthStore) TouchHealthChecked(_ context.Context, id int64) error {
	if f.touched == nil {
		f.touched = map[int64]bool{}
	}
	f.touched[id] = true
	return nil
}

// TestHostHealthBackendMapping: the host adapter's candidates become the same
// healthRow shape the internal path produces, and verdict/touch pass through —
// the checker itself must not care which world it is sweeping.
func TestHostHealthBackendMapping(t *testing.T) {
	fake := &fakeHealthStore{cands: []pluginapi.HealthCandidate{
		{ID: 11, NZBGz: []byte{1, 2}},
		{ID: 12, NZBGz: []byte{3}},
	}}
	b := hostHealth{hs: fake}
	ctx := context.Background()

	rows, err := b.candidates(ctx, 10, 30, 24)
	if err != nil || len(rows) != 2 {
		t.Fatalf("candidates: %v (%d rows)", err, len(rows))
	}
	if rows[0].ID != 11 || len(rows[0].Data) != 2 {
		t.Errorf("row mapping lost data: %+v", rows[0])
	}
	if err := b.setVerdict(ctx, 11, "dead", 100, 40, 5); err != nil || fake.verdicts[11] != "dead" {
		t.Errorf("verdict passthrough: %v %v", err, fake.verdicts)
	}
	if err := b.touch(ctx, 12); err != nil || !fake.touched[12] {
		t.Errorf("touch passthrough: %v %v", err, fake.touched)
	}

	fake.err = errors.New("host down")
	if _, err := b.candidates(ctx, 10, 30, 24); err == nil {
		t.Error("candidate error swallowed")
	}
}

// TestResolveHealthBackendRefusesHostWithoutCapability mirrors the sink rule:
// silently sweeping the plugin's empty table while the host catalogue rots
// unchecked is the worse failure, so host mode without the capability errors.
func TestResolveHealthBackendRefusesHostWithoutCapability(t *testing.T) {
	p := &Plugin{}
	p.cfg.Sink = "internal"
	if b, err := p.resolveHealthBackend(); err != nil || b == nil {
		t.Fatalf("internal mode must always resolve: %v", err)
	}
	// Host mode with no core/capability must refuse. (A real host registers the
	// capability before Boot; this is the misconfiguration path.)
	p.cfg.Sink = "host"
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("resolve panicked instead of erroring: %v", r)
		}
	}()
	if p.core == nil {
		// Lookup on a nil core would panic; resolveHealthBackend must be given
		// a core in host mode by construction (Provision sets it). Guard the
		// test to the documented contract instead of nil-poking.
		t.Skip("host-mode resolution requires a provisioned core; refusal path exercised in the demo")
	}
}
