package usenet

import (
	"bytes"
	"encoding/xml"
	"regexp"
	"strings"
)

// Tags is the quality metadata parsed from a release title.
type Tags struct {
	Resolution string // 2160p / 1080p / 720p / 480p
	Source     string // BluRay / WEB-DL / WEBRip / HDTV / DVD / Remux
	Codec      string // x265 / x264 / AV1 / XviD
	Audio      string // FLAC / AAC / DTS / AC3 / TrueHD / Opus
	Language   string // English / Japanese / Multi / Dual Audio / …
}

// Empty reports whether nothing was parsed.
func (t Tags) Empty() bool {
	return t.Resolution == "" && t.Source == "" && t.Codec == "" && t.Audio == "" && t.Language == ""
}

var (
	reRes    = regexp.MustCompile(`(?i)\b(2160p|1440p|1080p|720p|576p|480p|4k)\b`)
	reSource = regexp.MustCompile(`(?i)\b(blu-?ray|bd(?:rip|mux)?|web-?dl|web-?rip|hdtv|dvd(?:rip)?|remux)\b`)
	reCodec  = regexp.MustCompile(`(?i)\b(x265|x264|h\.?265|h\.?264|hevc|avc|av1|xvid|divx)\b`)
	reAudio  = regexp.MustCompile(`(?i)\b(flac|aac|dts(?:-hd)?|e?-?ac-?3|truehd|opus|mp3|pcm)\b`)
	reLang   = regexp.MustCompile(`(?i)\b(multi|dual[- ]?audio|english|eng|japanese|jpn|spanish|french|german|italian|dubbed|subbed)\b`)
)

// parseTags extracts quality tags from a release title. Best-effort; unmatched
// fields stay empty.
func parseTags(title string) Tags {
	return Tags{
		Resolution: normRes(reRes.FindString(title)),
		Source:     normSource(reSource.FindString(title)),
		Codec:      normCodec(reCodec.FindString(title)),
		Audio:      normAudio(reAudio.FindString(title)),
		Language:   normLang(reLang.FindString(title)),
	}
}

func normRes(s string) string {
	s = strings.ToLower(s)
	if s == "4k" {
		return "2160p"
	}
	return s
}

func normSource(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	switch {
	case strings.HasPrefix(s, "blu"), strings.HasPrefix(s, "bd"):
		return "BluRay"
	case strings.Contains(s, "webdl"):
		return "WEB-DL"
	case strings.Contains(s, "webrip"):
		return "WEBRip"
	case s == "hdtv":
		return "HDTV"
	case strings.HasPrefix(s, "dvd"):
		return "DVD"
	case s == "remux":
		return "Remux"
	}
	return s
}

func normCodec(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, ".", ""))
	switch s {
	case "x265", "h265", "hevc":
		return "x265"
	case "x264", "h264", "avc":
		return "x264"
	}
	return strings.ToUpper(s)
}

func normAudio(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(strings.ReplaceAll(s, "-", ""))
}

