package usenet

import (
	"errors"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The repair rewrites catalogue titles, and rot18 is its own inverse — so
// applying it to a row that was never rotated produces convincing-looking
// nonsense with nothing to signal that it happened. Every test here is about
// what the job must NOT touch, except the first, which is about getting the one
// case it does touch exactly right.

// The real production sample, both directions. Taken from the row that started
// this: rotating digits rotated the counters and the size too, which is why the
// decode has to be exact rather than merely plausible.
const (
	rot18Posted  = `[SHYY] - HGGRE XRRX CERFRAGF  Hc.7554.275c.OyhEnl.k719.CNE7 93 bs 08 (7/0)`
	rot18Decoded = `[FULL] - UTTER KEEK PRESENTS  Up.2009.720p.BluRay.x264.PAR2 48 of 53 (2/5)`
)

type retitleCall struct {
	id    int64
	title string
}

func runRepair(t *testing.T, rows []pluginapi.ReleaseTitle) (rot18Outcome, []retitleCall) {
	t.Helper()
	var calls []retitleCall
	out, err := repairRot18Batch(rows, func(id int64, title string) error {
		calls = append(calls, retitleCall{id, title})
		return nil
	})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	return out, calls
}

func TestRot18RepairDecodesTheProductionSample(t *testing.T) {
	out, calls := runRepair(t, []pluginapi.ReleaseTitle{
		{ID: 1, Title: rot18Posted, TotalSegments: 53},
	})
	if out.Repaired != 1 {
		t.Fatalf("Repaired = %d, want 1", out.Repaired)
	}
	if len(calls) != 1 || calls[0].title != rot18Decoded {
		t.Fatalf("decoded to %q,\n              want %q", calls[0].title, rot18Decoded)
	}
	if out.Broken != 0 {
		t.Errorf("Broken = %d, want 0 — this row assembled fine, it was only mis-named", out.Broken)
	}
}

// THE ONE THAT MATTERS. A real title must survive untouched, because there is
// no second signal: rotating it produces something that looks like a release
// name and nothing downstream would notice.
func TestRot18RepairLeavesRealTitlesAlone(t *testing.T) {
	real := []string{
		"[SubsPlease] Dr. Stone S3 - 07 (1080p) [A1B2C3D4].mkv",
		"Some.Real.Release.2024.1080p.BluRay.x264-GROUP",
		"[Judas] Liar Game - S01E17.mkv",
		"Show.Name.S02.part001.rar",
		// Deliberately adversarial: contains letters and digits that WOULD
		// rotate into something, and a bracketed tag shaped like the marker.
		"[FULL] - UTTER KEEK PRESENTS  Up.2009.720p.BluRay.x264.PAR2 48 of 53 (2/5)",
		"541279675.bin",
		"",
	}
	rows := make([]pluginapi.ReleaseTitle, len(real))
	for i, tt := range real {
		rows[i] = pluginapi.ReleaseTitle{ID: int64(i + 1), Title: tt, TotalSegments: 10}
	}
	out, calls := runRepair(t, rows)
	if out.Repaired != 0 || len(calls) != 0 {
		t.Fatalf("%d real title(s) were rewritten: %+v — rot18 is self-inverse, so this is silent corruption",
			out.Repaired, calls)
	}
	if out.Scanned != len(real) {
		t.Errorf("Scanned = %d, want %d", out.Scanned, len(real))
	}
}

// A row already flagged obfuscated has been decoded. Decoding it again
// re-encodes it into the poster's gibberish, which is the same corruption
// arriving by the opposite route.
func TestRot18RepairSkipsRowsAlreadyFlagged(t *testing.T) {
	out, calls := runRepair(t, []pluginapi.ReleaseTitle{
		// Flagged, and its title is the DECODED form — exactly the state the
		// job leaves a row in, so this is a re-run of the previous pass.
		{ID: 1, Title: rot18Decoded, Obfuscated: true, TotalSegments: 53},
		// Flagged AND still rotated: still skipped. The flag is the contract;
		// second-guessing it is how a job starts flip-flopping a title.
		{ID: 2, Title: rot18Posted, Obfuscated: true, TotalSegments: 53},
	})
	if out.Repaired != 0 || len(calls) != 0 {
		t.Fatalf("a flagged row was rewritten: %+v", calls)
	}
	if out.AlreadyFlagged != 2 {
		t.Errorf("AlreadyFlagged = %d, want 2", out.AlreadyFlagged)
	}
}

// Decoding twice is the identity, so the job must converge: feeding its own
// output back in changes nothing. Pinned because "self-inverse" is exactly the
// property that makes a bug here invisible.
func TestRot18RepairIsIdempotent(t *testing.T) {
	first, calls := runRepair(t, []pluginapi.ReleaseTitle{
		{ID: 1, Title: rot18Posted, TotalSegments: 53},
	})
	if first.Repaired != 1 {
		t.Fatal("the first pass did not repair the row")
	}
	// Second pass over the job's own result, flagged as the job would flag it.
	second, calls2 := runRepair(t, []pluginapi.ReleaseTitle{
		{ID: 1, Title: calls[0].title, Obfuscated: true, TotalSegments: 53},
	})
	if second.Repaired != 0 || len(calls2) != 0 {
		t.Fatalf("the second pass rewrote the row again: %+v — this would re-encode it", calls2)
	}
}

// A rename does not make a structurally broken release buildable. Rotated
// digits rotated the counters, so some sets staged with a total-parts of zero
// that completeness can never satisfy; reporting those as fixed would be a lie
// about the part that matters.
func TestRot18RepairCountsStructurallyBrokenSeparately(t *testing.T) {
	out, _ := runRepair(t, []pluginapi.ReleaseTitle{
		{ID: 1, Title: rot18Posted, TotalSegments: 53},
		{ID: 2, Title: rot18Posted, TotalSegments: 0},
		{ID: 3, Title: rot18Posted, TotalSegments: 0},
	})
	if out.Repaired != 3 {
		t.Fatalf("Repaired = %d, want 3", out.Repaired)
	}
	if out.Broken != 2 {
		t.Errorf("Broken = %d, want 2 — a repaired title does not make total_segments=0 buildable", out.Broken)
	}
}

// The cursor must advance past every row SEEN, not every row changed.
// Advancing only on repairs would re-walk the whole catalogue forever, since
// almost nothing is rotated.
func TestRot18RepairCursorAdvancesPastEveryRowSeen(t *testing.T) {
	out, _ := runRepair(t, []pluginapi.ReleaseTitle{
		{ID: 10, Title: "a real title"},
		{ID: 20, Title: rot18Posted},
		{ID: 30, Title: "another real title"},
	})
	if out.Cursor != 30 {
		t.Errorf("Cursor = %d, want 30 — it must pass the last row scanned, not the last repaired", out.Cursor)
	}
}

// A failed write stops the batch rather than counting a repair that did not
// happen. The cursor carries what was COMPLETED, stopping short of the failed
// row: ReleaseTitlesAfter is strictly greater-than, so a cursor that crossed
// the row is a promise never to look at it again — persisting the failed
// row's own ID turned a transient host error into a title that stayed
// gibberish forever while every later pass reported a clean walk.
func TestRot18RepairStopsOnAWriteFailure(t *testing.T) {
	boom := errors.New("host said no")
	var seen int
	out, err := repairRot18Batch([]pluginapi.ReleaseTitle{
		{ID: 10, Title: "A Real Title - 01"},
		{ID: 20, Title: rot18Posted},
		{ID: 30, Title: rot18Posted},
	}, func(id int64, title string) error {
		seen++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write error", err)
	}
	if seen != 1 {
		t.Errorf("attempted %d writes after a failure, want 1", seen)
	}
	if out.Repaired != 0 {
		t.Errorf("Repaired = %d after a failed write, want 0", out.Repaired)
	}
	if out.Cursor != 10 {
		t.Errorf("Cursor = %d, want 10 — past the walked real row, SHORT of the failed one", out.Cursor)
	}
}

// A failure on the very first row leaves the cursor at zero, which is what
// lets the caller's `batch.Cursor > cursor` guard keep the previously
// persisted position — the retry resumes AT the failed row.
func TestRot18RepairFirstRowFailureHoldsTheCursor(t *testing.T) {
	out, err := repairRot18Batch([]pluginapi.ReleaseTitle{
		{ID: 20, Title: rot18Posted},
	}, func(int64, string) error { return errors.New("boom") })
	if err == nil {
		t.Fatal("want the write error")
	}
	if out.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", out.Cursor)
	}
}

// The retry a failed pass sets up actually converges: the re-fetched batch
// (ID > the held cursor) repairs the row that failed and moves on.
func TestRot18RepairRetryConverges(t *testing.T) {
	out, err := repairRot18Batch([]pluginapi.ReleaseTitle{
		{ID: 20, Title: rot18Posted},
		{ID: 30, Title: rot18Posted},
	}, func(int64, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if out.Repaired != 2 || out.Cursor != 30 {
		t.Errorf("Repaired = %d, Cursor = %d, want 2 and 30", out.Repaired, out.Cursor)
	}
}

// Skipped rows (already flagged) still advance the cursor even when a later
// row fails — the skip IS that row's completion.
func TestRot18RepairSkipAdvancesBeforeAFailure(t *testing.T) {
	out, err := repairRot18Batch([]pluginapi.ReleaseTitle{
		{ID: 10, Title: rot18Posted, Obfuscated: true},
		{ID: 20, Title: rot18Posted},
	}, func(int64, string) error { return errors.New("boom") })
	if err == nil {
		t.Fatal("want the write error")
	}
	if out.Cursor != 10 {
		t.Errorf("Cursor = %d, want 10", out.Cursor)
	}
	if out.AlreadyFlagged != 1 {
		t.Errorf("AlreadyFlagged = %d, want 1", out.AlreadyFlagged)
	}
}
