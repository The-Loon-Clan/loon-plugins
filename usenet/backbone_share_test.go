package usenet

import (
	"testing"

	"github.com/the-loon-clan/loon/nntp"
)

// run builds a providerRun with a distinct non-nil pool per name, so assignment
// can be checked by identity. The pools are never dialled.
func run(name, backbone string, size int) providerRun {
	return providerRun{
		prov: provider{Name: name, Host: name + ".example.com", Port: 119, Backbone: backbone},
		pool: &nntp.Pool{},
		size: size,
	}
}

func TestGroupByBackbone(t *testing.T) {
	// Prod's shape: two accounts on netnews, plus one elsewhere.
	runs := []providerRun{
		run("frugal", "netnews", 50),
		run("eunews", "netnews", 50),
		run("other", "omicron", 20),
	}
	got := groupByBackbone(runs)
	if len(got) != 2 {
		t.Fatalf("want 2 backbone buckets, got %d", len(got))
	}
	if len(got[0]) != 2 || got[0][0].prov.Name != "frugal" || got[0][1].prov.Name != "eunews" {
		t.Errorf("netnews bucket wrong: %+v", got[0])
	}
	if len(got[1]) != 1 || got[1][0].prov.Name != "other" {
		t.Errorf("omicron bucket wrong: %+v", got[1])
	}

	// Order must follow activeFleet's choice, so the primary still leads its
	// bucket and is the account that answers GROUP.
	reordered := groupByBackbone([]providerRun{
		run("eunews", "netnews", 50),
		run("frugal", "netnews", 50),
	})
	if reordered[0][0].prov.Name != "eunews" {
		t.Errorf("bucket order must preserve input order, got %s", reordered[0][0].prov.Name)
	}

	if got := groupByBackbone(nil); len(got) != 0 {
		t.Errorf("nil runs: got %d buckets, want 0", len(got))
	}
}

func TestTotalConns(t *testing.T) {
	if got := totalConns([]providerRun{run("a", "bb", 50), run("b", "bb", 50)}); got != 100 {
		t.Errorf("got %d, want 100 — the whole point is that both accounts count", got)
	}
	if got := totalConns(nil); got != 0 {
		t.Errorf("nil: got %d, want 0", got)
	}
}

func TestAssignPools(t *testing.T) {
	a, b := run("a", "bb", 50), run("b", "bb", 50)
	runs := []providerRun{a, b}

	// Even split: dealing one at a time must alternate, not fill A then B.
	// Filling in order is the bug this replaces — it is how one account
	// saturated while the other sat idle.
	got := assignPools(runs, 100)
	if len(got) != 100 {
		t.Fatalf("want 100 assignments, got %d", len(got))
	}
	countA, countB := 0, 0
	for _, w := range got {
		switch w.pool {
		case a.pool:
			countA++
		case b.pool:
			countB++
		default:
			t.Fatal("assigned a pool that is not in the run set")
		}
	}
	if countA != 50 || countB != 50 {
		t.Errorf("uneven split: a=%d b=%d, want 50/50", countA, countB)
	}
	if got[0].pool != a.pool || got[1].pool != b.pool {
		t.Error("must alternate from the first worker, not fill one pool first")
	}

	// Fewer workers than capacity: still spread, not all on the first pool.
	got = assignPools(runs, 4)
	if len(got) != 4 {
		t.Fatalf("want 4, got %d", len(got))
	}
	if got[0].pool != a.pool || got[1].pool != b.pool || got[2].pool != a.pool || got[3].pool != b.pool {
		t.Error("a short pass must still alternate across accounts")
	}

	// Uneven capacity: a small standby account must not be handed work it
	// cannot serve. Once it is full, the remainder goes to whoever has room.
	small := run("small", "bb", 2)
	got = assignPools([]providerRun{a, small}, 10)
	countSmall := 0
	for _, w := range got {
		if w.pool == small.pool {
			countSmall++
		}
	}
	if countSmall != 2 {
		t.Errorf("small pool got %d workers, want exactly its size of 2", countSmall)
	}
	if len(got) != 10 {
		t.Errorf("want 10 assignments, got %d", len(got))
	}

	// Asking for more than the fleet can serve must terminate rather than spin.
	// batchWorkers caps at the same total so this is unreachable in practice,
	// but an unreachable infinite loop is still an infinite loop.
	got = assignPools([]providerRun{small}, 99)
	if len(got) != 2 {
		t.Errorf("over-request: got %d, want the 2 it can actually serve", len(got))
	}
	if got := assignPools(nil, 5); len(got) != 0 {
		t.Errorf("no providers: got %d, want 0", len(got))
	}
}

// primaryBackbone: the first ENABLED server in listServers order wins — the
// same backbone actionResetWatermark targets, so the prompt and the click
// cannot disagree.
func TestPrimaryBackbone(t *testing.T) {
	if got := primaryBackbone(nil); got != "" {
		t.Errorf("empty slice = %q, want \"\"", got)
	}
	if got := primaryBackbone([]provider{{ID: 1, Backbone: "omicron"}}); got != "" {
		t.Errorf("disabled-only = %q, want \"\"", got)
	}
	servers := []provider{
		{ID: 1, Backbone: "omicron"},                 // disabled
		{ID: 2, Backbone: "netnews", Enabled: true},  // first enabled wins
		{ID: 3, Backbone: "omicron", Enabled: true},
	}
	if got := primaryBackbone(servers); got != "netnews" {
		t.Errorf("primaryBackbone = %q, want netnews", got)
	}
	// An unset backbone resolves to the per-server key.
	if got := primaryBackbone([]provider{{ID: 7, Enabled: true}}); got != "srv:7" {
		t.Errorf("bare server = %q, want srv:7", got)
	}
}
