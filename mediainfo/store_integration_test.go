//go:build integration

package mediainfo

import (
	"context"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi/pgtest"
)

// The mediainfo store against a real Postgres.
//
// Two things here are only decidable by the database. RemoveReport's author
// check is `(user_id = $2 OR $3)` inside an UPDATE — the thing that stops a
// forged id withholding somebody else's contribution. And SummariesFor is a
// DISTINCT ON with an ORDER BY, which is the whole of "the newest surviving
// report per release" and is the sort of query that returns plausible rows when
// it is wrong.
//
// SummariesFor is also the batch a listing page calls once for fifty rows, so
// "returns the right thing for one release" is the least interesting property
// it has.

func testStore(t *testing.T) *PGStore {
	t.Helper()
	return NewPGStore(pgtest.SchemaDB(t, "mediainfo_store_test", migrations))
}

const (
	contributor = int64(7)
	someoneElse = int64(8)
	staffUser   = int64(99)
	release     = int64(500)
)

// report builds a parsed report that yields a recognisable summary.
func report(format, bitrate, audio string) Report {
	return Report{Tracks: []Track{
		{Kind: KindVideo, Label: "Video", Fields: []Field{
			{Name: "Format", Value: format},
			{Name: "Bit rate", Value: bitrate},
		}},
		{Kind: KindAudio, Label: "Audio", Fields: []Field{
			{Name: "Format", Value: audio},
			{Name: "Channel(s)", Value: "6 channels"},
		}},
	}}
}

func add(t *testing.T, s *PGStore, rel, user int64, format string) {
	t.Helper()
	rep := report(format, "10.4 Mb/s", "E-AC-3")
	if err := s.Upsert(context.Background(), rel, user, "raw "+format, rep); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func onlyReport(t *testing.T, s *PGStore, rel int64) ReportRow {
	t.Helper()
	rows, err := s.Reports(context.Background(), rel)
	if err != nil {
		t.Fatalf("Reports: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one report on release %d, got %d", rel, len(rows))
	}
	return rows[0]
}

// ── RemoveReport ────────────────────────────────────────────────────

// TestRemoveRefusesSomebodyElsesReport. The id comes off a form; the check that
// it belongs to the caller is the WHERE clause and nothing else.
func TestRemoveRefusesSomebodyElsesReport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	add(t, s, release, contributor, "HEVC")
	row := onlyReport(t, s, release)

	ok, err := s.RemoveReport(ctx, row.ID, someoneElse, false)
	if err != nil {
		t.Fatalf("RemoveReport: %v", err)
	}
	if ok {
		t.Error("a member withheld a report that was not theirs")
	}
	if onlyReport(t, s, release).Deleted() {
		t.Error("the report was withheld anyway")
	}
}

func TestAContributorCanRemoveTheirOwn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	add(t, s, release, contributor, "HEVC")
	row := onlyReport(t, s, release)

	if ok, err := s.RemoveReport(ctx, row.ID, contributor, false); err != nil || !ok {
		t.Fatalf("RemoveReport = %v, %v; want true, nil", ok, err)
	}
	after := onlyReport(t, s, release)
	if !after.Deleted() {
		t.Fatal("deleted_at was not set")
	}
	if after.DeletedBy == nil || *after.DeletedBy != contributor {
		t.Errorf("deleted_by = %v, want the contributor", after.DeletedBy)
	}
	if after.Raw == "" {
		t.Error("the raw text is gone; this is a soft delete and staff need to see what was posted")
	}
}

// TestStaffCanRemoveAnybodys — `OR $3` widens it, and that is the whole of the
// staff path.
func TestStaffCanRemoveAnybodys(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	add(t, s, release, contributor, "HEVC")
	row := onlyReport(t, s, release)

	if ok, err := s.RemoveReport(ctx, row.ID, staffUser, true); err != nil || !ok {
		t.Fatalf("RemoveReport = %v, %v; want true, nil", ok, err)
	}
	if by := onlyReport(t, s, release).DeletedBy; by == nil || *by != staffUser {
		t.Errorf("deleted_by = %v, want the staff member", by)
	}
}

// TestRemovingTwiceReportsFalse. `deleted_at IS NULL` keeps the first remover's
// attribution rather than letting a second overwrite it.
func TestRemovingTwiceReportsFalse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	add(t, s, release, contributor, "HEVC")
	row := onlyReport(t, s, release)

	if ok, _ := s.RemoveReport(ctx, row.ID, staffUser, true); !ok {
		t.Fatal("first removal failed")
	}
	if ok, err := s.RemoveReport(ctx, row.ID, staffUser+1, true); err != nil || ok {
		t.Errorf("RemoveReport = %v, %v; a second removal must report false", ok, err)
	}
	if by := onlyReport(t, s, release).DeletedBy; by == nil || *by != staffUser {
		t.Errorf("deleted_by = %v, want it unchanged", by)
	}
}

// TestRepostingBringsItBack is the documented behaviour of the Upsert's
// `deleted_at = NULL`, and it is a deliberate decision rather than an
// oversight: staff removed those words, and these are different ones.
func TestRepostingBringsItBack(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	add(t, s, release, contributor, "HEVC")
	row := onlyReport(t, s, release)
	if ok, _ := s.RemoveReport(ctx, row.ID, staffUser, true); !ok {
		t.Fatal("removal failed")
	}

	add(t, s, release, contributor, "AVC") // the same member, different words

	after := onlyReport(t, s, release)
	if after.Deleted() {
		t.Error("reposting did not clear the withholding")
	}
	if after.DeletedBy != nil {
		t.Errorf("deleted_by = %v, want it cleared alongside deleted_at", after.DeletedBy)
	}
	if after.EditedAt == nil {
		t.Error("edited_at was not stamped, so the page cannot say the words changed")
	}
}

