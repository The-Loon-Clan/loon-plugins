package usenet

import (
	"sync"
	"testing"
)

// The counter itself. What matters is that it cannot lose or double-count a
// pass's numbers, and that the sample stays stable while a pass runs — an admin
// reading the table should not be chasing a value that moves under them.

func TestBuildOutcomes_CountsPerReason(t *testing.T) {
	b := newBuildOutcomes()
	b.note(outcomeIncomplete, "first.incomplete")
	b.note(outcomeIncomplete, "second.incomplete")
	b.note(outcomeBuilt, "a.release")
	b.note(outcomeDuplicate, "a.dupe")

	if got := b.total(outcomeIncomplete); got != 2 {
		t.Errorf("incomplete = %d, want 2", got)
	}
	if got := b.total(outcomeBuilt); got != 1 {
		t.Errorf("built = %d, want 1", got)
	}
	if got := b.total(outcomeJunk); got != 0 {
		t.Errorf("junk = %d, want 0 for a reason never noted", got)
	}
}

// The FIRST sample is kept, not the last. A build pass drains hundreds of sets;
// keeping the last means the stored sample changes on every flush and cannot be
// used to judge whether a bucket is catching junk or eating releases.
func TestBuildOutcomes_KeepsTheFirstSample(t *testing.T) {
	b := newBuildOutcomes()
	b.note(outcomeJunk, "first.subject")
	b.note(outcomeJunk, "second.subject")
	b.note(outcomeJunk, "third.subject")

	out := b.drain()
	if out[outcomeJunk].sample != "first.subject" {
		t.Errorf("sample = %q, want first.subject", out[outcomeJunk].sample)
	}
	if out[outcomeJunk].count != 3 {
		t.Errorf("count = %d, want 3", out[outcomeJunk].count)
	}
}

// drain resets. A flush failure must lose one pass's numbers rather than
// double-count them into the next pass's upsert.
func TestBuildOutcomes_DrainResets(t *testing.T) {
	b := newBuildOutcomes()
	b.note(outcomeBuilt, "x")
	if len(b.drain()) != 1 {
		t.Fatal("first drain returned nothing")
	}
	if got := b.drain(); len(got) != 0 {
		t.Errorf("second drain returned %+v, want empty — the counts were not reset", got)
	}
	if got := b.total(outcomeBuilt); got != 0 {
		t.Errorf("built = %d after drain, want 0", got)
	}
}

// nil-safe, because the accounting is threaded through the build path
// optionally and must never change what the pass does.
func TestBuildOutcomes_NilIsInert(t *testing.T) {
	var b *buildOutcomes
	b.note(outcomeBuilt, "x") // must not panic
	if got := b.total(outcomeBuilt); got != 0 {
		t.Errorf("total on nil = %d, want 0", got)
	}
	if got := b.drain(); got != nil {
		t.Errorf("drain on nil = %+v, want nil", got)
	}
}

// The build pass notes from one goroutine today, but the counter sits beside
// filterHits which is written from pool workers — so it is mutex-guarded and
// that should stay true under -race.
func TestBuildOutcomes_ConcurrentNotes(t *testing.T) {
	b := newBuildOutcomes()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.note(outcomeIncomplete, "s")
		}()
	}
	wg.Wait()
	if got := b.total(outcomeIncomplete); got != 50 {
		t.Errorf("incomplete = %d, want 50", got)
	}
}

func TestSortedOutcomeKeysIsDeterministic(t *testing.T) {
	b := newBuildOutcomes()
	for _, r := range []buildOutcome{outcomeStoreError, outcomeBuilt, outcomeJunk, outcomeIncomplete} {
		b.note(r, "s")
	}
	keys := sortedOutcomeKeys(b.drain())
	want := []buildOutcome{outcomeBuilt, outcomeIncomplete, outcomeJunk, outcomeStoreError}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

// Every outcome const must be distinct: two reasons sharing a string would
// silently merge two buckets in the stored table, which is exactly the kind of
// mistake the typed const set exists to prevent.
func TestBuildOutcomeNamesAreUnique(t *testing.T) {
	all := []buildOutcome{
		outcomeBuilt, outcomeIncomplete, outcomeEmpty, outcomeDuplicate,
		outcomeBlockedExt, outcomeBlacklist, outcomeJunk,
		outcomeLoadError, outcomeXMLError, outcomeGzipError, outcomeStoreError,
	}
	seen := map[buildOutcome]bool{}
	for _, r := range all {
		if r == "" {
			t.Error("an outcome const is the empty string")
		}
		if seen[r] {
			t.Errorf("duplicate outcome name %q — two buckets would merge", r)
		}
		seen[r] = true
	}
}
