package usenet

import "testing"

func TestSweepRewritesOnlyWhatItIsSureAbout(t *testing.T) {
	for _, tc := range []struct {
		name              string
		current, proposed int
		want              bool
	}{
		{"a real correction is written", 5040, 5045, true},
		{"audio codec on a video release", 3040, 2030, true},
		{"the default finally categorised", 8010, 5070, true},
		{"agreement writes nothing", 2040, 2040, false},
		{"no opinion leaves a category alone", 5040, 8010, false},
		// The case that matters most: a row already at the default, which the
		// categoriser still cannot place, must not be written every single
		// sweep for the rest of time.
		{"no opinion about a default is still no write", 8010, 8010, false},
	} {
		if got := shouldRewrite(tc.current, tc.proposed); got != tc.want {
			t.Errorf("%s: shouldRewrite(%d, %d) = %v, want %v",
				tc.name, tc.current, tc.proposed, got, tc.want)
		}
	}
}
