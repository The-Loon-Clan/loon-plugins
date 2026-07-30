package usenet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/nntp"
)

// Provider roles.
const (
	roleActive = "active" // crawled every pass
	roleBackup = "backup" // idle; promoted only to cover a failing active provider
)

// provider is one configured news server. Each has its own connection pool, its
// own per-group watermarks, and its own coverage — article numbers are assigned
// per server, so nothing numeric is shared between providers.
type provider struct {
	ID          int
	Name        string
	Host        string
	Port        int
	TLS         bool
	Username    string
	Password    string
	Enabled     bool
	Role        string
	Priority    int
	Connections int    // per-worker pool size; 0 = fall back to the plugin-wide default
	AccountCap  int    // fleet total this account allows here; 0 = no cap (each worker gets its full per-worker size)
	Backbone    string // shared numbering identity; empty = this server alone
}

// backboneKey is what crawl state and coverage are keyed by. Article numbers are
// assigned per backbone, so two accounts on the SAME backbone must share
// watermarks (the second is extra connections, not extra coverage) while
// different backbones must never share them.
//
// An unset backbone means "assume nothing" and gives the server its own key.
// That is the safe default: wrongly treating two servers as one backbone makes
// each skip ranges the other fetched, silently losing articles.
func (pr provider) backboneKey() string {
	if b := strings.TrimSpace(pr.Backbone); b != "" {
		return strings.ToLower(b)
	}
	return fmt.Sprintf("srv:%d", pr.ID)
}

func (pr provider) addr() string {
	port := pr.Port
	if port == 0 {
		port = 119
	}
	return fmt.Sprintf("%s:%d", pr.Host, port)
}

func (pr provider) label() string {
	if pr.Name != "" {
		return pr.Name
	}
	return pr.Host
}

// conns resolves the provider's connection cap. Providers sell different limits,
// so this is per-provider with a plugin-wide fallback.
func (pr provider) conns(def int) int {
	if pr.Connections > 0 {
		return pr.Connections
	}
	if def > 0 {
		return def
	}
	return 1
}

// effectiveConns is the per-worker pool size once the fleet cap is applied.
// conns() gives the per-worker ceiling; when AccountCap is set it also bounds
// the SUM across the `workers` live crawlers, so each opens at most
// AccountCap/workers. The smaller of the two wins (min 1). The cap binds even
// for a lone worker — one crawler must not exceed the account limit either. A
// zero cap leaves the per-worker size untouched.
func (pr provider) effectiveConns(def, workers int) int {
	perWorker := pr.conns(def)
	if pr.AccountCap > 0 && workers > 0 {
		share := pr.AccountCap / workers
		if share < 1 {
			share = 1
		}
		if share < perWorker {
			return share
		}
	}
	return perWorker
}

// poolSpec is everything about a pool that is decided by config rather than by
// the provider row: the resolved per-worker size plus the transport bounds
// that are baked into the pool at construction.
type poolSpec struct {
	size        int
	keepalive   time.Duration
	dialTimeout time.Duration
	opTimeout   time.Duration
}

func specFor(cfg Config, size int) poolSpec {
	return poolSpec{
		size:        size,
		keepalive:   time.Duration(cfg.KeepaliveMin) * time.Minute,
		dialTimeout: time.Duration(cfg.DialTimeoutSec) * time.Second,
		opTimeout:   time.Duration(cfg.OpTimeoutSec) * time.Second,
	}
}

// poolKey changes whenever anything about how we'd dial changes, so the pool is
// rebuilt rather than silently kept against stale settings. Everything in
// poolSpec is part of the identity because all of it is baked into the pool at
// construction — without it here, changing a knob would leave the old pool
// running until some unrelated setting forced a reopen.
func (pr provider) poolKey(spec poolSpec) string {
	return fmt.Sprintf("%d|%s|%s|%t|%d|%s|%s|%s",
		pr.ID, pr.addr(), pr.Username, pr.TLS, spec.size, spec.keepalive, spec.dialTimeout, spec.opTimeout)
}

// chooseProviders decides who crawls this pass: every healthy active provider,
// plus one healthy backup promoted for each active that is currently down.
//
// Backups are not extra capacity — they are standby capacity. Running them
// alongside healthy actives would quietly exceed the connection budget the
// operator planned for, and (since each provider crawls independently) multiply
// the work rather than share it.
func chooseProviders(all []provider, isDown func(id int) bool) []provider {
	var active, backup []provider
	for _, p := range all {
		if !p.Enabled {
			continue
		}
		switch p.Role {
		case roleBackup:
			backup = append(backup, p)
		default:
			active = append(active, p)
		}
	}
	byPriority := func(s []provider) {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].Priority != s[j].Priority {
				return s[i].Priority < s[j].Priority
			}
			return s[i].ID < s[j].ID
		})
	}
	byPriority(active)
	byPriority(backup)

	out := make([]provider, 0, len(active))
	down := 0
	for _, p := range active {
		if isDown(p.ID) {
			down++
			continue
		}
		out = append(out, p)
	}
	for _, b := range backup {
		if down == 0 {
			break
		}
		if isDown(b.ID) {
			continue
		}
		out = append(out, b)
		down--
	}
	return out
}

