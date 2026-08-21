package curation

import (
	"strings"
	"testing"
)

func ip(n int) *int { return &n }

func TestDecide(t *testing.T) {
	tv1 := &AnimeFacts{Title: "Bocchi the Rock!", Format: "TV", TMDBSeasons: 1}
	tvMulti := &AnimeFacts{Title: "Shingeki no Kyojin", Format: "TV", TMDBSeasons: 4}
	sequel2nd := &AnimeFacts{Title: "Yama no Susume 2nd Season", Format: "TV", TMDBSeasons: 3}
	sequelWord := &AnimeFacts{Title: "Overlord Season 4", Format: "TV"}
	sequelRoman := &AnimeFacts{Title: "Working!! II", Format: "TV"}
	sequelNumeral := &AnimeFacts{Title: "Mob Psycho 100 2", Format: "TV", TMDBSeasons: 3}
	movie := &AnimeFacts{Title: "Kimi no Na wa.", Format: "MOVIE", TMDBSeasons: 1}
	ovaByType := &AnimeFacts{Title: "Hellsing Ultimate", Type: "OVA"}
	unknownFormat := &AnimeFacts{Title: "Obscure Show", TMDBSeasons: 1}
	onaSingle := &AnimeFacts{Title: "Great Pretender", Format: "ONA", TMDBSeasons: 1}
	romajiOrdinal := &AnimeFacts{Title: "Localized Name", RomajiTitle: "Show 3rd Season", Format: "TV"}
	// The AniList fallback writes one bucket for every entry it touches; the
	// host seam filters it out by source, so an unlinked sequel arrives here
	// with TMDBSeasons 0 — and must stay unresolved, not default to S1.
	finalSeason := &AnimeFacts{Title: "Shingeki no Kyojin: The Final Season", Format: "TV", TMDBSeasons: 0}
	// The same entry once the Fribb mapping knows it: no ordinal in the
	// name, but the community mapping says outright it is season 4.
	finalSeasonMapped := &AnimeFacts{Title: "Shingeki no Kyojin: The Final Season", Format: "TV", MappedSeason: 4}
	mappedAndNamed := &AnimeFacts{Title: "Yama no Susume 2nd Season", Format: "TV", MappedSeason: 2}
	movieMapped := &AnimeFacts{Title: "Kimi no Na wa.", Format: "MOVIE", MappedSeason: 1}

	cases := []struct {
		name       string
		ps, pe     *int
		facts      *AnimeFacts
		wantRule   string
		wantSeason *int
		wantEp     *int
	}{
		{"title season wins over everything", ip(2), ip(5), movie, RuleTitle, ip(2), ip(5)},
		{"title season without episode", ip(3), nil, tvMulti, RuleTitle, ip(3), nil},
		{"no metadata row", nil, nil, nil, RuleUnresolved, nil, nil},
		{"movie never gets a season", nil, nil, movie, RuleNonSeasonal, nil, nil},
		{"ova by AniDB type string", nil, nil, ovaByType, RuleNonSeasonal, nil, nil},
		{"entry named 2nd Season", nil, nil, sequel2nd, RuleMetaOrdinal, ip(2), nil},
		{"entry named Season 4", nil, nil, sequelWord, RuleMetaOrdinal, ip(4), nil},
		{"entry with roman-numeral suffix", nil, nil, sequelRoman, RuleMetaOrdinal, ip(2), nil},
		{"entry with bare numeral suffix", nil, nil, sequelNumeral, RuleMetaOrdinal, ip(2), nil},
		{"ordinal in romaji title only", nil, nil, romajiOrdinal, RuleMetaOrdinal, ip(3), nil},
		{"single TMDB season defaults to S1", nil, nil, tv1, RuleMetaSingleSeason, ip(1), nil},
		{"ONA with single season defaults too", nil, nil, onaSingle, RuleMetaSingleSeason, ip(1), nil},
		{"multi-season show stays unresolved", nil, nil, tvMulti, RuleUnresolved, nil, nil},
		{"unnamed sequel with no mapping stays unresolved", nil, nil, finalSeason, RuleUnresolved, nil, nil},
		{"mapping resolves the unnamed sequel", nil, nil, finalSeasonMapped, RuleMetaMapped, ip(4), nil},
		{"mapping outranks the entry-name ordinal", nil, nil, mappedAndNamed, RuleMetaMapped, ip(2), nil},
		{"movie stays non-seasonal even when mapped", nil, nil, movieMapped, RuleNonSeasonal, nil, nil},
		{"title still outranks the mapping", ip(3), nil, finalSeasonMapped, RuleTitle, ip(3), nil},
		{"unknown format refuses the S1 default", nil, nil, unknownFormat, RuleUnresolved, nil, nil},
		{"episode survives a non-seasonal verdict", nil, ip(1), ovaByType, RuleNonSeasonal, nil, ip(1)},
		{"episode survives unresolved", nil, ip(7), tvMulti, RuleUnresolved, nil, ip(7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.ps, tc.pe, tc.facts)
			if d.Rule != tc.wantRule {
				t.Errorf("rule = %q, want %q", d.Rule, tc.wantRule)
			}
			if !intPtrEq(d.Season, tc.wantSeason) {
				t.Errorf("season = %v, want %v", fmtPtr(d.Season), fmtPtr(tc.wantSeason))
			}
			if !intPtrEq(d.Episode, tc.wantEp) {
				t.Errorf("episode = %v, want %v", fmtPtr(d.Episode), fmtPtr(tc.wantEp))
			}
		})
	}
}

func TestMetadataOrdinalRejectsLookalikes(t *testing.T) {
	for name, want := range map[string]int{
		"Mob Psycho 100":            0, // three digits is a name, not a sequel marker
		"Steins;Gate 0":             0, // zero is never a season
		"Show 1":                    0, // bare 1 is part of the name more often than not
		"86":                        0,
		"Working!!":                 0,
		"Show II":                   2,
		"Show IX":                   9,
		"22nd Century Girl":         0, // ordinal without the word "season"
		"The 8th Son Season 2 Part": 2,
		"season 12 of something":    12,
	} {
		if got := metadataOrdinal(name); got != want {
			t.Errorf("metadataOrdinal(%q) = %d, want %d", name, got, want)
		}
	}
}

// The fragment must render from a fully populated view model without error —
// html/template streams, so a field mismatch aborts the page mid-render.
func TestPageRenders(t *testing.T) {
	v := vm{
		Stats:     Stats{AnimeCompleted: 100, SeasonNull: 40, EpisodeNull: 30},
		SeasonPct: 60,
		Rows: []rowVM{
			{ID: 1, Title: "Some Release", AnimeID: 7, AnimeName: "Some Show", Facts: "TV, 1 TMDB season(s)", Rule: RuleMetaSingleSeason, Fills: "S1"},
			{ID: 2, Title: "Stuck Release", AnimeID: 8, Facts: "no metadata row", Rule: RuleUnresolved},
		},
		Total: 2, Page: 1, HasNext: true, NextURL: "/admin/p/curation?page=2",
	}
	var b strings.Builder
	if err := pageTmpl.ExecuteTemplate(&b, "curation.html", v); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()
	for _, marker := range []string{
		"60%", "Some Release", "/release/1", "/anime/7", "unresolved",
		"tag tag--info\">S1", "no metadata row", "page=2",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("rendered page is missing %q", marker)
		}
	}
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func fmtPtr(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}
