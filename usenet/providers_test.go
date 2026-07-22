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

// TestProviderPoolKey: the key must change with anything that alters how we dial,
// so a settings edit rebuilds the pool instead of silently reusing a stale one.
func TestProviderPoolKey(t *testing.T) {
	base := provider{ID: 1, Host: "news.example.com", Port: 563, TLS: true, Username: "u", Connections: 10}
	k := base.poolKey(20)

	for _, mut := range []struct {
		name string
		p    provider
	}{
		{"host", func() provider { c := base; c.Host = "other"; return c }()},
		{"port", func() provider { c := base; c.Port = 119; return c }()},
		{"tls", func() provider { c := base; c.TLS = false; return c }()},
		{"user", func() provider { c := base; c.Username = "v"; return c }()},
		{"conns", func() provider { c := base; c.Connections = 11; return c }()},
		{"id", func() provider { c := base; c.ID = 2; return c }()},
	} {
		if mut.p.poolKey(20) == k {
			t.Errorf("changing %s did not change the pool key", mut.name)
		}
	}
	if base.poolKey(20) != k {
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
	f.bench(p1, now)
	if !f.isDown(1, now) {
		t.Error("a benched provider should be down")
	}
	if !f.isDown(1, now.Add(providerDownCooldown-time.Second)) {
		t.Error("should still be down inside the cooldown")
	}
	if f.isDown(1, now.Add(providerDownCooldown+time.Second)) {
		t.Error("should recover after the cooldown — a benched provider must get retried")
	}
}