// providerPool is one provider's live connection pool plus its failure state.
type providerPool struct {
	prov      provider
	pool      *nntp.Pool
	key       string
	downUntil time.Time // set when the pool cannot be opened
	// resetsBase banks the reset counts of every PREVIOUS pool this provider
	// had. The live counter is on *nntp.Pool, so each rebuild (key change,
	// bench, install race) restarted the dashboard's one churn gauge at zero
	// — the pathologies that involve the heaviest churn made the churn signal
	// look best. Folded in before every close; snapshotStats reports base+live.
	resetsBase int64
}

// foldResetsLocked banks the live pool's reset count before it is closed.
// Callers hold f.mu.
func foldResetsLocked(pp *providerPool) {
	if pp != nil && pp.pool != nil {
		pp.resetsBase += pp.pool.Stats().Resets
	}
}

// The bench cooldown lives in Config (provider_down_cooldown_min): without one
// a dead provider would be re-dialled every pass, and with a backup configured
// the fleet would flap between them.

// providers returns the enabled servers, preferred first.
func (s *PGStore) providers(ctx context.Context) ([]provider, error) {
	type row struct {
		ID          int    `db:"id"`
		Name        string `db:"name"`
		Host        string `db:"host"`
		Port        int    `db:"port"`
		TLS         bool   `db:"tls"`
		Username    string `db:"username"`
		Password    string `db:"password"`
		Enabled     bool   `db:"enabled"`
		Role        string `db:"role"`
		Priority    int    `db:"priority"`
		Connections int    `db:"connections"`
		AccountCap  int    `db:"account_cap"`
		Backbone    string `db:"backbone"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT id, name, host, port, tls, username, password, enabled, role, priority, connections, account_cap, backbone
			   FROM servers WHERE enabled = TRUE ORDER BY priority, id`)
	})
	if err != nil {
		return nil, err
	}
	out := make([]provider, len(rows))
	for i, r := range rows {
		out[i] = provider{
			ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port, TLS: r.TLS,
			Username: r.Username, Password: r.Password, Enabled: r.Enabled,
			Role: r.Role, Priority: r.Priority, Connections: r.Connections,
			AccountCap: r.AccountCap, Backbone: r.Backbone,
		}
	}
	return out, nil
}

// providerFleet is the set of pools to crawl with this pass, one per chosen
// provider. Pools are cached across passes and rebuilt only when the provider's
// dial settings change.
type providerFleet struct {
	mu    sync.Mutex
	pools map[int]*providerPool
}

func newProviderFleet() *providerFleet {
	return &providerFleet{pools: map[int]*providerPool{}}
}

// isDown reports whether a provider is currently benched.
func (f *providerFleet) isDown(id int, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	pp, ok := f.pools[id]
	return ok && now.Before(pp.downUntil)
}

// bench marks a provider unusable for the cooldown, so a backup can take over
// and we stop re-dialling a server that is refusing us.
//
// `now` must be read FRESH by the caller, immediately before benching — not
// captured at pass start. A failed open can stall for minutes against a
// black-holed host, and benching from the pass-start clock wrote a downUntil
// that was already in the past: the provider was never effectively benched,
// isDown reported healthy on the next resolve, and backups never promoted.
func (f *providerFleet) bench(pr provider, now time.Time, cooldown time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pp := f.pools[pr.ID]
	if pp == nil {
		pp = &providerPool{prov: pr}
		f.pools[pr.ID] = pp
	}
	if pp.pool != nil {
		foldResetsLocked(pp)
		_ = pp.pool.Close()
		pp.pool = nil
		pp.key = ""
	}
	pp.downUntil = now.Add(cooldown)
}

