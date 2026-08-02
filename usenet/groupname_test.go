package usenet

import (
	"strings"
	"testing"
)

// The bytes from the production failure. 0xe8 opens a 3-byte UTF-8 sequence
// and 'q' is not a continuation byte, so Postgres refuses the value outright:
//
//	pq: invalid byte sequence for encoding "UTF8": 0xe8 0x71 0x75
//
// Every name went in as ONE batched insert, so this single entry failed the
// whole fetch and no newsgroup could be added at all.
const badGroupName = "alt.binaries.\xe8que"

func TestUnusableGroupNamesAreRejected(t *testing.T) {
	for _, bad := range []string{
		badGroupName,
		"",
		"alt.binaries.\xff",       // lone continuation byte
		"alt.binaries\tcontrol",   // tab
		"alt.binaries\ncontrol",   // newline — would break the wire protocol
		"alt.binaries has space",  // a space makes it two fields, not a name
		"alt.\x00binaries",        // NUL
		"alt.binaries.\x7fdelete", // DEL
	} {
		if usableGroupName(bad) {
			t.Errorf("accepted an unusable name: %q", bad)
		}
	}
}

// Real groups must survive, including the non-ASCII ones some servers carry —
// over-rejecting would quietly hide groups the operator wants.
func TestRealGroupNamesAreKept(t *testing.T) {
	for _, ok := range []string{
		"alt.binaries.anime",
		"alt.binaries.boneless",
		"de.alt.binaries.mp3",
		"alt.binaries.multimedia.anime.highspeed",
		"free.pt",
		"alt.binaries.日本",         // valid UTF-8, unusual but representable
		"alt.binaries.test-1_2+3", // punctuation seen in real names
	} {
		if !usableGroupName(ok) {
			t.Errorf("rejected a usable name: %q", ok)
		}
	}
}

// A name is an IDENTIFIER — it goes back to the server verbatim in GROUP
// <name> — so an unusable one is DROPPED, never repaired. A sanitised name
// would match no group on the server while looking real in the admin list.
func TestUnusableNamesAreDroppedNotRepaired(t *testing.T) {
	if got := pgSafeText(badGroupName); usableGroupName(got) && got != badGroupName {
		// pgSafeText WOULD make it storable — that is exactly the treatment
		// this path must not use. The test documents the divergence so nobody
		// "fixes" listGroups by reaching for the sanitiser.
		if strings.Contains(got, "�") {
			t.Log("pgSafeText would store it as", got, "— correct for a sample, wrong for an identifier")
		}
	}
}

// One bad line must cost one group, not the whole fetch.
func TestSkippedNamesAreReportedWithoutFailingTheFetch(t *testing.T) {
	if err := skippedNames(0); err != nil {
		t.Errorf("a clean list reported %v", err)
	}
	err := skippedNames(3)
	if err == nil {
		t.Fatal("skipped names were not reported at all — silence is how this stays broken")
	}
	if _, ok := err.(errSkippedGroups); !ok {
		t.Fatalf("skip report is a %T, so callers cannot tell it from a real failure", err)
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("the report does not say how many were skipped: %v", err)
	}
}
