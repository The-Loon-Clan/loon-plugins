package usenet

import "testing"

// A charset label that lies is the common case, not an exotic one: the poster's
// software encodes UTF-8 and stamps ISO-8859-1 on it. Go believes the label and
// widens each byte, which is where 2,008 titles in this demo's index came from.
func TestDecodeSubjectReversesALyingCharsetLabel(t *testing.T) {
	for _, c := range []struct{ in, want, why string }{
		{"=?ISO-8859-1?Q?Espa=C3=B1ol?=", "Español",
			"UTF-8 bytes under a Latin-1 label"},
		{"=?ISO-8859-1?Q?Won=E2=80=99t?=", "Won’t",
			"a right single quote, whose widening is two C1 controls"},
		{"=?iso-8859-1?B?4oCccXVvdGVk4oCd?=", "“quoted”",
			"base64 spelling of the same lie"},
	} {
		if got := decodeSubject(c.in); got != c.want {
			t.Errorf("%s\n  in   %s\n  got  %q\n  want %q", c.why, c.in, got, c.want)
		}
	}
}

// The two honest cases must come through untouched, or the reversal is worse
// than the bug: it would corrupt every correctly-encoded subject on the wire.
func TestDecodeSubjectLeavesHonestSubjectsAlone(t *testing.T) {
	for _, c := range []struct{ in, want, why string }{
		{"=?UTF-8?Q?Espa=C3=B1ol?=", "Español", "told the truth in UTF-8"},
		{"=?ISO-8859-1?Q?Espa=F1ol?=", "Español", "genuinely Latin-1"},
		{"Plain.ASCII.Release.1080p", "Plain.ASCII.Release.1080p", "no encoded-word"},
		{"=?UTF-8?Q?=E7=99=BE=E6=97=A5?=", "百日", "CJK, far outside a byte"},
	} {
		if got := decodeSubject(c.in); got != c.want {
			t.Errorf("%s\n  in   %s\n  got  %q\n  want %q", c.why, c.in, got, c.want)
		}
	}
}

// unmojibake on its own, including the cases decodeSubject never reaches.
func TestUnmojibake(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"EspaÃ±ol", "Español"},
		{"plain ascii", "plain ascii"},
		{"Español", "Español"}, // a lone 0xF1 is not valid UTF-8: left alone
		{"百日", "百日"},           // runes past a byte: left alone
		{"", ""},
	} {
		if got := unmojibake(c.in); got != c.want {
			t.Errorf("unmojibake(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
