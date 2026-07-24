package catalog

import (
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// taxonomy is the standard Newznab category tree. Top-level ids are the
// thousands; the subcats are the common Newznab set. This is static data — the
// admin doesn't edit the tree, only which top-level categories are enabled.
var taxonomy = []pluginapi.Category{
	{ID: 1000, Name: "Console", Subcats: []pluginapi.Subcategory{
		{ID: 1010, Name: "NDS"}, {ID: 1020, Name: "PSP"}, {ID: 1030, Name: "Wii"}, {ID: 1040, Name: "Xbox"}, {ID: 1050, Name: "Xbox 360"}, {ID: 1080, Name: "PS3"}, {ID: 1180, Name: "Other"}}},
	{ID: 2000, Name: "Movies", Subcats: []pluginapi.Subcategory{
		{ID: 2010, Name: "Foreign"}, {ID: 2030, Name: "SD"}, {ID: 2040, Name: "HD"}, {ID: 2045, Name: "UHD"}, {ID: 2050, Name: "BluRay"}, {ID: 2060, Name: "3D"}}},
	{ID: 3000, Name: "Audio", Subcats: []pluginapi.Subcategory{
		{ID: 3010, Name: "MP3"}, {ID: 3030, Name: "Audiobook"}, {ID: 3040, Name: "Lossless"}, {ID: 3050, Name: "Other"}}},
	{ID: 4000, Name: "PC", Subcats: []pluginapi.Subcategory{
		{ID: 4010, Name: "0day"}, {ID: 4020, Name: "ISO"}, {ID: 4030, Name: "Mac"}, {ID: 4050, Name: "Games"}, {ID: 4060, Name: "Mobile-iOS"}, {ID: 4070, Name: "Mobile-Android"}}},
	{ID: 5000, Name: "TV", Subcats: []pluginapi.Subcategory{
		{ID: 5020, Name: "Foreign"}, {ID: 5030, Name: "SD"}, {ID: 5040, Name: "HD"}, {ID: 5045, Name: "UHD"}, {ID: 5060, Name: "Sport"}, {ID: 5070, Name: "Anime"}, {ID: 5080, Name: "Documentary"}}},
	{ID: 6000, Name: "XXX", Subcats: []pluginapi.Subcategory{
		{ID: 6010, Name: "DVD"}, {ID: 6040, Name: "x264"}, {ID: 6045, Name: "UHD"}, {ID: 6050, Name: "Pack"}, {ID: 6060, Name: "ImgSet"}, {ID: 6070, Name: "Other"}}},
	{ID: 7000, Name: "Books", Subcats: []pluginapi.Subcategory{
		{ID: 7010, Name: "Mags"}, {ID: 7020, Name: "Ebook"}, {ID: 7030, Name: "Comics"}, {ID: 7040, Name: "Technical"}}},
	{ID: 8000, Name: "Other", Subcats: []pluginapi.Subcategory{
		{ID: 8010, Name: "Misc"}, {ID: 8020, Name: "Hashed"}}},
}

// topLevelOf returns the thousands bucket a category id belongs to (5070 → 5000).
func topLevelOf(id int) int { return (id / 1000) * 1000 }

// keyword buckets for Categorize, checked in priority order (first match wins).
var catRules = []struct {
	cat      int
	keywords []string
}{
	{5070, []string{"anime", "subsplease", "erai-raws", "horriblesubs", "vostfr"}},
	{6070, []string{"xxx", "porn", "erotica", "brazzers", "onlyfans", "sex"}},
	{7020, []string{"ebook", "epub", "mobi", ".pdf", " pdf", "azw3"}},
	{7030, []string{"comic", "cbz", "cbr", "manga"}},
	{7010, []string{"magazine"}},
	{3040, []string{"flac", "lossless", "24bit", "dsd"}},
	{3030, []string{"audiobook"}},
	{3010, []string{"mp3", "320kbps", " m4a"}},
	{4010, []string{"crack", "keygen", "0day", "activator", "regged"}},
	{4020, []string{".iso", "installer", "portable", "setup"}},
	{4050, []string{"repack", "fitgirl", "dodi", "-codex", "-plaza", "-flt"}},
	{1000, []string{"nsw", "switch", "ps4", "ps5", "xbox", "-goldberg"}},
	{2050, []string{"bluray", "blu-ray", "remux"}},
	{5040, []string{"season", "hdtv", "pdtv"}},
}

// categorize maps a group + title to a best-fit Newznab category id. The TITLE
// is the primary signal (it's the most specific); the GROUP name is only a
// fallback for titles that keyword-match nothing — otherwise a group like
// "a.b.multimedia.anime" would force every release (even an ebook) to Anime.
func categorize(group, title string) int {
	if cat := categorizeText(strings.ToLower(title)); cat != 8010 {
		return cat
	}
	return groupCategory(strings.ToLower(group))
}

// categorizeText applies the keyword/episode/resolution rules to one string.
func categorizeText(h string) int {
	if hasEpisodePattern(h) {
		if strings.Contains(h, "anime") {
			return 5070
		}
		return 5040
	}
	for _, r := range catRules {
		for _, kw := range r.keywords {
			if strings.Contains(h, kw) {
				return r.cat
			}
		}
	}
	if strings.Contains(h, "1080p") || strings.Contains(h, "2160p") || strings.Contains(h, "720p") {
		return 2040 // resolution with no other signal → Movies/HD
	}
	return 8010
}

// groupCategory infers a category from the newsgroup name alone.
func groupCategory(g string) int {
	switch {
	case strings.Contains(g, "anime"):
		return 5070
	case strings.Contains(g, "erotica"), strings.Contains(g, "xxx"), strings.Contains(g, ".sex"):
		return 6070
	case strings.Contains(g, "ebook"), strings.Contains(g, "e-book"):
		return 7020
	case strings.Contains(g, "sound"), strings.Contains(g, "mp3"), strings.Contains(g, "music"):
		return 3010
	case strings.Contains(g, "console"), strings.Contains(g, "games"):
		return 1000
	case strings.Contains(g, "movie"):
		return 2040
	case strings.Contains(g, ".tv"), strings.Contains(g, "television"):
		return 5040
	}
	return 8010
}

// hasEpisodePattern spots S01E02 / 1x02 style markers.
func hasEpisodePattern(s string) bool {
	for i := 0; i+3 < len(s); i++ {
		// SxxExx
		if s[i] == 's' && isDigit(s[i+1]) && isDigit(s[i+2]) {
			for j := i + 3; j+2 < len(s) && j < i+6; j++ {
				if s[j] == 'e' && isDigit(s[j+1]) {
					return true
				}
			}
		}
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// categoryName returns "Parent/Sub" for a category id, or "Other" if unknown.
func categoryName(id int) string {
	top := topLevelOf(id)
	for _, c := range taxonomy {
		if c.ID == top {
			if id == top {
				return c.Name
			}
			for _, sub := range c.Subcats {
				if sub.ID == id {
					return c.Name + "/" + sub.Name
				}
			}
			return c.Name
		}
	}
	return "Other"
}
