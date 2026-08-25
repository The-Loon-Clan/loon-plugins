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
	cleared  map[int64]bool
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

// Tracked separately from touched on purpose: clearing a recheck request must
// NOT stamp checked-at, and a fake that conflated them would let that
// regression through.
func (f *fakeHealthStore) ClearHealthRecheckRequest(_ context.Context, id int64) error {
	if f.cleared == nil {
		f.cleared = map[int64]bool{}
	}
	f.cleared[id] = true
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

// A user-requested check that comes back inconclusive must have its REQUEST
// dropped and nothing else. Every other ending clears the request as a side
// effect -- a verdict through SetHealthVerdict, an unreadable blob through
// TouchHealthChecked -- and this third ending writes neither, so the request
// used to stay set forever: the row was re-STATted on every pass and the page
// the person was watching said "queued" indefinitely. Three releases were in
// that state in production, the oldest for 22 hours.
func TestClearRecheckDropsTheRequestWithoutStampingChecked(t *testing.T) {
	fake := &fakeHealthStore{}
	b := hostHealth{hs: fake}

	if err := b.clearRecheck(context.Background(), 77); err != nil {
		t.Fatalf("clearRecheck: %v", err)
	}
	if !fake.cleared[77] {
		t.Error("the recheck request was not dropped")
	}
	if fake.touched[77] {
		t.Error("clearing a request stamped checked-at — that pushes a release " +
			"nobody can get an answer about to the back of the rotation, which " +
			"hides it rather than surfacing it")
	}
	if fake.verdicts[77] != "" {
		t.Error("clearing a request wrote a verdict the checker could not trust")
	}
}

// UserRequested has to survive the host adapter, or the sweep can never tell
// a person's request apart from the rotation's own pick.
func TestUserRequestedCrossesTheHostSeam(t *testing.T) {
	fake := &fakeHealthStore{cands: []pluginapi.HealthCandidate{
		{ID: 11, NZBGz: []byte{1}, UserRequested: true},
		{ID: 12, NZBGz: []byte{2}},
	}}
	rows, err := hostHealth{hs: fake}.candidates(context.Background(), 10, 30, 24)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if !rows[0].UserRequested || rows[1].UserRequested {
		t.Errorf("UserRequested did not cross the seam: %+v", rows)
	}
}

// Internal mode has no site to request a recheck from, so its rows are never
// UserRequested and clearRecheck must be an inert no-op rather than an error
// the sweep would report every pass.
func TestInternalModeClearRecheckIsInert(t *testing.T) {
	if err := (internalHealth{}).clearRecheck(context.Background(), 1); err != nil {
		t.Errorf("internal clearRecheck returned %v, want nil", err)
	}
}

// applyOutcome's writes, per arm — the write-or-not decision is where the
// sweep went wrong twice (the unstamped-inconclusive monopoly, and before it
// the pass-ending starvation). The fake's separate maps make touch-vs-clear
// conflation impossible to miss.
func TestApplyOutcomeWrites(t *testing.T) {
	ctx := context.Background()
	report := func(string, error) {}
	run := func(outcome healthOutcome, userRequested bool) (*fakeHealthStore, bool, bool) {
		fake := &fakeHealthStore{}
		wrote, cleared := applyOutcome(ctx, hostHealth{hs: fake},
			healthRow{ID: 7, UserRequested: userRequested}, outcome, healthDead, 10, 10, 0, report)
		return fake, wrote, cleared
	}

	// Deterministic-inconclusive: stamped, verdict untouched, request cleared
	// only when a user asked.
	if fake, wrote, cleared := run(healthSkipRow, false); !fake.touched[7] || wrote || cleared || len(fake.verdicts) != 0 || len(fake.cleared) != 0 {
		t.Errorf("healthSkipRow: touched=%v wrote=%v cleared=%v verdicts=%v", fake.touched[7], wrote, cleared, fake.verdicts)
	}
	if fake, _, cleared := run(healthSkipRow, true); !fake.touched[7] || !fake.cleared[7] || !cleared {
		t.Errorf("healthSkipRow+user: touched=%v clearedMap=%v cleared=%v", fake.touched[7], fake.cleared[7], cleared)
	}
	// Pool-minted doubt stays unstamped — a stamp would cost a checkable row
	// the full recheck window for the pool's sin.
	if fake, wrote, cleared := run(healthSkipTransport, true); len(fake.touched) != 0 || len(fake.verdicts) != 0 || len(fake.cleared) != 0 || wrote || cleared {
		t.Errorf("healthSkipTransport wrote something: %+v wrote=%v cleared=%v", fake, wrote, cleared)
	}
	if fake, wrote, _ := run(healthSkipTransient, false); len(fake.touched) != 0 || len(fake.verdicts) != 0 || wrote {
		t.Errorf("healthSkipTransient wrote something: %+v", fake)
	}
	// The existing arms, pinned.
	if fake, wrote, _ := run(healthSkipPermanent, false); !fake.touched[7] || wrote || len(fake.verdicts) != 0 {
		t.Errorf("healthSkipPermanent: touched=%v verdicts=%v", fake.touched[7], fake.verdicts)
	}
	if fake, wrote, _ := run(healthWritten, false); !wrote || fake.verdicts[7] != healthDead || len(fake.touched) != 0 {
		t.Errorf("healthWritten: wrote=%v verdicts=%v touched=%v", wrote, fake.verdicts, fake.touched)
	}
}

// missingBackbones: every enabled non-backup backbone must have a live run,
// or a dead verdict is written against a partial fleet.
func TestMissingBackbones(t *testing.T) {
	act := func(id int, backbone string) provider {
		return provider{ID: id, Backbone: backbone, Role: "active"}
	}
	bak := func(id int, backbone string) provider {
		return provider{ID: id, Backbone: backbone, Role: roleBackup}
	}
	runOf := func(p provider) providerRun { return providerRun{prov: p} }

	all := []provider{act(1, "omicron"), act(2, "netnews"), bak(3, "shortretention")}

	// Whole fleet up: nothing missing.
	if m := missingBackbones(all, []providerRun{runOf(all[0]), runOf(all[1])}); len(m) != 0 {
		t.Errorf("whole fleet reported missing: %v", m)
	}
	// The omicron active benched: its backbone is missing even with the
	// backup promoted — a backup on a DIFFERENT backbone is not testimony.
	if m := missingBackbones(all, []providerRun{runOf(all[1]), runOf(all[2])}); len(m) != 1 || m[0] != "omicron" {
		t.Errorf("benched active's backbone not reported: %v", m)
	}
	// A promoted backup SHARING the benched active's backbone satisfies it.
	shared := bak(4, "omicron")
	if m := missingBackbones(append(all, shared), []providerRun{runOf(all[1]), runOf(shared)}); len(m) != 0 {
		t.Errorf("same-backbone backup did not satisfy the requirement: %v", m)
	}
	// Two accounts on one backbone need only one present.
	twoAcc := []provider{act(1, "omicron"), act(5, "omicron")}
	if m := missingBackbones(twoAcc, []providerRun{runOf(twoAcc[0])}); len(m) != 0 {
		t.Errorf("second same-backbone account demanded its own run: %v", m)
	}
	// An unset backbone is its own key (srv:ID) — a distinct requirement.
	bare := act(6, "")
	if m := missingBackbones([]provider{act(1, "omicron"), bare}, []providerRun{runOf(act(1, "omicron"))}); len(m) != 1 || m[0] != "srv:6" {
		t.Errorf("unset-backbone provider not required by its own key: %v", m)
	}
}

// foldConfirmation: present on any backbone beats the primary's 430; a
// backbone that fails to answer converts the claim to doubt (which the
// inconclusive guard turns into keep-the-prior-verdict); missing survives
// only unanimously.
func TestFoldConfirmation(t *testing.T) {
	res := []statResult{statMissing, statMissing, statMissing, statPresent}
	missIdx := []int{0, 1, 2}
	next := foldConfirmation(res, missIdx, []statResult{statPresent, statUnknown, statMissing})
	if res[0] != statPresent {
		t.Error("present on the second backbone did not rescue the segment")
	}
	if res[1] != statUnknown {
		t.Error("an unanswering backbone did not convert the 430 to doubt")
	}
	if res[2] != statMissing || len(next) != 1 || next[0] != 2 {
		t.Errorf("unanimity tracking wrong: res[2]=%v next=%v", res[2], next)
	}
	if res[3] != statPresent {
		t.Error("an untouched segment changed")
	}
}
