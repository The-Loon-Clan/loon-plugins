package hitrun

import (
	"testing"
	"time"
)

// A snatch that is squarely an offence, which each test then varies ONE thing
// from. Stating the situation this way means a failure names the single fact
// that changed the verdict.
func offender(now time.Time) Snatch {
	return Snatch{
		UserID:      7,
		InfoHash:    "aa",
		TorrentSize: 10 << 30, // 10 GiB
		Downloaded:  10 << 30, // took all of it
		Uploaded:    0,        // gave nothing back
		Seedtime:    3600,     // one hour of the seven days required
		Seeding:     false,    // and left
		LastSeen:    now.Add(-10 * 24 * time.Hour),
	}
}

func on() Policy {
	p := DefaultPolicy()
	p.Enabled = true
	return p
}

// Every early return in Evaluate is a reason NOT to punish somebody, so each
// one gets its own case. A rule that warns the wrong person is worse than one
// that misses an offender: the offender costs the site bandwidth, the false
// positive costs it a member.
func TestNobodyIsWarnedWithoutCause(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	long := now.Add(-30 * 24 * time.Hour) // prewarned ages ago: grace is spent

	for _, tc := range []struct {
		name   string
		mutate func(*Snatch)
		policy func(*Policy)
	}{
		{"the rules are off", nil, func(p *Policy) { p.Enabled = false }},
		{"the snatch is exempt", func(s *Snatch) { s.Immune = true }, nil},
		// The buffer. Someone who started a 10GiB torrent and took 500MiB
		// changed their mind; they did not hit and run.
		{"they barely took any of it", func(s *Snatch) { s.Downloaded = 500 << 20 }, nil},
		// Still connected. Whatever the clock says, they have not run.
		{"they are still seeding", func(s *Snatch) { s.Seeding = true }, nil},
		{"they met the seedtime", func(s *Snatch) { s.Seedtime = 604800 }, nil},
		// A full copy returned is a share done, however fast.
		{"they returned a full copy", func(s *Snatch) { s.Uploaded = s.Downloaded }, nil},
		{"they have only just left", func(s *Snatch) { s.LastSeen = now.Add(-2 * time.Hour) }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := offender(now)
			p := on()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			if tc.policy != nil {
				tc.policy(&p)
			}
			if got := Evaluate(p, s, long, now); got.Verdict != Satisfied {
				t.Errorf("verdict = %s (%q), want satisfied", got.Verdict, got.Reason)
			}
		})
	}
}

// The escalation is prewarn, then warn — never straight to a warning. A member
// is told before they are punished, and the grace exists so that being told
// means something.
func TestEscalationGoesThroughTheNotice(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	p := on()
	s := offender(now)

	// Never notified: the first verdict is the notice, not the warning.
	got := Evaluate(p, s, time.Time{}, now)
	if got.Verdict != Prewarn {
		t.Fatalf("first verdict = %s (%q), want prewarn", got.Verdict, got.Reason)
	}

	// Notified just now: the grace has not run, so nothing happens yet.
	if got := Evaluate(p, s, now.Add(-1*time.Hour), now); got.Verdict != Satisfied {
		t.Errorf("during grace = %s (%q), want satisfied", got.Verdict, got.Reason)
	}

	// Notified, grace spent, still gone: now it is a warning.
	spent := now.Add(-time.Duration(p.GraceDays)*24*time.Hour - time.Minute)
	if got := Evaluate(p, s, spent, now); got.Verdict != Warn {
		t.Errorf("after grace = %s (%q), want warn", got.Verdict, got.Reason)
	}
}

// Coming back during the grace period is the whole point of having one.
func TestReseedingDuringGraceClearsIt(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	p := on()
	s := offender(now)
	spent := now.Add(-30 * 24 * time.Hour)

	// Gone: a warning is due.
	if got := Evaluate(p, s, spent, now); got.Verdict != Warn {
		t.Fatalf("setup: want warn, got %s", got.Verdict)
	}
	// Reconnected — no warning, regardless of how long they were away.
	s.Seeding = true
	if got := Evaluate(p, s, spent, now); got.Verdict != Satisfied {
		t.Errorf("after reseeding = %s (%q), want satisfied", got.Verdict, got.Reason)
	}
}

// The ratio escape is a policy CHOICE, so it has to be switchable — a site that
// wants seedtime and nothing else must be able to say so.
func TestRatioEscapeCanBeTurnedOff(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s := offender(now)
	s.Uploaded = s.Downloaded // a full copy back, but only an hour of seeding
	spent := now.Add(-30 * 24 * time.Hour)

	p := on()
	if got := Evaluate(p, s, spent, now); got.Verdict != Satisfied {
		t.Errorf("with the escape on = %s, want satisfied", got.Verdict)
	}
	p.RatioSatisfies = false
	if got := Evaluate(p, s, spent, now); got.Verdict != Warn {
		t.Errorf("with the escape off = %s, want warn", got.Verdict)
	}
}