// get returns an open pool for the provider, dialling or rebuilding as needed.
func (f *providerFleet) get(ctx context.Context, pr provider, spec poolSpec) (*nntp.Pool, error) {
	key := pr.poolKey(spec)

	f.mu.Lock()
	pp := f.pools[pr.ID]
	if pp != nil && pp.pool != nil && pp.key == key {
		p := pp.pool
		f.mu.Unlock()
		return p, nil
	}
	if pp != nil && pp.pool != nil {
		// Settings changed: close BEFORE dialling the replacement, accepting a
		// brief no-pool window, because under an account cap the old pool's
		// live connections would 482 the new pool's dials — swap-on-success
		// deadlocks against exactly the providers the cap knob exists for.
		foldResetsLocked(pp)
		_ = pp.pool.Close()
		pp.pool, pp.key = nil, ""
	}
	f.mu.Unlock()

	pool := nntp.NewPool(nntp.PoolConfig{
		Addr:        pr.addr(),
		TLS:         pr.TLS,
		Username:    pr.Username,
		Password:    pr.Password,
		Size:        spec.size,
		DialTimeout: spec.dialTimeout,
		OpTimeout:   spec.opTimeout,
		// Probe at the configured interval, and treat a connection as idle
		// once it has gone one full interval unused — so a pool doing real
		// work generates no probes at all.
		KeepaliveInterval: spec.keepalive,
		KeepaliveIdle:     spec.keepalive,
	})
	// Bound the whole open, not just each dial. Open dials sequentially and a
	// black-holed host (firewall DROP, dead route, accept-but-silent) costs a
	// full DialTimeout per slot with no error class to break on — at 50
	// connections that was 25 minutes of every pass stalled at the fleet head,
	// in every job, recurring each cooldown lapse. Open only errors when it
	// opened NOTHING; if the bound trips mid-open on a living-but-slow server
	// the pool simply comes up partial and TopUp grows it later.
	octx, cancel := context.WithTimeout(ctx, 2*spec.dialTimeout)
	err := pool.Open(octx)
	cancel()
	if err != nil {
		return nil, err
	}
	return f.install(pr, pool, key), nil
}

// install publishes a freshly-opened pool under its key, resolving races with
// concurrent get() calls. Separated from get so the resolution is testable —
// it is where a pool leak lived.
//
// Same key: a concurrent caller (crawl vs backfill both fleeting the same
// provider) won the dial race while we were connecting; keep theirs and close
// ours promptly, so the account never holds two full pools.
//
// Different key: the concurrent winner dialled under other settings (the crawl
// runs a round on pass-start config while the backfill re-reads per round; a
// worker-count change moves effectiveConns). Close what we displace — every
// pool that leaves the fleet map must be Closed exactly once. The overwritten
// alternative kept `size` authenticated sockets alive forever via keepalive
// DATE probes, invisible to snapshotStats, counted against the account cap
// until process exit. A caller still holding the displaced pool sees its
// remaining batches fail and re-plan, which is bounded; the leak was not.
func (f *providerFleet) install(pr provider, pool *nntp.Pool, key string) *nntp.Pool {
	f.mu.Lock()
	defer f.mu.Unlock()
	pp := f.pools[pr.ID]
	if pp == nil {
		pp = &providerPool{prov: pr}
		f.pools[pr.ID] = pp
	}
	if pp.pool != nil {
		if pp.key == key {
			_ = pool.Close() // fresh loser: it served nothing, no resets to bank
			return pp.pool
		}
		foldResetsLocked(pp)
		_ = pp.pool.Close()
	}
	pp.prov, pp.pool, pp.key = pr, pool, key
	pp.downUntil = time.Time{}
	return pool
}

// providerStat is one provider's live pool state for the admin page, plus its
// fetch volume since worker start (overlaid from telemetry at publish time —
// pool state alone cannot tell a slow account from a busy one).
type providerStat struct {
	Open, Target, Busy int
	Resets             int64
	Down               bool
	Articles           int
	Staged             int
	WireBytes          int64
	FailedBatches      int
}

// snapshotStats reports pool health per provider id. Only providers that have
// been dialled appear; a configured-but-never-used one simply has no entry.
func (f *providerFleet) snapshotStats(now time.Time) map[int]providerStat {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]providerStat, len(f.pools))
	for id, pp := range f.pools {
		st := providerStat{Down: now.Before(pp.downUntil), Resets: pp.resetsBase}
		if pp.pool != nil {
			ps := pp.pool.Stats()
			st.Open, st.Target, st.Busy = ps.Open, ps.Target, ps.Busy
			st.Resets += ps.Resets
		}
		out[id] = st
	}
	return out
}

// closeAll tears every pool down on shutdown.
func (f *providerFleet) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pp := range f.pools {
		if pp.pool != nil {
			foldResetsLocked(pp)
			_ = pp.pool.Close()
			pp.pool = nil
			pp.key = ""
		}
	}
}

