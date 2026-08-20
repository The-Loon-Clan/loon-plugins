package cosmetics

import (
	"strings"
	"testing"
)

// cleanTitle is the most security-adjacent pure function in this plugin and had
// no test until now. What it guards is text somebody typed being published
// beside their name on every page they appear on — the highest-leverage
// user-supplied string on a site, per character.
//
// It is NOT moderation and these tests do not pretend it is. Staff read every
// title before it appears; what this removes is the tricks that are about the
// RENDERING rather than the words, and each of those is a case below.

func TestCleanTitleKeepsOrdinaryWords(t *testing.T) {
	for in, want := range map[string]string{
		"Keeper of the par2s":      "Keeper of the par2s",
		"  padded  ":               "padded",
		"Keeper   of   the  par2s": "Keeper of the par2s", // runs collapse
		"Ægir & Ránn — 日本語":        "Ægir & Ránn — 日本語",   // not an ASCII filter
		"<b>not html</b>":          "<b>not html</b>",     // escaping is the template's job
	} {
		got, ok := cleanTitle(in)
		if !ok {
			t.Errorf("cleanTitle(%q) refused a usable title", in)
			continue
		}
		if got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCleanTitleStripsBidiOverrides is the one that matters most.
//
// These characters do not stay inside their own element visually: they reorder
// whatever is drawn AROUND them, which on this site is somebody else's
// username. A title carrying one can make the line above it read backwards, or
// make its own author appear to be a different member.
func TestCleanTitleStripsBidiOverrides(t *testing.T) {
	// RLO, LRO, PDF, RLI, LRI, FSI, PDI, and the two directional marks.
	for _, r := range []rune{
		'‪', '‫', '‬', '‭', '‮',
		'⁦', '⁧', '⁨', '⁩',
		'‎', '‏',
	} {
		in := "safe" + string(r) + "words"
		got, ok := cleanTitle(in)
		if !ok {
			t.Errorf("U+%04X: refused outright; stripping is enough", r)
			continue
		}
		if strings.ContainsRune(got, r) {
			t.Errorf("U+%04X survived cleanTitle: %q", r, got)
		}
		if got != "safewords" {
			t.Errorf("U+%04X: got %q, want the surrounding words kept", r, got)
		}
	}
}

func TestCleanTitleStripsControlCharacters(t *testing.T) {
	got, ok := cleanTitle("a\x00b\x07c\x1bd")
	if !ok || got != "abcd" {
		t.Errorf("got %q/%v, want \"abcd\"", got, ok)
	}
}

// TestCleanTitleFoldsNewlinesRatherThanDropping. "a\nb" is two words: joining
// them into "ab" changes what was written, which a filter has no business
// doing. A single space is the honest fold.
func TestCleanTitleFoldsNewlinesRatherThanDropping(t *testing.T) {
	for _, in := range []string{"line one\nline two", "line one\r\nline two", "line one\tline two"} {
		got, ok := cleanTitle(in)
		if !ok || got != "line one line two" {
			t.Errorf("cleanTitle(%q) = %q/%v, want \"line one line two\"", in, got, ok)
		}
	}
}

// TestCleanTitleCapsCombiningMarks. Stacked marks paint OUTSIDE their own line
// box — enough of them and a title scribbles over the row above it, which is
// somebody else's content. Three is more than any real script stacks on one
// base character.
func TestCleanTitleCapsCombiningMarks(t *testing.T) {
	got, ok := cleanTitle("x" + strings.Repeat("̃", 40))
	if !ok {
		t.Fatal("refused outright; capping is enough")
	}
	if n := strings.Count(got, "̃"); n > 3 {
		t.Errorf("kept %d combining marks, want at most 3 (%q)", n, got)
	}
	// The cap is per RUN, not per title: a legitimately accented word must not
	// have its later accents eaten because an earlier one used the budget.
	got, ok = cleanTitle("é" + strings.Repeat("á", 10))
	if !ok || !strings.Contains(got, "á") {
		t.Errorf("a run of separately-accented letters lost its marks: %q", got)
	}
}

func TestCleanTitleRefusesWhatCannotBeShown(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"\n\n",
		"\x00\x01",                      // control characters only
		"‮",                             // an override and nothing else
		strings.Repeat("x", titleMax+1), // one rune over
		strings.Repeat("é", titleMax+1), // counted in RUNES, not bytes
	} {
		if got, ok := cleanTitle(in); ok {
			t.Errorf("cleanTitle(%q) accepted %q", in, got)
		}
	}
}

// TestCleanTitleLengthIsRunesNotBytes — a member writing in a non-Latin script
// must get the same number of CHARACTERS as anybody else, not a third as many.
func TestCleanTitleLengthIsRunesNotBytes(t *testing.T) {
	in := strings.Repeat("日", titleMax) // 3 bytes each, well over any byte cap
	got, ok := cleanTitle(in)
	if !ok {
		t.Fatalf("refused %d runes of a multi-byte script", titleMax)
	}
	if len([]rune(got)) != titleMax {
		t.Errorf("kept %d runes, want %d", len([]rune(got)), titleMax)
	}
}

// TestTitleUnlockCannotBeWornAsAnEffect. The right to a title is stored in the
// same table as the effects, under a prefixed pseudo-slug — so the thing that
// stops somebody equipping "grant:custom-title" as a name effect is that the
// catalogue does not contain it.
func TestTitleUnlockCannotBeWornAsAnEffect(t *testing.T) {
	if !strings.HasPrefix(titleUnlock, "grant:") {
		t.Errorf("titleUnlock = %q; the prefix is what keeps it out of the catalogue's namespace", titleUnlock)
	}
}

// TestKnownSlot refuses anything the contract does not name. This is the check
// standing between a forged post and an avatar frame worn on a username — the
// equip handler consults it before the catalogue, and the catalogue's own
// FitsSlot check after.
func TestKnownSlot(t *testing.T) {
	for _, good := range []string{"name", "title", "avatar", "profile"} {
		if !knownSlot(good) {
			t.Errorf("knownSlot(%q) refused a real slot", good)
		}
	}
	for _, bad := range []string{"", "Name", "name ", " name", "../name", "nam", "names"} {
		if knownSlot(bad) {
			t.Errorf("knownSlot(%q) accepted something the contract does not name", bad)
		}
	}
}
