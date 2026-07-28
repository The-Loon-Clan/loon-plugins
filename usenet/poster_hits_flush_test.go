package usenet

import (
	"context"
	"testing"
)

// A crawl pass can run for HOURS through the catch-up loop. Anything scoped to
// the pass — config read in, tallies written out — is therefore invisible for
// hours, which is indistinguishable from broken. These pin the round scope.

// recordingPosterStore records what was written. It EMBEDS Store so all 78
// methods exist without bodies — only recordPosterHits is exercised, and any
// other call would nil-panic, which is the correct outcome for a test that has
// wandered outside what it means to cover.
type recordingPosterStore struct {
	Store
	calls []map[posterHitKey]*posterHitVal
}

func (r *recordingPosterStore) recordPosterHits(_ context.Context, hits map[posterHitKey]*posterHitVal) error {
	copied := make(map[posterHitKey]*posterHitVal, len(hits))
	for k, v := range hits {
		copied[k] = &posterHitVal{count: v.count, sample: v.sample}
	}
	r.calls = append(r.calls, copied)
	return nil
}

// Flushing per round must not double-count against the deferred pass-end flush.
// drain() resets the accumulator, so the second flush has nothing to write;
// without that, every round's tallies would be re-added at pass end.
func TestPerRoundFlushDoesNotDoubleCount(t *testing.T) {
	rec := &recordingPosterStore{}
	p := &Plugin{st: rec, posterHits: newPosterHits(), tel: newTelemetry()}

	p.posterHits.note("tsukihime", "ingest", "staged", "first")
	p.posterHits.note("tsukihime", "ingest", "staged", "second")
	p.flushPosterHits(context.Background()) // end of round 1

	// End of pass, with no further articles: nothing left to write.
	p.flushPosterHits(context.Background())

	if len(rec.calls) != 1 {
		t.Fatalf("recordPosterHits called %d times, want 1 — an empty drain must not write", len(rec.calls))
	}
	total := int64(0)
	for _, v := range rec.calls[0] {
		total += v.count
	}
	if total != 2 {
		t.Errorf("recorded count = %d, want 2", total)
	}
}

// A second round's tallies are written as their own batch rather than being
// merged into the first — otherwise the round-scoped flush silently loses
// whatever arrives after it.
func TestPerRoundFlushWritesEachRound(t *testing.T) {
	rec := &recordingPosterStore{}
	p := &Plugin{st: rec, posterHits: newPosterHits(), tel: newTelemetry()}

	p.posterHits.note("tsukihime", "ingest", "staged", "round one")
	p.flushPosterHits(context.Background())
	p.posterHits.note("tsukihime", "build", "built", "round two")
	p.flushPosterHits(context.Background())

	if len(rec.calls) != 2 {
		t.Fatalf("got %d writes, want one per round", len(rec.calls))
	}
	if _, ok := rec.calls[1][posterHitKey{"tsukihime", "build", "built"}]; !ok {
		t.Error("the second round's tally was not written")
	}
}

// Flushing per GROUP rather than per round is what makes attribution readable
// while a long pass is still running. Many flushes across one pass must still
// sum to the truth — an upsert that replaced instead of accumulating would show
// only the last group's tally, which looks like a quiet crawler.
func TestManyFlushesSumToTheTruth(t *testing.T) {
	rec := &recordingPosterStore{}
	p := &Plugin{st: rec, posterHits: newPosterHits(), tel: newTelemetry()}

	// Five groups land in one round; the same poster appears in three of them.
	for i := 0; i < 5; i++ {
		if i%2 == 0 {
			p.posterHits.note("tsukihime", "ingest", "staged", "subject")
		}
		p.flushPosterHits(context.Background())
	}

	writes, total := 0, int64(0)
	for _, call := range rec.calls {
		for k, v := range call {
			if k.poster == "tsukihime" {
				writes++
				total += v.count
			}
		}
	}
	if writes != 3 {
		t.Errorf("%d writes carried the poster, want 3 — a flush with nothing new must not write", writes)
	}
	if total != 3 {
		t.Errorf("flushed counts sum to %d, want 3", total)
	}
}
