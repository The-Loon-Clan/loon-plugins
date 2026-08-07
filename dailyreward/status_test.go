package dailyreward

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubStore answers Get from a fixed State so the status seam can be exercised
// without a database. Claim is never called on this path.
type stubStore struct {
	st  State
	err error
}

func (s stubStore) Get(context.Context, int64) (State, error) { return s.st, s.err }
func (s stubStore) Claim(context.Context, int64, string, string) (int, int, bool, error) {
	return 0, 0, false, errors.New("not used by status")
}

// The extension exists so a host can ask "may this member claim right now?"
// without duplicating the once-per-day rule or reading this plugin's table.
// That makes the Claimed flag the whole point, and it is a date comparison —
// the one thing that silently goes wrong at a UTC day boundary.
func TestStatusAnswersTheClaimQuestion(t *testing.T) {
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	for _, tc := range []struct {
		name        string
		st          State
		wantClaimed bool
		wantStreak  int
		wantReward  int
	}{
		{
			name:        "already claimed today",
			st:          State{LastClaim: today(), Streak: 3},
			wantClaimed: true, wantStreak: 3,
			// Today's payout, not tomorrow's: there is no claim to make, so
			// nextStreak stands still. Harmless because a host reads Reward
			// only when Claimed is false — which is what the field's comment
			// now says, after this test caught it promising otherwise.
			wantReward: rewardFor(3),
		},
		{
			name:        "claimed yesterday, streak continues",
			st:          State{LastClaim: yesterday, Streak: 3},
			wantClaimed: false, wantStreak: 3, wantReward: rewardFor(4),
		},
		{
			name:        "never claimed",
			st:          State{},
			wantClaimed: false, wantStreak: 0, wantReward: rewardFor(1),
		},
		{
			name:        "streak broken by a missed day",
			st:          State{LastClaim: "2020-01-01", Streak: 9},
			wantClaimed: false, wantStreak: 9,
			// The stored streak is reported as-is, but the reward reflects the
			// RESTART — a host labelling its control with the old streak's
			// payout would promise points the claim will not pay.
			wantReward: rewardFor(1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plugin{st: stubStore{st: tc.st}}
			got, err := p.status(context.Background(), 42)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if got.Claimed != tc.wantClaimed {
				t.Errorf("Claimed = %v, want %v", got.Claimed, tc.wantClaimed)
			}
			if got.Streak != tc.wantStreak {
				t.Errorf("Streak = %d, want %d", got.Streak, tc.wantStreak)
			}
			if got.Reward != tc.wantReward {
				t.Errorf("Reward = %d, want %d", got.Reward, tc.wantReward)
			}
		})
	}
}

// A store failure must surface, not read as "nothing claimed yet" — a host
// would then offer a claim control that cannot work.
func TestStatusPropagatesStoreFailure(t *testing.T) {
	p := &Plugin{st: stubStore{err: errors.New("db down")}}
	if _, err := p.status(context.Background(), 42); err == nil {
		t.Error("a store failure was swallowed; the host would offer a claim that cannot succeed")
	}
}
