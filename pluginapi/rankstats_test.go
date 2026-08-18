package pluginapi

import (
	"context"
	"testing"
	"time"
)

// A host that predates ReleasesContributed still satisfies the contract.
//
// This is the additive claim, pinned rather than asserted in a comment. The
// implementation below is the shape every existing host has — it fills the
// tracker figures and the join date and has never heard of a release count — and
// the only way it stops compiling is if somebody makes the new field a required
// part of the contract, which is exactly the change that must not happen
// silently. The behavioural half is beside it: what such a host reports is "not
// proven", never a zero that a threshold could mistake for a real answer.
type hostBeforeReleaseCounts struct{}

func (hostBeforeReleaseCounts) AllStats(context.Context) (map[int64]MemberStats, error) {
	var s MemberStats
	s.Uploaded, s.Downloaded = 10<<30, 5<<30
	s.Ratio = 2.0
	s.JoinedAt = time.Now().AddDate(-1, 0, 0)
	return map[int64]MemberStats{1: s}, nil
}

var _ RankStats = hostBeforeReleaseCounts{}

func TestAHostWithNoReleaseCountReportsNotProven(t *testing.T) {
	all, err := hostBeforeReleaseCounts{}.AllStats(context.Background())
	if err != nil {
		t.Fatalf("AllStats: %v", err)
	}
	s := all[1]
	if n, known := s.Releases(); known || n != 0 {
		t.Errorf("Releases() = (%d, %v), want (0, false) — an unsupplied count must read as not proven", n, known)
	}
	// And the figures it DOES supply are untouched by the new field, which is the
	// other half of "additive".
	if s.Uploaded != 10<<30 || s.Ratio != 2.0 {
		t.Errorf("the existing figures changed: uploaded=%d ratio=%v", s.Uploaded, s.Ratio)
	}
}

// A host that answers reports the count as PROVEN, including a genuine zero.
//
// Zero is the value the pointer exists for: "this member has contributed
// nothing" is a real answer and must be distinguishable from "this host does not
// keep the figure", even though neither earns a rank.
func TestAProvenZeroIsNotTheSameAsAnAbsentCount(t *testing.T) {
	proven := MemberStats{ReleasesContributed: ReleaseCount(0)}
	if n, known := proven.Releases(); !known || n != 0 {
		t.Errorf("Releases() = (%d, %v), want (0, true) for a proven zero", n, known)
	}
	absent := MemberStats{}
	if _, known := absent.Releases(); known {
		t.Error("an absent count reported itself as proven")
	}
	counted := MemberStats{ReleasesContributed: ReleaseCount(42)}
	if n, known := counted.Releases(); !known || n != 42 {
		t.Errorf("Releases() = (%d, %v), want (42, true)", n, known)
	}
}
