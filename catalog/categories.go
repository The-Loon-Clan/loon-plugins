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
//
// notVideo marks a bucket that names a NON-VIDEO content type — an album, a
// book, a game, a console title. Their keywords double as ordinary words in
// video release names, and because these buckets sit above the video rules they
// claimed the release before anything looked at its resolution:
//
//	Naruto.075.v4.480p.DVD.Dual-Audio.FLAC2.0.Hi10P.x264  "flac"   -> Audio
//	Bad Boys - Ride or Die (2024) REPACK 1080p WEBRip     "repack" -> PC/Games
//	Family.Switch.2023.1080p.WEB-DL.x264                  "switch" -> Console
//
// A title carrying a resolution or a video codec IS video, whatever else it
// says, so these buckets are skipped for it. The video buckets — anime, XXX,
// BluRay, season/HDTV — are deliberately unmarked: video is what they are for.
//
// The scale this was hiding: 806 of the 810 releases in Audio were video, and
// 41 of the 43 in PC/Games. Whole categories, not rounding errors — and the
// reason Audio looked like it held music worth fetching metadata for.
var catRules = []struct {
	cat      int
	keywords []string
	notVideo bool
}{
	{cat: 5070, keywords: []string{"anime", "subsplease", "erai-raws", "horriblesubs", "vostfr"}},
	{cat: 6070, keywords: []string{"xxx", "porn", "erotica", "brazzers", "onlyfans", "sex"}},
	{notVideo: true, cat: 7020, keywords: []string{"ebook", "epub", "mobi", ".pdf", " pdf", "azw3"}},
	// "cbr" is NOT here. As a comic archive it is a file extension; as a video
	// term it is the constant-bitrate marker, and release names carry it as its
	// own dot-delimited token ("Yuddha.Kaandam.2022.1080p.CBR.AMZN.WEB-DL") —
	// so no boundary rule can separate the two, because both ARE whole tokens.
	// A .cbr release is still recognised, by the article filenames rather than
	// the title: see contentKindFromArticles in the usenet plugin, which reads
	// the actual file names and sets the Manga hint. "cbz" stays because it has
	// no second meaning.
	{notVideo: true, cat: 7030, keywords: []string{"comic", "cbz", "manga"}},
	{notVideo: true, cat: 7010, keywords: []string{"magazine"}},
	{notVideo: true, cat: 3040, keywords: []string{"flac", "lossless", "24bit", "dsd"}},
	{notVideo: true, cat: 3030, keywords: []string{"audiobook"}},
	{notVideo: true, cat: 3010, keywords: []string{"mp3", "320kbps", " m4a"}},
	{notVideo: true, cat: 4010, keywords: []string{"crack", "keygen", "0day", "activator", "regged"}},
	{notVideo: true, cat: 4020, keywords: []string{".iso", "installer", "portable", "setup"}},
	{notVideo: true, cat: 4050, keywords: []string{"repack", "fitgirl", "dodi", "-codex", "-plaza", "-flt"}},
	{notVideo: true, cat: 1000, keywords: []string{"nsw", "switch", "ps4", "ps5", "xbox", "-goldberg"}},
	{cat: 2050, keywords: []string{"bluray", "blu-ray", "remux"}},
	{cat: 5040, keywords: []string{"season", "hdtv", "pdtv"}},
}

// categorize maps a group + title to a best-fit Newznab category id. The TITLE
// is the primary signal (it's the most specific); the GROUP name is only a
// fallback for titles that keyword-match nothing — otherwise a group like
// "a.b.multimedia.anime" would force every release (even an ebook) to Anime.
func categorize(group, title string) int {
	g := strings.ToLower(group)
	if cat := categorizeText(strings.ToLower(title)); cat != 8010 {
		// An episodic title posted to an ANIME group is anime. The title only
		// established that the release is television; the group is the better
		// evidence for which shelf of television, and a bare "Episode 5" says
		// nothing either way.
		if cat/1000 == 5 && cat != 5070 && groupCategory(g) == 5070 {
			return 5070
		}
		return cat
	}
	return groupCategory(g)
}

