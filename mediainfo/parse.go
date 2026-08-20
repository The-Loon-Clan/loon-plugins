package mediainfo

import (
	"strings"
)

// Parsing MediaInfo's text output.
//
// MediaInfo emits a run of sections, each a heading on its own line followed by
// "Key   : Value" pairs:
//
//	General
//	Format                    : Matroska
//	Duration                  : 42 min
//
//	Video
//	Format                    : HEVC
//	Bit rate                  : 12.4 Mb/s
//
//	Audio #1
//	Format                    : E-AC-3
//	Channel(s)                : 6 channels
//
//	Text #2
//	Language                  : English
//	Forced                    : No
//
//	Menu
//	00:00:00.000              : en:Chapter 01
//
// WHAT THIS PARSER IS FOR, which decides how forgiving it is. It is reading a
// paste from a member, on a form, in a browser. It will be handed truncated
// output, output from six different MediaInfo versions, output with the
// leading spaces eaten by a chat client, and occasionally something that is not
// MediaInfo at all. So it never fails: it extracts what it recognises and
// leaves the rest, and the caller decides whether what came back is worth
// showing. A parser that rejected a paste would send a member away to reformat
// text they did not write.
//
// It also does NOT interpret. "12.4 Mb/s" is stored as "12.4 Mb/s" rather than
// as a number of bits, because the moment this converts, it is asserting
// something about a file it has never seen — and the whole reason this data is
// contributed is that nothing here can check it.

// Track is one section of a report.
type Track struct {
	// Kind is the section heading without its number: "General", "Video",
	// "Audio", "Text", "Menu".
	Kind string
	// Label is the heading as written ("Audio #2"), so a report with three
	// audio tracks renders as three distinguishable panels.
	Label string
	// Fields are the pairs, IN ORDER. A map would be smaller and would lose
	// the order MediaInfo chose, which is the order somebody reading a report
	// expects — format first, bitrate near it, language last.
	Fields []Field
}

// Field is one key/value line.
type Field struct{ Name, Value string }

// Get returns a field by name, empty when absent.
func (t Track) Get(name string) string {
	for _, f := range t.Fields {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

// Report is a whole parse.
type Report struct {
	Tracks []Track
	// Chapters are the Menu section's entries, pulled out because they are the
	// one part of a report that is a LIST rather than a set of properties, and
	// nothing else renders like them.
	Chapters []Chapter
}

// Chapter is one entry from the Menu section.
type Chapter struct {
	// At is the timestamp as written ("00:21:14.000").
	At string
	// Title is what follows, with the language prefix MediaInfo puts on it
	// ("en:") removed — that prefix is about the chapter NAME's language and
	// is noise in a list somebody is reading to find a scene.
	Title string
}

// Kinds worth rendering, and the order to render them in. Anything else parsed
// is kept in Tracks and simply not promoted — a General section is interesting,
// an "Image" section from a still is not.
const (
	KindGeneral = "General"
	KindVideo   = "Video"
	KindAudio   = "Audio"
	KindText    = "Text"
	KindMenu    = "Menu"
)

// maxLines bounds a paste. MediaInfo for a season pack runs long, but not
// thousands of lines long, and the bound is what stops a paste being a way to
// put a megabyte in a table.
const maxLines = 2000

// Parse reads MediaInfo's text output into a Report.
//
// Never returns an error. See the file header: the input is a human's paste and
// the failure mode is "recognised nothing", which the caller checks with
// Report.Meaningful.
func Parse(raw string) Report {
	var rep Report
	var cur *Track

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		name, value, isPair := splitPair(trimmed)

		// A HEADING is a line with no colon-separated pair on it. Checked this
		// way rather than against a list of known section names, because
		// MediaInfo localises its headings and numbers them, and a parser that
		// only recognised English would silently drop every track for a member
		// running it in German.
		if !isPair {
			cur = &Track{Kind: headingKind(trimmed), Label: trimmed}
			rep.Tracks = append(rep.Tracks, *cur)
			cur = &rep.Tracks[len(rep.Tracks)-1]
			continue
		}
		if cur == nil {
			// Pairs before any heading — a paste that starts mid-section,
			// which is what happens when somebody selects from the middle.
			// Kept under a General heading rather than dropped.
			rep.Tracks = append(rep.Tracks, Track{Kind: KindGeneral, Label: KindGeneral})
			cur = &rep.Tracks[len(rep.Tracks)-1]
		}

		// The Menu section's pairs are chapters: a timestamp on the left and a
		// title on the right, which is the same SHAPE as a property line and a
		// completely different thing.
		if cur.Kind == KindMenu {
			rep.Chapters = append(rep.Chapters, Chapter{At: name, Title: stripLangPrefix(value)})
			continue
		}
		cur.Fields = append(cur.Fields, Field{Name: name, Value: value})
	}
	return rep
}

// splitPair splits "Key : Value" on the FIRST colon that has a space in front
// of it, falling back to the first colon at all.
//
// The space matters: a Menu line is "00:00:00.000 : en:Chapter 01", and
// splitting on the first colon gives "00" and the rest. MediaInfo always pads
// its keys, so " : " is the reliable separator, and the bare fallback only runs
// on output somebody has reformatted.
func splitPair(line string) (name, value string, ok bool) {
	if i := strings.Index(line, " : "); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+3:]), true
	}
	// A key with no value ("Forced :") still ends in a colon and is still a
	// pair — dropping it would silently lose a field that was stated as empty.
	if strings.HasSuffix(line, " :") {
		return strings.TrimSpace(strings.TrimSuffix(line, " :")), "", true
	}
	return "", "", false
}

