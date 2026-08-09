package catalog

import "testing"

func TestCategorize(t *testing.T) {
	cases := map[string]int{
		"Rich Dad Poor Dad Ebook pdf":        7020, // Books/Ebook
		"[SubsPlease] Frieren - 12 (1080p)":  5070, // TV/Anime
		"Some.Movie.2024.1080p.BluRay.x264":  2050, // Movies/BluRay
		"Show.S01E05.1080p.WEB":              5040, // TV/HD (episode pattern)
		"Ashampoo Burning Studio Crack 2024": 4010, // PC/0day
		"Great Album - Artist 2023 FLAC":     3040, // Audio/Lossless
		"Some Random Doc 1080p":              2040, // Movies/HD (resolution fallback)
		"Strategic Trading Planner":          8010, // Other/Misc
	}
	for title, want := range cases {
		if got := categorize("", title); got != want {
			t.Errorf("categorize(%q) = %d (%s), want %d (%s)", title, got, categoryName(got), want, categoryName(want))
		}
	}

	// group signal: an anime group categorizes as TV/Anime even for a plain title.
	if got := categorize("a.b.multimedia.anime", "Episode 5"); got != 5070 {
		t.Errorf("anime group = %d, want 5070", got)
	}
}

// FLAC, MP3 and 320kbps describe a video's SOUNDTRACK as readily as they name
// an album, and the video is the release.
//
// 806 of the 810 titles in Audio on this index were video. It was not a
// rounding error in a small category — it was the whole category, and it made
// the index look like it held music worth fetching metadata for.
func TestAudioCodecsDoNotClaimVideo(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  int
	}{
		// Real cases from the Audio category, with what they actually are.
		{"Naruto.204.v4.480p.DVD.Dual-Audio.FLAC2.0.Hi10P.x264-JySzE", 2030},
		{"[Fabre-RAW] Detective Conan - 0045 [BDRip 1080p][FLAC][v2]", 2040},
		{"House.Mates.2025.Tamil 1080p ZEE5 WEB-DL x264 [AAC.2.0 - 320kbps]", 2040},
		{"The.Big.Sleep.1946.1080p.BluRay.x264.FLAC2.0-Pahe.in", 2050},
		{"The Royal Hunt (1976) {imdb-tt0074926} [480p][MP3][2.0][x264]", 2030},
		// A fansub checksum still wins for anime.
		{"[NH] The Rising of the Shield Hero - 25 (BD 1080p x264 10-bit FLAC) [C9C1EE03]", 5070},
	} {
		if got := categorize("alt.binaries.sounds.flac", tc.title); got != tc.want {
			t.Errorf("categorize(%q) = %d (%s), want %d (%s)",
				tc.title, got, categoryName(got), tc.want, categoryName(tc.want))
		}
	}

	// Music with no video marker anywhere is still music. This is the case the
	// rule must NOT break, and the reason it keys off video markers rather
	// than dropping the audio keywords.
	for _, tc := range []struct {
		title string
		want  int
	}{
		{"(Progressive Rock) Last Knight - Talking to the Moon - 2024, FLAC", 3040},
		{"Great Album - Artist 2023 FLAC", 3040},
		{"Some Artist - Some Album 2024 MP3 320kbps", 3010},
		{"Stephen Fry - The Hitchhikers Guide audiobook", 3030},
	} {
		if got := categorize("alt.binaries.sounds.mp3", tc.title); got != tc.want {
			t.Errorf("categorize(%q) = %d (%s), want %d (%s) — real music must stay in Audio",
				tc.title, got, categoryName(got), tc.want, categoryName(tc.want))
		}
	}
}

