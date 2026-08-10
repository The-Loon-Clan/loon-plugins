package tracker

import (
	"testing"
	"time"
)

func sample(mut func(*CheatSample)) CheatSample {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := CheatSample{
		UserID: 1, InfoHash: "a",
		PrevAt: base, CurAt: base.Add(time.Hour),
		PrevUp: 0, CurUp: 1 << 30, // 1 GiB in an hour ≈ 0.3 MB/s
		TorrentSize: 1 << 30, Peers: 5,
	}
	mut(&s)
	return s
}

func onPolicy() CheatPolicy {
	p := DefaultCheatPolicy()
	p.Enabled = true
	return p
}

// Off means off. Every rule here can end up accusing somebody, so the default
// has to be silence.
func TestCheatDisabledFindsNothing(t *testing.T) {
	s := sample(func(s *CheatSample) { s.CurUp = 1 << 50 }) // absurd
	if _, ok := EvaluateCheat(DefaultCheatPolicy(), s); ok {
		t.Error("a disabled policy produced a finding")
	}
}

func TestCheatFlagsImpossibleRate(t *testing.T) {
	// 1 TiB in an hour is ~291 MB/s sustained — past a gigabit line.
	s := sample(func(s *CheatSample) { s.CurUp = 1 << 40 })
	f, ok := EvaluateCheat(onPolicy(), s)
	if !ok || f.Kind != CheatImpossibleRate {
		t.Fatalf("got %+v ok=%v, want an impossible-rate finding", f, ok)
	}
	// The detail is read by a person deciding whether to act, so it has to
	// carry the measurement rather than just the verdict.
	if f.Detail == "" {
		t.Error("finding carries no detail for the reader")
	}
}

// An ordinary evening must not be flagged. This is the case the thresholds
// exist to protect: the cost of a wrong accusation is a member.
func TestCheatIgnoresPlausibleSeeding(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*CheatSample)
	}{
		{"a gigabyte an hour", func(s *CheatSample) { s.CurUp = 1 << 30 }},
		{"20x a small torrent to a real swarm", func(s *CheatSample) {
			s.TorrentSize = 700 << 20
			s.CurUp = 20 * (700 << 20)
			s.CurAt = s.PrevAt.Add(48 * time.Hour)
			s.Peers = 30
		}},
	} {
		if f, ok := EvaluateCheat(onPolicy(), sample(tc.mut)); ok {
			t.Errorf("%s was flagged as %s: %s", tc.name, f.Kind, f.Detail)
		}
	}
}

// A counter going backwards is the tracker's own housekeeping — a torrent
// removed and re-added, or a row rebuilt — not a member cheating.
func TestCheatIgnoresCounterResets(t *testing.T) {
	s := sample(func(s *CheatSample) { s.PrevUp = 5 << 30; s.CurUp = 1 << 20 })
	if f, ok := EvaluateCheat(onPolicy(), s); ok {
		t.Errorf("a counter reset was flagged as %s", f.Kind)
	}
}

// The guard against the detector inventing its own false positives: two
// snapshots seconds apart turn one ordinary announce into an impossible rate.
func TestCheatRefusesToDivideByATinyWindow(t *testing.T) {
	s := sample(func(s *CheatSample) {
		s.CurAt = s.PrevAt.Add(4 * time.Second)
		s.CurUp = 500 << 20 // 500 MiB — 125 MB/s over 4s, but the window is noise
	})
	if f, ok := EvaluateCheat(onPolicy(), s); ok {
		t.Errorf("judged a %v window and called it %s", 4*time.Second, f.Kind)
	}
	// The same bytes over a window long enough to mean something IS judged.
	s2 := sample(func(s *CheatSample) {
		s.CurAt = s.PrevAt.Add(10 * time.Minute)
		s.CurUp = 200 << 30
	})
	if _, ok := EvaluateCheat(onPolicy(), s2); !ok {
		t.Error("a wide window with an impossible rate was not flagged")
	}
}

// Small deltas are arithmetic on noise.
func TestCheatIgnoresTinyDeltas(t *testing.T) {
	s := sample(func(s *CheatSample) {
		s.CurUp = 1 << 20 // 1 MiB
		s.CurAt = s.PrevAt.Add(6 * time.Minute)
	})
	if _, ok := EvaluateCheat(onPolicy(), s); ok {
		t.Error("a 1 MiB delta produced a finding")
	}
}

// A ratio against an unknown size is a division this refuses rather than a
// very large number it reports.
func TestCheatSkipsRatioWhenSizeIsUnknown(t *testing.T) {
	s := sample(func(s *CheatSample) {
		s.TorrentSize = 0
		s.CurUp = 900 << 30
		s.CurAt = s.PrevAt.Add(400 * time.Hour) // slow enough not to trip the rate rule
	})
	if f, ok := EvaluateCheat(onPolicy(), s); ok && f.Kind == CheatRatioImplausible {
		t.Error("computed a ratio against a zero size")
	}
}

// The swarm softens the ratio rule: a torrent with peers can legitimately
// absorb far more upload than one with none.
func TestCheatRatioAllowanceGrowsWithTheSwarm(t *testing.T) {
	mk := func(peers int) CheatSample {
		return sample(func(s *CheatSample) {
			s.TorrentSize = 1 << 30
			s.CurUp = 900 << 30 // 900x the torrent
			s.CurAt = s.PrevAt.Add(400 * time.Hour)
			s.Peers = peers
		})
	}
	if _, ok := EvaluateCheat(onPolicy(), mk(0)); !ok {
		t.Error("900x to an empty swarm was not flagged")
	}
	if f, ok := EvaluateCheat(onPolicy(), mk(50)); ok {
		t.Errorf("900x to a swarm of 50 was flagged as %s: %s", f.Kind, f.Detail)
	}
}
