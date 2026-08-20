package polls

import (
	"strings"
	"testing"
	"time"
)

// The four pure functions in this plugin, none of which had a test: the whole
// results-policy matrix was verified once by hand against a running site, which
// proved it worked that afternoon and nothing since.

// TestShowResults is the plugin's only real editorial decision, and it is a
// three-by-two-by-two matrix that reads as obvious and is not.
func TestShowResults(t *testing.T) {
	for _, tc := range []struct {
		policy string
		voted  bool
		closed bool
		want   bool
		why    string
	}{
		// after_vote — the default, and the one that keeps a poll honest: a
		// running tally you can see before answering moves how you answer.
		{ResultsAfterVote, false, false, false, "a non-voter must not see the tally while it is open"},
		{ResultsAfterVote, true, false, true, "you have committed, so you may see it"},

		// always — a temperature check where the tally IS the point.
		{ResultsAlways, false, false, true, "always means always, voted or not"},
		{ResultsAlways, true, false, true, ""},

		// on_close — for a vote where an early lead would campaign.
		{ResultsOnClose, false, false, false, ""},
		{ResultsOnClose, true, false, false, "even the voter waits; that is the whole policy"},

		// CLOSED shows results under every policy, including to somebody who
		// never voted. The reason to withhold stopped applying when the last
		// vote was cast, and a closed poll nobody can read serves nobody.
		{ResultsAfterVote, false, true, true, "closed: withholding now serves nobody"},
		{ResultsOnClose, false, true, true, "closed: that is what on_close MEANS"},
		{ResultsAlways, false, true, true, ""},

		// An unrecognised policy falls back to the safe default rather than to
		// "show everything" — a typo in a stored value must not publish a
		// tally the operator chose to withhold.
		{"nonsense-from-an-older-version", false, false, false, "unknown policy must not default to open"},
		{"", false, false, false, "empty policy must not default to open"},
	} {
		got := showResults(tc.policy, tc.voted, tc.closed)
		if got != tc.want {
			t.Errorf("showResults(%q, voted=%v, closed=%v) = %v, want %v — %s",
				tc.policy, tc.voted, tc.closed, got, tc.want, tc.why)
		}
	}
}

func TestPercent(t *testing.T) {
	for _, tc := range []struct {
		n, total, want int
		why            string
	}{
		{0, 0, 0, "no votes at all must not divide by zero"},
		{0, 10, 0, ""},
		{5, 10, 50, ""},
		{1, 3, 33, "rounds"},
		{2, 3, 67, "rounds up at .5 and above"},
		{10, 10, 100, ""},
		{1, 8, 13, "12.5 rounds to 13"},
	} {
		if got := percent(tc.n, tc.total); got != tc.want {
			t.Errorf("percent(%d, %d) = %d, want %d %s", tc.n, tc.total, got, tc.want, tc.why)
		}
	}
}

// TestSlugify is what turns a question into the name a shortcode refers to, so
// an operator who never types a slug still gets a usable one.
func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Should we allow re-encodes?":  "should-we-allow-re-encodes",
		"  Leading and trailing  ":     "leading-and-trailing",
		"Multiple   spaces":            "multiple-spaces",
		"Punctuation!!! everywhere???": "punctuation-everywhere",
		"UPPER Case":                   "upper-case",
		"already-a-slug":               "already-a-slug",
		"2160p or 1080p":               "2160p-or-1080p",
		"???":                          "", // nothing usable in it
		"":                             "",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSlugifyNeverStartsOrEndsWithADash, and never doubles one — the shortcode
// is typed by a person and a slug with a trailing dash is one they will get
// wrong.
func TestSlugifyNeverStartsOrEndsWithADash(t *testing.T) {
	for _, in := range []string{
		"...leading punctuation", "trailing punctuation...",
		"!!!both!!!", "a???b", strings.Repeat("Very long question ", 20),
	} {
		got := slugify(in)
		if got == "" {
			continue
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("slugify(%q) = %q — dangling dash", in, got)
		}
		if strings.Contains(got, "--") {
			t.Errorf("slugify(%q) = %q — doubled dash", in, got)
		}
		if len(got) > 60 {
			t.Errorf("slugify(%q) is %d chars, want at most 60", in, len(got))
		}
	}
}

// TestPollClosed — two ways to end, kept as two columns because they are
// different facts, and this is the function that stops every caller from
// having to know that.
func TestPollClosed(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	for _, tc := range []struct {
		name string
		poll Poll
		want bool
	}{
		{"no deadline, never closed", Poll{}, false},
		{"deadline in the future", Poll{ClosesAt: &future}, false},
		{"deadline passed", Poll{ClosesAt: &past}, true},
		{"closed by hand", Poll{ClosedAt: &past}, true},
		// Reopening clears closed_at and leaves closes_at alone, which means a
		// poll whose deadline has passed closes again immediately. That is the
		// documented behaviour, not an oversight: an operator reopening an
		// expired poll should also move the deadline, and this makes that
		// visible rather than silently discarding what they set.
		{"reopened but the deadline already passed", Poll{ClosesAt: &past}, true},
		{"closed by hand though the deadline is ahead", Poll{ClosedAt: &past, ClosesAt: &future}, true},
	} {
		if got := tc.poll.Closed(now); got != tc.want {
			t.Errorf("%s: Closed() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestClosedIsExclusiveAtTheBoundary. A deadline of exactly now counts as
// closed — a poll that says it closes at noon should not still be taking votes
// at noon.
func TestClosedIsExclusiveAtTheBoundary(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if !(Poll{ClosesAt: &now}).Closed(now) {
		t.Error("a poll at its exact deadline was still open")
	}
}
