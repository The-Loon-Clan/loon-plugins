package usenet

import "testing"

// token_size_tail — the size-annotation sibling of the bare_alnum_token bands.
//
// The family it exists for: obfuscated posts whose subjects carry the release
// size, so the derived base is "<random token> - 8,82 GB" — not bare, and
// therefore invisible to every token rule at every size. One staging window
// held 471,563 such articles across 692 bases, every one of which completed,
// assembled and INDEXED under a garbage title; the catalogue audit found the
// same shape already leaked in ("QYZNRMFJKEGLCHSUTBPA - 10,78 GB") with
// exactly zero real titles matching. Fixtures below are real bases from that
// measurement, not synthetic.
func TestTokenSizeTail_Catches(t *testing.T) {
	for _, title := range []string{
		"rP8nmcYiqE2eAjw7 - 49,37 GB",               // the largest single post: 39,875 articles
		"3hehrnk86mlv - 8,82 GB",                    // the two-counter family's poster
		"Cj74i3x3MQvdsK1 - 7.70 GB",                 // dot decimal, not comma
		"fwltl6Rms2x6TFE - 831.38 MB",               // MB tail
		"4oV1g1RSsz - 5.39 GB",                      // 10-char head
		"QYZNRMFJKEGLCHSUTBPA - 10,78 GB",           // the catalogue leak: 20+ head, no digit needed
		"Wzc1sGrt4kbNaVuGU0It.vol175+190 - 3,70 GB", // par2 volume splinter of the same post
		"Mxg3z1WiCLSNTMWsMn39.vol112+112 - 9,77 GB",
	} {
		if got := whichJunkRule(title); got != "token_size_tail" {
			t.Errorf("whichJunkRule(%q) = %q, want token_size_tail", title, got)
		}
	}
}

func TestTokenSizeTail_Spares(t *testing.T) {
	for _, title := range []string{
		// Pure-letter heads survive under 20 chars, exactly as the bands spare
		// them bare: the digit gate is the whole false-positive defence.
		"Lamune - 1,2 GB",
		"Gunbuster - 4,7 GB",
		// Any real-name punctuation in the head rejects it outright.
		"Mac.OSX.Snow.Leopard.v10.6.7-HOTiSO__www.realmom.info__ 6,84 GB",
		"Description - Adriana Trigiani - Der Himmel ueber Carrara (Ungekuerzt) - 760,92 MB",
		"<TOWN> www.town.ag > sponsored by www.ssl-news.info > flt-cry2 - 8,07 GB",
		// Heads under 6 chars are below every band floor.
		"Se7en - 2,1 GB",
		"BB520 - 10 GB",
		// Pure digits are bare_numeric_token's ground, not this rule's.
		"12345678 - 1,2 GB",
		// No size tail, no rule: the bands own the bare forms.
		"Release.Name.S01E05.1080p.WEB",
		// A number alone is not a size.
		"Episode 12 - 45",
		// The unit must follow a number with a space, or it is part of a name.
		"My2GB",
	} {
		if got := whichJunkRule(title); got == "token_size_tail" {
			t.Errorf("whichJunkRule(%q) = token_size_tail; must not fire", title)
		}
	}
}

// Two boundary facts worth pinning so nobody "fixes" them later:
//
// A word+digit head like "Terminator2" IS junked with a size tail — the same
// verdict short_alnum_token gives it bare ("Persona5Royal" and "Danganronpa2"
// are parity-pinned junk above). The rule inherits the bands' accepted
// trade-off rather than inventing a softer one.
//
// And a 24+ char token with a size tail is long_alnum_run's catch, not ours:
// the run rule sits first and owns every 24+ run wherever it appears, which
// keeps attribution consistent with prod.
func TestTokenSizeTail_Boundaries(t *testing.T) {
	if got := whichJunkRule("Terminator2 - 4,3 GB"); got != "token_size_tail" {
		t.Errorf("word+digit head = %q, want token_size_tail (the bands' own trade-off)", got)
	}
	if got := whichJunkRule("QTVxBgZmUbZnAJFWgJq6AB12 - 9,10 GB"); got != "long_alnum_run" {
		t.Errorf("24+ head = %q, want long_alnum_run to keep first-rule attribution", got)
	}
}