// headingKind strips the "#2" and any trailing detail off a section heading.
func headingKind(heading string) string {
	k := heading
	if i := strings.Index(k, "#"); i >= 0 {
		k = k[:i]
	}
	return strings.TrimSpace(k)
}

// stripLangPrefix removes the "en:" MediaInfo puts in front of a chapter title.
//
// Bounded to a short alphabetic prefix so a chapter genuinely called
// "Interstellar: the arrival" keeps its colon and its words.
func stripLangPrefix(title string) string {
	i := strings.Index(title, ":")
	if i <= 0 || i > 3 {
		return title
	}
	for _, r := range title[:i] {
		if r < 'a' || r > 'z' {
			return title
		}
	}
	return strings.TrimSpace(title[i+1:])
}

// Meaningful reports whether the parse found enough to be worth storing.
//
// The bar is a track with fields in it. A paste that produced only headings, or
// only a General section with nothing under it, is somebody who pasted the
// wrong thing — and storing it would put an empty panel on a release page under
// their name.
func (r Report) Meaningful() bool {
	if len(r.Chapters) > 0 {
		return true
	}
	for _, t := range r.Tracks {
		if len(t.Fields) > 0 {
			return true
		}
	}
	return false
}

// Of returns the tracks of one kind, in the order they appeared.
func (r Report) Of(kind string) []Track {
	var out []Track
	for _, t := range r.Tracks {
		if strings.EqualFold(t.Kind, kind) {
			out = append(out, t)
		}
	}
	return out
}

// Summary is the one-line description a listing row could carry: the video
// format and the audio formats, which is what "which copy is this" comes down
// to.
//
// Empty when the report does not say, rather than assembled out of whatever is
// present — half a summary reads as a whole one.
func (r Report) Summary() string {
	var parts []string
	if v := r.Of(KindVideo); len(v) > 0 {
		f := v[0].Get("Format")
		if br := v[0].Get("Bit rate"); f != "" && br != "" {
			parts = append(parts, f+" at "+br)
		} else if f != "" {
			parts = append(parts, f)
		}
	}
	auds := r.Of(KindAudio)
	if len(auds) > 0 {
		a := auds[0].Get("Format")
		if ch := auds[0].Get("Channel(s)"); a != "" && ch != "" {
			a += " " + ch
		}
		if a != "" {
			if len(auds) > 1 {
				a += " +" + itoa(len(auds)-1)
			}
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " · ")
}

// itoa for the one small case above, so the file needs no strconv import for a
// number that is never more than a handful.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