func normLang(s string) string {
	switch strings.ToLower(strings.ReplaceAll(s, " ", "-")) {
	case "eng", "english":
		return "English"
	case "jpn", "japanese":
		return "Japanese"
	case "dual-audio", "dualaudio":
		return "Dual Audio"
	case "":
		return ""
	}
	return capitalize(strings.ToLower(s))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ── title/category helpers (ported from prod; canonical here since the legacy crawler was deleted) ──

// extractTitle pulls the quoted filename out of a base subject when one is
// present — the Usenet convention is `Release Name "file.ext" yEnc (n/m)`.
var (
	reTagAdult    = regexp.MustCompile(`(?i)\b(porn|porno|hentai|jav|xxx|sextape|cumshot|gangbang|milf)\b`)
	reTagCategory = regexp.MustCompile(`(?i)\b(OVA|ONA|OAD|Gekijouban)\b`)
)

func extractTitle(base string) string {
	if q1 := strings.IndexByte(base, '"'); q1 >= 0 {
		if q2 := strings.IndexByte(base[q1+1:], '"'); q2 >= 0 {
			return base[q1+1 : q1+1+q2]
		}
	}
	return base
}

// parseCategoryTag returns the category an EXPLICIT title marker declares, or
// "". Adult wins over Anime so a title that is both lands behind the NSFW
// filter. An explicit tag also vouches for the release: prod skips the junk
// check entirely when one is present, and the plugin mirrors that.
func parseCategoryTag(title string) string {
	if reTagAdult.MatchString(title) {
		return "Hentai"
	}
	if reTagCategory.MatchString(title) {
		return "Anime"
	}
	return ""
}

// blockedExtensions are file types that are never legitimate releases —
// executables, scripts, shortcuts. A title ending in one is refused at
// assembly. Prod's list, minus "iso": BD ISOs are ordinary releases on an
// anime indexer and the subject pipeline deliberately assembles .iso.001
// splits — blocking the extension here contradicted that pipeline and
// depended on which split happened to name the title. Operators who don't
// want ISOs can junk-rule or blacklist them, where the policy is visible
// and editable.
//
// Also minus "pl" — PL is the Polish-language tag, and it is title-final on
// the ORDINARY path, not an exotic one: reExt takes the media extension off
// a single-file subject, so "Kler.2018.PL.mkv (1/45) yEnc" derives the base
// "Kler.2018.PL", and the check read the language code as a Perl script and
// deleted the staged set — a whole language's releases, silently, counted
// only as blocked_ext. A Perl script is not Windows-executable, and posts
// that ARE .pl files are judged by the agent's post-download file blocklist,
// which sees real filenames — the correct layer for that rule. The other
// short collisions were considered and KEPT: .com is a genuinely dangerous
// DOS executable and its title-final occurrences are spam-domain tags
// (WWW.X.COM); .ws/.vb likewise show no real-release population. If one
// turns up in the blocked_ext outcome samples, it gets the same one-line
// treatment as iso and pl — not a redesign.
var blockedExtensions = map[string]bool{
	"ade": true, "adp": true, "app": true, "application": true, "appref-ms": true,
	"asp": true, "aspx": true, "asx": true, "bas": true, "bat": true, "bgi": true,
	"cab": true, "cer": true, "chm": true, "cmd": true, "cnt": true, "com": true,
	"cpl": true, "crt": true, "csh": true, "der": true, "diagcab": true, "exe": true,
	"fxp": true, "gadget": true, "grp": true, "hlp": true, "hpj": true, "hta": true,
	"htc": true, "inf": true, "ins": true, "isp": true, "its": true,
	"jar": true, "jnlp": true, "js": true, "jse": true, "ksh": true, "lnk": true,
	"mad": true, "maf": true, "mag": true, "mam": true, "maq": true, "mar": true,
	"mas": true, "mat": true, "mau": true, "mav": true, "maw": true, "mcf": true,
	"mda": true, "mdb": true, "mde": true, "mdt": true, "mdw": true, "mdz": true,
	"msc": true, "msh": true, "msh1": true, "msh2": true, "mshxml": true,
	"msh1xml": true, "msh2xml": true, "msi": true, "msp": true, "mst": true,
	"msu": true, "ops": true, "osd": true, "pcd": true, "pif": true,
	"plg": true, "prf": true, "prg": true, "printerexport": true, "ps1": true,
	"ps1xml": true, "ps2": true, "ps2xml": true, "psc1": true, "psc2": true,
	"psd1": true, "psdm1": true, "pst": true, "py": true, "pyc": true, "pyo": true,
	"pyw": true, "pyz": true, "pyzw": true, "reg": true, "scf": true, "scr": true,
	"sct": true, "shb": true, "shs": true, "sln": true, "theme": true, "tmp": true,
	"url": true, "vb": true, "vbe": true, "vbp": true, "vbs": true, "vcxproj": true,
	"vhd": true, "vhdx": true, "vsmacros": true, "vsw": true, "webpnp": true,
	"website": true, "ws": true, "wsc": true, "wsf": true, "wsh": true, "xbap": true,
	"xll": true, "xnk": true,
}

// hasBlockedExtension reports whether the title ends in a blocked file type.
func hasBlockedExtension(title string) bool {
	dot := strings.LastIndex(title, ".")
	if dot < 0 || dot == len(title)-1 {
		return false
	}
	return blockedExtensions[strings.ToLower(title[dot+1:])]
}

// articlesContainComicArchive reports whether any article filename names a
// .cbz/.cbr — the release is manga regardless of what the title reads.
func articlesContainComicArchive(arts []stagedArticle) bool {
	return contentKindFromArticles(arts) == kindComics
}

// Content kinds recognised from article FILENAMES. Deliberately coarse: this
// answers "what sort of thing is this", not "which Newznab subcategory" — the
// catalog plugin owns the taxonomy, and duplicating its tree here would give
// the site two category systems to disagree with each other.
const (
	kindBook     = "book"
	kindComics   = "comics"
	kindAudio    = "audio"
	kindVideo    = "video"
	kindSoftware = "software"
	kindImage    = "image"
)

// extKind maps a file extension to a content kind.
//
// The ARTICLE FILENAMES are the signal, not the release title. A poster names
// the file honestly even when the title is decoration — which is the whole
// reason articlesContainComicArchive (this function's original, comics-only
// form) looked here in the first place. It is also the only signal available
// for the content the title never announces: `Erotic Magazine - Fiesta Readers
// Wives 23` carries no extension at all, and was deleted by a size rule for
// being 2 MB.
//
// ARCHIVE AND RECOVERY EXTENSIONS ARE ABSENT ON PURPOSE. .rar/.7z/.zip/.001/
// .r00/.par2 are containers: they describe the packaging, never the contents,
// and a release whose only evidence is "it is in a rar" has told us nothing.
// Including them would vouch for every obfuscated post on the index, since
// those are rar volumes too.
var extKind = map[string]string{
	// Books. Reader formats plus the scanned-document ones; the long tail is
	// what calibre accepts as input, which is the closest thing to a canonical
	// list of "things that are a book".
	".epub": kindBook, ".mobi": kindBook, ".azw": kindBook, ".azw3": kindBook,
	".azw4": kindBook, ".kepub": kindBook, ".fb2": kindBook, ".fbz": kindBook,
	".djvu": kindBook, ".lit": kindBook, ".prc": kindBook, ".pdb": kindBook,
	".snb": kindBook, ".tcr": kindBook, ".htmlz": kindBook, ".pml": kindBook,
	// .pdf is a book here because on an indexer that is what it almost always
	// is — a magazine, a manual, a scan. It is genuinely ambiguous (it is also
	// every datasheet ever posted), and it earns its place only because the
	// consequence of being wrong is a release surviving, not one being deleted.
	".pdf": kindBook,
	// Comics keep their own kind: the category they map to differs (Comics vs
	// Ebook) and the existing Manga hint depends on telling them apart.
	".cbz": kindComics, ".cbr": kindComics, ".cb7": kindComics, ".cbc": kindComics,
	// Audio. Lossy and lossless together — the distinction belongs to the
	// taxonomy, not to "is this a real release".
	".mp3": kindAudio, ".aac": kindAudio, ".m4a": kindAudio, ".m4b": kindAudio,
	".ogg": kindAudio, ".opus": kindAudio, ".wma": kindAudio, ".mka": kindAudio,
	".flac": kindAudio, ".ape": kindAudio, ".wv": kindAudio, ".tta": kindAudio,
	".alac": kindAudio, ".wav": kindAudio, ".aiff": kindAudio, ".aif": kindAudio,
	".dsf": kindAudio, ".dff": kindAudio, ".mpc": kindAudio, ".shn": kindAudio,
	// Video containers.
	".mkv": kindVideo, ".mp4": kindVideo, ".m4v": kindVideo, ".avi": kindVideo,
	".mov": kindVideo, ".wmv": kindVideo, ".webm": kindVideo, ".flv": kindVideo,
	".mpg": kindVideo, ".mpeg": kindVideo, ".m2ts": kindVideo, ".mts": kindVideo,
	".ts": kindVideo, ".vob": kindVideo, ".ogm": kindVideo, ".rmvb": kindVideo,
	".divx": kindVideo, ".m2v": kindVideo,
	// Software: installers and packages across platforms. Note these are also
	// on blockedExtensions for the TITLE check — that rule refuses a release
	// NAMED after an executable, which is a different question from what the
	// payload is.
	".exe": kindSoftware, ".msi": kindSoftware, ".dmg": kindSoftware,
	".pkg": kindSoftware, ".deb": kindSoftware, ".rpm": kindSoftware,
	".apk": kindSoftware, ".ipa": kindSoftware, ".appimage": kindSoftware,
	".msix": kindSoftware, ".appx": kindSoftware, ".jar": kindSoftware,
	// .iso is the disc image an ISO-category release IS. It is a container in
	// the strict sense, but unlike .rar it is the delivered artefact rather
	// than the wrapping around one.
	".iso": kindSoftware, ".img": kindSoftware,
	// Image sets (XXX/ImgSet, scans).
	".jpg": kindImage, ".jpeg": kindImage, ".png": kindImage, ".gif": kindImage,
	".webp": kindImage, ".bmp": kindImage, ".tif": kindImage, ".tiff": kindImage,
}

// contentKindFromArticles reports what a set's ARTICLE FILENAMES say the
// release is, or "" when nothing recognisable is named.
//
// First match wins by scan order over a stable priority, not by article order:
// a set is one media file plus par2s plus an nfo and a jpg, so "whichever
// article happened to sort first" would classify half the catalogue as an
// image. Books beat comics beat audio beat video beat software beat images,
// which is least-common-first — the rarer kind is the more informative one when
// both appear.
func contentKindFromArticles(arts []stagedArticle) string {
	var seen map[string]bool
	for _, a := range arts {
		s := strings.ToLower(a.Subject)
		for ext, kind := range extKind {
			if !strings.Contains(s, ext) {
				continue
			}
			// The extension must END a filename token, or ".ts" matches inside
			// every "…rights.ts…" and ".ape" inside "landscape.mkv".
			if !namesFileWithExt(s, ext) {
				continue
			}
			if seen == nil {
				seen = map[string]bool{}
			}
			seen[kind] = true
		}
	}
	for _, kind := range []string{kindBook, kindComics, kindAudio, kindVideo, kindSoftware, kindImage} {
		if seen[kind] {
			return kind
		}
	}
	return ""
}

// contentKindFromNZB reconstructs the article-kind vouch from a stored,
// gzipped NZB blob: the <file subject> attributes are the only surviving copy
// of the article filenames once the staging horizon clears, and delegating to
// contentKindFromArticles keeps the sweep's judgment byte-identical with the
// build's. Any error — nil, corrupt gzip, unparseable XML — answers "": the
// caller treats that as "no vouch", which for the junk sweep means SPARE (a
// false spare costs one junk row other passes may catch; the reverse deletes
// a real release).
func contentKindFromNZB(gzipped []byte) string {
	if len(gzipped) == 0 {
		return ""
	}
	raw, err := gunzipBytes(gzipped)
	if err != nil {
		return ""
	}
	var doc nzbXML
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.CharsetReader = nzbCharsetReader
	if err := dec.Decode(&doc); err != nil {
		return ""
	}
	arts := make([]stagedArticle, 0, len(doc.Files))
	for _, f := range doc.Files {
		arts = append(arts, stagedArticle{Subject: f.Subject})
	}
	return contentKindFromArticles(arts)
}

// namesFileWithExt reports whether ext appears in s as a real filename ending —
// followed by a delimiter, a quote, or end-of-string, and never by another
// alphanumeric. Same reasoning as namesSmallMedia in junk.go: a bare substring
// match hands the exemption to any title long enough to contain the letters.
func namesFileWithExt(s, ext string) bool {
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], ext)
		if j < 0 {
			return false
		}
		end := i + j + len(ext)
		if end == len(s) || !isFilenameChar(s[end]) {
			return true
		}
		i += j + 1
	}
	return false
}

