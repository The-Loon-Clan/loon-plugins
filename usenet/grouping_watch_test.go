package usenet

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// ungroupedStem exists so members of ONE unrecognised-counter cohort collide
// while unrelated singletons stay apart — the "{ 1 | 100 }" family must map
// to a single stem or the near-miss detector never sees the cohort.
func TestUngroupedStem(t *testing.T) {
	a := ungroupedStem(`Show Name { 1 | 100 } yEnc`)
	b := ungroupedStem(`Show Name { 42 | 100 } yEnc`)
	if a == "" || a != b {
		t.Errorf("cohort members split: %q vs %q — digit runs must normalise", a, b)
	}
	if x := ungroupedStem("Some Movie 2024 1080p"); x == ungroupedStem("Other Movie 2019 720p") {
		t.Error("unrelated singletons collided on one stem")
	}
	long := ungroupedStem(strings.Repeat("a", 500))
	if len(long) > 96 {
		t.Errorf("stem length %d uncapped — open-ended stems must stay bounded", len(long))
	}
	if ungroupedStem("ABC def") != ungroupedStem("abc def") {
		t.Error("stems are case-sensitive; the same cohort posted with different casing would split")
	}
}

// The stem counter is bounded: past the cap, new stems fold into an overflow
// tally instead of growing the map — one multi-million-article round must not
// hoard memory in an observability buffer.
func TestGroupingWatchStemCapAndSampling(t *testing.T) {
	g := newGroupingWatch()
	for i := 0; i < ungroupedStemCap+500; i++ {
		// Distinct alphabetic stems (digits would collapse).
		g.noteSubject("g", fmt.Sprintf("unique subject %c%c%c yEnc", 'a'+i%26, 'a'+(i/26)%26, 'a'+(i/676)%26)+strings.Repeat("x", i%7), true, false)
	}
	_, stems, overflow := g.drain()
	if len(stems) > ungroupedStemCap {
		t.Errorf("stem map grew to %d past the cap %d", len(stems), ungroupedStemCap)
	}
	if len(stems) == ungroupedStemCap && overflow == 0 {
		t.Error("cap reached but overflow uncounted — the hidden remainder must be visible")
	}
	// Junked subjects must never reach the stems: digit-heavy junk collapses
	// onto giant fake cohorts.
	g2 := newGroupingWatch()
	for i := 0; i < 100; i++ {
		g2.noteSubject("g", fmt.Sprintf("%d%d%d%d", i, i, i, i), true, true)
	}
	if _, stems2, _ := g2.drain(); len(stems2) != 0 {
		t.Errorf("junked residues produced %d stems — junk must be corpus-only", len(stems2))
	}
}

// episodeToken must be conservative: explicit E##/EP##/S##E## only. Loose
// matches would flag legitimate sets as merge suspects, and a noisy detector
// is an ignored detector.
func TestEpisodeToken(t *testing.T) {
	for subject, want := range map[string]string{
		`Show Name S01E04 1080p "file.mkv" yEnc (1/50)`:  "E4",
		`Show Name EP12 "file.mkv" yEnc (1/50)`:          "E12",
		`[Group] Show - E07 (1080p) yEnc`:                "E7",
		`Show Name 1080p BluRay x264 yEnc (1/50)`:        "",
		`PES2024 Update yEnc (1/3)`:                      "",
		`Movie (2019) Remastered yEnc (1/9)`:             "",
		`Show Name S02E115 batch "file.mkv" yEnc (1/50)`: "E115",
	} {
		if got := episodeToken(subject); got != want {
			t.Errorf("episodeToken(%q) = %q, want %q", subject, got, want)
		}
	}
}

// mergeSuspect fires only on DISTINCT markers: a real multi-segment episode
// (every subject carrying the same E04) is not a suspect, and a set carrying
// E01 and E02 is — the "Show EP1"+"Show EP2" false-merge signature.
func TestMergeSuspect(t *testing.T) {
	same := []stagedArticle{
		{Subject: `Show S01E04 "a.mkv" yEnc (1/3)`},
		{Subject: `Show S01E04 "a.mkv" yEnc (2/3)`},
		{Subject: `Show S01E04 "a.mkv" yEnc (3/3)`},
	}
	if sus, _ := mergeSuspect(same); sus {
		t.Error("a single-episode set flagged as a merge suspect")
	}
	mixed := []stagedArticle{
		{Subject: `Show S01E01 "a.mkv" yEnc (1/2)`},
		{Subject: `Show S01E02 "b.mkv" yEnc (1/2)`},
	}
	if sus, pair := mergeSuspect(mixed); !sus || pair == "" {
		t.Errorf("two distinct episodes in one set not flagged (sus=%v pair=%q)", sus, pair)
	}
	if sus, _ := mergeSuspect([]stagedArticle{{Subject: "no markers here yEnc (1/2)"}}); sus {
		t.Error("markerless set flagged")
	}
}

// The residue definition, end to end through parseOverviews: an unrecognised
// counter format stages as a singleton AND lands in the grouping watch, while
// a parsed multi-part subject does not. This is the hook every future "we
// missed a format" investigation starts from.
func TestParseOverviewsFeedsGroupingWatch(t *testing.T) {
	g := newGroupingWatch()
	ovs := []nntp.MessageOverview{
		{MessageId: "<r1@x>", Subject: `Show Name { 1 | 100 } yEnc`, Date: time.Now()},
		{MessageId: "<r2@x>", Subject: `Show Name { 2 | 100 } yEnc`, Date: time.Now()},
		{MessageId: "<ok@x>", Subject: `Fine Release (1/45) yEnc`, Date: time.Now()},
	}
	parseOverviews(ovs, "a.b.group", time.Now().Add(-24*time.Hour), nil, nil, nil, g)

	_, stems, _ := g.drain()
	var cohort int64
	for _, v := range stems {
		cohort += v.count
	}
	if cohort != 2 {
		t.Errorf("residue stems counted %d, want 2 — the unrecognised counter family must register", cohort)
	}
}