// categorizeText applies the keyword/episode/resolution rules to one string.
func categorizeText(h string) int {
	if hasEpisodePattern(h) {
		if strings.Contains(h, "anime") {
			return 5070
		}
		// 5000 + the tier, so a 480p episode lands on TV/SD rather than being
		// called HD because it is television. Same tiers as the movie shelves
		// below, which is why they share resolutionTier.
		return 5000 + resolutionTier(h)
	}
	// A fansub signal is as strong as an episode pattern, so it is read at the
	// same height — BEFORE the keyword rules.
	//
	// It used to sit after them, which cost "Haikyuu!! - 04 [Hindi AAC 2.0 +
	// English FLAC 2.0 + Japanese FLAC 2.0]" its shelf: the title carries no
	// resolution, so isVideo is false, so nothing stopped the lossless-audio
	// rule claiming an anime episode as an ALBUM.
	if hasCRCTag(h) || hasFansubNumbering(h) {
		return 5070
	}
	// Resolved once: several rules below ask the same question.
	video := isVideo(h)
	for _, r := range catRules {
		// A non-video bucket cannot own a title that is plainly video. Skipping
		// lets it fall through to the video rules below.
		if r.notVideo && video {
			continue
		}
		for _, kw := range r.keywords {
			if containsToken(h, kw) {
				return r.cat
			}
		}
	}
	// No keyword matched, so the video markers decide — and the resolution
	// says which shelf. See isVideo for what counts and why.
	if video {
		return 2000 + resolutionTier(h)
	}
	return 8010
}

// hasCRCTag spots the fansub convention of an 8-digit CRC32 in brackets:
//
//	Dragon Ball GT.22.DBOX.480p.x264-iKaos [v2] [79CA895A]
//	[CBM]_Tomo-chan_is_a_Girl_-_06_-_Birthday_Present_[H.265_10bit]_[5D9F...]
//	Crayon Shin-chan - 0146 - Hindi+Tamil+Telugu dub [ATTKC][491BD7B9]
//
// The checksum is an anime-distribution habit rather than a general scene one,
// which makes it a sharper signal than the word "anime": all 280 titles it
// matched on this index were anime, including the ones whose episode number is
// a bare ".22." that no season/episode rule can see.
//
// It runs AFTER the keyword rules — most titles match one of those and never
// reach here — and only scans forward from a '[', so titles without brackets
// pay one byte comparison per character.
// hasFansubNumbering spots the other fansub convention: a bare episode number
// between the title and a bracketed tag block, with no CRC32 to give it away.
//
//	[Toonworld4all] Naruto Shippuden - 015 [1080p BD x265 10bit]
//	Haikyuu!! - 04 [Hindi AAC 2.0 + English FLAC 2.0]
//	The Vision of Escaflowne - 04 [Director's Cut] [BD 1440 x 1080 x264 FLAC]
//
// Anime rather than plain television, because this naming belongs to fansub
// groups specifically — every one of the 161 releases matching it on this
// index is anime, and they were all shelved as FILMS because the number is not
// attached to an "E" or an "S" and no keyword rule fires.
//
// The bracket is what makes this safe to key on a bare number: " - 04 " alone
// would catch a film's part marker, but " - 04 [" is the fansub shape.
func hasFansubNumbering(h string) bool {
	for i := 0; i+4 < len(h); i++ {
		if h[i] != '-' || h[i+1] != ' ' {
			continue
		}
		j := i + 2
		start := j
		for j < len(h) && isDigit(h[j]) {
			j++
		}
		if n := j - start; n < 1 || n > 3 {
			continue
		}
		if j+1 < len(h) && h[j] == ' ' && h[j+1] == '[' {
			return true
		}
	}
	return false
}

func hasCRCTag(h string) bool {
	for i := 0; i+9 < len(h); i++ {
		if h[i] != '[' || h[i+9] != ']' {
			continue
		}
		hex := true
		for j := i + 1; j < i+9; j++ {
			c := h[j]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				hex = false
				break
			}
		}
		if hex {
			return true
		}
	}
	return false
}

// isVideo reports whether a title carries any marker that makes it video, by
// resolution, source or codec.
//
// Only 1080p/2160p/720p used to count, so everything standard-definition fell
// through to Other/Misc: 1,427 releases on this index, plus 789 more carrying a
// source marker (DVDRip, WEB-DL, XviD) and no pixel count at all. Between them
// that is 43% of everything the categoriser could not place — SD content was
// not mis-shelved so much as invisible.
//
// Token matching, not substring: "cam" is a source marker AND the first three
// letters of Camelot, Cambridge and campaign.
//
// bluray/remux/hdtv are deliberately absent — catRules above already claims
// them (2050 and 5040), so listing them here would be unreachable code that
// reads like a rule.
//
// "ts" is absent too. It means telesync, but it is also the MPEG
// transport-stream extension on HD broadcast captures; the two readings point
// at opposite shelves, so the token carries no usable signal.
func isVideo(h string) bool {
	return containsAnyToken(h,
		"2160p", "4320p", "uhd", "4k", "8k", "1080p", "1440p", "720p",
		"480p", "576p", "540p", "360p", "240p",
		"dvdrip", "dvdscr", "dvdr", "xvid", "divx", "vhsrip", "cam", "telesync",
		"web-dl", "webdl", "webrip", "bdrip", "brrip", "x264", "x265", "hevc",
	)
}

