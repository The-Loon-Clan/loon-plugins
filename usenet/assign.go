package usenet

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/jmoiron/sqlx"
)

// Splitting the newsgroups between crawlers.
//
// Two layers, and they do different jobs:
//
//   - ASSIGNMENT (here) decides which groups a worker should even attempt, so N
//     crawlers divide the work instead of racing for it. Without this, greedy
//     leasing means the first worker to start claims everything and the rest
//     idle — alternation, not parallelism.
//   - LEASES (lease.go) guarantee that two workers never crawl the same group,
//     whatever assignment says.
//
// The layering matters: assignment is an optimisation and is allowed to be
// briefly wrong (during a membership change, two workers may disagree about who
// owns a group). Leases are the correctness guarantee that makes that
// disagreement harmless.
//
// Membership is stable within a TERM. A worker that joins mid-term is not
// counted until the next boundary, so the divisor cannot change underneath a
// pass in flight — add a third crawler and it waits out the term before taking
// its third.

// heartbeat records this worker as alive. joined_at is preserved on conflict so
// a long-running worker keeps its seniority and stays in the current term's
// divisor.
func (s *PGStore) heartbeat(ctx context.Context, worker string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO crawler_workers (worker_id, joined_at, last_seen)
			 VALUES ($1, now(), now())
			 ON CONFLICT (worker_id) DO UPDATE SET last_seen = now()`, worker)
		return err
	})
}

// eligibleWorkers lists the crawlers that count toward this term's split: alive
// (heartbeated recently) AND present since the term began.
func (s *PGStore) eligibleWorkers(ctx context.Context, termStart time.Time, staleAfter time.Duration) ([]string, error) {
	var out []string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT worker_id FROM crawler_workers
			  WHERE last_seen > now() - make_interval(secs => $1)
			    AND joined_at <= $2
			  ORDER BY worker_id`, staleAfter.Seconds(), termStart)
	})
	return out, err
}

// reapWorkers drops long-gone workers so the table doesn't accumulate one row
// per restart forever (the worker id carries a random suffix).
func (s *PGStore) reapWorkers(ctx context.Context, staleAfter time.Duration) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM crawler_workers WHERE last_seen < now() - make_interval(secs => $1)`,
			(staleAfter * 10).Seconds())
		return err
	})
}

// termStart truncates now to the current term boundary. Every worker computing
// this independently gets the same answer, which is what lets them agree on
// membership without talking to each other.
func termStart(now time.Time, term time.Duration) time.Time {
	if term <= 0 {
		term = 15 * time.Minute
	}
	return now.Truncate(term)
}

// shareOf returns the groups this worker should attempt.
//
// Assignment is by hash of the group name rather than by position, so adding or
// removing a NEWSGROUP moves only that group between workers instead of
// reshuffling everyone. (Adding a WORKER does reshuffle, which is why that only
// happens on a term boundary.)
//
// A worker not in the member list gets nothing: it joined mid-term and waits.
func shareOf(groups []groupRow, workers []string, me string) []groupRow {
	if len(workers) <= 1 {
		return groups
	}
	idx := -1
	for i, w := range workers {
		if w == me {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	n := uint32(len(workers))
	out := make([]groupRow, 0, len(groups)/len(workers)+1)
	for _, g := range groups {
		if groupSlot(g.Name, n) == uint32(idx) {
			out = append(out, g)
		}
	}
	return out
}

// groupSlot maps a group name to a worker slot.
func groupSlot(name string, n uint32) uint32 {
	if n == 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32() % n
}

// myGroups narrows the active groups to this worker's share for the current
// term. On any error it returns everything: a presence problem must not stop a
// single-worker install from crawling, and the leases still prevent overlap.
func (p *Plugin) myGroups(ctx context.Context, groups []groupRow, cfg Config) []groupRow {
	me := workerID()
	term := time.Duration(cfg.AssignTermMin) * time.Minute
	if term <= 0 {
		term = 15 * time.Minute
	}
	stale := time.Duration(cfg.WorkerStaleSec) * time.Second
	if stale <= 0 {
		stale = 90 * time.Second
	}

	workers, err := p.st.eligibleWorkers(ctx, termStart(time.Now(), term), stale)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/workers", err)
		return groups
	}
	// Nobody eligible yet (first boot, or everyone joined inside this term):
	// act alone rather than stalling until the next boundary.
	if len(workers) == 0 {
		return groups
	}
	mine := shareOf(groups, workers, me)
	if len(workers) > 1 {
		p.crawlJob.Log("assignment: %d of %d group(s), %d crawler(s) this term",
			len(mine), len(groups), len(workers))
	}
	return mine
}

// startHeartbeat keeps this worker visible to the others. Without it a crawler
// is invisible to the split and its share is handed to someone else.
func (p *Plugin) startHeartbeat(ctx context.Context, cfg Config) {
	stale := time.Duration(cfg.WorkerStaleSec) * time.Second
	if stale <= 0 {
		stale = 90 * time.Second
	}
	// Beat well inside the staleness window so one missed tick is survivable.
	every := stale / 3
	if every < 5*time.Second {
		every = 5 * time.Second
	}

	go func() {
		if err := p.st.heartbeat(ctx, workerID()); err != nil {
			p.core.Errors.Report(ctx, "usenet/heartbeat", err)
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := p.st.heartbeat(ctx, workerID()); err != nil {
					p.core.Errors.Report(ctx, "usenet/heartbeat", err)
				}
				_ = p.st.reapWorkers(ctx, stale)
			}
		}
	}()
}
