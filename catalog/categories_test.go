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
