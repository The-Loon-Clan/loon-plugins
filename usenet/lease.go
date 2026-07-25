package usenet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// Leases let the crawler run on several hosts at once.
//
// Two scopes, one primitive:
//
//   - scope "group" — who crawls one BACKBONE'S view of one GROUP. Crawl state
//     is keyed (backbone, group_name), so two workers on the same backbone but
//     different groups touch separate rows and never contend. That makes the
//     group, not the backbone, the right unit: a second account on the same
//     backbone is useful for OTHER groups rather than idle.
//   - scope "job" — jobs that are not backbone-scoped (build, prune, tag fill,
//     health) and must still run once cluster-wide.
//
// A lease is a row with an expiry rather than a Postgres advisory lock, because
// an advisory lock is session-scoped: holding one for the length of a crawl
// would pin a connection idle for minutes. A row survives a worker being killed
// (it simply expires) and needs no pinned connection.

const (
	leaseScopeGroup = "group"
	leaseScopeJob   = "job"
)

// workerID identifies this process across the cluster. Host and pid make it
// readable in the leases table when diagnosing "who is holding this"; the random
// suffix keeps it unique across restarts, so a crashed worker's own lease is
// never mistaken for a live one by its replacement.
var (
	workerIDOnce sync.Once
	workerIDVal  string
)

func workerID() string {
	workerIDOnce.Do(func() {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		var b [4]byte
		_, _ = rand.Read(b[:])
		workerIDVal = fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b[:]))
	})
	return workerIDVal
}

// leaseOwnerDeadAfter is how long a lease owner may go without a heartbeat
// before its leases become claimable regardless of expiry. Workers heartbeat
// every 30s (startHeartbeat), so two minutes is four missed beats — dead, not
// slow. Deliberately independent of worker_stale_sec: presence reaping tunes
// term membership; this guards correctness of takeover.
const leaseOwnerDeadAfter = 2 * time.Minute

// claimLease takes or renews a lease. It succeeds when the key is unheld, has
// expired, is already ours, OR its owner has stopped heartbeating — all in one
// atomic upsert, so two workers racing for the same key cannot both win.
//
// The dead-owner clause is what makes a deploy seamless: container recreation
// changes the hostname, so the replacement worker is "another worker" to the
// lease table, and a worker killed mid-pass never runs its release. Without
// the clause every deploy idled the crawler until the TTL lapsed (observed on
// prod 2026-07-24: "every group already claimed by another worker" for 15
// minutes after each restart). Liveness comes from crawler_workers heartbeats,
// which every worker writes before its first pass.
func (s *PGStore) claimLease(ctx context.Context, scope, key, worker string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	got := false
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO leases (scope, key, worker_id, expires_at)
			 VALUES ($1, $2, $3, now() + make_interval(secs => $4))
			 ON CONFLICT (scope, key) DO UPDATE
			    SET worker_id  = EXCLUDED.worker_id,
			        claimed_at = CASE WHEN leases.worker_id = EXCLUDED.worker_id
			                          THEN leases.claimed_at ELSE now() END,
			        expires_at = EXCLUDED.expires_at
			  WHERE leases.worker_id = EXCLUDED.worker_id
			     OR leases.expires_at < now()
			     OR NOT EXISTS (SELECT 1 FROM crawler_workers w
			                     WHERE w.worker_id = leases.worker_id
			                       AND w.last_seen > now() - make_interval(secs => $5))`,
			scope, key, worker, ttl.Seconds(), leaseOwnerDeadAfter.Seconds())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		got = n > 0
		return nil
	})
	return got, err
}

// releaseLease drops a lease we hold, so another worker can take it immediately
// instead of waiting for the expiry.
func (s *PGStore) releaseLease(ctx context.Context, scope, key, worker string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM leases WHERE scope = $1 AND key = $2 AND worker_id = $3`,
			scope, key, worker)
		return err
	})
}

// leaseHolders reports the current holder per key, for diagnostics.
func (s *PGStore) leaseHolders(ctx context.Context, scope string) (map[string]string, error) {
	type row struct {
		Key    string `db:"key"`
		Worker string `db:"worker_id"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT key, worker_id FROM leases WHERE scope = $1 AND expires_at > now()`, scope)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Worker
	}
	return out, nil
}

