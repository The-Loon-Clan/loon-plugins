//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The batched unnest insert, against a real Postgres. The per-row form it
// replaced made semantics obvious; the array form has to prove them: the
// staged count excludes conflicts, NULL posted survives the array round-trip,
// and chunking covers batches past stageChunk.
func TestPGStageArticlesBatchInsert(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	arts := make([]stagedArticle, stageChunk+50)
	for i := range arts {
		arts[i] = stagedArticle{
			Group: "a.b.group", BaseSubject: "Batch.Release",
			Subject:   fmt.Sprintf("Batch.Release (%d/2000)", i+1),
			MessageID: fmt.Sprintf("<b%d@x>", i),
			Poster:    "p", Bytes: 1000,
			PartNum: i + 1, TotalParts: len(arts), SegTotal: len(arts),
		}
		if i%2 == 0 {
			arts[i].Posted = time.Now()
		} // odd rows keep the zero time → must store as NULL, not 0001-01-01
	}
	n, err := s.stageArticles(ctx, arts)
	if err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	if n != len(arts) {
		t.Fatalf("staged %d, want %d", n, len(arts))
	}

	// Re-staging the same ids is the crawl's routine overlap: count must be 0.
	n, err = s.stageArticles(ctx, arts)
	if err != nil {
		t.Fatalf("re-stage: %v", err)
	}
	if n != 0 {
		t.Errorf("re-stage counted %d, want 0 — ON CONFLICT rows must not count", n)
	}

	var nulls, rows int
	if err := s.db.DB().QueryRow(
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE posted IS NULL) FROM `+s.db.Schema()+`.articles`).
		Scan(&rows, &nulls); err != nil {
		t.Fatal(err)
	}
	if rows != len(arts) {
		t.Errorf("table holds %d rows, want %d", rows, len(arts))
	}
	if nulls != len(arts)/2 {
		t.Errorf("%d NULL posted, want %d — zero times must not serialize as a real date", nulls, len(arts)/2)
	}
}

// The pg pre-filter's multi-file arm, against a real Postgres — this HAVING
// clause had no integration coverage while carrying the file-0 hole: a
// "[00/N]" header or unnumbered companion stages at file_num 0, and counting
// it toward MAX(total_files) surfaced sets one real file early. The pre-filter
// may stay optimistic (isComplete re-verifies per file), but it must not admit
// a set purely on the strength of its file-0 bucket.
func TestCandidateGroupsIgnoresFileZero(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	stage := func(base string, fileNum, part, segTotal, totalFiles int) {
		t.Helper()
		fileParts := totalFiles > 0
		if _, err := s.stageArticles(ctx, []stagedArticle{{
			Group: "a.b.group", BaseSubject: base,
			Subject:   fmt.Sprintf("%s [%02d/%02d]", base, fileNum, totalFiles),
			MessageID: fmt.Sprintf("<%s-%d-%d@x>", base, fileNum, part),
			Poster:    "p", Bytes: 1000, Posted: time.Now(),
			PartNum: part, TotalParts: segTotal, SegTotal: segTotal,
			FileNum: fileNum, TotalFiles: totalFiles, FileParts: fileParts,
		}}); err != nil {
			t.Fatalf("stage %s [%d/%d] part %d: %v", base, fileNum, totalFiles, part, err)
		}
	}

	// Short: files 1,2 of 3 plus a file-0 header — three distinct file
	// numbers, which is exactly what fooled the old COUNT(DISTINCT file_num).
	for _, fn := range []int{1, 2} {
		stage("Short.Release", fn, 1, 1, 3)
	}
	stage("Short.Release", 0, 1, 1, 3)

	// Full: the same shape with all three real files present.
	for _, fn := range []int{1, 2, 3} {
		stage("Full.Release", fn, 1, 1, 3)
	}
	stage("Full.Release", 0, 1, 1, 3)

	// Single-file arm, untouched by the fix: 2-of-2 parts.
	for _, p := range []int{1, 2} {
		if _, err := s.stageArticles(ctx, []stagedArticle{{
			Group: "a.b.group", BaseSubject: "Single.Release",
			Subject:   fmt.Sprintf("Single.Release (%d/2)", p),
			MessageID: fmt.Sprintf("<single-%d@x>", p),
			Poster:    "p", Bytes: 1000, Posted: time.Now(),
			PartNum: p, TotalParts: 2, SegTotal: 2,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	keys, _, err := s.candidateGroups(ctx, 100)
	if err != nil {
		t.Fatalf("candidateGroups: %v", err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k.Base] = true
	}
	if got["Short.Release"] {
		t.Error("Short.Release surfaced as a candidate: the file-0 bucket was " +
			"counted toward total_files while a real file is still missing")
	}
	if !got["Full.Release"] {
		t.Error("Full.Release not surfaced: the file_num >= 1 filter is over-strict")
	}
	if !got["Single.Release"] {
		t.Error("Single.Release not surfaced: the single-file arm regressed")
	}
}
