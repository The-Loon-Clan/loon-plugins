package news

import "testing"

// slugify is the only pure helper in the news plugin; PGStore is a
// thin SQL passthrough that belongs to integration tests. These
// cases lock in the "collapse every non-alphanumeric run to a single
// dash, then trim edge dashes" contract it shares byte-for-byte with
// the host admin_handler slug helper.
func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World":        "hello-world",
		"  FAQ & Help!  ":    "faq-help",
		"multi   space":      "multi-space",
		"already-slugged":    "already-slugged",
		"--trim--edges--":    "trim-edges",
		"MiXeD CaSe 123":     "mixed-case-123",
		"punctuation!!!only": "punctuation-only",
		"":                   "",
		"   ":                "",
		"!!!":                "",
		"Ünïcode strîpped 9": "n-code-str-pped-9",
		"under_score":        "under-score",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
