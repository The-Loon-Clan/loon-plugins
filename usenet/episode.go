package usenet

import (
	"regexp"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Series, season and episode, parsed out of a release title.
//
// 64% of the titles in a real index carry an SxxExx, and until now none of it
// was read: a release was a row with a resolution and a codec, so "every copy
// of Silo S03E07" and "everything in season 3" were questions the index held
// the answer to and could not be asked.
//
// Best-effort and DELIBERATELY narrow. A title is a scene filename, not a
// record, and the ways it can encode an episode are endless; this reads the
// forms that dominate real data and leaves everything else unparsed rather
// than guessing. An unparsed release is a release that browses exactly as it
// did before — the failure mode is no feature, never a wrong answer.

// Episode is what a title says about where a release sits in a series.
type Episode struct {
	// Series is the show's name as it appeared, cleaned of separators:
	// "The.Blacklist" → "The Blacklist".
	Series string
	// SeriesKey is Series folded for grouping and lookup — lowercase, no
	// punctuation, no spaces. It is what "the same show" means, because
	// "Marvels.Agents.of.S.H.I.E.L.D." and "Marvel's Agents of SHIELD" are one
	// show and no operator should have to reconcile them by hand.
	SeriesKey string
	Season    int
	// Episode is 0 for a whole-season pack (S03, S03.COMPLETE), which is a
	// real thing to index and a different thing from episode zero.
	Episode int
	// Pack marks that whole-season release, so a page can group it with the
	// season rather than losing it among the episodes.
	Pack bool
}

// Found reports whether the title said anything usable.
func (e Episode) Found() bool { return e.SeriesKey != "" && e.Season > 0 }

var (
	// The dominant form, and the only one worth trusting for the episode
	// number: S03E07, s03e07, S3E7, and the multi-episode S03E07E08 (whose
	// first number is the one that files it).
	//
	// KNOWN LIMITATION: a double-bill "S03E07E08" files only episode 7, so a
	// gap check for episode 8 reports a false gap when 8 was posted only as
	// part of the pair. Recording both would need the row to hold an episode
	// RANGE, not one int; deferred as a schema change, uncommon on the demo's
	// mainstream-TV focus outside the occasional finale.
	reSxxExx = regexp.MustCompile(`(?i)\bS(\d{1,2})[. _-]?E(\d{1,3})`)
	// A whole-season pack: S03, Season 3, Season.03. Only trusted when NO
	// SxxExx matched, because "S03E07" contains "S03" and would otherwise
	// file every episode as a pack.
	rePack = regexp.MustCompile(`(?i)\b(?:S(\d{1,2})\b|Season[. _-]?(\d{1,2})\b)`)
	// The fansub convention, which is how anime is named and which nothing
	// above matches: "[SubsPlease] Chainsaw Man - 07 (1080p)" — a bracketed
	// group, the series, a dash, an ABSOLUTE episode number with no season.
	//
	// Measured rather than guessed: it is 4,887 of the 4,887 TV-category
	// titles this parser left alone on a real index, which made it the whole
	// of the miss rather than a corner of it.
	//
	// Filed as season 1, the convention every indexer uses for absolute
	// numbering. It is a decision rather than a fact — One Piece episode 1077
	// is not in season 1 by any broadcast reckoning — but a reader looking for
	// it wants it under the show, and inventing seasons from an absolute
	// number would be a guess with no source.
	//
	// KNOWN LIMITATION (gap detection): the TV-gap join keys the index by the
	// TVmaze BROADCAST season/episode, which for a multi-cour anime is not
	// season 1. So an absolute-numbered anime held here under season 1 does
	// not match a broadcast-season gap query and every aired episode reads as
	// a false gap. Correct matching needs an absolute<->broadcast map (XEM-
	// style) the pipeline does not have; the demo's focus is mainstream
	// broadcast-numbered TV, where this does not arise.
	reFansub = regexp.MustCompile(`^\s*(?:\[[^\]]+\]\s*)+([^\[\]]+?)\s+-\s+(\d{1,4})(?:v\d)?\s*[\[(]`)

	// Everything after the episode marker is quality, group and noise. The
	// series name is what precedes it.
	reSeparators = regexp.MustCompile(`[._]+`)
	reSpaces     = regexp.MustCompile(`\s+`)
)

// ParseEpisode reads a title. Zero value when it says nothing usable.
func ParseEpisode(title string) Episode {
	if m := reSxxExx.FindStringSubmatchIndex(title); m != nil {
		season := atoiSafe(title[m[2]:m[3]])
		ep := atoiSafe(title[m[4]:m[5]])
		if season == 0 && ep == 0 {
			// S00E00 is a marker, not a location.
			return Episode{}
		}
		return withSeries(title[:m[0]], season, ep, false)
	}
	if m := reFansub.FindStringSubmatch(title); m != nil {
		if ep := atoiSafe(m[2]); ep > 0 {
			return withSeries(m[1], 1, ep, false)
		}
	}
	if m := rePack.FindStringSubmatchIndex(title); m != nil {
		season := atoiSafe(firstNonEmpty(title, m, 2, 4))
		if season == 0 {
			return Episode{}
		}
		return withSeries(title[:m[0]], season, 0, true)
	}
	return Episode{}
}

// withSeries cleans the part of the title before the episode marker into a
// name and a key.
func withSeries(prefix string, season, ep int, pack bool) Episode {
	name := strings.TrimSpace(reSpaces.ReplaceAllString(
		reSeparators.ReplaceAllString(prefix, " "), " "))
	// A leading year or bracketed junk left by an odd naming scheme. Trimmed
	// rather than parsed: it is the difference between "Silo" and "[Group] Silo".
	name = strings.Trim(name, " -–—[](){}")
	if name == "" {
		return Episode{}
	}
	key := seriesKey(name)
	if key == "" {
		return Episode{}
	}
	return Episode{Series: name, SeriesKey: key, Season: season, Episode: ep, Pack: pack}
}

// seriesKey folds a name to what "the same show" means.
//
// A thin wrapper over the contract's fold rather than a copy of it. The rule
// belongs to pluginapi.SeriesKey because Key is part of the SeriesIndex
// contract and consumers outside this plugin -- the host's TV calendar, asking
// whether an episode that aired is in the index -- have to produce the same
// key. Two implementations of one sentence agree until the first name with an
// apostrophe in it.
func seriesKey(name string) string { return pluginapi.SeriesKey(name) }

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 9999 {
			return 0
		}
	}
	return n
}

// firstNonEmpty returns whichever of two alternation groups matched.
func firstNonEmpty(s string, m []int, a, b int) string {
	if m[a] >= 0 {
		return s[m[a]:m[a+1]]
	}
	if m[b] >= 0 {
		return s[m[b]:m[b+1]]
	}
	return ""
}