// leaseRenewInterval is how often a held lease is refreshed while work is in
// flight. It must be comfortably shorter than the TTL, or a slow pass would let
// its own lease lapse and a second worker would start the same work.
func leaseRenewInterval(ttl time.Duration) time.Duration {
	d := ttl / 3
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

// withLease runs fn while holding a lease, renewing it in the background so a
// long pass cannot outlive its own claim. Returns false without running fn when
// the lease is held elsewhere.
//
// fn receives a context that is CANCELLED the moment renewal loses the lease:
// from that point another worker may legitimately own the work, so continuing
// to run — and especially to write — risks overlap. fn must treat that
// cancellation like any other and stop promptly.
func (p *Plugin) withLease(ctx context.Context, scope, key string, ttl time.Duration, fn func(context.Context)) bool {
	me := workerID()
	got, err := p.st.claimLease(ctx, scope, key, me, ttl)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/lease-claim", fmt.Errorf("%s/%s: %w", scope, key, err))
		return false
	}
	if !got {
		return false
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	renewCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(leaseRenewInterval(ttl))
		defer t.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-t.C:
				// A failed renewal is not fatal on its own — the lease may still
				// be valid — but if it keeps failing the TTL will lapse and
				// another worker takes over, which is the correct outcome. The
				// loss cancels workCtx so the protected work stops instead of
				// running on and overlapping the new owner.
				if ok, err := p.st.claimLease(renewCtx, scope, key, me, ttl); err != nil || !ok {
					if renewCtx.Err() == nil {
						p.core.Errors.Report(renewCtx, "usenet/lease-renew-lost",
							fmt.Errorf("%s/%s: renewal lost mid-work (err=%v ok=%v) — cancelling the pass", scope, key, err, ok))
						cancelWork()
					}
					return
				}
			}
		}
	}()

	defer func() {
		stop()
		wg.Wait()
		// Release on a fresh context: ctx may already be cancelled by shutdown,
		// and a lease left behind would idle that work until it expires.
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.st.releaseLease(relCtx, scope, key, me); err != nil {
			p.core.Errors.Report(ctx, "usenet/lease-release", err)
		}
	}()

	fn(workCtx)
	return true
}

// groupLeaseKey is the unit of crawl parallelism: one BACKBONE'S view of one
// GROUP. Crawl state is keyed (backbone, group_name), so two workers on the same
// backbone but different groups touch entirely separate rows and never contend —
// which makes the group, not the backbone, the right thing to lease.
func groupLeaseKey(backbone, group string) string { return backbone + "|" + group }

// claimGroupLeases takes leases for as many of the given groups as are free and
// returns those we own, a pass context, and a release func. Partial acquisition
// is the normal case, not an error — whatever another worker holds simply isn't
// ours this pass, and it will crawl those groups instead.
//
// A single renewer refreshes the whole set while work is in flight, so a slow
// pass cannot let part of its own claim lapse and invite a second worker in.
// The returned context is CANCELLED the moment renewal loses any of the set:
// a sibling may legitimately own these groups from that point, so the pass
// must stop crawling and writing. Run the pass on it, not on ctx.
func (p *Plugin) claimGroupLeases(ctx context.Context, backbone string, groups []groupRow, ttl time.Duration) ([]groupRow, context.Context, func()) {
	me := workerID()
	var held []groupRow
	var keys []string
	for _, g := range groups {
		k := groupLeaseKey(backbone, g.Name)
		got, err := p.st.claimLease(ctx, leaseScopeGroup, k, me, ttl)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/lease-claim", fmt.Errorf("%s: %w", k, err))
			continue
		}
		if got {
			held = append(held, g)
			keys = append(keys, k)
		}
	}
	if len(held) == 0 {
		return nil, ctx, func() {}
	}

	passCtx, cancelPass := context.WithCancel(ctx)
	renewCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(leaseRenewInterval(ttl))
		defer t.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-t.C:
				for _, k := range keys {
					if ok, err := p.st.claimLease(renewCtx, leaseScopeGroup, k, me, ttl); err != nil || !ok {
						// One key's loss abandons renewal for the whole set and
						// cancels the pass — a sibling may now legitimately
						// claim these groups, so crawling on would overlap it.
						if renewCtx.Err() == nil {
							p.core.Errors.Report(renewCtx, "usenet/lease-renew-lost",
								fmt.Errorf("group lease %s: renewal lost mid-pass (err=%v ok=%v) — cancelling the pass", k, err, ok))
							cancelPass()
						}
						return
					}
				}
			}
		}
	}()

	release := func() {
		stop()
		wg.Wait()
		cancelPass()
		// Fresh context: ctx may already be cancelled by shutdown, and a lease
		// left behind idles that group until it expires.
		relCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, k := range keys {
			if err := p.st.releaseLease(relCtx, leaseScopeGroup, k, me); err != nil {
				p.core.Errors.Report(ctx, "usenet/lease-release", err)
			}
		}
	}
	return held, passCtx, release
}

// leaseTTL resolves the configured lease lifetime.
func (p *Plugin) leaseTTL(cfg Config) time.Duration {
	m := cfg.LeaseTTLMin
	if m <= 0 {
		m = 15
	}
	return time.Duration(m) * time.Minute
}
