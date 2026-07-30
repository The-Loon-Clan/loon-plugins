package usenet

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/nntp"
)

func fleetPlugin() *Plugin {
	return &Plugin{
		fleet: newProviderFleet(),
		tel:   newTelemetry(),
		core:  &core.Core{Errors: core.NewErrorReporter(core.ErrorAdapter{})},
	}
}

func fleetCfg() Config {
	var cfg Config
	cfg.applyDefaults()
	// Fast failure dials so the failure paths run in test time, not 30s each.
	cfg.DialTimeoutSec = 1
	return cfg
}

func openTestPool(t *testing.T, s *fakeNNTP, size int) *nntp.Pool {
	t.Helper()
	pool := nntp.NewPool(nntp.PoolConfig{
		Addr: s.ln.Addr().String(), Size: size,
		DialTimeout: 2 * time.Second, OpTimeout: 2 * time.Second,
	})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	return pool
}

// Every pool that leaves the fleet map must be Closed exactly once. The
// differing-key race — crawl resolving on pass-start config while backfill
// resolves on a fresh per-round read, or a worker-count change moving
// effectiveConns — overwrote the installed pool WITHOUT closing it: the
// displaced pool's keepalive kept its authenticated sockets alive until
// process exit, counted against the provider's account cap and visible in no
// statistic. install() is the race's resolution point, extracted so this test
// can drive both arms.
func TestFleetInstallClosesWhatItDisplaces(t *testing.T) {
	s := newFakeNNTP(t, false)
	f := newProviderFleet()
	pr := s.asProvider(1, roleActive)

	p1 := openTestPool(t, s, 1)
	if got := f.install(pr, p1, "k1"); got != p1 {
		t.Fatal("first install did not keep the offered pool")
	}

	// Different key: the newcomer wins and the displaced pool is closed.
	p2 := openTestPool(t, s, 1)
	if got := f.install(pr, p2, "k2"); got != p2 {
		t.Fatal("differing-key install did not adopt the new pool")
	}
	if p1.Stats().Open != 0 {
		t.Error("displaced pool still holds connections — the account-cap leak is back")
	}

	// Same key: the incumbent wins and the loser is closed promptly.
	p3 := openTestPool(t, s, 1)
	if got := f.install(pr, p3, "k2"); got != p2 {
		t.Fatal("same-key race did not keep the incumbent")
	}
	if p3.Stats().Open != 0 {
		t.Error("same-key race loser still holds connections")
	}
	if p2.Stats().Open == 0 {
		t.Error("the installed pool was wrongly closed")
	}
}

// A provider that dies AFTER its pool opened must be benched and its backup
// promoted in the SAME resolve. Before this, bench had exactly one call site
// (an Open failure), so a cached corpse pool — every connection discarded to a
// nil slot — was re-selected every pass forever: never benched, the configured
// backup never promoted, and the dead pool still dealt a full share of batch
// workers. And because chooseProviders ran only on the pre-bench down-state,
// even an Open-failure bench left the pass under-strength until the NEXT pass,
// recurring at every cooldown lapse.
func TestOpenFleetBenchesTheDeadAndPromotesTheBackupSamePass(t *testing.T) {
	active := newFakeNNTP(t, false)
	backup := newFakeNNTP(t, false)
	p := fleetPlugin()
	cfg := fleetCfg()
	all := []provider{active.asProvider(1, roleActive), backup.asProvider(2, roleBackup)}
	ctx := context.Background()

	// Healthy pass: the backup is standby capacity, not extra capacity.
	runs, err := p.openFleet(ctx, all, cfg)
	if err != nil || len(runs) != 1 || runs[0].prov.ID != 1 {
		t.Fatalf("healthy pass: runs=%+v err=%v, want the active alone", runs, err)
	}
	poolA := runs[0].pool

	// Kill the active mid-life and let its pool notice: the failed lease
	// discards the connection to a nil slot, which is exactly the state a
	// provider outage leaves behind.
	active.shutdown()
	_ = poolA.Do(ctx, func(c *nntp.Conn) error {
		_, _, _, err := c.Group("a.b.c")
		return err
	})
	if poolA.Stats().Open != 0 {
		t.Fatalf("setup: pool still reports %d open connections after the outage", poolA.Stats().Open)
	}

	// The next resolve must not return the corpse: TopUp finds the server
	// gone, the provider is benched, and the re-selection promotes the backup
	// NOW — not on some future pass.
	runs, err = p.openFleet(ctx, all, cfg)
	if err != nil {
		t.Fatalf("resolve after outage: %v", err)
	}
	if len(runs) != 1 || runs[0].prov.ID != 2 {
		t.Fatalf("after outage runs=%+v, want the backup promoted in the same resolve", runs)
	}
	if !p.fleet.isDown(1, time.Now()) {
		t.Error("the dead active was not benched")
	}
}

// A pool that OPENED and then lost every connection takes no batch workers.
// assignPools' comment always promised dealing "by what each pool actually
// opened"; the code dealt by the resolved budget, so a 50-connection corpse
// took half the workers — each failing its batch in microseconds and pulling
// the next job off the shared channel, starving the healthy account of work
// it could have served.
func TestAssignPoolsSkipsPoolsWithNoLiveConnections(t *testing.T) {
	s := newFakeNNTP(t, false)
	healthy := providerRun{pool: livePool(t, 1), size: 1, prov: provider{Name: "healthy"}}

	deadPool := openTestPool(t, s, 1)
	s.shutdown()
	_ = deadPool.Do(context.Background(), func(c *nntp.Conn) error {
		_, _, _, err := c.Group("a.b.c")
		return err
	})
	if deadPool.Stats().Open != 0 {
		t.Fatalf("setup: dead pool still reports %d open", deadPool.Stats().Open)
	}
	dead := providerRun{pool: deadPool, size: 50, prov: provider{Name: "dead"}}

	got := assignPools([]providerRun{dead, healthy}, 10)
	for _, p := range got {
		if p == deadPool {
			t.Fatal("a worker was dealt to a pool with zero live connections")
		}
	}
	if len(got) != 1 {
		t.Errorf("dealt %d workers, want 1 — exactly the healthy pool's live connection", len(got))
	}
}

// One pool.Open against an unresponsive host must cost about a dial timeout,
// not Size of them. Open dials sequentially and treats a timeout as a
// per-slot failure to skip — against a black-holed host (accepts, never
// greets; or a firewall DROP) that was Size×DialTimeout: 25 minutes at 50
// connections, at the head of every pass, in every job.
func TestFleetGetBoundsAStalledOpen(t *testing.T) {
	silent := newFakeNNTP(t, true)
	f := newProviderFleet()
	pr := silent.asProvider(1, roleActive)

	cfg := fleetCfg() // DialTimeoutSec = 1
	spec := specFor(cfg, 5)

	start := time.Now()
	_, err := f.get(context.Background(), pr, spec)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("get() against a silent host returned a pool")
	}
	// Unbounded, five sequential 1s greeting timeouts cost ≥5s. The 2×dial
	// bound must cut that to ~2-3s (the in-flight dial finishes its timeout).
	if elapsed > 4*time.Second {
		t.Errorf("open against a silent host took %v — the per-open bound is not applied "+
			"(unbounded this is Size×DialTimeout: 25 minutes at 50 connections)", elapsed)
	}
}