// The same mistake as the audio buckets, in the categories nobody looks at:
// a keyword that names a non-video content type sitting above the video rules
// and claiming a film on its way past.
//
// 41 of the 43 releases in PC/Games were video, and 63 of the 67 in Comics.
func TestNonVideoBucketsDoNotClaimVideo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
		want  int
	}{
		// REPACK is a universal scene tag — a re-released package of ANYTHING.
		// Only the game-specific groups still mean a game.
		{"repack is not a game", "Bad Boys - Ride or Die (2024) REPACK 1080p DS4K WEBRip x265 HEVC", 2040},
		{"repack on a bluray remux", "Casablanca.1942.1080p.REPACK.BluRay.REMUX.AVC.1.0-SIUUU", 2050},
		// The original case here was "Tom.and.Jerry.1947.E27.Cat.Fishin...",
		// which the bare-episode rule now reads as TELEVISION — correctly, it
		// is a numbered cartoon short. It still proves the point it was added
		// for (repack does not mean PC/Games), just under a different shelf, so
		// it stays as its own case with the answer it actually deserves.
		{"a numbered cartoon short is television", "Tom.and.Jerry.1947.E27.Cat.Fishin.1080p.REPACK.BluRay.REMUX.AVC.1.0-SIUUU", 5040},
		// "Switch" and "Family Switch" are films; the token is also a console.
		{"a film called Switch", "Family.Switch.2023.1080p.WEB-DL.x264.6CH-Pahe.in", 2040},
		// A film whose title contains a comics word.
		{"comics word in a film", "Kanimangalam.Kovilakam.2026.1080p.SNXT.WEB-DL.Comic", 2040},
		// "Portable" is an ISO keyword and an ordinary English word.
		{"a film called Portable", "The.Portable.Door.2023.1080p.WEB-DL.x264", 2040},
	} {
		if got := categorize("alt.binaries.misc", tc.title); got != tc.want {
			t.Errorf("%s: categorize(%q) = %d (%s), want %d (%s)",
				tc.name, tc.title, got, categoryName(got), tc.want, categoryName(tc.want))
		}
	}

	// The real things must still classify. None of these carry a resolution or
	// a video codec, which is exactly why keying off video markers works.
	for _, tc := range []struct {
		title string
		want  int
	}{
		{"FitGirl Repack - Some Game v1.2", 4050},
		{"Some.Game-CODEX", 4050},
		{"Zelda Tears of the Kingdom NSW", 1000},
		{"Adobe Photoshop 2024 Crack", 4010},
		{"Ubuntu 24.04 Desktop.iso", 4020},
		{"Some Manga Vol 3 (2024) cbz", 7030},
		{"National Geographic Magazine April 2026", 7010},
		{"Some Book - Author.epub", 7020},
	} {
		if got := categorize("alt.binaries.misc", tc.title); got != tc.want {
			t.Errorf("categorize(%q) = %d (%s), want %d (%s) — the real thing must still classify",
				tc.title, got, categoryName(got), tc.want, categoryName(tc.want))
		}
	}
}

// Standard-definition video used to fall through to Other/Misc, because the
// resolution fallback only knew 1080p, 2160p and 720p. Half of everything the
// categoriser could not place was video it simply could not see.
//
// Every title here is real, taken from the 5,103 that sat in Other/Misc.
func TestCategorizeFindsStandardDefinitionVideo(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  int
	}{
		// A pixel count below 720p is still a shelf, not an absence of one.
		{"Akkaran.2024.480p.SS.WEB-DL.Tamil.AAC2.0.H.264", 2030},
		{"Macherla Niyojakavargam (2022) 576p ZEE5 WEB-DL", 2030},
		// A source marker that implies SD, with no pixel count anywhere.
		{"Ramayana.The.Legend.of.Prince.Rama.1992.DVDRip.x264-HANDJOB", 2030},
		{"Das Bankentrio (1989) - DVDRiP - Xvid", 2030},
		// A codec that implies HD without saying so.
		{"Anaganaga Oka Roju (1996) - WebDL x264 - [DD 2.0 CLEANED]", 2040},
		// 4K spelled as 4K. This was filed HD until the token was added.
		{"Prince And Family (2025) 4k TRUE WEB-DL - SDR - HEVC", 2045},
		{"Devdas.2002.4320p.YT.WEB-DL.AAC2.0.AV1-Anmol", 2045},
	} {
		if got := categorize("alt.binaries.misc", tc.title); got != tc.want {
			t.Errorf("categorize(%q) = %d (%s), want %d (%s)",
				tc.title, got, categoryName(got), tc.want, categoryName(tc.want))
		}
	}
}

// Television is filed by its own resolution rather than assumed HD.
func TestEpisodesUseTheirOwnResolution(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  int
	}{
		{"Show.S01E05.480p.WEB", 5030},
		{"Show.S01E05.1080p.WEB", 5040},
		{"Show.S01E05.2160p.WEB", 5045},
		{"Show.S01E05", 5040}, // no marker at all: HD is the default guess
	} {
		if got := categorize("", tc.title); got != tc.want {
			t.Errorf("categorize(%q) = %d (%s), want %d (%s)",
				tc.title, got, categoryName(got), tc.want, categoryName(tc.want))
		}
	}
}

// The episode scanner required a digit within three bytes of the season, so
// "S02 EP04" failed on the 'p' and 141 real releases were filed as Other/Misc.
func TestCategorizeReadsLooseEpisodeForms(t *testing.T) {
	for _, title := range []string{
		"Game of Thrones S04 EP09",
		"DAHMER S01 EP03 Doin A Dahmer",
		"True Detective (2015) S02 EP04",
		"Show.S02.E04.720p",
		"Show S2E4",
		"Some Show Season 2 Episode 4",
	} {
		if got := categorize("", title); topLevelOf(got) != 5000 {
			t.Errorf("categorize(%q) = %d (%s) — an episode not filed under TV",
				title, got, categoryName(got))
		}
	}
	// A season number with no episode is NOT an episode pattern; it is a pack,
	// and catRules claims it separately.
	if got := hasEpisodePattern("show.s02.1080p.web"); got {
		t.Error("hasEpisodePattern matched a season with no episode")
	}
}

