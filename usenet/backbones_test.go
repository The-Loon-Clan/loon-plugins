package usenet

import (
	"strings"
	"testing"
)

// TestBackboneForHost covers the matching rule. The boundary case is the one
// that matters: "eweka.nl" must not match "fake-eweka.nl", or a lookalike
// hostname would key a provider's crawl state onto someone else's backbone —
// which is the silent article-loss failure PROVIDERS.md warns about.
func TestBackboneForHost(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"news.eweka.nl", "omicron"},
		{"eweka.nl", "omicron"},
		{"EWEKA.NL", "omicron"},                 // case-insensitive
		{"  news.eweka.nl  ", "omicron"},        // trimmed
		{"news.eweka.nl:563", "omicron"},        // port tolerated
		{"nntp://news.eweka.nl:563", "omicron"}, // scheme tolerated
		{"news.eweka.nl.", "omicron"},           // trailing dot (FQDN form)
		{"news.bulknews.eu", "abavia"},
		{"news.sunnyusenet.com", "omicron"},

		// Must NOT match.
		{"fake-eweka.nl", ""},     // label boundary, not substring
		{"eweka.nl.evil.com", ""}, // domain appearing as a left-hand label
		{"news.example.com", ""},  // simply unknown
		{"", ""},
	}
	for _, tc := range cases {
		if got := backboneForHost(tc.host); got != tc.want {
			t.Errorf("backboneForHost(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// TestBackboneSeedParses guards the shipped table: a malformed row would just
// silently disable pre-filling, so assert it actually loaded.
func TestBackboneSeedParses(t *testing.T) {
	entries := loadBackbones()
	if len(entries) < 5 {
		t.Fatalf("loaded %d backbone entries, want the shipped table", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Domain == "" || e.Backbone == "" {
			t.Errorf("incomplete entry: %+v", e)
		}
		if e.Domain != strings.ToLower(e.Domain) {
			t.Errorf("domain %q should be normalised lower-case", e.Domain)
		}
		if seen[e.Domain] {
			t.Errorf("duplicate domain %q — the longest-match rule makes duplicates ambiguous", e.Domain)
		}
		seen[e.Domain] = true
		switch e.Confidence {
		case "confirmed", "likely", "":
		default:
			t.Errorf("domain %q: unknown confidence %q", e.Domain, e.Confidence)
		}
	}
}
