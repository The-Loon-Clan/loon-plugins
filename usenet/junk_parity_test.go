package usenet

import "testing"

// Parity vectors for the full prod port. Every case here was checked against
// prod's whichJunkPattern / whichJunkPatternSized behaviour; several are prod's
// own documented examples. The regression that motivated the port leads the
// file: "0N70ZyFoz8n50" was indexed by the FIRST live crawl because the partial
// lift had no short_alnum_token rule.

func TestJunkParityUnsized(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		// The live-crawl regression, and its class.
		{"0N70ZyFoz8n50", "short_alnum_token"},
		{"S7rEx773", "short_alnum_token"},
		{"MGACB13C1210", "short_alnum_token"},
		{"rqNGWU64", "short_alnum_token"},
		{"Persona5Royal", "short_alnum_token"}, // prod flags it too — bare compact forms lose
		{"Danganronpa2", "short_alnum_token"},

		// The band above it, and the 20+ band.
		{"pE9F9wMjNxWZqpq9V", "mid_alnum_token"},
		{"QTVxBgZmUbZnAJFWgJq6", "single_token_20"},
		{"f2c8b393559540cfb9e33471cfda340c", "long_alnum_run"},

		// Orphan PAR2 recovery volume (2026-07-25): single-file-form posts
		// name only the volume, so it never groups under its parent.
		{"sigma-sun.vol024+02", "par2_volume"},
		{"sigma-sun.vol000+01", "par2_volume"},

		// Patterns the partial lift was missing entirely.
		{"1715012345678", "long_digit_run"},
		{"Microsoft Office 2024 Pre-Cracked (x64)", "software_warez"},
		{"input", "software_warez"},
		{"App Setup 9.4.7.0 Multilingual", "software_warez"},
		{"cerfragf ol znfgrenaqv", "rot13_archive"},
		{"rtNJ rtNJ", "repeated_short_tok"},
		{"07w5a8eborq1saw8zg.1v8", "alnum_blob_ext"},
		{"Release ${title} here", "template_token"}, // {title} inside it — template_token runs first, as in prod
		{"Release ${a-b} here", "js_template_leak"}, // dash keeps template_token out; only the ${...} rule sees it
		{"$&!@#=$%^&*=!@", "high_special_chars"},
		{"EXxxU3CnKN2D extra", "random_words"}, // 12 random chars of 17 non-punct = 70%+
		{"EXxxUeCnKNXD extra", ""},             // no digit — isRandomWord needs one, in prod too

		// Pure-letter compact titles survive: the digit gate is what spares them.
		{"Gunbuster", ""},
		{"SteinsGate", ""},        // mixed-case no-digit: only the SIZED path flags this shape
		{"aBcDeFgHiJkLmNoP", ""},  // same, 16 chars — the old plugin-only rule wrongly junked it
		{"CardCaptorSakura", ""},  // ditto
		{"New York New York", ""}, // repeated WORDS are not repeated tokens
		{"One Piece 1085 [720p]", ""},
		{"Kaguya-sama.Love.is.War.S03.1080p.BluRay.x265-RARBG", ""},
		{"<<<Nimue>>>< file.mkv >< 01/99 >", ""}, // angle brackets are legitimate wrapping
	}
	for _, c := range cases {
		if got := whichJunkRule(c.title); got != c.want {
			t.Errorf("whichJunkRule(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestJunkParitySized(t *testing.T) {
	const (
		kb  = int64(1024)
		mib = int64(1048576)
	)
	cases := []struct {
		title string
		size  int64
		want  string
	}{
		// Sized-section shapes at their documented sizes.
		{"node_one_7bd4f69e", 3 * mib, "word_word_hex"},
		{"abcXYZ.!#soup-without-spaces", 700 * kb, "tiny_no_space"},
		{"wardcfsejnodpdci", 900 * kb, "short_lowercase_token"},
		{"aB3.cD4.eF5.gH6.iJ7=kL8.mN9*oP1.qR2?sT3.uV4|wX5.yZ6~aB7.cD8+eF9", 800 * kb, "long_no_space"},
		{")yury0WgtSBL&LmHFHgI1IFskNRLzPAv k2cwcKzuD!2PlOS7HR-p mB#nbz-B7^", 723 * kb, "chaotic_specials_small"},
		{"XIfhyEYhXpZaXTVK", 433 * mib, "short_random_token"}, // size-agnostic since May 2026
		{"SteinsGate", 700 * mib, "short_random_token"},       // prod's tradeoff, kept faithfully

		// The catchalls: anime-only policy, tiny NZBs are junk whatever the title.
		{"A Real Looking Title - 01", 500 * kb, "under_1mib"},
		{"A Real Looking Title - 01", 2 * mib, "under_5mib"},

		// And their boundaries: at 5 MiB+ a clean title survives everything.
		{"A Real Looking Title - 01", 5 * mib, ""},
		{"[SubsPlease] Frieren - 12 (1080p) [ABCD1234].mkv", 700 * mib, ""},

		// Size unknown: every sized rule stays silent (the ingest path).
		{"node_one_7bd4f69e", 0, ""},
		{"wardcfsejnodpdci", 0, ""},
		{"A Real Looking Title - 01", 0, ""},
	}
	for _, c := range cases {
		if got := whichJunkRuleSized(c.title, c.size); got != c.want {
			t.Errorf("whichJunkRuleSized(%q, %d) = %q, want %q", c.title, c.size, got, c.want)
		}
	}
}

// TestJunkAttributionOrder pins first-match attribution on titles several rules
// could claim — the hit counters are only comparable to prod's if the same rule
// gets the credit.
func TestJunkAttributionOrder(t *testing.T) {
	cases := []struct {
		title string
		size  int64
		want  string
	}{
		// long_alnum_run outranks single_token_20 on a 24+ run.
		{"Pzz8CzBPoBNsCu8oRPpDYwESRkpq5UU3jGlz", 0, "long_alnum_run"},
		// dot_sep_obfuscated is checked before alnum_blob_ext.
		{"f329yZ98AaYf2qHd.QPv2", 0, "dot_sep_obfuscated"},
		// A specific sized rule wins attribution over the catchall band it sits in.
		{"wardcfsejnodpdci", 900 * 1024, "short_lowercase_token"},
		// A UUID embedded in warez boilerplate: uuid runs first? No — prod checks
		// software_warez AFTER uuid, so uuid gets credit.
		{"550e8400-e29b-41d4-a716-446655440000 Microsoft Office", 0, "uuid"},
	}
	for _, c := range cases {
		if got := whichJunkRuleSized(c.title, c.size); got != c.want {
			t.Errorf("attribution(%q, %d) = %q, want %q", c.title, c.size, got, c.want)
		}
	}
}
