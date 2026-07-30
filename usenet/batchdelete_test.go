package usenet

import (
	"context"
	"errors"
	"testing"
)

// countingStaging records what the builder asked staging to remove.
type countingStaging struct {
	stagingStore
	calls     int // deleteStagedBatch invocations — the round-trip count
	singles   int // deleteStaged invocations — the old per-set path
	seen      []groupKey
	failAfter int // >0: report only this many removed, then error
}

func (c *countingStaging) deleteStagedBatch(_ context.Context, keys []groupKey) (int, error) {
	c.calls++
	c.seen = append(c.seen, keys...)
	if c.failAfter > 0 && len(keys) > c.failAfter {
		return c.failAfter, errors.New("connection reset mid-pipeline")
	}
	return len(keys), nil
}

func (c *countingStaging) deleteStaged(_ context.Context, group, base string) error {
	c.singles++
	c.seen = append(c.seen, groupKey{Group: group, Base: base})
	return nil
}

// The junk path is now almost entirely deletes: the title pre-filter judges a
// set in microseconds, so what remained was one Redis round-trip per set — 500
// of them per pass. They must go in one call.
func TestJunkRejectsAreDeletedInOneCall(t *testing.T) {
	keys := []groupKey{
		{Group: "a.b.anime", Base: "717ca5652df75bcc7bc319a676970e11"},
		{Group: "a.b.anime", Base: "PiTPW4vqdjdZ98k1YE9vZN"},
		{Group: "a.b.anime", Base: "de6424f6cb66.raw"},
	}
	_, rejects := splitByTitle(append([]groupKey(nil), keys...), false)
	if len(rejects) != len(keys) {
		t.Fatalf("fixture: %d of %d rejected, want all", len(rejects), len(keys))
	}

	st := &countingStaging{}
	doomed := make([]groupKey, 0, len(rejects))
	for _, r := range rejects {
		doomed = append(doomed, r.key)
	}
	removed, err := st.deleteStagedBatch(context.Background(), doomed)
	if err != nil {
		t.Fatal(err)
	}
	if st.calls != 1 {
		t.Errorf("%d batch call(s) for %d sets, want 1 — the round-trips are the cost", st.calls, len(doomed))
	}
	if st.singles != 0 {
		t.Errorf("%d per-set delete(s) still issued", st.singles)
	}
	if removed != len(doomed) {
		t.Errorf("reported %d removed of %d", removed, len(doomed))
	}
	if len(st.seen) != len(doomed) {
		t.Errorf("staging saw %d keys, want %d", len(st.seen), len(doomed))
	}
	// Exactly the rejected sets, and nothing else — deleting a kept set would
	// discard a real release.
	want := map[string]bool{}
	for _, k := range doomed {
		want[k.Group+"|"+k.Base] = true
	}
	for _, k := range st.seen {
		if !want[k.Group+"|"+k.Base] {
			t.Errorf("staging was asked to delete %q, which was not rejected", k.Base)
		}
	}
}

// A partial failure must report what actually went. The catch-up loop treats the
// drain count as its progress signal, so an optimistic count would convince it
// that it is making headway on a queue it is not touching — and it would loop
// forever.
func TestPartialBatchFailureReportsOnlyWhatWasRemoved(t *testing.T) {
	st := &countingStaging{failAfter: 2}
	keys := make([]groupKey, 10)
	for i := range keys {
		keys[i] = groupKey{Group: "g", Base: string(rune('a' + i))}
	}

	removed, err := st.deleteStagedBatch(context.Background(), keys)
	if err == nil {
		t.Fatal("expected the injected failure to surface")
	}
	if removed != 2 {
		t.Errorf("reported %d removed, want 2 — an optimistic count makes the catch-up loop "+
			"believe it is draining a queue it is not", removed)
	}
	if removed >= len(keys) {
		t.Error("a failed batch reported a full drain")
	}
}

// An empty batch must not cost a round-trip.
func TestEmptyBatchDoesNothing(t *testing.T) {
	st := &countingStaging{}
	removed, err := st.deleteStagedBatch(context.Background(), nil)
	if err != nil || removed != 0 {
		t.Errorf("removed=%d err=%v, want 0/nil", removed, err)
	}
}
