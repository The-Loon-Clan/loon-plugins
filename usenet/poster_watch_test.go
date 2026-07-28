package usenet

import "testing"

// Matching is substring and case-insensitive because the operator knows the
// poster, not the exact From header. They asked about "tsukihime"; the wire
// carries "TsukiHime <usenet.bot@tsukihime.org>".
func TestPosterWatchMatching(t *testing.T) {
	w := newPosterWatch([]string{"tsukihime", "  ", "ANIMETOSHO", ""})

	for _, from := range []string{
		"TsukiHime <usenet.bot@tsukihime.org>",
		"usenet.bot@tsukihime.org",
		"<bot@animetosho.xyz>",
		"Anime Tosho <usenet.bot@animetosho.org>",
	} {
		if _, ok := w.watched(from); !ok {
			t.Errorf("%q not matched", from)
		}
	}
	for _, from := range []string{
		"someone.else@example.com",
		"",
		"aninzb@tosho-lives-on.local",
	} {
		if who, ok := w.watched(from); ok {
			t.Errorf("%q matched %q, want no match", from, who)
		}
	}

	// The KEY is the pattern, not the raw header. A poster's From varies —
	// display name added, address reformatted, a bot appending a counter — and
	// keying on the raw value scatters one poster's history across rows that
	// never add up.
	a, _ := w.watched("TsukiHime <usenet.bot@tsukihime.org>")
	b, _ := w.watched("usenet.bot@tsukihime.org (TsukiHime bot #4)")
	if a != b || a != "tsukihime" {
		t.Errorf("same poster keyed as %q and %q; want both %q", a, b, "tsukihime")
	}
}

// Empty watch is the default and sits on the per-article ingest path, so it
// must cost nothing and must never match.
func TestPosterWatchEmptyAndNil(t *testing.T) {
	if _, ok := newPosterWatch(nil).watched("anyone@example.com"); ok {
		t.Error("empty watch matched")
	}
	if _, ok := newPosterWatch([]string{"", "   "}).watched("anyone@example.com"); ok {
		t.Error("blank-only watch matched")
	}
	var nilWatch *posterWatch
	if _, ok := nilWatch.watched("anyone@example.com"); ok {
		t.Error("nil watch matched")
	}
}

func TestPosterHitsAccumulateAndDrain(t *testing.T) {
	h := newPosterHits()
	h.note("tsukihime", "ingest", "staged", "first subject")
	h.note("tsukihime", "ingest", "staged", "second subject")
	h.note("tsukihime", "build", "under_1mib", "some release")
	h.note("other", "build", "built", "another release")

	// A missing counter must never change behaviour, so these are all no-ops
	// rather than panics: an observability feature that can break filtering
	// would be worse than none.
	h.note("", "ingest", "staged", "no poster")
	h.note("tsukihime", "ingest", "", "no reason")
	var nilHits *posterHits
	nilHits.note("tsukihime", "ingest", "staged", "x")

	got := h.drain()
	if len(got) != 3 {
		t.Fatalf("drained %d keys, want 3", len(got))
	}
	k := posterHitKey{"tsukihime", "ingest", "staged"}
	if got[k].count != 2 {
		t.Errorf("count = %d, want 2", got[k].count)
	}
	// FIRST sample kept, so a page refreshed mid-pass is not chasing a moving
	// target — same rule as filterHits.
	if got[k].sample != "first subject" {
		t.Errorf("sample = %q, want the first one", got[k].sample)
	}

	// Drain resets, so an overlapping flush cannot double-count.
	if again := h.drain(); len(again) != 0 {
		t.Errorf("drain did not reset: %d keys remain", len(again))
	}
	if nilHits.drain() != nil {
		t.Error("nil drain should return nil")
	}
}
