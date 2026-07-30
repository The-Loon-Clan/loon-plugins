package usenet

import (
	"context"
	"testing"
)

// The deadlock this gate shipped with, reproduced exactly.
//
// Observed in production: pressure 74% against an 85% pause threshold, ready
// queue at 119, and every job idle 92% of the time. Not throttled — stuck. The
// hysteresis had latched when pressure once touched 85%, and it only releases
// below 70%. But what fills memory here is INCOMPLETE sets: releases waiting for
// segments that nothing except the backfill can fetch. The builder cannot
// assemble those, so memory never fell, the latch never cleared, and the one
// component that could have broken the cycle was the one being held off.
//
// Memory cannot drop until sets complete. Sets cannot complete until they are
// fetched. The fetcher was stopped waiting for memory to drop.
func TestBackfillDoesNotDeadlockWithNothingToBuild(t *testing.T) {
	cfg := Config{
		BackfillPressureHighPct:    85,
		BackfillPressureLowPct:     70,
		BackfillPressureCeilingPct: 92,
	}

	// The exact production state: latched, above the low mark, nothing queued.
	p := &Plugin{staging: fakePressure{pr: 0.74, ready: 0}}
	p.backfillPaused = true // latched by an earlier excursion past 85%

	yield, pr, _ := p.backfillYields(context.Background(), cfg)
	if yield {
		t.Errorf("backfill still yielding at %.0f%% with an EMPTY ready queue — this is the "+
			"deadlock: the memory is incomplete sets, only fetching completes them, and the "+
			"fetcher is what is being stopped", pr*100)
	}
	if p.backfillPaused {
		t.Error("the latch was not cleared, so the next round deadlocks again")
	}

	// The safety half must survive. With nothing to build, it keeps going — but
	// not past the ceiling, because at maxmemory Redis EVICTS rather than
	// refusing the write, and what it evicts is the sets still assembling. That
	// is how 97 million keys were destroyed.
	p = &Plugin{staging: fakePressure{pr: 0.95, ready: 0}}
	if yield, _, _ := p.backfillYields(context.Background(), cfg); !yield {
		t.Error("backfill kept fetching past the ceiling with staging nearly full — the next " +
			"write evicts a half-assembled release")
	}

	// And when there IS something to build, pausing is correct: the builder can
	// actually reduce the pressure, so yielding to it is not a deadlock.
	p = &Plugin{staging: fakePressure{pr: 0.86, ready: 50_000}}
	if yield, _, _ := p.backfillYields(context.Background(), cfg); !yield {
		t.Error("backfill did not yield at 86% with 50,000 sets queued — the builder is behind " +
			"and fetching more only crowds it out")
	}

	// A backend that cannot answer cheaply (pg computes completeness per draw)
	// reports -1, which must fall back to the plain hysteresis rather than being
	// mistaken for an empty queue.
	p = &Plugin{staging: fakePressure{pr: 0.74, ready: -1}}
	p.backfillPaused = true
	if yield, _, _ := p.backfillYields(context.Background(), cfg); !yield {
		t.Error("an unknown ready depth was treated as an empty queue; pg mode must keep the " +
			"behaviour it had before the probe existed")
	}
}

// Below the pause threshold with a queue present, nothing should stop it — the
// case that was already working and must not regress.
func TestBackfillRunsFreelyBelowTheThreshold(t *testing.T) {
	cfg := Config{
		BackfillPressureHighPct:    85,
		BackfillPressureLowPct:     70,
		BackfillPressureCeilingPct: 92,
	}
	for _, ready := range []int64{0, 500, -1} {
		p := &Plugin{staging: fakePressure{pr: 0.40, ready: ready}}
		if yield, _, _ := p.backfillYields(context.Background(), cfg); yield {
			t.Errorf("ready=%d: yielded at 40%% pressure", ready)
		}
	}
}
