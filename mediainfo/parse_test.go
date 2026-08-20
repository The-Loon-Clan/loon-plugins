package mediainfo

import "testing"

// A real-shaped paste: two audio tracks, two subtitle tracks, chapters.
const sample = `General
Unique ID                                : 2094...
Format                                   : Matroska
Format version                           : Version 4
File size                                : 3.42 GiB
Duration                                 : 42 min 11 s
Overall bit rate                         : 11.6 Mb/s

Video
ID                                       : 1
Format                                   : HEVC
Format profile                           : Main 10@L5@High
Codec ID                                 : V_MPEGH/ISO/HEVC
Bit rate                                 : 10.4 Mb/s
Width                                    : 3 840 pixels
Height                                   : 2 160 pixels
Frame rate                               : 23.976 FPS
Bit depth                                : 10 bits
HDR format                               : Dolby Vision, Version 1.0

Audio #1
ID                                       : 2
Format                                   : E-AC-3 JOC
Commercial name                          : Dolby Digital Plus with Dolby Atmos
Channel(s)                               : 6 channels
Bit rate                                 : 768 kb/s
Language                                 : English
Default                                  : Yes

Audio #2
ID                                       : 3
Format                                   : AC-3
Channel(s)                               : 2 channels
Language                                 : Dutch
Default                                  : No

Text #1
ID                                       : 4
Format                                   : UTF-8
Language                                 : English
Forced                                   : No

Text #2
ID                                       : 5
Format                                   : UTF-8
Language                                 : Dutch
Forced                                   : Yes

Menu
00:00:00.000                             : en:Chapter 01
00:07:41.320                             : en:Chapter 02
00:21:14.000                             : en:Interstellar: the arrival
`

func TestParseFindsEveryTrack(t *testing.T) {
	r := Parse(sample)
	if got := len(r.Of(KindVideo)); got != 1 {
		t.Errorf("video tracks = %d, want 1", got)
	}
	if got := len(r.Of(KindAudio)); got != 2 {
		t.Errorf("audio tracks = %d, want 2", got)
	}
	if got := len(r.Of(KindText)); got != 2 {
		t.Errorf("text tracks = %d, want 2", got)
	}
}

func TestParseKeepsTheNumberedLabel(t *testing.T) {
	r := Parse(sample)
	auds := r.Of(KindAudio)
	if auds[0].Label != "Audio #1" || auds[1].Label != "Audio #2" {
		t.Errorf("labels = %q, %q — a report with two audio tracks must render two distinguishable panels",
			auds[0].Label, auds[1].Label)
	}
}

func TestParseReadsFieldsInOrder(t *testing.T) {
	r := Parse(sample)
	v := r.Of(KindVideo)[0]
	if v.Fields[0].Name != "ID" || v.Fields[1].Name != "Format" {
		t.Errorf("first fields = %q, %q — MediaInfo's order is the order somebody reads in",
			v.Fields[0].Name, v.Fields[1].Name)
	}
	if got := v.Get("Bit rate"); got != "10.4 Mb/s" {
		t.Errorf("bit rate = %q, want the string verbatim — converting asserts something about a file nothing here has seen", got)
	}
}

// TestChaptersSplitOnTheRightColon is the whole reason splitPair looks for
// " : " rather than ":". A Menu line is "00:00:00.000 : en:Chapter 01", and
// splitting on the first colon gives "00" and a mangled rest.
func TestChaptersSplitOnTheRightColon(t *testing.T) {
	r := Parse(sample)
	if len(r.Chapters) != 3 {
		t.Fatalf("chapters = %d, want 3", len(r.Chapters))
	}
	if r.Chapters[1].At != "00:07:41.320" {
		t.Errorf("timestamp = %q, want 00:07:41.320", r.Chapters[1].At)
	}
	if r.Chapters[1].Title != "Chapter 02" {
		t.Errorf("title = %q, want Chapter 02 — the en: prefix is noise", r.Chapters[1].Title)
	}
}

// TestAChapterMayContainAColon — stripping the language prefix must not eat a
// colon that belongs to the title.
func TestAChapterMayContainAColon(t *testing.T) {
	r := Parse(sample)
	if got := r.Chapters[2].Title; got != "Interstellar: the arrival" {
		t.Errorf("title = %q, want the colon kept", got)
	}
}

func TestSummaryIsTheOneLineAnswer(t *testing.T) {
	r := Parse(sample)
	want := "HEVC at 10.4 Mb/s · E-AC-3 JOC 6 channels +1"
	if got := r.Summary(); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// TestParseNeverFails: the input is a paste from a member, and the failure mode
// is "recognised nothing" rather than an error that sends them away to reformat
// text they did not write.
func TestParseNeverFails(t *testing.T) {
	for _, junk := range []string{"", "hello", "{}", "\n\n\n", "not: mediainfo: at: all"} {
		r := Parse(junk)
		if r.Meaningful() && junk != "not: mediainfo: at: all" {
			t.Errorf("Parse(%q) claimed to be meaningful", junk)
		}
	}
}

// TestHeadingsAreNotAnEnglishWhitelist. MediaInfo localises its section names,
// and a parser that only knew "Video" would silently drop every track for a
// member running it in another language — so a heading is any line that is not
// a pair.
func TestHeadingsAreNotAnEnglishWhitelist(t *testing.T) {
	r := Parse("Vidéo\nFormat                    : HEVC\n")
	if len(r.Tracks) != 1 || r.Tracks[0].Kind != "Vidéo" {
		t.Fatalf("tracks = %+v, want one kept under its own heading", r.Tracks)
	}
	if r.Tracks[0].Get("Format") != "HEVC" {
		t.Error("fields under a non-English heading were dropped")
	}
}

// TestPairsBeforeAnyHeading — somebody selecting from the middle of the output
// starts mid-section, and those lines are worth keeping.
func TestPairsBeforeAnyHeading(t *testing.T) {
	r := Parse("Format                    : Matroska\nDuration                  : 42 min\n")
	if !r.Meaningful() {
		t.Fatal("a paste that starts mid-section was thrown away")
	}
	if got := r.Of(KindGeneral); len(got) != 1 || len(got[0].Fields) != 2 {
		t.Errorf("got %+v, want two fields under General", got)
	}
}

// TestAStatedEmptyFieldSurvives — "Forced :" with nothing after it is a field
// somebody stated as empty, which is different from a field that is absent.
func TestAStatedEmptyFieldSurvives(t *testing.T) {
	r := Parse("Text\nLanguage                  : English\nForced                    :\n")
	tr := r.Of(KindText)
	if len(tr) != 1 || len(tr[0].Fields) != 2 {
		t.Fatalf("fields = %+v, want both kept", tr)
	}
}

// TestLongPastesAreBounded — a paste is a way to put text in a table, and an
// unbounded one is a way to put a megabyte in it.
func TestLongPastesAreBounded(t *testing.T) {
	var b []byte
	for i := 0; i < maxLines*2; i++ {
		b = append(b, []byte("Key                     : value\n")...)
	}
	r := Parse(string(b))
	total := 0
	for _, t := range r.Tracks {
		total += len(t.Fields)
	}
	if total > maxLines {
		t.Errorf("kept %d fields from a %d-line paste, want at most %d", total, maxLines*2, maxLines)
	}
}
