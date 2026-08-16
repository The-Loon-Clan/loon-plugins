package usenet

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// The classifier's job is to be BOUNDED. Its output is a primary key column,
// so a label carrying an ephemeral port or an article number turns a few
// hundred rows a month into a few million.
func TestClassifyOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, outcomeOK},
		// The one that actually happens. Wrapped three deep, and anchoring at
		// the start of the string would classify it as "other" — which is the
		// same as not classifying it.
		{"wrapped nntp code", errors.New("overview failed: XOVER: 511 issue with group (OVER: 500 command unimplemented)"), "511"},
		{"bare nntp code", errors.New("430 no such article"), "430"},
		{"idle timeout carries a code", errors.New("400 Idle timeout."), "400"},
		{"pool exhaustion", errors.New("nntp: no usable connection in pool"), "pool"},
		{"all busy is also pool", errors.New("nntp: all connections busy"), "pool"},
		{"transport timeout", errors.New("read tcp 172.18.0.6:45686->23.182.120.44:443: i/o timeout"), "timeout"},
		{"reset", errors.New("write tcp 10.0.0.1:5->1.2.3.4:119: connection reset by peer"), "reset"},
		{"cancelled", errors.New("context canceled"), "cancelled"},
		{"unrecognised", errors.New("something entirely new"), outcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyOutcome(tc.err); got != tc.want {
				t.Errorf("classifyOutcome(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// An ephemeral port is four or five digits and appears in every transport
// error. Reading one as an NNTP status would produce an unbounded label set —
// the exact cardinality explosion this column cannot afford.
func TestClassifyOutcomeIgnoresPortNumbers(t *testing.T) {
	seen := map[string]bool{}
	for port := 40000; port < 40010; port++ {
		err := fmt.Errorf("read tcp 172.18.0.6:%d->23.182.120.44:443: i/o timeout", port)
		seen[classifyOutcome(err)] = true
	}
	if len(seen) != 1 {
		t.Errorf("ten ports produced %d distinct outcomes (%v) — the label set is unbounded", len(seen), seen)
	}
	for k := range seen {
		if k != "timeout" {
			t.Errorf("outcome = %q, want timeout", k)
		}
	}
}

// noteErr exists so a call site cannot count the failure and forget the
// success, which is how you end up with a numerator and no denominator.
func TestOpCounterCountsBothOutcomes(t *testing.T) {
	c := newOpCounter()
	for i := 0; i < 7; i++ {
		c.noteErr("overview", nil)
	}
	c.noteErr("overview", errors.New("511 issue with group"))
	c.noteErr("overview", errors.New("511 issue with group"))

	got := c.drain()
	if got[opStatKey{"overview", outcomeOK}] != 7 {
		t.Errorf("ok = %d, want 7", got[opStatKey{"overview", outcomeOK}])
	}
	if got[opStatKey{"overview", "511"}] != 2 {
		t.Errorf("511 = %d, want 2", got[opStatKey{"overview", "511"}])
	}
	// Drained counters must not be counted twice on the next flush.
	if again := c.drain(); len(again) != 0 {
		t.Errorf("a second drain returned %d counters", len(again))
	}
}

// The counter is written from every crawl worker at once.
func TestOpCounterIsConcurrencySafe(t *testing.T) {
	c := newOpCounter()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				c.noteErr("overview", nil)
			}
		}()
	}
	wg.Wait()
	if got := c.drain()[opStatKey{"overview", outcomeOK}]; got != 4000 {
		t.Errorf("counted %d, want 4000", got)
	}
}

// A nil counter must be inert rather than panic: the plugin constructs it in
// Start, and a pass reached before that (or in a test) should not crash.
func TestOpCounterNilIsSafe(t *testing.T) {
	var c *opCounter
	c.note("op", "ok")
	c.noteErr("op", errors.New("boom"))
	if got := c.drain(); got != nil {
		t.Errorf("nil counter drained %v", got)
	}
}