// resolutionTier returns the subcategory OFFSET for a video title's quality —
// 30 (SD), 40 (HD) or 45 (UHD). Added to 2000 it names a movie shelf, to 5000 a
// TV one, because Newznab numbers both the same way.
func resolutionTier(h string) int {
	switch {
	case containsAnyToken(h, "2160p", "4320p", "uhd", "4k", "8k"):
		return 45
	case containsAnyToken(h, "480p", "576p", "540p", "360p", "240p"):
		return 30
	case containsAnyToken(h, "dvdrip", "dvdscr", "dvdr", "xvid", "divx", "vhsrip", "cam", "telesync"):
		// Source markers that IMPLY standard definition. A DVD is 576p at best.
		return 30
	}
	// HD is the default rather than a match: it covers 1080p/1440p/720p, the
	// codecs that imply HD without saying so, and titles with an episode number
	// and no quality marker at all. Guessing HD is the better error — these
	// formats are overwhelmingly HD in practice, and for the episode branch the
	// alternative is Other/Misc, which is not a guess at all.
	return 40
}

// containsToken reports whether kw appears in h as a whole token rather than as
// any substring.
//
// A plain Contains mis-files real releases, and the failures are not exotic:
//
//	Mangalavaar.2023.720p.WEB-DL       → "manga"  → Comics   (an Indian film)
//	Aum.Mangalam.Singlem.2022          → "manga"  → Comics   (likewise)
//	Yuddha.Kaandam.2022.1080p.CBR.AMZN → "cbr"    → Comics   (CBR = constant bitrate)
//
// Sixty-seven releases sat in Comics on this index, most of them films. The
// keywords are short and English words are long, so the collisions are
// guaranteed rather than unlucky.
//
// A "token" boundary here is any non-alphanumeric, which suits release names:
// they separate words with dots, underscores, dashes and brackets far more
// often than with spaces. Keywords that already carry their own delimiter
// (".pdf", " pdf", "-codex", "-flt") still work — the delimiter is part of the
// match and the boundary test applies to what surrounds the whole keyword.
func containsToken(h, kw string) bool {
	if kw == "" {
		return false
	}
	for i := 0; i+len(kw) <= len(h); i++ {
		j := strings.Index(h[i:], kw)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(kw)
		if !alnumByte(kw[0]) || start == 0 || !alnumByte(h[start-1]) {
			if !alnumByte(kw[len(kw)-1]) || end == len(h) || !alnumByte(h[end]) {
				return true
			}
		}
		i = start
	}
	return false
}

func alnumByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
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
	// BEFORE the mp3 case, because the groups are named
	// "alt.binaries.mp3.audiobooks" and the substring that decides is the one
	// checked first. 5,145 audiobooks on this index sat in Audio/MP3 for
	// exactly that reason — a shelf that is wrong about what they are, and one
	// no book source would ever look at.
	case strings.Contains(g, "audiobook"), strings.Contains(g, "audio.book"):
		return 3030
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

// hasEpisodePattern reports whether a title names a season and an episode.
//
// Written as a scan rather than a regex because it runs over every title the
// crawler indexes. It accepts the forms that actually appear:
//
//	S01E02  S1E2  S02.E04  S02 EP04  Season 2 Episode 4
//
// The "EP" spelling and the separated forms were missed by the original, which
// required the 'e' within three bytes of the season digits and a digit
// immediately after it — so "S02 EP04" failed on the 'p'. 141 releases on this
// index were filed as Other/Misc for that one letter.
func hasEpisodePattern(s string) bool {
	for i := 0; i+3 < len(s); i++ {
		if s[i] != 's' || !isDigit(s[i+1]) {
			continue
		}
		// The season number, however many digits.
		j := i + 2
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		// An optional separator between season and episode.
		for j < len(s) && (s[j] == ' ' || s[j] == '.' || s[j] == '-' || s[j] == '_') {
			j++
		}
		if j >= len(s) || s[j] != 'e' {
			continue
		}
		j++
		// The "EP" spelling.
		if j < len(s) && s[j] == 'p' {
			j++
		}
		if j < len(s) && isDigit(s[j]) {
			return true
		}
	}
	// The spelled-out form, which carries no S/E shorthand at all.
	if strings.Contains(s, "season ") && strings.Contains(s, "episode ") {
		return true
	}
	return hasSpelledEpisodeNumber(s) || hasSeasonNumberForm(s) || hasBareEpisodeToken(s)
}

