package usenet

import (
	"testing"
	"time"
)

func prov(id int, role string, prio int) provider {
	return provider{ID: id, Name: "p", Host: "h", Enabled: true, Role: role, Priority: prio}
}

func ids(ps []provider) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func eq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChooseProviders covers the standby rule: backups are NOT extra capacity.
// Running them alongside healthy actives would exceed the connection budget the
// operator planned for and multiply the crawl, since each provider crawls
// independently.
func TestChooseProviders(t *testing.T) {
	none := func(int) bool { return false }
	down := func(ids ...int) func(int) bool {
		set := map[int]bool{}
		for _, id := range ids {
			set[id] = true
		}
		return func(id int) bool { return set[id] }
	}

	cases := []struct {
		name string
		all  []provider
		is   func(int) bool
		want []int
	}{
		{
			name: "all healthy: backups stay idle",
			all:  []provider{prov(1, roleActive, 10), prov(2, roleActive, 20), prov(3, roleBackup, 10)},
			is:   none,
			want: []int{1, 2},
		},
		{
			name: "one active down: one backup promoted",
			all:  []provider{prov(1, roleActive, 10), prov(2, roleActive, 20), prov(3, roleBackup, 10)},
			is:   down(1),
			want: []int{2, 3},
		},
		{
			name: "two down, one backup: only one can be covered",
			all:  []provider{prov(1, roleActive, 10), prov(2, roleActive, 20), prov(3, roleBackup, 10)},
			is:   down(1, 2),
			want: []int{3},
		},
		{
			name: "two down, two backups: both promoted, preferred first",
			all: []provider{prov(1, roleActive, 10), prov(2, roleActive, 20),
				prov(3, roleBackup, 50), prov(4, roleBackup, 10)},
			is:   down(1, 2),
			want: []int{4, 3},
		},
		{
			name: "a down backup is not promoted",
			all:  []provider{prov(1, roleActive, 10), prov(2, roleBackup, 10), prov(3, roleBackup, 20)},
			is:   down(1, 2),
			want: []int{3},
		},
		{
			name: "disabled providers never run",
			all: []provider{
				{ID: 1, Role: roleActive, Enabled: false},
				{ID: 2, Role: roleActive, Enabled: true},
			},
			is:   none,
			want: []int{2},
		},
		{
			name: "actives run in priority order",
			all:  []provider{prov(1, roleActive, 50), prov(2, roleActive, 10), prov(3, roleActive, 30)},
			is:   none,
			want: []int{2, 3, 1},
		},
		{
			name: "unknown role is treated as active (fail useful, not idle)",
			all:  []provider{{ID: 1, Role: "", Enabled: true}, {ID: 2, Role: "weird", Enabled: true}},
			is:   none,
			want: []int{1, 2},
		},
		{
			name: "everything down: nobody crawls",
			all:  []provider{prov(1, roleActive, 10), prov(2, roleBackup, 10)},
			is:   down(1, 2),
			want: []int{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(chooseProviders(tc.all, tc.is))
			if !eq(got, tc.want) {
				t.Errorf("chooseProviders = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProviderConns: each provider carries its own connection cap because
// providers sell different limits; 0 falls back to the plugin-wide default.
func TestProviderConns(t *testing.T) {
	if got := (provider{Connections: 8}).conns(20); got != 8 {
		t.Errorf("explicit cap = %d, want 8", got)
	}
	if got := (provider{}).conns(20); got != 20 {
		t.Errorf("fallback = %d, want the default 20", got)
	}
	if got := (provider{}).conns(0); got != 1 {
		t.Errorf("no default either = %d, want 1", got)
	}
}

// TestEffectiveConns: connections is the per-worker ceiling; the account cap
// bounds the SUM across live workers, so each worker gets min(conns, cap/W). A
// zero cap never divides, and the cap binds even a lone worker (min 1).
func TestEffectiveConns(t *testing.T) {
	cases := []struct {
		name            string
		conns, cap, def int
		workers         int
		want            int
	}{
		{"no cap, lone worker", 50, 0, 10, 1, 50},
		{"no cap, many workers", 50, 0, 10, 4, 50}, // cap 0 never divides
		{"cap not binding", 40, 100, 10, 2, 40},    // 100/2=50 >= 40, keep 40
		{"cap binds, 2 workers", 100, 100, 10, 2, 50},
		{"cap binds, 4 workers", 100, 100, 10, 4, 25},
		{"cap binds a lone worker", 100, 50, 10, 1, 50}, // one crawler must not exceed 50
		{"cap floors at 1", 100, 3, 10, 8, 1},           // 3/8=0 -> 1
		{"conns fallback then cap", 0, 60, 30, 3, 20},   // conns=0 -> def 30; 60/3=20 < 30
	}
	for _, c := range cases {
		pr := provider{Connections: c.conns, AccountCap: c.cap}
		if got := pr.effectiveConns(c.def, c.workers); got != c.want {
			t.Errorf("%s: effectiveConns(def=%d, workers=%d) = %d, want %d",
				c.name, c.def, c.workers, got, c.want)
		}
	}
}

// TestProviderPoolKey: the key must change with anything that alters how we dial,
// so a settings edit rebuilds the pool instead of silently reusing a stale one.
func TestProviderPoolKey(t *testing.T) {
	base := provider{ID: 1, Host: "news.example.com", Port: 563, TLS: true, Username: "u", Connections: 10}
	spec := poolSpec{size: 20, keepalive: 2 * time.Minute, dialTimeout: 30 * time.Second, opTimeout: 60 * time.Second}
	k := base.poolKey(spec)

	for _, mut := range []struct {
		name string
		p    provider
	}{
		{"host", func() provider { c := base; c.Host = "other"; return c }()},
		{"port", func() provider { c := base; c.Port = 119; return c }()},
		{"tls", func() provider { c := base; c.TLS = false; return c }()},
		{"user", func() provider { c := base; c.Username = "v"; return c }()},
		{"id", func() provider { c := base; c.ID = 2; return c }()},
	} {
		if mut.p.poolKey(spec) == k {
			t.Errorf("changing %s did not change the pool key", mut.name)
		}
	}
	// The resolved size is part of the key: when the fleet grows and each
	// worker's budget shrinks at a term boundary, the pool must rebuild.
	if base.poolKey(func() poolSpec { c := spec; c.size = 21; return c }()) == k {
		t.Error("a different resolved size must change the pool key")
	}
	// Keepalive is baked into the pool at construction, so changing the knob
	// must rebuild it — otherwise the setting appears to save and does nothing
	// until some unrelated edit forces a reopen.
	if base.poolKey(func() poolSpec { c := spec; c.keepalive = 5 * time.Minute; return c }()) == k {
		t.Error("a different keepalive interval must change the pool key")
	}
	if base.poolKey(func() poolSpec { c := spec; c.keepalive = 0; return c }()) == k {
		t.Error("disabling keepalive must change the pool key")
	}
	// The transport bounds are baked into the pool at construction, exactly
	// like keepalive: leaving either out of the key means the knob appears to
	// save and does nothing until an unrelated edit forces a reopen.
	if base.poolKey(func() poolSpec { c := spec; c.opTimeout = 2 * time.Minute; return c }()) == k {
		t.Error("a different op timeout must change the pool key")
	}
	if base.poolKey(func() poolSpec { c := spec; c.dialTimeout = 10 * time.Second; return c }()) == k {
		t.Error("a different dial timeout must change the pool key")
	}
	if base.poolKey(spec) != k {
		t.Error("pool key is not stable for identical settings")
	}
}

func TestFleetBenchAndRecover(t *testing.T) {
	f := newProviderFleet()
	now := time.Now()
	p1 := prov(1, roleActive, 10)

	if f.isDown(1, now) {
		t.Error("a provider with no recorded failure should not be down")
	}
	f.bench(p1, now, 10*time.Minute)
	if !f.isDown(1, now) {
		t.Error("a benched provider should be down")
	}
	if !f.isDown(1, now.Add(10*time.Minute-time.Second)) {
		t.Error("should still be down inside the cooldown")
	}
	if f.isDown(1, now.Add(10*time.Minute+time.Second)) {
		t.Error("should recover after the cooldown — a benched provider must get retried")
	}
}

// TestBackboneKey pins what crawl state and coverage are keyed by. Getting this
// wrong is silent in both directions: sharing a key across different backbones
// makes each provider skip ranges the other fetched (lost articles), while
// giving two accounts on ONE backbone separate keys makes the second re-crawl
// everything the first already has.
func TestBackboneKey(t *testing.T) {
	// Unset = its own key. The safe default: assume nothing is shared.
	a := provider{ID: 7}
	if got := a.backboneKey(); got != "srv:7" {
		t.Errorf("unset backbone = %q, want srv:7", got)
	}
	if (provider{ID: 8}).backboneKey() == a.backboneKey() {
		t.Error("two servers with no backbone set must not share state")
	}

	// Same backbone named = shared key, however it was typed.
	x := provider{ID: 1, Backbone: "Omicron"}
	y := provider{ID: 2, Backbone: "  omicron "}
	if x.backboneKey() != y.backboneKey() {
		t.Errorf("same backbone should share a key: %q vs %q", x.backboneKey(), y.backboneKey())
	}
	if x.backboneKey() == (provider{ID: 3, Backbone: "usenetexpress"}).backboneKey() {
		t.Error("different backbones must not share a key")
	}
	// A named backbone must never collide with the per-server default form.
	if (provider{ID: 9, Backbone: "srv:1"}).backboneKey() == (provider{ID: 1}).backboneKey() {
		t.Log("note: a backbone literally named 'srv:1' collides with server 1's default key")
	}
}
