package usenet

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Real bytes from prod's error log: a Latin-1 subject that claimed UTF-8.
// Postgres refuses the value, and because the counters flush as ONE batched
// statement, this single byte lost an entire pass's counts.
//
//	pq: invalid byte sequence for encoding "UTF8": 0xe1 0x6e 0x61
const badSubject = "Espa\xf1a - Cancio\xe1n - \xe1na.mkv"

// The 2026-08-04 bytes, from usenet/resolutions-flush (528 occurrences):
//
//	pq: invalid byte sequence for encoding "UTF8": 0xca 0x34
//
// 0xCA is a Big5/Shift_JIS lead byte that passthroughCharset hands back
// unconverted by design; 0x34 is the ASCII '4' that followed it, which is not a
// valid continuation byte. Shaped like a real release subject because the point
// is that the readable half survives.
const badBase = "[Group] Anime \xca4 Title - 01 [1080p][ABCD1234].mkv"

func TestPgSafeTextMakesStorableUTF8(t *testing.T) {
	got := pgSafeText(badSubject)
	if !utf8.ValidString(got) {
		t.Fatalf("still invalid UTF-8: %q", got)
	}
	// Readable text either side of the bad byte must survive — the sample is
	// there to be recognised by a human.
	for _, want := range []string{"Espa", " - Cancio", ".mkv"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q from the sample: %q", want, got)
		}
	}
}

// NUL is valid UTF-8 and still cannot go in a text column, so it is stripped
// rather than replaced.
func TestPgSafeTextStripsNUL(t *testing.T) {
	got := pgSafeText("before\x00after")
	if strings.ContainsRune(got, 0) {
		t.Errorf("NUL survived: %q", got)
	}
	if got != "beforeafter" {
		t.Errorf("got %q, want the two halves joined", got)
	}
}

// Valid input must pass through untouched, including multi-byte text — a
// sanitiser that mangles Japanese titles would be worse than the bug.
func TestPgSafeTextLeavesValidTextAlone(t *testing.T) {
	for _, s := range []string{"", "plain ascii", "葬送のフリーレン 第01話", "émoji 🎬 ok"} {
		if got := pgSafeText(s); got != s {
			t.Errorf("pgSafeText(%q) = %q, want it unchanged", s, got)
		}
	}
}

// The counters are the data; the samples are garnish. A bad byte in a sample
// must not be able to take the count with it.
func TestBadBytesDoNotPoisonTheCounters(t *testing.T) {
	f := newFilterHits()
	f.note("junk", "long_alnum_run", badSubject)
	f.note("junk", "long_alnum_run", "clean")
	// An instrument counter puts subject-derived text in the RULE column, so
	// the key needs sanitising too, not just the sample.
	f.noteN("ungrouped", "stem-"+badSubject, 42, badSubject)

	out := f.drain()
	if len(out) != 2 {
		t.Fatalf("got %d keys, want 2", len(out))
	}
	for k, v := range out {
		if !utf8.ValidString(k.rule) {
			t.Errorf("rule key is not storable: %q", k.rule)
		}
		if !utf8.ValidString(v.sample) {
			t.Errorf("sample is not storable: %q", v.sample)
		}
	}
	for k, v := range out {
		if k.kind == "ungrouped" && v.count != 42 {
			t.Errorf("ungrouped count = %d, want the accumulated 42", v.count)
		}
		if k.kind == "junk" && v.count != 2 {
			t.Errorf("junk count = %d, want 2 — a bad sample must not drop the count", v.count)
		}
	}
}

// Poster is a key column and comes straight off the From header.
func TestPosterHitsSanitiseKeyAndSample(t *testing.T) {
	p := newPosterHits()
	p.note("bad\xffposter@example.com", "ingest", "junk", badSubject)
	for k, v := range p.drain() {
		if !utf8.ValidString(k.poster) {
			t.Errorf("poster key is not storable: %q", k.poster)
		}
		if !utf8.ValidString(v.sample) {
			t.Errorf("poster sample is not storable: %q", v.sample)
		}
	}
}

// The bind is the guarantee. pgSafeText existed for three days and two new
// writers still bypassed it, so what is tested here is that binding through
// pgTextArray sanitises EVERY element without touching the valid ones — a
// version that mangled Japanese titles would be worse than the bug it fixes.
func TestPgTextArraySanitisesEveryElement(t *testing.T) {
	in := []string{badBase, "clean", "葬送のフリーレン", "with\x00nul", ""}
	got := pgTextArray(in)
	if len(got) != len(in) {
		t.Fatalf("length changed: %d, want %d", len(got), len(in))
	}
	for i, s := range got {
		if !utf8.ValidString(s) {
			t.Errorf("element %d is not storable: %q", i, s)
		}
		if strings.ContainsRune(s, 0) {
			t.Errorf("element %d kept a NUL: %q", i, s)
		}
	}
	if got[1] != "clean" || got[2] != "葬送のフリーレン" {
		t.Errorf("valid elements were altered: %q", got)
	}
	// The readable halves either side of the bad byte must survive, or the
	// row is storable but useless.
	for _, want := range []string{"[Group] Anime ", " Title - 01 [1080p][ABCD1234].mkv"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("lost %q from the subject: %q", want, got[0])
		}
	}
}

// build_outcomes.last_sample has no length cap in the schema, and the samples
// it stores are longest in exactly the obfuscated-junk case the counter exists
// to measure.
func TestBuildOutcomeSampleIsBoundedAndStorable(t *testing.T) {
	got := truncateSample(strings.Repeat("あ", 300) + badBase)
	if !utf8.ValidString(got) {
		t.Errorf("sample is not storable: %q", got)
	}
	if len(got) > 210 {
		t.Errorf("sample = %d bytes, want it bounded near 200", len(got))
	}
}

// Truncation still lands on a rune boundary after sanitising.
func TestTruncateSampleStaysValidWhenLong(t *testing.T) {
	got := truncateSample(strings.Repeat("あ", 300) + badSubject)
	if !utf8.ValidString(got) {
		t.Errorf("truncated sample is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long sample was not truncated: %q", got[len(got)-20:])
	}
}
