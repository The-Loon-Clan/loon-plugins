package predb

import (
	"testing"
	"time"
)

// The announce format is newznab-tmux's, because the channel is theirs. These
// cases are shaped from IRCScraper::processChannelMessages rather than
// invented, so a change to this parser that "looks tidier" has to argue with
// the bot that actually sends the lines.

func TestParseNewAnnouncement(t *testing.T) {
	const line = `NEW: [DT: 2026-08-14 05:12:33] [TT: Some.Show.S01E02.1080p.WEB.H264-GROUP] ` +
		`[SC: PRE] [CT: TV/HD] [RQ: 12345:alt.binaries.teevee] [SZ: 1.4 GB] [FL: 24F] ` +
		`[FN: some.show.s01e02]`

	p, ok := Parse(line)
	if !ok {
		t.Fatal("a well-formed NEW announcement did not parse")
	}
	if p.Title != "Some.Show.S01E02.1080p.WEB.H264-GROUP" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Source != "PRE" || p.Category != "TV/HD" {
		t.Errorf("source=%q category=%q", p.Source, p.Category)
	}
	if p.Size != "1.4 GB" || p.Files != "24F" || p.Filename != "some.show.s01e02" {
		t.Errorf("size=%q files=%q filename=%q", p.Size, p.Files, p.Filename)
	}
	if p.ReqID != 12345 || p.Group != "alt.binaries.teevee" {
		t.Errorf("reqid=%d group=%q — RQ is the one field tying a pre to where it was posted",
			p.ReqID, p.Group)
	}
	want := time.Date(2026, 8, 14, 5, 12, 33, 0, time.UTC)
	if !p.At.Equal(want) {
		t.Errorf("time = %v, want %v", p.At, want)
	}
	if p.Nuked {
		t.Error("a NEW announcement was read as nuked")
	}
}

// A nuked release is still a real release and still de-obfuscates, so the nuke
// is an attribute rather than a reason to drop the row.
func TestParseNuke(t *testing.T) {
	const line = `NUK: [DT: 2026-08-14 05:20:00] [TT: Bad.Release-GROUP] [SC: PRE] ` +
		`[CT: TV/SD] [RQ: N/A] [SZ: N/A] [FL: N/A] [NUKED: dupe.of.Better.Release-OTHER]`

	p, ok := Parse(line)
	if !ok {
		t.Fatal("a NUK announcement did not parse")
	}
	if p.Title != "Bad.Release-GROUP" {
		t.Errorf("title = %q", p.Title)
	}
	if !p.Nuked || p.NukeType != "NUKED" {
		t.Errorf("nuked=%v type=%q", p.Nuked, p.NukeType)
	}
	if p.Reason != "dupe.of.Better.Release-OTHER" {
		t.Errorf("reason = %q", p.Reason)
	}
}

// UNNUKED reverses a nuke, and the reversal is itself information.
func TestParseUnnukeClearsTheFlag(t *testing.T) {
	const line = `NUK: [DT: 2026-08-14 05:21:00] [TT: Fine.Release-GROUP] [SC: PRE] ` +
		`[CT: TV/SD] [RQ: N/A] [SZ: N/A] [FL: N/A] [UNNUKED: nuke.was.wrong]`
	p, ok := Parse(line)
	if !ok {
		t.Fatal("an UNNUKED announcement did not parse")
	}
	if p.Nuked {
		t.Error("UNNUKED left the release marked nuked")
	}
	if p.NukeType != "UNNUKED" {
		t.Errorf("nuke type = %q", p.NukeType)
	}
}

// N/A is the bot's placeholder for "no value". Stored verbatim it fills the
// catalogue with releases whose category is literally the string "N/A".
func TestParseTreatsNAAsAbsent(t *testing.T) {
	const line = `NEW: [DT: 2026-08-14 05:12:33] [TT: Minimal-GROUP] [SC: N/A] ` +
		`[CT: N/A] [RQ: N/A] [SZ: N/A] [FL: N/A]`
	p, ok := Parse(line)
	if !ok {
		t.Fatal("a minimal announcement did not parse")
	}
	if p.Source != "" || p.Category != "" || p.Size != "" || p.Files != "" {
		t.Errorf("N/A stored verbatim: source=%q category=%q size=%q files=%q",
			p.Source, p.Category, p.Size, p.Files)
	}
	if p.ReqID != 0 || p.Group != "" {
		t.Errorf("N/A request parsed: reqid=%d group=%q", p.ReqID, p.Group)
	}
}

// The bot does not always send every bracket. A stricter pattern drops real
// announcements, and a dropped announcement is a de-obfuscation that silently
// never happens.
func TestParseToleratesMissingTrailingFields(t *testing.T) {
	for name, line := range map[string]string{
		"title only":     `NEW: [DT: 2026-08-14 05:12:33] [TT: Sparse.Release-GROUP]`,
		"through source": `UPD: [DT: 2026-08-14 05:12:33] [TT: Sparse.Release-GROUP] [SC: PRE]`,
		"no filename":    `NEW: [DT: 2026-08-14 05:12:33] [TT: Sparse-GROUP] [SC: PRE] [CT: XXX] [RQ: N/A] [SZ: 1 GB] [FL: 2F]`,
	} {
		t.Run(name, func(t *testing.T) {
			p, ok := Parse(line)
			if !ok {
				t.Fatalf("did not parse: %s", line)
			}
			if p.Title == "" {
				t.Error("title lost")
			}
		})
	}
}

// The channel carries chatter, joins and bot noise. Treating an unparsed line
// as an error would make the log useless.
func TestParseRejectsNonAnnouncements(t *testing.T) {
	for _, line := range []string{
		"",
		"hello everyone",
		"*** Joins: someone (~user@host)",
		"NEW: this is not the format",
		`NEW: [DT: 2026-08-14 05:12:33]`, // no title
		`NEW: [DT: 2026-08-14 05:12:33] [TT: ]`,
	} {
		if _, ok := Parse(line); ok {
			t.Errorf("parsed a non-announcement: %q", line)
		}
	}
}

// A malformed timestamp must not cost us the release name, which is the part
// that makes a pre useful at all.
func TestParseKeepsTheReleaseWhenTheTimeIsUnreadable(t *testing.T) {
	const line = `NEW: [DT: not-a-date] [TT: Still.Useful-GROUP] [SC: PRE]`
	p, ok := Parse(line)
	if !ok {
		t.Fatal("an unreadable timestamp lost the whole announcement")
	}
	if p.Title != "Still.Useful-GROUP" {
		t.Errorf("title = %q", p.Title)
	}
	if p.At.IsZero() {
		t.Error("time left zero rather than falling back to now")
	}
}
