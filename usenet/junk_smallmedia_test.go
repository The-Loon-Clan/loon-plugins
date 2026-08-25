package usenet

import "testing"

// The size catchalls used to mean "too small to be real". On a catalogue that
// collects books, music and manga they have to mean "too small to be real AND
// unnamed" — the band is full of legitimate content whose whole delivery is a
// few megabytes.
//
// This is not hypothetical tuning. under_5mib's last catch before the change
// was `Blackthorn - J.T. Geissinger.epub`, a real book deleted for being 2 MB,
// while under_1mib's was `elys-1907283e54a1cae9 - [KXPSbTzl] qoS5w5nwO0OvHnwj`.
// Both are pinned below, because the rule has to keep telling them apart.
func TestSizeCatchallSparesNamedMedia(t *testing.T) {
	const (
		twoMB  = 2 << 20
		halfMB = 512 << 10
	)
	spared := []struct {
		title string
		size  int64
	}{
		{"Blackthorn - J.T. Geissinger.epub", twoMB}, // the real deletion
		{"Some Author - A Novel.epub [retail]", twoMB},
		{"Artist - Album - 01 Track.flac", twoMB},
		{"Chapter 214.cbz", twoMB},
		{"Manga Volume 3.cbr", halfMB},
		{"Some.Show.S01E01.srt", halfMB},
		{"A Short Story.mobi", halfMB},
		{"Lecture Notes.pdf", twoMB},
	}
	for _, c := range spared {
		if rule := whichJunkRuleSized(c.title, c.size); rule != "" {
			t.Errorf("whichJunkRuleSized(%q, %d) = %q — real small-media content junked", c.title, c.size, rule)
		}
	}

	// Still junk: the band's actual residents. A nameless token is what the
	// catchall exists for, and an archive or video extension at this size is a
	// broken big release rather than a small whole one.
	junked := []struct {
		title string
		size  int64
		want  string
	}{
		// A nameless token pair, which is what the catchall exists for. This
		// used to be "elys-1907283e54a1cae9 - [KXPSbTzl] qoS5w5nwO0OvHnwj";
		// that title is now claimed by the more specific elys_campaign_tag, so
		// it moved to the case below and a shape-equivalent stand-in keeps
		// under_1mib itself under test.
		{"qoS5w5nwO0OvHnwj KXPSbTzl", halfMB, "under_1mib"},
		// The elys campaign, pinned at its new attribution. Same verdict as
		// before — junk — but credited to the rule that names the campaign
		// rather than to the size catchall that happened to sit under it. It
		// only ever reached under_1mib on the sized build path anyway; the
		// campaign rule catches it at ingest, where no size is known.
		{"elys-1907283e54a1cae9 - [KXPSbTzl] qoS5w5nwO0OvHnwj", halfMB, "elys_campaign_tag"},
		{"4194.13671", 2 << 20, "under_5mib"},
		{"1Mb.dat", 2 << 20, "under_5mib"},
		{"Wond37 suc suchanfragen 15 07 26", 2 << 20, "under_5mib"},
		// An archive or video this small is a fragment of something large.
		// tiny_no_space claims the dotted ones first — it sits above the
		// catchalls and its own gate (no whitespace, under ~790 KB) fits them.
		// Which rule fires matters less than that one does.
		{"Some.Real.Movie.2024.1080p.BluRay.x264-GRP.mkv", 2 << 20, "under_5mib"},
		{"Some.Real.Movie.2024.part01.rar", halfMB, "tiny_no_space"},
	}
	for _, c := range junked {
		if rule := whichJunkRuleSized(c.title, c.size); rule != c.want {
			t.Errorf("whichJunkRuleSized(%q, %d) = %q, want %q", c.title, c.size, rule, c.want)
		}
	}
}

// The extension has to look like one. A substring match alone would spare
// anything containing the letters, and the obfuscated titles this rule targets
// are exactly the kind of random string that contains them by accident.
func TestNamesSmallMediaWantsARealExtension(t *testing.T) {
	for _, yes := range []string{
		"book.epub", "book.epub extra", "Track.mp3 [V0]", "x.flac)",
		"Album.flac (2024)", "notes.pdf, revised",
		"BOOK.EPUB", // case-insensitive
	} {
		if !namesSmallMedia(yes) {
			t.Errorf("namesSmallMedia(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{
		"qoS5w5nwO0OvHnwj",
		".mobile phones",       // .mobi followed by an alnum
		"something.epubx",      // .epub followed by an alnum
		"nomp3here",            // no dot
		"Movie.2024.1080p.mkv", // not a small-media type
		"archive.rar",          // ditto
		// A DOT does not end a filename. Found by the prefilter's fuzz test:
		// this random-byte title was spared by an earlier version that
		// accepted any non-alphanumeric after the extension.
		"1\xa5e1-||=iy\x97\x9c-_o0'p\xe6}.pdf.1\xe6\xaa8Zp",
		"a.cbz-b", // a dash likewise: the token continues
		"",
	} {
		if namesSmallMedia(no) {
			t.Errorf("namesSmallMedia(%q) = true, want false", no)
		}
	}
}

// The exemption is per-rule, and a junk-SHAPED title naming a media type is
// still caught by its shape rules: spare_named_media excuses a rule's OWN
// verdict, never the whole engine's. (This test used to claim the exemption
// was sized-only; long_digit_run — a title rule that runs at ingest — now
// opts in too, so the honest claim is the narrower one this asserts.)
func TestSmallMediaExemptionDoesNotExcuseJunkShapes(t *testing.T) {
	// A junk title that happens to carry a media extension is still junk by its
	// own shape, with or without a size.
	const obfuscated = "0N70ZyFoz8n50.epub"
	if rule := whichJunkRule(obfuscated); rule == "" {
		t.Errorf("whichJunkRule(%q) = \"\" — a junk-shaped title escaped because it names a media type", obfuscated)
	}
}

// long_digit_run's exemption: an ISBN is 10/13 digits and ebook posters print
// it in the title — a real 2 MB book was dropped at ingest and its catalogued
// copy deleted for carrying its own catalogue number. A music barcode
// (EAN-13) is the same shape. A digit-run title naming no medium is still a
// timestamp.
func TestLongDigitRunSparesNamedMedia(t *testing.T) {
	spared := []string{
		"Peter Watts - Blindsight (9780765319647).epub", // ISBN-13
		"Author - Title (0765319640).epub",              // ISBN-10
		"Artist - Album (5099749534728).flac",           // EAN-13 barcode
	}
	for _, title := range spared {
		if rule := whichJunkRule(title); rule != "" {
			t.Errorf("whichJunkRule(%q) = %q — dropped at ingest", title, rule)
		}
		if rule := whichJunkRuleSized(title, 2<<20); rule != "" {
			t.Errorf("whichJunkRuleSized(%q, 2MiB) = %q — the sweep would delete the catalogued copy", title, rule)
		}
	}
	// The catch survives the exemption.
	if rule := whichJunkRule("1723456789012345 aBc"); rule != "long_digit_run" {
		t.Errorf("nameless digit run attributed to %q, want long_digit_run", rule)
	}
	// Known residual, pinned deliberately: without an extension the engine
	// cannot tell an ISBN from a timestamp (RE2 has no lookaround), so an
	// extensionless ebook title still dies. If these show up in the drops
	// view, the follow-up is a 978/979-prefixed heuristic, not a wider spare.
	if rule := whichJunkRule("Peter Watts - Blindsight (9780765319647)"); rule != "long_digit_run" {
		t.Errorf("extensionless ISBN title attributed to %q — if this changed on purpose, update the residual note", rule)
	}
}