// TestOneReportPerMemberPerRelease — the unique key is (release_id, user_id),
// so a second post replaces rather than accumulates.
func TestOneReportPerMemberPerRelease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	add(t, s, release, contributor, "HEVC")
	add(t, s, release, contributor, "AVC")
	add(t, s, release, someoneElse, "VP9")

	rows, err := s.Reports(ctx, release)
	if err != nil {
		t.Fatalf("Reports: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d reports, want 2 — one per member", len(rows))
	}
	mine, found, err := s.MineFor(ctx, release, contributor)
	if err != nil || !found {
		t.Fatalf("MineFor = %v, %v", found, err)
	}
	if mine.Raw != "raw AVC" {
		t.Errorf("raw = %q, want the replacement", mine.Raw)
	}
}

// ── SummariesFor ────────────────────────────────────────────────────

// TestSummariesForIsABatch. This is what a listing page calls once for fifty
// rows; a version that only worked for one release would look correct on a
// release page and be wrong everywhere it matters.
func TestSummariesForIsABatch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	add(t, s, 100, contributor, "HEVC")
	add(t, s, 200, contributor, "AVC")
	add(t, s, 300, someoneElse, "VP9")

	got, err := s.SummariesFor(ctx, []int64{100, 200, 300, 999})
	if err != nil {
		t.Fatalf("SummariesFor: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d summaries, want 3: %v", len(got), got)
	}
	for rel, want := range map[int64]string{100: "HEVC", 200: "AVC", 300: "VP9"} {
		if sum := got[rel]; sum == "" || !strings.Contains(sum, want) {
			t.Errorf("release %d summary = %q, want it to mention %s", rel, sum, want)
		}
	}
	// A release nobody has described is ABSENT rather than present-and-empty,
	// so a template can range over the map and get the rows that have one.
	if _, present := got[999]; present {
		t.Error("a release with no report has an entry in the map")
	}
}

// TestSummariesForPrefersTheNewest — the DISTINCT ON / ORDER BY pair. Two
// members describing one release is normal (a re-encode and the original), and
// a listing row has space for one line.
func TestSummariesForPrefersTheNewest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	add(t, s, release, contributor, "AVC") // first
	add(t, s, release, someoneElse, "HEVC")

	got, err := s.SummariesFor(ctx, []int64{release})
	if err != nil {
		t.Fatalf("SummariesFor: %v", err)
	}
	if !strings.Contains(got[release], "HEVC") {
		t.Errorf("summary = %q, want the most recent report's (HEVC)", got[release])
	}
}

// TestSummariesForSkipsWithheldReports. A report staff removed must not go on
// supplying the line under a listing row — which is a separate query from the
// one the release page uses, and so a separate chance to forget the filter.
func TestSummariesForSkipsWithheldReports(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	add(t, s, release, contributor, "AVC")
	add(t, s, release, someoneElse, "HEVC")
	rows, _ := s.Reports(ctx, release)

	// Withhold the newest, which is the one currently supplying the summary.
	var newest ReportRow
	for _, r := range rows {
		if r.UserID == someoneElse {
			newest = r
		}
	}
	if ok, _ := s.RemoveReport(ctx, newest.ID, staffUser, true); !ok {
		t.Fatal("removal failed")
	}

	got, err := s.SummariesFor(ctx, []int64{release})
	if err != nil {
		t.Fatalf("SummariesFor: %v", err)
	}
	if strings.Contains(got[release], "HEVC") {
		t.Errorf("summary = %q — a withheld report is still describing the release", got[release])
	}
	// And it falls back to the surviving one rather than to nothing.
	if !strings.Contains(got[release], "AVC") {
		t.Errorf("summary = %q, want the remaining report's (AVC)", got[release])
	}

	// Withhold that one too, and the release drops out of the map entirely.
	for _, r := range rows {
		if r.UserID == contributor {
			if ok, _ := s.RemoveReport(ctx, r.ID, staffUser, true); !ok {
				t.Fatal("second removal failed")
			}
		}
	}
	if got, err = s.SummariesFor(ctx, []int64{release}); err != nil {
		t.Fatalf("SummariesFor: %v", err)
	}
	if _, present := got[release]; present {
		t.Errorf("every report is withheld and the release still has a summary: %q", got[release])
	}
}

// TestSummariesForWithNoIdsAsksNothing. The guard matters: `= ANY('{}')` is a
// query that scans and returns nothing, and this is called from a listing that
// may legitimately have no rows.
func TestSummariesForWithNoIdsAsksNothing(t *testing.T) {
	s := testStore(t)
	got, err := s.SummariesFor(context.Background(), nil)
	if err != nil {
		t.Fatalf("SummariesFor(nil): %v", err)
	}
	if got == nil {
		t.Error("returned a nil map; a caller ranging over it should not have to nil-check")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// TestSummariesForSkipsAnUndescribableReport. A parse that recognised nothing
// yields an empty summary, and an empty string under a listing row is worse
// than no line at all — the release must be absent from the map.
func TestSummariesForSkipsAnUndescribableReport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, release, contributor, "not a mediainfo dump at all", Report{}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.SummariesFor(ctx, []int64{release})
	if err != nil {
		t.Fatalf("SummariesFor: %v", err)
	}
	if _, present := got[release]; present {
		t.Errorf("an empty summary was published: %q", got[release])
	}
}
