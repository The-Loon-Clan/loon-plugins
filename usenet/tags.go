package usenet

import (
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
// assembly. Prod's list, verbatim.
var blockedExtensions = map[string]bool{
	"ade": true, "adp": true, "app": true, "application": true, "appref-ms": true,
	"asp": true, "aspx": true, "asx": true, "bas": true, "bat": true, "bgi": true,
	"cab": true, "cer": true, "chm": true, "cmd": true, "cnt": true, "com": true,
	"cpl": true, "crt": true, "csh": true, "der": true, "diagcab": true, "exe": true,
	"fxp": true, "gadget": true, "grp": true, "hlp": true, "hpj": true, "hta": true,
	"htc": true, "inf": true, "ins": true, "iso": true, "isp": true, "its": true,
	"jar": true, "jnlp": true, "js": true, "jse": true, "ksh": true, "lnk": true,
	"mad": true, "maf": true, "mag": true, "mam": true, "maq": true, "mar": true,
	"mas": true, "mat": true, "mau": true, "mav": true, "maw": true, "mcf": true,
	"mda": true, "mdb": true, "mde": true, "mdt": true, "mdw": true, "mdz": true,
	"msc": true, "msh": true, "msh1": true, "msh2": true, "mshxml": true,
	"msh1xml": true, "msh2xml": true, "msi": true, "msp": true, "mst": true,
	"msu": true, "ops": true, "osd": true, "pcd": true, "pif": true, "pl": true,
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
	for _, a := range arts {
		s := strings.ToLower(a.Subject)
		if strings.Contains(s, ".cbz") || strings.Contains(s, ".cbr") {
			return true
		}
	}
	return false
}
