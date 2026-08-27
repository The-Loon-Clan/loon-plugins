package usenet

import (
	"strings"
	"testing"
)

// The mismatched-bracket counter, reported from the demo index 26 Aug 2026.
//
// Some posters close a counter with the wrong bracket, consistently across a
// whole post — "(06/65]" and "[06/65)". That is house style, not corruption in
// transit, and neither form matched reFileOf or rePartOf.
//
// The miss cost twice, because those two regexes both READ the counter and
// STRIP it from the base. So no counter was parsed AND the marker stayed in
// the base subject, which means every article derived a base containing its
// own counter and a 65-part post became 65 one-part "releases". Measured on
// production: 123 rows that are really 36 posts for "(n/m]", 27 rows that are
// really 7 for "[n/m)".
//
// This is the rar-volume split's opposite number in tractability, and the
// contrast is worth keeping. That one cannot be fixed by making the base
// collapse, because there is no file counter to separate the volumes once
// they share a key — see TestDroppingTheBoundaryAloneBuildsAMosaic. Here the
// well-formed spelling ALREADY produces the right answer end to end, so
// accepting the typo only routes it onto a proven path.
//
// Zero live staged articles carried either shape when this landed, so no
// in-flight set was re-keyed. That is provable rather than sampled: the change
// is strictly additive — only a subject containing a mismatched pair newly
// matches — so a corpus containing none of them cannot have a base change.

// The opener decides which counter it is. That asymmetry is the rule: the
// opening bracket is typed deliberately, the closing one is where a shifted
// keystroke lands, so following the opener follows the intent rather than the
// mistake.
func TestOpenerDecidesTheCounterKind(t *testing.T) {
	// "(" opens a SEGMENT counter even when "]" closes it.
	_, pn, tp, _, fn, tf, fp := parseSubject(`Mayday Live Sets 2012 by mell(06/65] - Mayday Live-Sets`)
	if pn != 6 || tp != 65 {
		t.Errorf("segment = %d/%d, want 6/65", pn, tp)
	}
	if fp || fn != 0 || tf != 0 {
		t.Errorf("file = %d/%d fp=%v, want none — a lone counter is a segment counter", fn, tf, fp)
	}

	// "[" opens a FILE counter even when ")" closes it.
	_, pn, tp, _, fn, tf, fp = parseSubject(`Some Release [03/12) - "file.rar" yEnc (7/40)`)
	if fn != 3 || tf != 12 || !fp {
		t.Errorf("file = %d/%d fp=%v, want 3/12 fp=true", fn, tf, fp)
	}
	if pn != 7 || tp != 40 {
		t.Errorf("segment = %d/%d, want 7/40", pn, tp)
	}
}

// The consequence that matters: the marker leaves the base, so every article
// of the post groups, AND each keeps its own field key so they do not
// overwrite one another. Both halves are required — the rar split has the
// first without the second and builds a mosaic.
func TestMismatchedCounterGroupsWithoutColliding(t *testing.T) {
	a, pnA, _, _, fnA, _, _ := parseSubject(`Mayday Live Sets 2012 by mell(06/65] - Mayday Live-Sets`)
	b, pnB, _, _, fnB, _, _ := parseSubject(`Mayday Live Sets 2012 by mell(07/65] - Mayday Live-Sets`)

	if a != b {
		t.Fatalf("segments derived different bases:\n  %q\n  %q", a, b)
	}
	if a == "" {
		t.Fatal("base is empty")
	}
	// The counter itself must be gone; leaving it in is the whole bug.
	for _, leaked := range []string{"06/65", "07/65", "]"} {
		if strings.Contains(a, leaked) {
			t.Errorf("base %q still carries %q", a, leaked)
		}
	}
	if formatFieldKey(fnA, pnA) == formatFieldKey(fnB, pnB) {
		t.Fatalf("segments 6 and 7 share field key %q — they would overwrite each other",
			formatFieldKey(fnA, pnA))
	}
}

// The well-formed spellings and the two-counter form are untouched. Widening a
// regex that both reads and strips is exactly the change that quietly re-keys
// a live index, so the unchanged cases are pinned beside the fixed ones.
func TestWellFormedCountersUnchanged(t *testing.T) {
	cases := []struct {
		subject        string
		wantBase       string
		wantPN, wantTP int
		wantFN, wantTF int
		wantFP         bool
	}{
		{`Some Release [03/12] - "file.rar" yEnc (7/40)`, "Some Release", 7, 40, 3, 12, true},
		{`"BB520.part001.rar" - (001/225) - yEnc (100/391)`, "BB520", 100, 391, 1, 225, true},
		{`"Ratatouille.2007.1080p.DVDR-COX.part001.rar" yEnc (81/137)`,
			"Ratatouille.2007.1080p.DVDR-COX", 81, 137, 0, 0, false},
	}
	for _, tc := range cases {
		base, pn, tp, _, fn, tf, fp := parseSubject(tc.subject)
		if base != tc.wantBase {
			t.Errorf("parseSubject(%q) base = %q, want %q", tc.subject, base, tc.wantBase)
		}
		if pn != tc.wantPN || tp != tc.wantTP {
			t.Errorf("parseSubject(%q) segment = %d/%d, want %d/%d", tc.subject, pn, tp, tc.wantPN, tc.wantTP)
		}
		if fn != tc.wantFN || tf != tc.wantTF || fp != tc.wantFP {
			t.Errorf("parseSubject(%q) file = %d/%d fp=%v, want %d/%d fp=%v",
				tc.subject, fn, tf, fp, tc.wantFN, tc.wantTF, tc.wantFP)
		}
	}
}
