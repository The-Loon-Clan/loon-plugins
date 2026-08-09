package hitrun

import (
	"context"
	"testing"
	"time"
)

// fakeStore records what the sweep did, so a test asserts on ACTIONS rather
// than on rows. The sweep's job is to turn verdicts into effects exactly once
// each, and that is what this checks.
type fakeStore struct {
	cands      []Candidate
	prewarned  []string
	warnings   []Warning
	active     map[int64]int
	expired    int
	expireCall int
}

func (f *fakeStore) Candidates(context.Context, int) ([]Candidate, error) { return f.cands, nil }

func (f *fakeStore) RecordPrewarning(_ context.Context, u int64, h string) error {
	f.prewarned = append(f.prewarned, h)
	return nil
}

func (f *fakeStore) IssueWarning(_ context.Context, w Warning) error {
	f.warnings = append(f.warnings, w)
	if f.active == nil {
		f.active = map[int64]int{}
	}
	f.active[w.UserID]++
	return nil
}

func (f *fakeStore) ActiveWarnings(_ context.Context, u int64) (int, error) { return f.active[u], nil }

func (f *fakeStore) ExpireWarnings(context.Context, time.Time) (int, error) {
	f.expireCall++
	return f.expired, nil
}

func (f *fakeStore) ClearWarning(context.Context, int64, string) error { return nil }

func (f *fakeStore) UserSnatches(context.Context, int64) ([]Candidate, error) {
	return f.cands, nil
}
func (f *fakeStore) Standing(context.Context, int64) (Standing, error) { return Standing{}, nil }

func snatchFor(user int64, hash string, now time.Time) Candidate {
	return Candidate{
		Snatch: Snatch{
			UserID: user, InfoHash: hash,
			TorrentSize: 10 << 30, Downloaded: 10 << 30,
			Seedtime: 60, Seeding: false,
			LastSeen: now.Add(-10 * 24 * time.Hour),
		},
		TorrentName: "Something " + hash,
	}
}

// A member who has never been told gets the notice, not the warning — even
// though every other condition for a warning already holds.
func TestSweepPrewarnsBeforeItWarns(t *testing.T) {
	now := time.Now()
	f := &fakeStore{cands: []Candidate{snatchFor(1, "aa", now)}}
	res, err := Sweep(context.Background(), f, on(), Notifier{}, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Prewarned != 1 || res.Warned != 0 {
		t.Errorf("result = %+v, want one prewarn and no warnings", res)
	}
	if len(f.warnings) != 0 {
		t.Errorf("issued %d warnings on the first sight of an offence", len(f.warnings))
	}
}

// Once the notice has aged and the member is still gone, the warning carries
// the reason and an expiry taken from the policy.
func TestSweepWarnsWithAnExpiryAndAReason(t *testing.T) {
	now := time.Now()
	c := snatchFor(1, "aa", now)
	c.PrewarnedAt = now.Add(-30 * 24 * time.Hour)
	f := &fakeStore{cands: []Candidate{c}}

	p := on()
	if _, err := Sweep(context.Background(), f, p, Notifier{}, 100, now); err != nil {
		t.Fatal(err)
	}
	if len(f.warnings) != 1 {
		t.Fatalf("issued %d warnings, want 1", len(f.warnings))
	}
	w := f.warnings[0]
	if w.Reason == "" {
		t.Error("warning carries no reason — a member cannot act on that")
	}
	want := now.Add(time.Duration(p.ExpireDays) * 24 * time.Hour)
	if !w.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v (%d days)", w.ExpiresAt, want, p.ExpireDays)
	}
}

// The limit is checked ONCE per member, not once per warning. Somebody who
// trips three warnings in a single pass should be told once that their
// downloads are gone.
func TestLimitIsAnnouncedOncePerMember(t *testing.T) {
	now := time.Now()
	prewarned := now.Add(-30 * 24 * time.Hour)
	var cands []Candidate
	for _, h := range []string{"aa", "bb", "cc"} {
		c := snatchFor(1, h, now)
		c.PrewarnedAt = prewarned
		cands = append(cands, c)
	}
	f := &fakeStore{cands: cands}

	var limitCalls, blockedAt int
	n := Notifier{LimitReached: func(_ context.Context, _ int64, active int) {
		limitCalls++
		blockedAt = active
	}}
	res, err := Sweep(context.Background(), f, on(), n, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Warned != 3 {
		t.Errorf("warned %d, want 3", res.Warned)
	}
	if limitCalls != 1 {
		t.Errorf("announced the limit %d times, want once", limitCalls)
	}
	if blockedAt != 3 {
		t.Errorf("limit announced at %d warnings, want 3", blockedAt)
	}
}

// Turning the rules off must not freeze the warnings already on the record.
// Somebody warned yesterday should still see it lapse.
func TestExpiryRunsEvenWhenTheRulesAreOff(t *testing.T) {
	f := &fakeStore{expired: 4, cands: []Candidate{snatchFor(1, "aa", time.Now())}}
	res, err := Sweep(context.Background(), f, DefaultPolicy(), Notifier{}, 100, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.expireCall != 1 {
		t.Errorf("expiry ran %d times with the policy off, want 1", f.expireCall)
	}
	if res.Expired != 4 {
		t.Errorf("expired = %d, want 4", res.Expired)
	}
	// And nothing new is decided.
	if res.Prewarned != 0 || res.Warned != 0 || res.Considered != 0 {
		t.Errorf("a disabled policy still acted: %+v", res)
	}
}

// The notices are optional seams. A host that wires neither still gets the
// accounting — silently, which is worse for the member but not wrong for the
// site — and must not crash for the omission.
func TestSweepSurvivesUnwiredNotifiers(t *testing.T) {
	now := time.Now()
	c := snatchFor(1, "aa", now)
	c.PrewarnedAt = now.Add(-30 * 24 * time.Hour)
	f := &fakeStore{cands: []Candidate{c, snatchFor(2, "bb", now)}}
	if _, err := Sweep(context.Background(), f, on(), Notifier{}, 100, now); err != nil {
		t.Fatalf("sweep with no notifiers: %v", err)
	}
}

// The site's veto. A freeleech token is the case this exists for: a site that
// told somebody a download was free has already said what it owes, and warning
// them for not seeding it would be the site contradicting itself.
func TestExemptSeamExcusesASnatch(t *testing.T) {
	now := time.Now()
	c := snatchFor(1, "aa", now)
	c.PrewarnedAt = now.Add(-30 * 24 * time.Hour) // squarely a warning otherwise

	// Without the seam it is warned.
	SetDeps(Deps{})
	f := &fakeStore{cands: []Candidate{c}}
	if res, _ := Sweep(context.Background(), f, on(), Notifier{}, 100, now); res.Warned != 1 {
		t.Fatalf("setup: warned %d, want 1", res.Warned)
	}

	// With it, the same snatch is excused — and the seam is asked about the
	// right member and torrent.
	var askedUser int64
	var askedHash string
	SetDeps(Deps{Exempt: func(_ context.Context, u int64, h string) bool {
		askedUser, askedHash = u, h
		return true
	}})
	defer SetDeps(Deps{})
	f2 := &fakeStore{cands: []Candidate{c}}
	res, err := Sweep(context.Background(), f2, on(), Notifier{}, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Warned != 0 || res.Prewarned != 0 {
		t.Errorf("an exempt snatch was acted on: %+v", res)
	}
	if askedUser != 1 || askedHash != "aa" {
		t.Errorf("Exempt asked about (%d,%q), want (1,\"aa\")", askedUser, askedHash)
	}
}
