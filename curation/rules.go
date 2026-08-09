package curation

import (
	"regexp"
	"strconv"
	"strings"
)

// Rule names — stable labels for counters, logs and the admin page's chips.
const (
	// RuleTitle: the release title itself carried a season marker; the
	// host's canonical parser found it. Highest trust.
	RuleTitle = "title"
	// RuleMetaOrdinal: the AniDB entry's own name carries a season ordinal
	// ("2nd Season", "Season 3", a roman-numeral or bare-numeral sequel
	// suffix). AniDB names each season as its own entry, so the entry name
	// is authoritative for which season this is.
	RuleMetaOrdinal = "meta-ordinal"
	// RuleMetaSingleSeason: TMDB knows this show and says it has exactly one
	// season, the entry is a seasonal format, and neither the title nor the
	// entry name says otherwise — so it can only be season 1. This is the
	// operator's stated rule verbatim.
	RuleMetaSingleSeason = "meta-single-season"
	// RuleNonSeasonal: movies, OVAs and specials do not get a season number;
	// the display layer buckets them by keyword instead, and writing
	// season 1 here would relabel a movie as "Season 1" on every surface.
	RuleNonSeasonal = "non-seasonal"
	// RuleUnresolved: nothing above applied. The row stays NULL and appears
	// in the fail-to-parse report.
	RuleUnresolved = "unresolved"
)

// Decision is one row's outcome. Season/Episode nil means "leave NULL".
type Decision struct {
	Season  *int
	Episode *int
	Rule    string
}

// Ordinal markers in AniDB entry names. The word forms accept 1..20; the
// suffix forms accept only 2..9 — a trailing "1" or "I" is far more often
// part of the name than a sequel marker ("Mob Psycho 100" ends in a numeral
// and means nothing of the kind, which is why the bare-numeral form takes a
// single digit only).
var (
	reOrdinalSeason = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)\s+season\b`)
	reWordSeason    = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\b`)
	reRomanSuffix   = regexp.MustCompile(`\s(II|III|IV|V|VI|VII|VIII|IX)$`)
	reNumeralSuffix = regexp.MustCompile(`\s([2-9])$`)
)

var romanValues = map[string]int{
	"II": 2, "III": 3, "IV": 4, "V": 5, "VI": 6, "VII": 7, "VIII": 8, "IX": 9,
}

// metadataOrdinal extracts a season number from an AniDB entry name, 0 when
// the name carries none.
func metadataOrdinal(name string) int {
	if name == "" {
		return 0
	}
	for _, re := range []*regexp.Regexp{reOrdinalSeason, reWordSeason} {
		if m := re.FindStringSubmatch(name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= 20 {
				return n
			}
		}
	}
	if m := reRomanSuffix.FindStringSubmatch(name); m != nil {
		return romanValues[m[1]]
	}
	if m := reNumeralSuffix.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// nonSeasonal reports whether the entry is a format that never takes a
// season number. Checked against BOTH metadata sources: AniList format is
// the cleaner vocabulary but is missing on older rows, where the AniDB type
// string ("Movie", "OVA", "TV Special") is all we have.
func nonSeasonal(f *AnimeFacts) bool {
	switch f.Format {
	case "MOVIE", "OVA", "SPECIAL", "MUSIC":
		return true
	}
	t := strings.ToLower(f.Type)
	return strings.Contains(t, "movie") || strings.Contains(t, "ova") ||
		strings.Contains(t, "special") || strings.Contains(t, "music video")
}

// seasonal reports whether the entry is a format where "one TMDB season"
// licenses the season-1 default. Unknown format AND unknown type refuse the
// default — an inference this automatic must stand on positive evidence.
func seasonal(f *AnimeFacts) bool {
	switch f.Format {
	case "TV", "ONA":
		return true
	}
	t := strings.ToLower(f.Type)
	return strings.Contains(t, "tv series") || strings.Contains(t, "web")
}

// Decide applies the rules in trust order to one release. parsedSeason /
// parsedEpisode are the host parser's read of the release title; facts may be
// nil when the aid has no metadata row. The episode travels through
// unchanged: episodes are only ever taken from the title (a pack or a movie
// correctly has none), seasons are what metadata can add.
func Decide(parsedSeason, parsedEpisode *int, facts *AnimeFacts) Decision {
	d := Decision{Episode: parsedEpisode}
	if parsedSeason != nil {
		d.Season = parsedSeason
		d.Rule = RuleTitle
		return d
	}
	if facts == nil {
		d.Rule = RuleUnresolved
		return d
	}
	if nonSeasonal(facts) {
		d.Rule = RuleNonSeasonal
		return d
	}
	if n := metadataOrdinal(facts.Title); n > 0 {
		d.Season = &n
		d.Rule = RuleMetaOrdinal
		return d
	}
	if n := metadataOrdinal(facts.RomajiTitle); n > 0 {
		d.Season = &n
		d.Rule = RuleMetaOrdinal
		return d
	}
	if facts.TMDBSeasons == 1 && seasonal(facts) {
		one := 1
		d.Season = &one
		d.Rule = RuleMetaSingleSeason
		return d
	}
	d.Rule = RuleUnresolved
	return d
}
