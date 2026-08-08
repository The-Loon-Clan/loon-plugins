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
		{"elys-1907283e54a1cae9 - [KXPSbTzl] qoS5w5nwO0OvHnwj", halfMB, "under_1mib"},
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

// The exemption must not leak onto the unsized ingest path: there the size is
// unknown, the catchalls never run at all, and an .epub-named obfuscated post
// must still be judged by the title rules like anything else.
func TestSmallMediaExemptionIsSizedOnly(t *testing.T) {
	// A junk title that happens to carry a media extension is still junk by its
	// own shape, with or without a size.
	const obfuscated = "0N70ZyFoz8n50.epub"
	if rule := whichJunkRule(obfuscated); rule == "" {
		t.Errorf("whichJunkRule(%q) = \"\" — a junk-shaped title escaped because it names a media type", obfuscated)
	}
}