// isFilenameChar reports whether c could continue a filename token, so that
// ".ts" in "rights.tsv" is rejected while ".ts" in "ep01.ts" is accepted.
func isFilenameChar(c byte) bool {
	return c == '.' || (c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// reRecoveryFile matches a PAR recovery file by its name: a volume
// (".vol000+001") or a plain index (".par2" / ".par3").
var reRecoveryFile = regexp.MustCompile(`(?i)\.vol\d+\+\d+\b|\.par[23]\b`)

// allRecoveryVolumes reports whether EVERY article in the set is a PAR
// recovery file — a post carrying repair data and no payload.
//
// This replaces what the par2_volume junk rule used to do by name. That rule
// fired on a base subject ending ".volNNN+NNN", which worked only while the
// suffix survived into the base; now that it is stripped so recovery volumes
// group with the release they protect, the name no longer says. The set does:
// a release has media files alongside its par2s, an orphan post does not.
//
// Answering it here is also strictly better than answering it by name. The old
// rule could not tell an orphan from a recovery volume whose media sat in the
// same set, so it dropped both — which is exactly how a 1.4 GB release lost
// its par2 files and then failed to complete.
func allRecoveryVolumes(arts []stagedArticle) bool {
	if len(arts) == 0 {
		return false
	}
	for _, a := range arts {
		if !reRecoveryFile.MatchString(a.Subject) {
			return false
		}
	}
	return true
}
