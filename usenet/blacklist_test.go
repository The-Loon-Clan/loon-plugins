package usenet

import (
	"strings"
	"testing"
)

func withBlacklist(t *testing.T, rules ...blacklistRule) {
	t.Helper()
	prev := activeBlacklist.Load()
	t.Cleanup(func() { activeBlacklist.Store(prev) })
	m, errs := newBlacklistMatcher(rules)
	if len(errs) > 0 {
		t.Fatalf("compile: %v", errs)
	}
	activeBlacklist.Store(m)
}

// TestBlacklistMatchesPerField pins that a rule only ever tests the field it was
// written for. A poster rule that also matched titles would quietly drop
// releases whose NAME happens to contain the poster's handle.
func TestBlacklistMatchesPerField(t *testing.T) {
	withBlacklist(t, blacklistRule{Pattern: "spammer", Field: "poster", Enabled: true})

	if got := whichBlacklistRule(release{Poster: "spammer@example.com"}); got != "spammer" {
		t.Errorf("poster rule did not fire: %q", got)
	}
	if got := whichBlacklistRule(release{Title: "Great.Show.by.spammer"}); got != "" {
		t.Errorf("poster rule matched a title: %q", got)
	}
}

// TestBlacklistUnknownFieldFailsClosed: a typo in the field name must index
// everything, never drop everything.
func TestBlacklistUnknownFieldFailsClosed(t *testing.T) {
	withBlacklist(t, blacklistRule{Pattern: ".*", Field: "psoter", Enabled: true})
	if got := whichBlacklistRule(release{
		Subject: "x", Title: "x", Poster: "x", Group: "x",
	}); got != "" {
		t.Errorf("a rule with an unknown field matched (%q) — it would drop everything", got)
	}
	if validBlacklistField("psoter") {
		t.Error("validBlacklistField accepted a typo")
	}
}

// TestBlacklistDisabledRuleIgnored: toggling off is the operator's undo, and it
// has to take effect on reload.
func TestBlacklistDisabledRuleIgnored(t *testing.T) {
	withBlacklist(t, blacklistRule{Pattern: "drop-me", Field: "title", Enabled: false})
	if got := whichBlacklistRule(release{Title: "drop-me now"}); got != "" {
		t.Errorf("disabled rule still fired: %q", got)
	}
}

// TestBlacklistEmptyTargetNeverMatches: a rule like `^$` or `.*` against a field
// we have no value for (no poster on a staged set) must not drop the release.
func TestBlacklistEmptyTargetNeverMatches(t *testing.T) {
	withBlacklist(t, blacklistRule{Pattern: ".*", Field: "poster", Enabled: true})
	if got := whichBlacklistRule(release{Title: "Real.Release"}); got != "" {
		t.Errorf("matched an absent poster: %q", got)
	}
}

// TestBlacklistBadPatternDoesNotDisableTheRest: one uncompilable rule must not
// take the whole blacklist offline — that would silently start indexing
// everything the operator had excluded.
func TestBlacklistBadPatternDoesNotDisableTheRest(t *testing.T) {
	prev := activeBlacklist.Load()
	t.Cleanup(func() { activeBlacklist.Store(prev) })

	m, errs := newBlacklistMatcher([]blacklistRule{
		{Pattern: "([unclosed", Field: "title", Enabled: true},
		{Pattern: "keep-working", Field: "title", Enabled: true},
	})
	if len(errs) != 1 {
		t.Fatalf("got %d compile errors, want 1", len(errs))
	}
	activeBlacklist.Store(m)
	if got := whichBlacklistRule(release{Title: "keep-working please"}); got != "keep-working" {
		t.Errorf("the good rule stopped working after a bad one: %q", got)
	}
}

// TestBlacklistEmptyIsInert: the default install has no rules and must index
// everything.
func TestBlacklistEmptyIsInert(t *testing.T) {
	withBlacklist(t)
	if got := whichBlacklistRule(release{
		Subject: "anything", Title: "anything", Poster: "anyone", Group: "alt.binaries.x",
	}); got != "" {
		t.Errorf("empty blacklist matched: %q", got)
	}
}

// TestFilterHitsAccumulateAndDrain: counters must survive many notes and hand
// over cleanly, since a flush that double-counts makes the share column lie.
func TestFilterHitsAccumulateAndDrain(t *testing.T) {
	h := newFilterHits()
	for i := 0; i < 5; i++ {
		h.note("junk", "bare-token", "AbC123xyz")
	}
	h.note("blacklist", "spammer", "Some.Release-spammer")

	got := h.drain()
	if len(got) != 2 {
		t.Fatalf("got %d keys, want 2", len(got))
	}
	if v := got[filterHitKey{"junk", "bare-token"}]; v == nil || v.count != 5 {
		t.Errorf("junk count = %+v, want 5", v)
	}
	if v := got[filterHitKey{"junk", "bare-token"}]; v != nil && v.sample != "AbC123xyz" {
		t.Errorf("sample = %q, want the first one seen", v.sample)
	}
	if again := h.drain(); again != nil {
		t.Errorf("drain did not clear: %+v", again)
	}
}

// TestFilterHitsNilSafe: pure helpers take the sink optionally, so a nil must be
// a no-op rather than a panic on the ingest path.
func TestFilterHitsNilSafe(t *testing.T) {
	var h *filterHits
	h.note("junk", "rule", "sample") // must not panic
}

// TestTruncateSampleKeepsValidUTF8: obfuscated subjects are exactly the case
// these counters measure, and they can be kilobytes long.
func TestTruncateSampleKeepsValidUTF8(t *testing.T) {
	long := strings.Repeat("あ", 300) // 3 bytes per rune, so the cut lands mid-rune
	got := truncateSample(long)
	if len(got) > 210 {
		t.Errorf("sample not truncated: %d bytes", len(got))
	}
	for i, r := range got {
		if r == '�' {
			t.Fatalf("truncation split a rune at byte %d", i)
		}
	}
	if short := truncateSample("  fine  "); short != "fine" {
		t.Errorf("short sample = %q, want trimmed", short)
	}
}

// TestSortedHitKeysDeterministic: the flush takes row locks in this order, so an
// unstable one would let two workers deadlock against each other.
func TestSortedHitKeysDeterministic(t *testing.T) {
	m := map[filterHitKey]*filterHitVal{
		{"junk", "z"}: {}, {"blacklist", "a"}: {}, {"junk", "a"}: {}, {"blacklist", "z"}: {},
	}
	want := []filterHitKey{{"blacklist", "a"}, {"blacklist", "z"}, {"junk", "a"}, {"junk", "z"}}
	for i := 0; i < 3; i++ {
		got := sortedHitKeys(m)
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("run %d position %d: got %+v, want %+v", i, j, got[j], want[j])
			}
		}
	}
}
