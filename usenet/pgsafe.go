package usenet

import (
	"strings"

	"github.com/lib/pq"
)

// Making poster-controlled bytes storable in a Postgres text column.
//
// This lives in its own file rather than beside its first caller because the
// first version did not, and that cost 528 lost flushes. pgSafeText was added
// on 2026-08-01 (buried in blacklist.go) and wired into three of the six
// writers that need it; set_resolutions and build_outcomes were written after
// it existed, have the identical accumulate/drain/batch-write shape, and still
// bypassed it. A helper nobody can find is a helper nobody uses.

// pgTextArray binds a []string to a text[] parameter, sanitising every element.
//
// Use this in place of pq.Array for ANY []string bound to a text column. The
// guarantee belongs to the BIND rather than to whoever remembered, because
// "remember to sanitise" has now demonstrably failed twice. Valid input is free:
// strings.ToValidUTF8 returns the input string unchanged, with no allocation,
// when it is already valid.
func pgTextArray(ss []string) pq.StringArray {
	out := make(pq.StringArray, len(ss))
	for i, s := range ss {
		out[i] = pgSafeText(s)
	}
	return out
}

// truncateSample bounds what goes in the sample column. Subjects can be
// kilobytes of base64 in exactly the obfuscated-junk case these counters exist
// to measure, and the page only ever shows a line of it.
func truncateSample(s string) string {
	const max = 200
	s = strings.TrimSpace(pgSafeText(s))
	if len(s) <= max {
		return s
	}
	// Trim to a rune boundary so the truncation does not split a rune.
	cut := max
	for cut > 0 && !isUTF8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// pgSafeText makes a string storable in a Postgres text column.
//
// Usenet subjects are arbitrary bytes. RFC 2047 decoding produces valid UTF-8
// only when the header declared its charset honestly, and plenty do not — a
// Latin-1 body labelled UTF-8, or raw 8-bit with no encoded-word at all. On top
// of that, decodeSubject deliberately passes CJK encoded-words through
// unconverted (see passthroughCharset), so a Shift_JIS subject arrives here as
// invalid UTF-8 BY DESIGN. Postgres rejects such a value outright, and because
// these counters flush as ONE batched statement, a single bad byte in a single
// sample loses the whole pass:
//
//	pq: invalid byte sequence for encoding "UTF8": 0xe1 0x6e 0x61
//	pq: invalid byte sequence for encoding "UTF8": 0xca 0x34
//
// Observed in production on 2026-08-01 (filter_hits, subject_corpus) and again
// on 2026-08-04 (set_resolutions, build_outcomes). The samples are diagnostic
// garnish; the counts are the data, and they were being discarded to preserve
// bytes nobody can read anyway.
//
// Replacement rather than removal: these are anime release subjects, and
// "Espa\xf1a" -> "Espa<?>a" stays recognisable and greppable where "Espaa"
// reads as the poster's own typo and destroys the evidence a byte was ever bad.
//
// NUL is stripped rather than replaced: it is valid UTF-8 and still cannot go
// in a text column, and it never carries meaning in a subject line.
func pgSafeText(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "�")
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }
