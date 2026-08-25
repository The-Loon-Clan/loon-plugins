package usenet

import (
	"fmt"
	"testing"
	"time"
)

// The junk sweep must never delete what the build deliberately stored. The
// build grants two exemptions — the explicit category tag bypasses the junk
// engine, and a content kind recognised from the ARTICLE filenames demotes to
// the unsized rules — and the sweep re-judging bare (title, size) with the
// sized rules deleted exactly those rows every prune pass: the 2.2 MB
// magazine whose articles named .pdf, the 4 MB tagged doujin, the FLAC
// single. junkSweepVerdict mirrors the build's decision order; blob-vouch
// resolution is the second pass.

func TestJunkSweepVerdictMirrorsTheBuild(t *testing.T) {
	cases := []struct {
		title string
		size  int64
		want  sweepVerdict
	}{
		// The Fiesta war story: no dotted extension in the title, condemned
		// only by under_5mib — the blob decides.
		{"Erotic Magazine - Fiesta Readers Wives 23", 2 << 20, sweepNeedsBlob},
		// The tag bypass: stored on purpose, spared outright.
		{"Some Doujin CG [hentai]", 4 << 20, sweepSpare},
		// A media TAG without a dotted extension: sized-condemned, blob decides.
		{"Artist - Song [FLAC]", 4 << 20, sweepNeedsBlob},
		// Structural junk deletes regardless of size — a recognised kind
		// never excused a junk NAME at build either.
		{"0N70ZyFoz8n50", 400 << 20, sweepDelete},
		// A clean title at a healthy size: nothing fires.
		{"[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2].mkv", 700 << 20, sweepSpare},
	}
	for _, c := range cases {
		if got := junkSweepVerdict(c.title, c.size); got != c.want {
			t.Errorf("junkSweepVerdict(%q, %d) = %v, want %v", c.title, c.size, got, c.want)
		}
	}
}

func TestContentKindFromNZBRoundTrips(t *testing.T) {
	mk := func(subjects ...string) []byte {
		var arts []stagedArticle
		for i, s := range subjects {
			arts = append(arts, stagedArticle{
				MessageID: fmt.Sprintf("<k%d@x>", i), Subject: s, Poster: "p",
				Posted: time.Now(), PartNum: 1, TotalParts: 1, Bytes: 100,
			})
		}
		xmlOut, _, err := buildNZB(arts)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzipBytes(xmlOut)
		if err != nil {
			t.Fatal(err)
		}
		return gz
	}

	if kind := contentKindFromNZB(mk(`Magazine - "issue23.pdf" yEnc (1/3)`)); kind != kindBook {
		t.Errorf("pdf-naming blob vouched %q, want %q", kind, kindBook)
	}
	if kind := contentKindFromNZB(mk("wholly obfuscated subject")); kind != "" {
		t.Errorf("nameless blob vouched %q, want none", kind)
	}
	// The failure postures: this decision deletes a release, so unreadable
	// answers "no vouch" and the caller spares.
	if kind := contentKindFromNZB(nil); kind != "" {
		t.Errorf("nil blob vouched %q", kind)
	}
	if kind := contentKindFromNZB([]byte("not gzip at all")); kind != "" {
		t.Errorf("garbage blob vouched %q", kind)
	}
}
