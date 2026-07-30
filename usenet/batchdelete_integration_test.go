//go:build integration

package usenet

import (
	"context"
	"testing"
	"time"
)

// deleteStagedBatch against a real Redis.
//
// A stub cannot test this: the whole point is which KEYS the pipeline touches —
// art:, grp:, the per-group active set and the nzb:ready entry — and a fake
// would only restate the assumption being checked. An earlier version of this
// test did exactly that, passed, and left four mutations alive.
func TestRedisDeleteStagedBatchRemovesEverySet(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	const group = "alt.binaries.anime"
	bases := []string{"junk-one", "junk-two", "junk-three", "keep-me"}
	for _, base := range bases {
		if _, err := r.stageArticles(ctx, []stagedArticle{{
			MessageID: "<" + base + "@x>", Subject: base, BaseSubject: base,
			Group: group, Poster: "p@x", Bytes: 10, Posted: time.Now(),
			PartNum: 1, TotalParts: 1,
		}}); err != nil {
			t.Fatalf("stage %s: %v", base, err)
		}
	}

	doomed := []groupKey{
		{Group: group, Base: "junk-one"},
		{Group: group, Base: "junk-two"},
		{Group: group, Base: "junk-three"},
	}
	removed, err := r.deleteStagedBatch(ctx, doomed)
	if err != nil {
		t.Fatalf("deleteStagedBatch: %v", err)
	}
	if removed != len(doomed) {
		t.Errorf("reported %d removed, want %d", removed, len(doomed))
	}

	// Every key class must be gone for the deleted sets. Leaving the nzb:ready
	// entry behind is the one that matters most: candidateGroups PEEKS rather
	// than pops, so a surviving entry is drawn again every pass and the queue
	// never drains — which looks exactly like a builder that cannot keep up.
	for _, k := range doomed {
		hash := groupHashKey(k.Group, k.Base)
		for name, key := range map[string]string{
			"articles hash": artKey(k.Group, hash),
			"group meta":    grpKey(k.Group, hash),
		} {
			if n, err := rdb.Exists(ctx, key).Result(); err != nil || n != 0 {
				t.Errorf("%s: %s still exists after the batch delete (n=%d err=%v)", k.Base, name, n, err)
			}
		}
		if ok, err := rdb.SIsMember(ctx, activeKey(k.Group), hash).Result(); err != nil || ok {
			t.Errorf("%s: still in the group's active set", k.Base)
		}
		if ok, err := rdb.SIsMember(ctx, readyKey, k.Group+":"+hash).Result(); err != nil || ok {
			t.Errorf("%s: still in nzb:ready — it would be redrawn every pass and the queue "+
				"would never drain", k.Base)
		}
	}

	// And the set NOT named must be untouched. Deleting a kept set discards a
	// real release with nothing to say so.
	keepHash := groupHashKey(group, "keep-me")
	if n, err := rdb.Exists(ctx, artKey(group, keepHash)).Result(); err != nil || n != 1 {
		t.Errorf("the un-named set was deleted too (n=%d err=%v) — a real release would vanish", n, err)
	}
	if ok, err := rdb.SIsMember(ctx, readyKey, group+":"+keepHash).Result(); err != nil || !ok {
		t.Error("the un-named set was removed from nzb:ready")
	}
}

// More sets than the internal chunk size, to prove every chunk is executed
// rather than only the first.
func TestRedisDeleteStagedBatchSpansChunks(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	const group = "alt.binaries.anime"
	const n = 1200 // > the 500-key chunk
	doomed := make([]groupKey, 0, n)
	arts := make([]stagedArticle, 0, n)
	for i := 0; i < n; i++ {
		base := "set-" + itoaTest(i)
		arts = append(arts, stagedArticle{
			MessageID: "<" + base + "@x>", Subject: base, BaseSubject: base,
			Group: group, Poster: "p@x", Bytes: 10, Posted: time.Now(),
			PartNum: 1, TotalParts: 1,
		})
		doomed = append(doomed, groupKey{Group: group, Base: base})
	}
	if _, err := r.stageArticles(ctx, arts); err != nil {
		t.Fatalf("stage: %v", err)
	}

	removed, err := r.deleteStagedBatch(ctx, doomed)
	if err != nil {
		t.Fatalf("deleteStagedBatch: %v", err)
	}
	if removed != n {
		t.Errorf("removed %d of %d — a chunk was skipped, so the queue drains partially "+
			"while reporting success", removed, n)
	}
	if left, err := rdb.SCard(ctx, readyKey).Result(); err != nil || left != 0 {
		t.Errorf("nzb:ready still holds %d entr(ies) (err=%v)", left, err)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