// A zero in the config is almost always a key nobody set, not an operator
// asking for no grace at all. Reading it literally turns a missing line into a
// site that warns everybody the moment they disconnect.
func TestUnsetSettingsFallBackToDefaults(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// Everything zero except the switch — what an operator gets from
	// `plugins.hitrun.enabled = true` and nothing else.
	p := Policy{Enabled: true}
	s := offender(now)
	s.Seedtime = 604800 // meets the DEFAULT requirement

	if got := Evaluate(p, s, time.Time{}, now); got.Verdict != Satisfied {
		t.Errorf("verdict = %s (%q) — a zero seedtime requirement would warn everyone",
			got.Verdict, got.Reason)
	}
	// And a zero MaxWarnings must not mean "blocked at zero warnings".
	if DownloadsBlocked(p, 0) {
		t.Error("DownloadsBlocked(0 warnings) = true — an unset limit blocked everybody")
	}
}

func TestDownloadsBlockAtTheLimit(t *testing.T) {
	p := on()
	for _, tc := range []struct {
		warnings int
		want     bool
	}{{0, false}, {2, false}, {3, true}, {4, true}} {
		if got := DownloadsBlocked(p, tc.warnings); got != tc.want {
			t.Errorf("DownloadsBlocked(%d) = %v, want %v", tc.warnings, got, tc.want)
		}
	}
	// Off means off, however many warnings are on the record.
	off := DefaultPolicy()
	if DownloadsBlocked(off, 99) {
		t.Error("a disabled policy still blocked downloads")
	}
}

// A member is owed an explanation they can act on. "Policy violation" is not
// one; the numbers that produced the decision are.
func TestTheReasonNamesTheNumbers(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	got := Evaluate(on(), offender(now), now.Add(-30*24*time.Hour), now)
	if got.Verdict != Warn {
		t.Fatalf("want warn, got %s", got.Verdict)
	}
	for _, want := range []string{"7 days", "1 hour", "3 day"} {
		if !contains(got.Reason, want) {
			t.Errorf("reason %q does not mention %q", got.Reason, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Two clocks, and BOTH must run out before a warning.
//
// This is the case that made the notice-only version wrong: a member told a
// month ago, who came back, seeded, and left again an hour before the job ran,
// has an ancient notice and is not the person the rule is for. UNIT3D requires
// the same pair — prewarned_at past the prewarn threshold AND updated_at (the
// last announce) past the grace one.
func TestGraceIsMeasuredFromTheNoticeAndFromTheLastAnnounce(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	p := on()
	ancient := now.Add(-30 * 24 * time.Hour)

	// Told long ago, but seen an hour ago: not a hit and run.
	s := offender(now)
	s.LastSeen = now.Add(-1 * time.Hour)
	if got := Evaluate(p, s, ancient, now); got.Verdict != Satisfied {
		t.Errorf("recently seen = %s (%q), want satisfied", got.Verdict, got.Reason)
	}

	// Gone a long time, but only just told: also not yet.
	s = offender(now)
	if got := Evaluate(p, s, now.Add(-1*time.Hour), now); got.Verdict != Satisfied {
		t.Errorf("just told = %s (%q), want satisfied", got.Verdict, got.Reason)
	}

	// Both clocks spent: now it counts.
	if got := Evaluate(p, offender(now), ancient, now); got.Verdict != Warn {
		t.Errorf("both spent = %s (%q), want warn", got.Verdict, got.Reason)
	}
}

// The member page asks a different question from the sweep, and Evaluate
// cannot answer it: a snatch inside its notice period and one seeded to term
// both come back Satisfied, yet to a member those are opposite situations.
func TestAtRiskSeesDebtBeforeTheClocksDo(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	p := on()

	// Left an hour ago, nowhere near the seedtime: no action is due yet, but
	// the member owes seeding and should be told now rather than after the
	// notice, when it is too late to avoid it.
	fresh := offender(now)
	fresh.LastSeen = now.Add(-time.Hour)
	if got := Evaluate(p, fresh, time.Time{}, now); got.Verdict != Satisfied {
		t.Fatalf("setup: want no action yet, got %s", got.Verdict)
	}
	if !AtRisk(p, fresh) {
		t.Error("AtRisk = false for a snatch that owes seeding — the page would hide it")
	}

	// Everything that excuses a snatch also clears the debt.
	for _, tc := range []struct {
		name   string
		mutate func(*Snatch)
	}{
		{"still seeding", func(s *Snatch) { s.Seeding = true }},
		{"seedtime met", func(s *Snatch) { s.Seedtime = 604800 }},
		{"full copy returned", func(s *Snatch) { s.Uploaded = s.Downloaded }},
		{"below the buffer", func(s *Snatch) { s.Downloaded = 100 << 20 }},
		{"exempt", func(s *Snatch) { s.Immune = true }},
	} {
		s := offender(now)
		tc.mutate(&s)
		if AtRisk(p, s) {
			t.Errorf("%s: AtRisk = true, want false", tc.name)
		}
	}

	// Off means nothing is owed.
	if AtRisk(DefaultPolicy(), offender(now)) {
		t.Error("AtRisk = true with the rules disabled")
	}
}

func TestOwedCountsDownAndStopsAtZero(t *testing.T) {
	p := on()
	s := offender(time.Now())
	s.Seedtime = 604800 - 3600 // an hour short
	if got := Owed(p, s); got != time.Hour {
		t.Errorf("Owed = %v, want 1h", got)
	}
	s.Seedtime = 604800
	if got := Owed(p, s); got != 0 {
		t.Errorf("Owed once met = %v, want 0", got)
	}
	s.Seedtime = 999999 // over-seeded: still zero, never negative
	if got := Owed(p, s); got != 0 {
		t.Errorf("Owed when over = %v, want 0", got)
	}
}
