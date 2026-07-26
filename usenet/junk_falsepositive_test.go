package usenet

import "testing"

// The junk engine drops billions of titles — long_alnum_run alone is past 1.4B
// on the live site, and a build pass there currently reports ~100% junk. At
// that volume the question that matters is not "does it catch obfuscation" but
// "can it eat a real release", because a false positive is invisible: the post
// never appears and nobody knows to look for it.
//
// These are the shapes an anime indexer actually sees. Every one MUST survive.
// This is the guard rail for tuning a rule later — widen one and a real
// release stops being indexed, with no error anywhere to say so.
func TestJunkRules_RealReleaseNamesSurvive(t *testing.T) {
	survivors := []string{
		// Standard scene / fansub naming. Many dots and separators, which is
		// what keeps them clear of every bare-token and single-dot rule.
		"Sousou.no.Frieren.S01E12.1080p.WEB-DL.AAC2.0.H.264-VARYG",
		"[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2]",
		"Bocchi.the.Rock.S01.1080p.BluRay.FLAC2.0.x265-ZR",
		"One.Piece.1071.1080p.CR.WEB-DL.AAC2.0.H.264-VARYG",
		"[Erai-raws] Kusuriya no Hitorigoto - 24 [1080p][Multiple Subtitle]",
		"Attack on Titan Final Season THE FINAL CHAPTERS Special 2",
		"Mushoku Tensei Jobless Reincarnation Season 2 Part 2",
		"Spy.x.Family.S02E12.1080p.WEB.H264-SenpaiSubs",
		// Pure-letter short names: the digit gate on the bare-token rules is
		// what spares these, and it is the property most worth pinning because
		// single-word anime titles are common (Lamune, Gunbuster, Monster).
		"Frieren.mkv",
		"Bebop.mkv",
		"Monster",
		"Gunbuster",
	}
	for _, title := range survivors {
		if rule := whichJunkRule(title); rule != "" {
			t.Errorf("REAL RELEASE DROPPED: %q matched %q — this post would never be indexed", title, rule)
		}
	}
}

// The other half: the obfuscation these rules exist for must still be caught.
// A survival-only test would be satisfied by disabling everything.
//
// The expected rule NAMES here are attribution order, not a guess: several
// rules can match one title and the engine reports the first. Pinning the name
// rather than just "non-empty" means a reordering shows up as a diff instead
// of silently changing what the filter-hits table attributes drops to.
func TestJunkRules_ObfuscationStillCaught(t *testing.T) {
	junk := map[string]string{
		"f329yZ98AaYf2qHd.QPv2":                "dot_sep_obfuscated",
		"Pzz8CzBPoBNsCu8oRPpDYwESRkpq5UU3jGlz": "long_alnum_run",
		"550e8400-e29b-41d4-a716-446655440000": "uuid",
		// 26-char run then a dot: long_alnum_run reaches it before the
		// dot-separated rule does.
		"2681dSNK8Q0BF58aX0Ly86Oa1N.7lm": "long_alnum_run",
	}
	for title, want := range junk {
		if got := whichJunkRule(title); got != want {
			t.Errorf("whichJunkRule(%q) = %q, want %q", title, got, want)
		}
	}
}

// dot_sep_obfuscated looks like the dangerous one — dots are how every real
// release separates its fields — but it cannot reach a normal name, and the
// two properties that make that true are worth pinning explicitly because
// losing either silently widens it.
func TestDotSepObfuscated_CannotReachARealName(t *testing.T) {
	// 1. EXACTLY ONE DOT. The pattern is ^[A-Za-z0-9]{6,}\.[A-Za-z0-9]{1,12}$
	//    and the segments admit no dots, so anything with a second one is out
	//    of reach however obfuscated it looks. Every real release has several.
	if rule := whichJunkRule("aB3xY9zQ.mK4.QPv2"); rule == "dot_sep_obfuscated" {
		t.Error("dot_sep_obfuscated matched a two-dot title; it must require exactly one")
	}

	// 2. ALL THREE CHARACTER GATES — mixed case AND a digit together.
	for _, safe := range []string{
		"Frierenx.mkv",  // no digit
		"frieren2x.mkv", // no uppercase
		"FRIEREN2X.MKV", // no lowercase
	} {
		if rule := whichJunkRule(safe); rule == "dot_sep_obfuscated" {
			t.Errorf("%q matched dot_sep_obfuscated — a character gate is not being applied", safe)
		}
	}
}

// The rule that CAN over-reach is not the dotted one — it is
// short_alnum_token, a bare 6-15 char run mixing letters and digits, which the
// engine reaches after stripping a file extension. A one-word title with a
// number in it is exactly that shape.
//
// Documented rather than changed: the rule is prod's single largest junk class
// and the shape it catches ("S7rEx773") is overwhelmingly spam, so loosening it
// to spare "Frieren2" would trade a rare false positive for a large false
// negative. This test exists so the trade-off is a recorded decision instead of
// a surprise the next time someone asks why a short title vanished.
func TestShortAlnumToken_EatsShortOneWordTitlesWithADigit(t *testing.T) {
	for _, title := range []string{"Frieren2.mkv", "frieren2.mkv", "Bocchi2"} {
		if rule := whichJunkRule(title); rule != "short_alnum_token" {
			t.Errorf("whichJunkRule(%q) = %q, want short_alnum_token — "+
				"if this rule changed, revisit whether the trade-off still holds", title, rule)
		}
	}
	// The digit gate is the whole defence for single-word titles, so it gets
	// its own assertion: without a digit these must survive.
	for _, title := range []string{"Frieren.mkv", "Bocchi", "Lamune"} {
		if rule := whichJunkRule(title); rule != "" {
			t.Errorf("pure-letter title %q matched %q — the digit gate is gone", title, rule)
		}
	}
}
