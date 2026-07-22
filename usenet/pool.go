package usenet

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// ensurePool returns the shared connection pool, opening it on first use and
// rebuilding it if the server or the connection count changed.
//
// The pool is opened lazily rather than in Start because a fresh install has no
// server row yet (and Provision must not do I/O at all). Crawl and backfill
// share ONE pool on purpose: the provider caps concurrent connections per
// account, so a second pool would just push the account over its limit.
func (p *Plugin) ensurePool(ctx context.Context, cfg Config) (*nntp.Pool, error) {
	srv, ok, err := p.st.getServer(ctx)
	if err != nil {
		return nil, err
	}
	if !ok || srv.Host == "" {
		return nil, errNoServer
	}
	port := srv.Port
	if port == 0 {
		port = 119
	}
	// Any change to how we'd dial (or how many) invalidates the pool.
	key := fmt.Sprintf("%s:%d|%s|%t|%d", srv.Host, port, srv.Username, srv.TLS, cfg.Connections)

	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	if p.pool != nil && p.poolKey == key {
		return p.pool, nil
	}
	if p.pool != nil {
		_ = p.pool.Close()
		p.pool, p.poolKey = nil, ""
	}

	pool := nntp.NewPool(nntp.PoolConfig{
		Addr:        fmt.Sprintf("%s:%d", srv.Host, port),
		TLS:         srv.TLS,
		Username:    srv.Username,
		Password:    srv.Password,
		Size:        cfg.Connections,
		DialTimeout: 30 * time.Second,
		OpTimeout:   60 * time.Second,
	})
	if err := pool.Open(ctx); err != nil {
		return nil, err
	}
	p.pool, p.poolKey = pool, key
	return pool, nil
}

// closePool tears the pool down on shutdown.
func (p *Plugin) closePool() {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	if p.pool != nil {
		_ = p.pool.Close()
		p.pool, p.poolKey = nil, ""
	}
}