// A bracketed CRC32 is a fansub habit, which makes it a sharper anime signal
// than the word "anime". Without it these carry a resolution and land in
// Movies — a wrong shelf, where Other/Misc was merely an unknown one.
func TestFansubChecksumMeansAnime(t *testing.T) {
	for _, title := range []string{
		"Dragon Ball GT.23.DBOX.480p.x264-iKaos [v2] [9C1CEE03]",
		"Crayon Shin-chan - 0146 - Hindi+Tamil+Telugu dub [ATTKC][491BD7B9]",
		"[CBM]_Tomo-chan_is_a_Girl_-_06_-_Birthday_Present_[H.265_10bit]_[5D9F1A2B]",
	} {
		if got := categorize("alt.binaries.misc", title); got != 5070 {
			t.Errorf("categorize(%q) = %d (%s), want 5070 (TV/Anime)",
				title, got, categoryName(got))
		}
	}
	// Eight hex digits, but not in brackets, and a bracket group that is not a
	// checksum. Neither is the fansub convention.
	for _, title := range []string{
		"Some.Movie.2024.1080p.DEADBEEF.WEB-DL",
		"[RlsGroup] Some Movie 2024 1080p",
	} {
		if got := categorize("alt.binaries.movies", title); got == 5070 {
			t.Errorf("categorize(%q) called anime on a non-checksum bracket", title)
		}
	}
}

// Keywords are short and English words are long, so a plain substring match
// collides by construction. Every case here was found in the live catalogue.
func TestCategorizeMatchesWholeTokens(t *testing.T) {
	films := []string{
		"Mangalavaar.2023.720p.JC.WEB-DL.Hindi.DDP5.1.H.264-Archie",
		"Aum.Mangalam.Singlem.2022.720p.SM.WEB-DL.AAC5.1.H.264-PrimeFix",
		"Yuddha.Kaandam.2022.1080p.CBR.AMZN.WEB-DL.DDP2.0.H.264-PMI",
	}
	for _, f := range films {
		if got := categorize("alt.binaries.movies", f); got/1000 == 7 {
			t.Errorf("categorize(%q) = %d — a film filed under Books", f, got)
		}
	}
	// The real forms still classify.
	for _, tc := range []struct {
		title string
		want  int
	}{
		{"Some Manga Vol 3 (2024) cbz", 7030},
		{"Naruto.Shippuden.manga.collection", 7030},
		{"Artist - Album 2024 FLAC", 3040},
		{"Some Book - Author.epub", 7020},
		{"National Geographic Magazine April 2026", 7010},
	} {
		if got := categorize("alt.binaries.misc", tc.title); got != tc.want {
			t.Errorf("categorize(%q) = %d, want %d", tc.title, got, tc.want)
		}
	}
}

// An episode number with NO season beside it. Common in Asian drama and in
// cartoon libraries, where a show is numbered straight through and never
// carries a season — 689 releases sat in MOVIES on this index because of it,
// a top-level category no film source can ever match them out of.
func TestBareEpisodeNumbersAreTelevision(t *testing.T) {
	for _, title := range []string{
		"The.K2.2016.E01.1080p.NF.WEB-DL.DDP2.0.x264-DEEP_Kyle",
		"Hotel.Del.Luna.2019.E10.2160p.WEB-DL.x265.10bit.AAC-Deresisi",
		"To Love E39 1080p WEB-DL AAC H.264-Luvmichelle",
		"Tom.and.Jerry.EP15.The.Bodyguard.1944.1080p.BluRay",
		"Ryomaden.Ep16.2010.BluRay.1080p.x265.10bit.MNHD-FRDS",
		"Cross.Fire.2020.E27.WEB-DL.1080p.H264.AAC-PuTao",
	} {
		if got := categorize("alt.binaries.movies", title); topLevelOf(got) != 5000 {
			t.Errorf("categorize(%q) = %d (%s) — an episode filed outside TV",
				title, got, categoryName(got))
		}
	}

	// TWO OR THREE digits only. A single digit would swallow "Star Wars EP4",
	// which is a film — and the strictness costs nothing: 686 of the 689 real
	// cases use two or three digits.
	for _, title := range []string{
		"Star.Wars.EP4.A.New.Hope.1977.1080p.BluRay.x264",
		"Some.Film.2024.1080p.WEB-DL.DDP5.1.E5",
	} {
		if got := categorize("alt.binaries.movies", title); topLevelOf(got) == 5000 {
			t.Errorf("categorize(%q) = %d (%s) — a film called television by a one-digit token",
				title, got, categoryName(got))
		}
	}

	// The token must stand alone. A letter before it or a non-digit after it
	// means it is part of another word, not an episode number.
	for _, s := range []string{"se7en", "the.matrix.e-ac3.1080p", "movie.1080p.aac2.0"} {
		if hasBareEpisodeToken(s) {
			t.Errorf("hasBareEpisodeToken(%q) = true", s)
		}
	}
}
