//go:build integration

package usenet

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// backdate rewinds a set's touched_at so the sweep's grace period has lapsed.
// stageArticles stamps now on every touch, so tests age sets explicitly.
func backdate(t *testing.T, r *redisStaging, group, base string, by time.Duration) {
	t.Helper()
	gk := grpKey(group, groupHashKey(group, base))
	past := time.Now().Add(-by).Unix()
	if err := r.rdb.HSet(context.Background(), gk, "touched_at", strconv.FormatInt(past, 10)).Err(); err != nil {
		t.Fatal(err)
	}
}

// The sweep must evict exactly the walk-past dead and nothing beside them —
// every spared population here is a release that could still (or already did)
// complete, and destroying one would be strictly worse than the memory it
// frees.
func TestSweepWalkPastEvictsOnlyTheDead(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	const group = "a.b.group"

	stage := func(base string, parts, totalParts int, artNum int) {
		t.Helper()
		arts := make([]stagedArticle, parts)
		for i := range arts {
			arts[i] = stagedArticle{
				Group: group, BaseSubject: base,
				Subject:   fmt.Sprintf("%s (%d/%d)", base, i+1, totalParts),
				MessageID: fmt.Sprintf("<%s-%d@x>", base, i+1),
				Poster:    "p", Bytes: 1000, Posted: time.Now(),
				PartNum: i + 1, TotalParts: totalParts, SegTotal: totalParts,
				ArticleNum: artNum + i,
			}
		}
		if _, err := r.stageArticles(ctx, arts); err != nil {
			t.Fatal(err)
		}
	}

	stage("Dead.Set", 2, 5, 100)     // short, span [100,101] inside coverage
	stage("Graced.Set", 2, 5, 100)   // same shape, but freshly touched
	stage("Uncovered.Set", 2, 5, 10) // span [10,11] outside coverage
	stage("Complete.Set", 2, 2, 120) // bound met: queued for the builder
	stage("Spanless.Set", 2, 5, 0)   // ArticleNum 0: span never recorded

	for _, base := range []string{"Dead.Set", "Uncovered.Set", "Complete.Set", "Spanless.Set"} {
		backdate(t, r, group, base, time.Hour)
	}

	cov := map[string][]articleRange{group: {{Start: 50, End: 300}}}
	scanned, evicted, _, err := r.sweepWalkPast(ctx, cov, 15*time.Minute, 10_000, 0)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if scanned < 5 {
		t.Errorf("scanned %d, want all 5", scanned)
	}
	if evicted != 1 {
		t.Fatalf("evicted %d, want exactly 1 (Dead.Set)", evicted)
	}

	exists := func(base string) bool {
		h := groupHashKey(group, base)
		n, _ := rdb.Exists(ctx, artKey(group, h), grpKey(group, h)).Result()
		return n == 2
	}
	if exists("Dead.Set") {
		t.Error("Dead.Set survived: span fully fetched, still short, past grace — it can never complete")
	}
	for _, base := range []string{"Graced.Set", "Uncovered.Set", "Complete.Set", "Spanless.Set"} {
		if !exists(base) {
			t.Errorf("%s was evicted — the sweep destroyed a set it must spare", base)
		}
	}
	// The dead set's active ref must go with its keys, or the pending sample
	// walks a ghost forever.
	if n, _ := rdb.SIsMember(ctx, activeKey(group), groupHashKey(group, "Dead.Set")).Result(); n {
		t.Error("Dead.Set still referenced in active_groups after eviction")
	}
	// And the complete set must still be drawable.
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 1 {
		t.Errorf("ready queue holds %d, want Complete.Set's 1 entry", n)
	}
}

// Bounded per call, converging across calls: the cursor must persist, or a
// population deeper than one budget has its head window re-examined forever
// while the tail is never reached — the exact defect the ready-queue reaper
// had before its cursor.
func TestSweepWalkPastIsBoundedAndConverges(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	const group, n = "a.b.group", 300

	arts := make([]stagedArticle, n)
	for i := range arts {
		base := fmt.Sprintf("Dead.%03d", i)
		arts[i] = stagedArticle{
			Group: group, BaseSubject: base,
			Subject:   base + " (1/2)",
			MessageID: fmt.Sprintf("<%s@x>", base),
			Poster:    "p", Bytes: 1000, Posted: time.Now(),
			PartNum: 1, TotalParts: 2, SegTotal: 2,
			ArticleNum: 1000 + i,
		}
	}
	if _, err := r.stageArticles(ctx, arts); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		backdate(t, r, group, fmt.Sprintf("Dead.%03d", i), time.Hour)
	}

	cov := map[string][]articleRange{group: {{Start: 1, End: 5000}}}
	_, evicted, _, err := r.sweepWalkPast(ctx, cov, 15*time.Minute, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if evicted == 0 {
		t.Fatal("a bounded sweep must still make progress")
	}
	if evicted >= n {
		t.Fatalf("one call evicted all %d against a budget of 100 — the bound is not applied", n)
	}
	// The cursor must survive the call. Convergence alone cannot prove this
	// (evicting the head window shrinks the set, so even a cursor stuck at 0
	// eventually clears an all-dead population) — but a population the sweep
	// SPARES would then have its head window re-examined forever while the
	// tail is never judged, the exact defect the ready-queue reaper had.
	r.walkMu.Lock()
	cur := r.walkCursors[group]
	r.walkMu.Unlock()
	if cur == 0 {
		t.Error("SSCAN cursor not persisted after a partial sweep")
	}

	for i := 0; i < 20; i++ {
		if _, _, _, err := r.sweepWalkPast(ctx, cov, 15*time.Minute, 100, 0); err != nil {
			t.Fatal(err)
		}
		if left, _ := rdb.SCard(ctx, activeKey(group)).Result(); left == 0 {
			return
		}
	}
	left, _ := rdb.SCard(ctx, activeKey(group)).Result()
	t.Errorf("%d dead sets remain after 20 bounded sweeps — the cursor is not persisting", left)
}