// activeFleet resolves the providers to use this pass and opens their pools. A
// provider that fails to open — or whose cached pool turns out to have no live
// connections left — is benched (freeing a backup to replace it) and the pass
// continues with whoever is left: one dead provider must not stop the others
// from crawling.
func (p *Plugin) activeFleet(ctx context.Context, cfg Config) ([]providerRun, error) {
	all, err := p.st.providers(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errNoServer
	}
	return p.openFleet(ctx, all, cfg)
}

// openFleet is activeFleet minus the provider load — the seam the fleet
// lifecycle tests drive with fake servers.
func (p *Plugin) openFleet(ctx context.Context, all []provider, cfg Config) ([]providerRun, error) {
	now := time.Now()
	chosen := chooseProviders(all, func(id int) bool { return p.fleet.isDown(id, now) })

	// The account cap is split across the crawlers sharing this account, using
	// the same term-stable membership that splits the groups (assign.go). A
	// lone worker owns the whole cap. Presence is only consulted when a cap
	// exists — effectiveConns ignores the worker count otherwise, so cap-free
	// installs skip a database read per resolve.
	workers := 1
	if anyAccountCap(all) {
		workers = p.liveWorkerCount(ctx, cfg)
	}

	runs, benched := p.openProviders(ctx, chosen, cfg, workers)
	if benched {
		// Something was benched JUST NOW, and benching it is what frees its
		// backup — but chooseProviders ran on the pre-bench down-state, so the
		// backup was excluded (nothing was down yet). Without a second look
		// the pass runs under-strength or aborts entirely while a healthy
		// backup sits idle until the NEXT pass — and then the cooldown lapses,
		// the dead active is chosen again, and the cycle repeats forever. One
		// bounded re-selection, dialling only providers not already running.
		now = time.Now()
		rechosen := chooseProviders(all, func(id int) bool { return p.fleet.isDown(id, now) })
		have := make(map[int]bool, len(runs))
		for _, r := range runs {
			have[r.prov.ID] = true
		}
		var extra []provider
		for _, pr := range rechosen {
			if !have[pr.ID] {
				extra = append(extra, pr)
			}
		}
		if len(extra) > 0 {
			more, _ := p.openProviders(ctx, extra, cfg, workers)
			runs = append(runs, more...)
		}
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no usable provider (%d configured)", len(all))
	}
	return runs, nil
}

// openProviders dials one list of providers, benching the ones that fail and
// reporting whether anything was benched (the signal for a re-selection).
func (p *Plugin) openProviders(ctx context.Context, chosen []provider, cfg Config, workers int) (runs []providerRun, benched bool) {
	cooldown := time.Duration(cfg.ProviderDownCooldownMin) * time.Minute
	for _, pr := range chosen {
		size := pr.effectiveConns(cfg.Connections, workers)
		spec := specFor(cfg, size)
		pool, err := p.fleet.get(ctx, pr, spec)
		if err != nil {
			// time.Now(), not a pass-start capture: the open may just have
			// stalled for minutes, and a stale bench timestamp can already be
			// expired when written (see bench).
			p.fleet.bench(pr, time.Now(), cooldown)
			p.core.Errors.Report(ctx, "usenet/provider-open",
				fmt.Errorf("%s: %w", pr.label(), err))
			benched = true
			continue
		}
		// A cached pool can be a corpse: providers reap idle sessions and an
		// outage kills the rest, and every dead connection is discarded to a
		// nil slot. get() has no liveness view — a key match returns whatever
		// is installed — so a provider that died AFTER its pool opened was
		// re-selected every pass forever: never benched, its backup never
		// promoted, and its zero-connection pool still dealt a full share of
		// batch workers that all failed instantly. One TopUp is the cheap
		// second chance (it refills dead slots, behind its own cooldown);
		// still zero live connections means the provider is down.
		if pool.Stats().Open == 0 {
			tctx, cancel := context.WithTimeout(ctx, 2*spec.dialTimeout)
			pool.TopUp(tctx)
			cancel()
			if pool.Stats().Open == 0 {
				p.fleet.bench(pr, time.Now(), cooldown)
				p.core.Errors.Report(ctx, "usenet/provider-dead",
					fmt.Errorf("%s: no live connections and top-up opened none", pr.label()))
				benched = true
				continue
			}
		}
		runs = append(runs, providerRun{prov: pr, pool: pool, size: size})
	}
	return runs, benched
}

// anyAccountCap reports whether any enabled provider splits its connection
// budget across the worker fleet.
func anyAccountCap(all []provider) bool {
	for _, pr := range all {
		if pr.AccountCap > 0 {
			return true
		}
	}
	return false
}

// providerRun pairs a provider with its open pool for one pass.
type providerRun struct {
	prov provider
	pool *nntp.Pool
	size int // resolved per-worker connection budget this pass (after the fleet cap)
}
