package usenet

import (
	"fmt"
	"strings"
	"testing"
)

// The "(0/N)" text header a classic poster sends before the yEnc parts. It is
// not one of the poster's N parts, and counting it completed a single-file set
// one real segment early: the NZB shipped without its final part, the set was
// deleted, and the late part TTL'd out as an orphan — the tail of the release
// permanently lost. The multi-file twin (a "[00/N]" header at file_num 0) was
// a confirmed production bug and got its guard; the single-file arm never did
// until the demo session's adversarial review found the asymmetry (2026-08-25).

func partZeroSet(t *testing.T) []stagedArticle {
	t.Helper()
	subs := []string{
		`My.Release.2024.mkv (0/3)`, // the text header
		`My.Release.2024.mkv yEnc (1/3)`,
		`My.Release.2024.mkv yEnc (2/3)`,
		`My.Release.2024.mkv yEnc (3/3)`,
	}
	var arts []stagedArticle
	bases := map[string]bool{}
	for i, s := range subs {
		base, part, total, seg, fn, tf, fp := parseSubject(s)
		bases[base] = true
		arts = append(arts, stagedArticle{
			MessageID: fmt.Sprintf("<%d@x>", i), Group: "a.b", BaseSubject: base,
			Subject: s, PartNum: part, TotalParts: total, SegTotal: seg,
			FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	if len(bases) != 1 {
		t.Fatalf("header and parts should share one base, got %d: %v", len(bases), bases)
	}
	if arts[0].PartNum != 0 {
		t.Fatalf("the (0/3) header should parse as part 0, got %d", arts[0].PartNum)
	}
	return arts
}

func TestPartZeroHeaderDoesNotCompleteASingleFileSet(t *testing.T) {
	arts := partZeroSet(t)
	// Header + parts 1 and 2: three distinct part numbers against total 3,
	// which is exactly the premature-completion window.
	if isComplete(arts[:3]) {
		t.Error("header + 2 of 3 parts read as complete — the last part would be lost")
	}
	if !isComplete(arts) {
		t.Error("all 3 real parts present should be complete, header or not")
	}
	// And without the header at all, nothing regresses.
	if !isComplete(arts[1:]) {
		t.Error("3 of 3 parts with no header should be complete")
	}
}

func TestPartZeroHeaderIsNotEmittedAsASegment(t *testing.T) {
	arts := partZeroSet(t)
	xmlOut, totals, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Segments != 3 {
		t.Errorf("NZB should hold the 3 real parts, got %d segments", totals.Segments)
	}
	if strings.Contains(string(xmlOut), "&lt;0@x&gt;") || strings.Contains(string(xmlOut), "<0@x>") {
		t.Error("the header article's message-id is in the NZB")
	}
	// The file subject comes from part 1, not from the header the
	// lowest-part-wins pick would otherwise choose.
	if !strings.Contains(string(xmlOut), "yEnc (1/3)") {
		t.Error("file subject should come from part 1")
	}
}

func TestPartZeroHeaderAloneStillEmitsSomething(t *testing.T) {
	// Salvage builds partial docs; a set holding only the header must not
	// produce an empty <file>.
	arts := partZeroSet(t)[:1]
	_, totals, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Segments != 1 {
		t.Errorf("header-only doc should emit its one article, got %d", totals.Segments)
	}
}

func TestRedisGroupCompleteIgnoresPartZero(t *testing.T) {
	meta := map[string]string{"file_parts": "0", "total_parts": "45"}
	fields := []string{formatFieldKey(0, 0)} // the header
	for p := 1; p <= 44; p++ {
		fields = append(fields, formatFieldKey(0, p))
	}
	// 45 fields, but only 44 real parts.
	if isGroupComplete(meta, fields) {
		t.Error("header + 44 of 45 parts queued ready — the stage-time twin of the builder bug")
	}
	fields = append(fields, formatFieldKey(0, 45))
	if !isGroupComplete(meta, fields) {
		t.Error("45 real parts present should be complete, header staged or not")
	}
}
