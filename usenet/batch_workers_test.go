package usenet

import "testing"

func TestBatchWorkers(t *testing.T) {
	cases := []struct {
		name        string
		conns, jobs int
		want        int
	}{
		// The lone-worker case: the whole account cap is this pass's budget.
		{"one crawler host, plenty of work", 50, 500, 50},
		// The case the fix is for. With two crawler hosts the account cap is
		// split, so the pool opens 25 — but the worker count used to come from
		// the site-wide connections setting (50) and put 25 surplus goroutines
		// into the pool's blocking fallback, contending for connections that
		// were never going to exist.
		{"fleet-split budget bounds the workers", 25, 500, 25},
		// Never spawn more goroutines than there are batches.
		{"less work than connections", 50, 3, 3},
		{"exactly as much work as connections", 10, 10, 10},
		// Degenerate inputs must still produce a runnable pass rather than a
		// pool of zero goroutines that silently drains nothing.
		{"no jobs", 50, 0, 1},
		{"zero budget", 0, 100, 1},
		{"negative budget", -5, 100, 1},
	}
	for _, c := range cases {
		if got := batchWorkers(c.conns, c.jobs); got != c.want {
			t.Errorf("%s: batchWorkers(%d, %d) = %d, want %d",
				c.name, c.conns, c.jobs, got, c.want)
		}
	}
}
