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

// poolKey changes whenever anything about how we'd dial changes, so the pool is
// rebuilt rather than silently kept against stale settings. `size` is the
// resolved per-worker connection budget for this pass — it moves when the fleet
// size changes at a term boundary, so the pool is rebuilt to the new budget.
func (pr provider) poolKey(size int) string {
	return fmt.Sprintf("%d|%s|%s|%t|%d", pr.ID, pr.addr(), pr.Username, pr.TLS, size)
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
}

// providerDownCooldown is how long a provider stays benched after failing to
// open. Without it a dead provider would be re-dialled every pass, and with a
// backup configured the fleet would flap between them.
const providerDownCooldown = 10 * time.Minute

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
func (f *providerFleet) bench(pr provider, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pp := f.pools[pr.ID]
	if pp == nil {
		pp = &providerPool{prov: pr}
		f.pools[pr.ID] = pp
	}
	if pp.pool != nil {
		_ = pp.pool.Close()
		pp.pool = nil
		pp.key = ""
	}
	pp.downUntil = now.Add(providerDownCooldown)
}

// get returns an open pool for the provider, dialling or rebuilding as needed.
func (f *providerFleet) get(ctx context.Context, pr provider, size int) (*nntp.Pool, error) {
	key := pr.poolKey(size)

	f.mu.Lock()
	pp := f.pools[pr.ID]
	if pp != nil && pp.pool != nil && pp.key == key {
		p := pp.pool
		f.mu.Unlock()
		return p, nil
	}
	if pp != nil && pp.pool != nil {
		_ = pp.pool.Close() // settings changed
		pp.pool, pp.key = nil, ""
	}
	f.mu.Unlock()

	pool := nntp.NewPool(nntp.PoolConfig{
		Addr:        pr.addr(),
		TLS:         pr.TLS,
		Username:    pr.Username,
		Password:    pr.Password,
		Size:        size,
		DialTimeout: 30 * time.Second,
		OpTimeout:   60 * time.Second,
	})
	if err := pool.Open(ctx); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if pp == nil {
		pp = f.pools[pr.ID]
	}
	if pp == nil {
		pp = &providerPool{prov: pr}
		f.pools[pr.ID] = pp
	}
	if pp.pool != nil && pp.key == key {
		// A concurrent caller (crawl vs backfill both fleeting the same
		// provider) won the dial race while we were connecting. Keep theirs;
		// closing ours promptly is what stops the account seeing two full
		// pools and the loser's sockets leaking until process exit.
		_ = pool.Close()
		return pp.pool, nil
	}
	pp.prov, pp.pool, pp.key = pr, pool, key
	pp.downUntil = time.Time{}
	return pool, nil
}

// providerStat is one provider's live pool state for the admin page.
type providerStat struct {
	Open, Target, Busy int
	Resets             int64
	Down               bool
}

// snapshotStats reports pool health per provider id. Only providers that have
// been dialled appear; a configured-but-never-used one simply has no entry.
func (f *providerFleet) snapshotStats(now time.Time) map[int]providerStat {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]providerStat, len(f.pools))
	for id, pp := range f.pools {
		st := providerStat{Down: now.Before(pp.downUntil)}
		if pp.pool != nil {
			ps := pp.pool.Stats()
			st.Open, st.Target, st.Busy, st.Resets = ps.Open, ps.Target, ps.Busy, ps.Resets
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
			_ = pp.pool.Close()
			pp.pool = nil
			pp.key = ""
		}
	}
}

// activeFleet resolves the providers to use this pass and opens their pools. A
// provider that fails to open is benched (freeing a backup to replace it) and
// the pass continues with whoever is left — one dead provider must not stop the
// others from crawling.
func (p *Plugin) activeFleet(ctx context.Context, cfg Config) ([]providerRun, error) {
	all, err := p.st.providers(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errNoServer
	}
	now := time.Now()
	chosen := chooseProviders(all, func(id int) bool { return p.fleet.isDown(id, now) })

	// The account cap is split across the crawlers sharing this account, using
	// the same term-stable membership that splits the groups (assign.go). A lone
	// worker owns the whole cap.
	workers := p.liveWorkerCount(ctx, cfg)

	var runs []providerRun
	for _, pr := range chosen {
		size := pr.effectiveConns(cfg.Connections, workers)
		pool, err := p.fleet.get(ctx, pr, size)
		if err != nil {
			p.fleet.bench(pr, now)
			p.core.Errors.Report(ctx, "usenet/provider-open",
				fmt.Errorf("%s: %w", pr.label(), err))
			continue
		}
		runs = append(runs, providerRun{prov: pr, pool: pool, size: size})
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no usable provider (%d configured)", len(all))
	}
	return runs, nil
}

// providerRun pairs a provider with its open pool for one pass.
type providerRun struct {
	prov provider
	pool *nntp.Pool
	size int // resolved per-worker connection budget this pass (after the fleet cap)
}