// hasSpelledEpisodeNumber spots "Episode 12" with NO season anywhere near it:
//
//	Death Note - Episode 12 - Love 1080p Hybrid ITA BDRip
//	Kuroko's Basketball - Episode 37 - Thanks 1080p BDRip
//
// The season+episode rule above needs BOTH words, and hasBareEpisodeToken
// needs the digits to follow the "e" immediately — so the spelled-out word
// with the digits after it fell between the two and 102 releases sat in
// MOVIES.
//
// Digits only, one to three. "Star Wars: Episode IV" is Roman and never
// matches; measured over every 2xxx release on this index, none of the 102
// hits is a film.
func hasSpelledEpisodeNumber(s string) bool {
	const word = "episode"
	for i := 0; i+len(word) < len(s); i++ {
		if !strings.HasPrefix(s[i:], word) {
			continue
		}
		if i > 0 && alnumByte(s[i-1]) {
			continue // mid-word
		}
		j := i + len(word)
		if j < len(s) && s[j] == 's' {
			j++ // "Episodes"
		}
		for j < len(s) && isSeparatorByte(s[j]) {
			j++
		}
		start := j
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if n := j - start; n >= 1 && n <= 3 {
			return true
		}
	}
	return false
}

// hasSeasonNumberForm spots a season marker whose episode number is NOT
// introduced by an "E":
//
//	Askimiz.Yeter.2024.S02.12.Bolum.1080p.TABII.WEB-DL   (Turkish: bolum = episode)
//	Avatar the Last Airbender S02 Episodes [ 1 to 20 ]   (a merged-season file)
//
// hasEpisodePattern above requires an "e" after the season, so both forms
// missed and landed in MOVIES.
//
// The trailing number is capped at three digits so a season followed by a YEAR
// ("S01 2024") is not read as an episode, and it must stand alone so a
// resolution or codec running on from it does not qualify.
func hasSeasonNumberForm(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] != 's' || !isDigit(s[i+1]) {
			continue
		}
		if i > 0 && alnumByte(s[i-1]) {
			continue // mid-word, so "Ocean's 11" and codec tags are safe
		}
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		k := j
		for k < len(s) && isSeparatorByte(s[k]) {
			k++
		}
		if k == j {
			continue // season and what follows must actually be separated
		}
		if strings.HasPrefix(s[k:], "episode") {
			return true
		}
		start := k
		for k < len(s) && isDigit(s[k]) {
			k++
		}
		if n := k - start; n >= 1 && n <= 3 {
			if k >= len(s) || !alnumByte(s[k]) {
				return true
			}
		}
	}
	return false
}

func isSeparatorByte(b byte) bool {
	return b == ' ' || b == '.' || b == '-' || b == '_'
}

// hasBareEpisodeToken spots an episode number with NO season beside it:
//
//	The.K2.2016.E01.1080p.NF.WEB-DL          Hotel.Del.Luna.2019.E10.2160p
//	Tom.and.Jerry.EP15.The.Bodyguard.1944    To Love E39 1080p WEB-DL
//
// Common in Asian drama and in cartoon libraries, where a show is numbered
// straight through and never carries a season. 689 releases on this index sat
// in MOVIES because of it — a top-level category no film source can ever match
// them out of.
//
// TWO OR THREE digits, deliberately. A single digit would also catch
// "Star.Wars.EP4.A.New.Hope", which is a film, and the strictness costs
// nothing measurable: 686 of the 689 use two or three, and the three that do
// not are television by some other rule anyway.
//
// The token must stand alone on both sides, so "Se7en" (letter before) and
// "E-AC3" (no digits after) do not qualify, and neither does the "E5" in an
// audio tag.
func hasBareEpisodeToken(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 'e' {
			continue
		}
		if i > 0 && alnumByte(s[i-1]) {
			continue // mid-word, not a token
		}
		j := i + 1
		if j < len(s) && s[j] == 'p' {
			j++ // the "EP" spelling
		}
		start := j
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if n := j - start; n < 2 || n > 3 {
			continue
		}
		if j < len(s) && alnumByte(s[j]) {
			continue // the digits run into something else
		}
		return true
	}
	return false
}

// containsAnyToken reports whether any needle appears in h as a whole token.
func containsAnyToken(h string, needles ...string) bool {
	for _, n := range needles {
		if containsToken(h, n) {
			return true
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
