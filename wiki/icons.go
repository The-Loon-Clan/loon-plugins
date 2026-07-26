package wiki

import "regexp"

// The topic appearance vocabulary: which glyphs an admin may choose and what
// counts as a colour. Both are validated here rather than at the call site so
// the store, the handler and any future importer agree on what is storable.

// IconKey names one built-in glyph. The set is CLOSED on purpose. Topic icons
// render as inline SVG, so a free-text field would let an admin store markup
// that every visitor then executes — a stored-XSS hole traded for a folder
// picture. Adding a glyph is a code change in both this list and the
// wiki-icon template, which is the right amount of friction for something
// that ships script to every reader.
type IconKey string

// TopicIcons is the pickable set, in the order the admin form lists them.
// The keys match the branches of the "wiki-icon" template, and the labels are
// what the form shows.
var TopicIcons = []struct {
	Key   IconKey
	Label string
}{
	{"folder", "Folder (default)"},
	{"book", "Book / guides"},
	{"tools", "Tools"},
	{"server", "Server / usenet"},
	{"code", "Code / API"},
	{"shield", "Shield / policies"},
	{"people", "Community"},
	{"star", "Star"},
	{"question", "Question / FAQ"},
	{"warning", "Warning"},
}

// ValidIcon reports whether key is one this build can render. Empty is valid
// and means "leave the default" — the slug-derived glyph.
func ValidIcon(key string) bool {
	if key == "" {
		return true
	}
	for _, i := range TopicIcons {
		if string(i.Key) == key {
			return true
		}
	}
	return false
}

// hexColor is the only colour shape accepted. Six digits, leading hash, no
// named colours and no CSS functions: the value is interpolated into a style
// attribute, and while html/template escapes it, keeping the domain this
// narrow means there is nothing to reason about.
var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// NormalizeColor returns the colour to store: lowercased #rrggbb, or empty for
// "leave the default". Anything unparseable becomes empty rather than an
// error — a mistyped colour should fall back to the theme, not block saving a
// topic's name.
func NormalizeColor(v string) string {
	if !hexColor.MatchString(v) {
		return ""
	}
	out := []rune(v)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + 32
		}
	}
	return string(out)
}
