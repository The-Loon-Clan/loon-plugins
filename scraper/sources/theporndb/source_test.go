package theporndb

import (
	"testing"
)

// TestJavCodeRoutesToTheRightEndpoint. The regex decides which of two APIs the
// query goes to, and there is no fallback — a title routed to /jav that is not
// a product code simply finds nothing, and the member sees "no match" for a
// release that is in the other index.
func TestJavCodeRoutesToTheRightEndpoint(t *testing.T) {
	for _, in := range []string{
		"RKI-395", "rki-395", "SSIS-001", "ABP-12", "abcdef-123456",
		"RKI395", // the separator is optional; scene releases drop it
	} {
		if !javCode.MatchString(in) {
			t.Errorf("javCode did not match %q — it would be searched as a title", in)
		}
	}
	for _, tc := range []struct{ in, why string }{
		{"", "empty"},
		{"A-123", "one letter is a false positive waiting to happen"},
		{"abcdefg-123", "seven letters is past any real code"},
		{"RKI-1", "one digit"},
		{"RKI-1234567", "seven digits"},
		{"Some Scene Title", "an ordinary title"},
		{"Blacked - Something", "a studio name with a dash"},
		{"RKI-395 extended", "trailing words; the anchors must hold"},
		{"the RKI-395", "leading words, likewise"},
		{"12-345", "digits where the letters go"},
	} {
		if javCode.MatchString(tc.in) {
			t.Errorf("javCode matched %q — %s", tc.in, tc.why)
		}
	}
}

func TestYearOf(t *testing.T) {
	for in, want := range map[string]int{
		"2019-04-11": 2019,
		"2019":       2019,
		"1998-01-01": 1998,
		"":           0,
		"201":        0,    // too short to hold a year
		"n/a":        0,    // not a number
		"unknown":    0,    // ditto, and long enough to try
		"0000-01-01": 0,    // parses, but a zero year is not a year
		"20190411":   2019, // no separators
	} {
		if got := yearOf(in); got != want {
			t.Errorf("yearOf(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "third"); got != "third" {
		t.Errorf("got %q, want the first non-empty", got)
	}
	if got := firstNonEmpty("first", "second"); got != "first" {
		t.Errorf("got %q, want the first", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("got %q from nothing at all", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q, want empty when nothing qualifies", got)
	}
}

// ── the mapping ─────────────────────────────────────────────────────

func scene() tpdbScene {
	s := tpdbScene{
		ID:          "abc-123",
		Title:       "A Scene",
		Date:        "2019-04-11",
		Description: "what happens in it",
		Image:       "https://example.com/image.jpg",
	}
	s.Site.Name = "A Studio"
	s.Posters.Large = "https://example.com/poster.jpg"
	s.Background.Large = "https://example.com/background.jpg"
	return s
}

// TestToEntryPrefersThePoster for an ordinary scene, and the DMM wrap for JAV —
// the one branch in this mapping that is a judgement rather than a copy.
func TestToEntryCoverPreference(t *testing.T) {
	if got := toEntry(scene(), false).CoverURL; got != "https://example.com/poster.jpg" {
		t.Errorf("scene cover = %q, want the poster", got)
	}
	if got := toEntry(scene(), true).CoverURL; got != "https://example.com/image.jpg" {
		t.Errorf("jav cover = %q, want the DMM front+back wrap", got)
	}

	// With no poster, anything is better than a blank tile.
	s := scene()
	s.Posters.Large = ""
	if got := toEntry(s, false).CoverURL; got != "https://example.com/image.jpg" {
		t.Errorf("fallback cover = %q, want the image", got)
	}
	s.Image = ""
	if got := toEntry(s, false).CoverURL; got != "https://example.com/background.jpg" {
		t.Errorf("second fallback = %q, want the background", got)
	}
	s.Background.Large = ""
	if got := toEntry(s, false).CoverURL; got != "" {
		t.Errorf("with nothing to show, cover = %q, want empty", got)
	}
	// A JAV scene with no image falls back the same way rather than to nothing.
	s = scene()
	s.Image = ""
	if got := toEntry(s, true).CoverURL; got != "https://example.com/poster.jpg" {
		t.Errorf("jav with no image = %q, want the poster", got)
	}
}

func TestToEntryCarriesTheIdentifiers(t *testing.T) {
	e := toEntry(scene(), false)
	if e.Ref.Kind != "xxx" {
		t.Errorf("Ref.Kind = %q, want xxx", e.Ref.Kind)
	}
	if len(e.External) != 1 || e.External[0].Namespace != "tpdb" || e.External[0].Value != "abc-123" {
		t.Errorf("External = %v, want one tpdb id — this is what a re-scrape matches on", e.External)
	}
	if e.Title != "A Scene" || e.Year != 2019 {
		t.Errorf("Title/Year = %q/%d", e.Title, e.Year)
	}
	if e.Fields["studio"] != "A Studio" || e.Fields["date"] != "2019-04-11" {
		t.Errorf("Fields = %v", e.Fields)
	}
}

// TestToEntryDropsNamelessTagsAndPerformers. The API returns entries with an id
// and no name; a blank chip on a page is worse than one fewer chip.
func TestToEntryDropsNamelessTagsAndPerformers(t *testing.T) {
	s := scene()
	s.Tags = []struct {
		Name string `json:"name"`
	}{{Name: "one"}, {Name: ""}, {Name: "two"}}
	s.Performers = []struct {
		Name string `json:"name"`
	}{{Name: ""}, {Name: "somebody"}}

	e := toEntry(s, false)
	if len(e.Genres) != 2 || e.Genres[0] != "one" || e.Genres[1] != "two" {
		t.Errorf("Genres = %v, want the two named tags", e.Genres)
	}
	p, _ := e.Fields["performers"].([]string)
	if len(p) != 1 || p[0] != "somebody" {
		t.Errorf("performers = %v, want the one named performer", p)
	}
}

// TestToEntryNeverLeavesANilSlice. Genres and performers are ranged over by
// templates; an empty slice and a nil slice render the same, but the JSON these
// are stored as does not — null and [] are different to read back.
func TestToEntryNeverLeavesANilSlice(t *testing.T) {
	s := scene()
	s.Tags = nil
	s.Performers = nil
	e := toEntry(s, false)
	if e.Genres == nil {
		t.Error("Genres is nil, want an empty slice")
	}
	if p, _ := e.Fields["performers"].([]string); p == nil {
		t.Error("performers is nil, want an empty slice")
	}
}

// TestToEntryWithNothingInIt — an unusable response must map to an empty entry
// rather than panicking on a missing sub-object.
func TestToEntryWithNothingInIt(t *testing.T) {
	e := toEntry(tpdbScene{}, false)
	if e.Title != "" || e.Year != 0 || e.CoverURL != "" {
		t.Errorf("an empty scene produced %+v", e)
	}
}
