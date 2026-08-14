package predb

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parsing the #PreNNTmux announce line.
//
// A PreDB is a record of scene release names captured as they are ANNOUNCED,
// which is the cheap answer to de-obfuscation: matching an obfuscated posting
// against a known real name costs nothing on the wire, works on RAR'd and
// passworded content where the yEnc header gives you "abc123.part01.rar", and
// needs no article body at all (docs/BODY-FETCH.md §4).
//
// The format is newznab-tmux's, because the channel is theirs:
//
//	NEW: [DT: 2026-08-14 05:12:33] [TT: Some.Release-GROUP] [SC: PRE] \
//	     [CT: TV/HD] [RQ: N/A] [SZ: 1.4 GB] [FL: 24F] [FN: some.release]
//
// Three verbs share it. NEW is a fresh release, UPD adds detail to one already
// seen, and NUK marks one nuked with a reason -- a nuked release is still a
// real release and still de-obfuscates, so all three are kept and the nuke is
// recorded as an attribute rather than a deletion.

// announce matches the bot's line. Written against
// newznab-tmux's IRCScraper::processChannelMessages so the field set matches
// the channel that produces it rather than what looks reasonable.
//
// Every bracket after TT is optional in practice even though the bot usually
// sends them, so they are non-greedy and the trailing ones may be absent: a
// stricter pattern drops real announcements when the bot omits a field, and a
// dropped announcement is a de-obfuscation that silently never happens.
var announce = regexp.MustCompile(
	`^(?i)(?P<verb>NEW|UPD|NUK): ` +
		`\[DT: (?P<time>[^\]]+)\]\s*` +
		`\[TT: (?P<title>[^\]]+)\]\s*` +
		`(?:\[SC: (?P<source>[^\]]*)\]\s*)?` +
		`(?:\[CT: (?P<category>[^\]]*)\]\s*)?` +
		`(?:\[RQ: (?P<req>[^\]]*)\]\s*)?` +
		`(?:\[SZ: (?P<size>[^\]]*)\]\s*)?` +
		`(?:\[FL: (?P<files>[^\]]*)\]\s*)?` +
		`(?:\[FN: (?P<filename>[^\]]*)\]\s*)?` +
		`(?:\[(?P<nukeverb>(?:UN|MOD|RE|OLD)?NUKED?): (?P<reason>[^\]]*)\]\s*)?$`)

// Pre is one announced release.
type Pre struct {
	Title    string
	Source   string
	Category string
	Filename string
	Size     string
	Files    string
	Group    string // newsgroup from the RQ field, when the bot supplies one
	ReqID    int64
	At       time.Time
	Nuked    bool
	NukeType string // NUKED, UNNUKED, MODNUKED, RENUKED, OLDNUKED
	Reason   string
}

// naValues are the placeholders the bot sends for "no value". Treated as
// absent rather than stored verbatim, or the catalogue fills with releases
// whose category is literally the string "N/A".
func isNA(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.EqualFold(s, "N/A")
}

// Parse reads one channel message. ok is false for anything that is not an
// announcement -- the channel carries chatter, joins and bot noise, and
// treating an unparsed line as an error would make the log useless.
func Parse(msg string) (Pre, bool) {
	m := announce.FindStringSubmatch(strings.TrimSpace(msg))
	if m == nil {
		return Pre{}, false
	}
	g := func(name string) string {
		i := announce.SubexpIndex(name)
		if i < 0 || i >= len(m) {
			return ""
		}
		return strings.TrimSpace(m[i])
	}
	p := Pre{Title: g("title")}
	if p.Title == "" {
		return Pre{}, false // a pre with no name is not usable for anything
	}
	if v := g("source"); !isNA(v) {
		p.Source = v
	}
	if v := g("category"); !isNA(v) {
		p.Category = v
	}
	if v := g("filename"); !isNA(v) {
		p.Filename = v
	}
	if v := g("size"); !isNA(v) {
		p.Size = v
	}
	if v := g("files"); !isNA(v) {
		// Capped like newznab's substr($hits['files'], 0, 50): the field is
		// free text from a bot and there is no reason to store more of it.
		if len(v) > 50 {
			v = v[:50]
		}
		p.Files = v
	}
	// RQ carries "<reqid>:<newsgroup>" when present, which is the one field
	// that ties a pre to where it was posted.
	if v := g("req"); !isNA(v) {
		if id, group, found := strings.Cut(v, ":"); found {
			if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil {
				p.ReqID = n
			}
			p.Group = strings.TrimSpace(group)
		}
	}
	if v := g("nukeverb"); v != "" {
		p.NukeType = strings.ToUpper(v)
		// UNNUKED reverses a nuke. Recorded as un-nuked rather than dropped,
		// because the reversal is itself information.
		p.Nuked = !strings.HasPrefix(p.NukeType, "UN")
		p.Reason = g("reason")
	}
	p.At = parseAnnounceTime(g("time"))
	return p, true
}

// parseAnnounceTime reads the DT field, which the bot sends as UTC.
//
// Falls back to now() rather than failing the whole announcement: the release
// NAME is what makes a pre useful, and losing one because a timestamp was
// formatted unexpectedly trades the valuable half for the cheap half.
func parseAnnounceTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05 -0700",
		"01/02/2006 15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0).UTC()
	}
	return time.Now().UTC()
}
