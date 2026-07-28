package usenet

import (
	"strconv"
	"strings"
	"testing"
)

// formatPerFileTotals is the diagnostic that turns "have 1,000 / need 11,314"
// from a dead end into an answer: Need for a multi-file set is DERIVED from
// these numbers, so one file declaring a bogus total inflates it and the set can
// never complete.
func TestPerFileTotalsReadout(t *testing.T) {
	// File order, not map order — an unstable readout is unreadable when the
	// operator is comparing two polls.
	got := formatPerFileTotals(map[int]int{3: 14, 1: 1000, 2: 14})
	if got != "1:1000 2:14 3:14" {
		t.Errorf("got %q, want file-ordered \"1:1000 2:14 3:14\"", got)
	}
	if formatPerFileTotals(nil) != "" {
		t.Error("nil should render empty, not a stray separator")
	}

	// Bounded, because a set may hold hundreds of par2 volumes and this goes
	// into telemetry JSON polled by the admin page. The remainder is STATED:
	// silent truncation reads as "that is all there is".
	many := map[int]int{}
	for i := 1; i <= 40; i++ {
		many[i] = i
	}
	out := formatPerFileTotals(many)
	if !strings.Contains(out, "+28 more") {
		t.Errorf("truncation not reported: %q", out)
	}
	if strings.Count(out, ":") > 13 {
		t.Errorf("unbounded readout, %d entries: %q", strings.Count(out, ":"), out)
	}
	// The shown entries must still be the FIRST files, so the big media file
	// (always low-numbered) is never the part that gets dropped.
	if !strings.HasPrefix(out, "1:1 2:2 3:3") {
		t.Errorf("truncated from the wrong end: %q", out)
	}
}

// The readout has to reconcile with groupNeededParts, or it explains a number
// the builder is not using — the exact failure the forming-releases card
// already had once.
func TestPerFileReadoutExplainsNeed(t *testing.T) {
	meta := map[string]string{
		"file_parts": "1", "total_files": "13",
		segFieldKey(1): "1000", segFieldKey(2): "14",
	}
	need := groupNeededParts(meta)
	// 1000 + 14 seen, plus one article owed by each of the 11 unseen files.
	if want := 1000 + 14 + 11; need != want {
		t.Fatalf("need = %d, want %d", need, want)
	}

	per := perFileSegTotals(meta)
	sum := 0
	for _, n := range per {
		sum += n
	}
	files, _ := strconv.Atoi(meta["total_files"])
	if reconstructed := sum + (files - len(per)); reconstructed != need {
		t.Errorf("the readout's numbers rebuild %d but the builder needs %d — "+
			"an operator cannot check the builder's arithmetic from the card",
			reconstructed, need)
	}
	if out := formatPerFileTotals(per); out != "1:1000 2:14" {
		t.Errorf("readout = %q", out)
	}
}
