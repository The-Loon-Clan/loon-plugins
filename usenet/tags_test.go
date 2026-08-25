package usenet

import (
	"strings"
	"testing"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		title string
		want  Tags
	}{
		{
			"Some.Show.S01E01.1080p.BluRay.x265.FLAC-GROUP",
			Tags{Resolution: "1080p", Source: "BluRay", Codec: "x265", Audio: "FLAC"},
		},
		{
			"Movie.2024.2160p.WEB-DL.DDP5.1.H.264-XYZ",
			Tags{Resolution: "2160p", Source: "WEB-DL", Codec: "x264"},
		},
		{
			"Anime Title - 05 [720p][HEVC][Dual Audio]",
			Tags{Resolution: "720p", Codec: "x265", Language: "Dual Audio"},
		},
		{
			"just a plain title with no tags",
			Tags{},
		},
	}
	for _, tc := range tests {
		got := parseTags(tc.title)
		if got != tc.want {
			t.Errorf("parseTags(%q)\n  got  %+v\n  want %+v", tc.title, got, tc.want)
		}
	}
}

// The blocked-extension gate reads the title's LAST dot-token, and a title's
// last token is often a tag, not a filename: the fixture derives Kler's base
// through the real parser so the .PL-final shape is proven, not assumed.
func TestHasBlockedExtension(t *testing.T) {
	base, _, _, _, _, _, _ := parseSubject("Kler.2018.PL.mkv (1/45) yEnc")
	if !strings.HasSuffix(base, ".PL") {
		t.Fatalf("fixture drift: parseSubject derived %q, want a .PL-final base", base)
	}
	cases := []struct {
		title string
		want  bool
	}{
		{"KMSpico.Setup.exe", true},
		{"setup.msi", true},
		{base, false},                // the Polish tag is a language, not Perl
		{"Nazwa.Filmu.2023.PL", false},
		{"WWW.SPAMSITE.COM", true},   // considered and KEPT — spam-domain tag
		{"no dots here", false},
		{"trailing.", false},
	}
	for _, c := range cases {
		if got := hasBlockedExtension(c.title); got != c.want {
			t.Errorf("hasBlockedExtension(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}
