//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
	"time"
)

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
