//go:build integration

package usenet

import (
	"context"
	"testing"
)

// The full apply → rollback cycle against a real database. The point is that
// the token restores the table as it WAS, including rows the apply never
// touched — a rollback that only re-enables what it disabled would silently
// turn on a watch the operator had already retired.
func TestPosterWatchApplyThenRollbackRestoresExactly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	o := optimizer{p: &Plugin{st: s}}

	for _, w := range []posterWatchRow{
		{Pattern: "aninzb", Note: "checking uploads", Enabled: true},
		{Pattern: "tsukihime", Note: "", Enabled: true},
		{Pattern: "retired", Note: "done with this one", Enabled: false},
	} {
		if err := s.setPosterWatch(ctx, w.Pattern, w.Note, w.Enabled); err != nil {
			t.Fatal(err)
		}
	}

	rec, err := o.Inspect(ctx, optPosterWatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Changes) != 2 || rec.NoOp {
		t.Fatalf("inspect proposed %d change(s), want the 2 ACTIVE watches only: %+v", len(rec.Changes), rec.Changes)
	}
	if len(rec.Evidence) < 2 {
		t.Error("a recommendation without evidence is a guess with a UI")
	}

	applied, err := o.Apply(ctx, optPosterWatch, optPosterWatch)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Changed != 2 {
		t.Errorf("changed %d, want 2", applied.Changed)
	}
	// The fast path is what this buys: no pattern may remain enabled.
	if live, err := s.posterWatchPatterns(ctx); err != nil || len(live) != 0 {
		t.Fatalf("after apply: %d watch(es) still enabled (err=%v)", len(live), err)
	}

	// Applying twice must be a no-op, not an error, and must still hand back a
	// usable token — an agent retrying should not be punished for it.
	again, err := o.Apply(ctx, optPosterWatch, optPosterWatch)
	if err != nil || again.Changed != 0 || again.RollbackToken == "" {
		t.Errorf("second apply: changed=%d err=%v token=%q", again.Changed, err, again.RollbackToken)
	}

	if err := o.Rollback(ctx, applied.RollbackToken); err != nil {
		t.Fatal(err)
	}
	rows, err := s.posterWatchRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]posterWatchRow{}
	for _, w := range rows {
		got[w.Pattern] = w
	}
	if len(got) != 3 {
		t.Fatalf("rollback left %d row(s), want 3 — disabling must never delete", len(got))
	}
	for _, want := range []posterWatchRow{
		{Pattern: "aninzb", Note: "checking uploads", Enabled: true},
		{Pattern: "tsukihime", Note: "", Enabled: true},
		// The already-disabled one must STAY disabled. This is why the token
		// carries every row rather than only the ones it changed.
		{Pattern: "retired", Note: "done with this one", Enabled: false},
	} {
		if got[want.Pattern] != want {
			t.Errorf("%s restored as %+v, want %+v", want.Pattern, got[want.Pattern], want)
		}
	}
}

// The evidence number has to come from the outcome table, not from a constant.
func TestTitleRejectableTotalsCountsOnlyTitleDecidableReasons(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.recordBuildOutcomes(ctx, map[buildOutcome]*outcomeVal{
		outcomeJunk:       {count: 100, sample: "j"},
		outcomeBlacklist:  {count: 20, sample: "b"},
		outcomeBlockedExt: {count: 5, sample: "e"},
		// Not title-decidable: these needed the articles regardless, so
		// counting them would overstate what the fast path could have saved.
		outcomeBuilt:      {count: 900, sample: "ok"},
		outcomeIncomplete: {count: 7000, sample: "part"},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.titleRejectableTotals(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 125 {
		t.Errorf("got %d, want 125 (junk+blacklist+blocked_ext only)", n)
	}
}
