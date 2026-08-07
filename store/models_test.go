package store

import "testing"

// The icon is derived, so the mapping IS the feature: an unmapped reward type
// must fall back rather than produce an empty <use href="#">, which renders as
// a blank gap the card has no way to explain.
func TestItemIconCoversEveryRewardType(t *testing.T) {
	for _, tc := range []struct {
		rewardType string
		want       string
	}{
		{string(RewardRank), "shield"},
		{string(RewardInvite), "envelope"},
		{"freeleech", "tag"}, // not implemented yet — must not blank out
		{"", "tag"},          // a row written before reward_type was required
		{"NONSENSE", "tag"},
	} {
		i := &Item{RewardType: tc.rewardType}
		if got := i.Icon(); got != tc.want {
			t.Errorf("Icon() for reward_type %q = %q, want %q", tc.rewardType, got, tc.want)
		}
	}
}
