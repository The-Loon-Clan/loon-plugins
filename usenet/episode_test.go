package usenet

import "testing"

// The parser decides what "the same episode" and "the same show" mean, so the
// cases below are the feature's contract. Every title here is the shape of a
// real one from a live index.
func TestParseEpisode(t *testing.T) {
	for _, tc := range []struct {
		title  string
		key    string
		season int
		ep     int
		pack   bool
	}{
		// The dominant form, and its spacing variants.
		{"Silo S03E07 MULTI HDR 2160p WEB H265-HiggsBoson", "silo", 3, 7, false},
		{"The.Blacklist.S10E22.1080p.WEB.h264-ETHEL", "theblacklist", 10, 22, false},
		{"Cobra.Kai.s06e05.720p.WEBRip.x264", "cobrakai", 6, 5, false},
		{"Friends S1E1 720p BluRay", "friends", 1, 1, false},
		// Multi-episode: the FIRST number files it, because a reader looking
		// for E07 must find the E07-E08 double.
		{"Dexter.S08E11E12.1080p.BluRay.x265", "dexter", 8, 11, false},
		// Separators inside the marker.
		{"Suits.S05.E10.HDTV.x264", "suits", 5, 10, false},
		// Four-digit absolute numbering in SxxExx form (Sonarr's convention
		// for long-running shows). E(\d{1,3}) truncated this to episode 107 —
		// listed beside the real 107, absent from its own episode's search,
		// and reported as a gap the index already held.
		{"One.Piece.S01E1077.1080p.WEB.x264", "onepiece", 1, 1077, false},

		// Whole-season packs. Episode 0 and Pack, which is a different thing
		// from "episode zero" and has to stay tellable apart.
		{"The.Simpsons.S32.COMPLETE.1080p.WEB.x265", "thesimpsons", 32, 0, true},
		{"Pokemon Season 18 COMPLETE DVDRip", "pokemon", 18, 0, true},
		// An SxxE00 special files at episode 0, NON-pack — deliberately (only
		// S00E00 is refused as a marker). The series queries treat episode 0
		// as "not an episode number" on the strength of this vector.
		{"The.Show.S14E00.Special.1080p", "theshow", 14, 0, false},

		// Punctuation folds away, so one show is one key.
		{"Marvels.Agents.of.S.H.I.E.L.D..S07E13.1080p", "marvelsagentsofshield", 7, 13, false},
		{"Marvel's Agents of SHIELD S07E13 1080p", "marvelsagentsofshield", 7, 13, false},
	} {
		t.Run(tc.title, func(t *testing.T) {
			got := ParseEpisode(tc.title)
			if !got.Found() {
				t.Fatalf("not parsed at all")
			}
			if got.SeriesKey != tc.key {
				t.Errorf("key = %q, want %q", got.SeriesKey, tc.key)
			}
			if got.Season != tc.season || got.Episode != tc.ep {
				t.Errorf("S%dE%d, want S%dE%d", got.Season, got.Episode, tc.season, tc.ep)
			}
			if got.Pack != tc.pack {
				t.Errorf("pack = %v, want %v", got.Pack, tc.pack)
			}
		})
	}
}

// What must NOT parse. Each one would put a release on a page it does not
// belong to, which is worse than leaving it unfiled — an unparsed release
// browses exactly as it did before.
func TestParseEpisodeRefusals(t *testing.T) {
	for _, title := range []string{
		"Some.Movie.2024.1080p.BluRay.x264-GROUP", // a film
		"Ubuntu.24.04.LTS.amd64.iso",              // not video at all
		"S03E07.1080p.WEB",                        // no series name before the marker
		"Kali-Linux-2023.3-installer-amd64",       // digits that are not a season
		"Random.Album.Discography.320kbps",        // music
		"The.Show.S00E00.SAMPLE",                  // the marker, not a location
	} {
		t.Run(title, func(t *testing.T) {
			if got := ParseEpisode(title); got.Found() {
				t.Errorf("parsed as %+v — it should have been left unfiled", got)
			}
		})
	}
}

// A pack marker inside an episode title must not win: S03E07 contains S03, and
// reading the pack form first would file every episode in the index as a
// whole-season release.
func TestPackNeverStealsAnEpisode(t *testing.T) {
	got := ParseEpisode("Silo.S03E07.2160p.WEB.H265")
	if got.Pack {
		t.Error("an episode was filed as a season pack")
	}
	if got.Episode != 7 {
		t.Errorf("episode = %d, want 7", got.Episode)
	}
}

// The series name is what a reader sees, so it keeps its shape while the key
// does the folding.
func TestSeriesNameIsReadable(t *testing.T) {
	got := ParseEpisode("The.Blacklist.S10E22.1080p.WEB.h264-ETHEL")
	if got.Series != "The Blacklist" {
		t.Errorf("series = %q, want %q", got.Series, "The Blacklist")
	}
}

// The fansub convention, which is how anime is named and which the SxxExx
// forms above do not touch: a bracketed group, the series, a dash, and an
// ABSOLUTE episode number with no season anywhere.
//
// Added on evidence, not on a hunch: it was 4,887 of the 4,887 TV-category
// titles the parser left alone on a real 160k index — the whole of the miss
// rather than a corner of it. Coverage went 64.8% → 66.2%.
func TestParseEpisodeFansubForm(t *testing.T) {
	for _, tc := range []struct {
		title string
		key   string
		ep    int
	}{
		{"[SubsPlease] Chainsaw Man - 07 (1080p) [BBF1FDA4]", "chainsawman", 7},
		{"[SubsPlease] Chainsaw Man - 02v2 (1080p) [5E88C757]", "chainsawman", 2},
		{"[SubsPlease] One Piece - 1077 (1080p) [50AB7020]", "onepiece", 1077},
		{"[Erai-raws] Jujutsu Kaisen 2nd Season - 14 [1080p][Multiple Subtitle]", "jujutsukaisen2ndseason", 14},
		{"[sam] Vinland Saga - 22 [BD 1080p FLAC] [41D499F0]", "vinlandsaga", 22},
	} {
		t.Run(tc.title, func(t *testing.T) {
			got := ParseEpisode(tc.title)
			if !got.Found() {
				t.Fatal("not parsed")
			}
			if got.SeriesKey != tc.key {
				t.Errorf("key = %q, want %q", got.SeriesKey, tc.key)
			}
			if got.Episode != tc.ep {
				t.Errorf("episode = %d, want %d", got.Episode, tc.ep)
			}
			// Season 1 by convention for absolute numbering — a decision, not
			// a fact, and the one every indexer makes. One Piece 1077 is not
			// in season 1 by any broadcast reckoning, but a reader looking for
			// it wants it under the show.
			if got.Season != 1 {
				t.Errorf("season = %d, want 1 (the absolute-numbering convention)", got.Season)
			}
		})
	}
}
