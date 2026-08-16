package usenet

// The spot index pass: read free.pt forward from the high watermark, then
// backward from the back watermark until the whole history is covered.
//
// WHY THIS IS NOT A BRANCH INSIDE runCrawl. The forward crawl is built around
// reassembly: base_subject grouping, segment counting, set completion, junk by
// subject, poster watches, coverage ranges. A spot is one article that is
// already a complete release description, so none of that applies, and running
// the assembler over 5.9M one-article postings would stage millions of subjects
// that can never complete a set. The crawler's group query therefore skips
// kind='spots' and this pass owns them.
//
// WHAT IT DOES SHARE is everything that matters for cost and control: the same
// connection pool (providers cap connections per account, so a second pool
// would just push the account over its limit), the same per-group watermark
// columns, and the same job machinery — so off-peak gating, the write gate,
// manual triggers and the interval override all work with no new plumbing.
//
// THE ASYMMETRY THAT SHAPES IT. XOVER carries the whole spot header — poster,
// key, category, size, posted time — at roughly one round trip per thousand
// articles, so free.pt's entire history is a few thousand round trips. The
// document behind each spot needs one fetch EACH, which is millions. This pass
// does only the cheap half, to completion; the expensive half works off the
// spots table as a worklist and can lag by months without holding it up.

import (
	"context"
	"errors"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// Defaults for the pass's knobs. All three are admin-configurable; these are
// the starting points, not constants.
//
// 200 batches of 1000 is 200k articles a pass. free.pt's 5.9M-article history
// therefore closes in roughly 30 passes -- a few hours at the default cadence,
// which is fast enough that "backfill everything" is a real option rather than
// a theoretical one, and slow enough that it never owns the pool.
const (
	defaultSpotIntervalMin = 15
	defaultSpotBatchSize   = 1000
	defaultSpotMaxBatches  = 200

	// The fetch pass. 200 spots is 400 article reads, which is a fraction of
	// what the crawler moves in the same window and leaves the pool to it.
	defaultSpotFetchIntervalMin = 10
	defaultSpotFetchBatch       = 200
)

// runSpotIndex lists every active spot group, forward then backward.
func (p *Plugin) runSpotIndex(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.mayWrite(ctx, p.spotJob) {
		return
	}
	if !p.spotMu.TryLock() {
		p.spotJob.Log("spot index already running — skipping overlap")
		return
	}
	defer p.spotMu.Unlock()
	p.spotJob.SetRunning()
	cfg := p.effective(ctx)

	groups, err := p.st.spotGroups(ctx)
	if err != nil {
		p.spotJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/spot-groups", err)
		return
	}
	if len(groups) == 0 {
		// Not an error: Spotnet is opt-in. Naming the fix beats a silent idle.
		p.spotJob.Log("no spot groups — mark %s as a spot index on the Spots tab", SpotGroup)
		p.spotJob.SetIdle(p.nextSpotIndex(ctx))
		return
	}

	runs, err := p.activeFleet(ctx, cfg)
	if err != nil || len(runs) == 0 {
		if errors.Is(err, errNoServer) || len(runs) == 0 {
			p.spotJob.Log("no server configured")
			p.spotJob.SetIdle(p.nextSpotIndex(ctx))
			return
		}
		p.spotJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/spot-fleet", err)
		return
	}

	// One pool, the crawler's. Providers cap connections per ACCOUNT, so a
	// second pool would not buy capacity — it would push the account over its
	// limit and get every connection refused.
	pool := runs[0].pool

	total, back := 0, 0
	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		f, b, err := p.indexSpotGroup(ctx, cfg, pool, g)
		total += f
		back += b
		if err != nil {
			p.reportErr(ctx, "usenet/spot-index", err)
			p.spotJob.Log("%s: %v", g.Name, err)
		}
	}
	p.spotJob.Log("indexed %d new spots (%d from backfill)", total, back)
	p.spotJob.SetIdle(p.nextSpotIndex(ctx))
}

// indexSpotGroup walks one group. Returns (forward, backfilled) counts.
func (p *Plugin) indexSpotGroup(ctx context.Context, cfg Config, pool *nntp.Pool, g spotGroup) (int, int, error) {
	batch := cfg.SpotBatchSize
	if batch <= 0 {
		batch = defaultSpotBatchSize
	}
	budget := cfg.SpotMaxBatches
	if budget <= 0 {
		budget = defaultSpotMaxBatches
	}

	var low, high int64
	// TryDo, never Do: the crawler owns this pool and a spot pass that blocked
	// for a slot would queue ahead of it, taking the connection the moment one
	// frees. Yielding is the point.
	if err := pool.TryDo(ctx, func(c *nntp.Conn) error {
		_, l, h, err := c.Group(g.Name)
		if err != nil {
			return err
		}
		low, high = int64(l), int64(h)
		return nil
	}); err != nil {
		return 0, 0, err
	}
	if err := p.st.setSpotGroupExtent(ctx, g.Name, low, high); err != nil {
		return 0, 0, err
	}
	// Mirror what the seed just wrote. Both watermarks initialise only on
	// first sight, so the in-memory copy is stale exactly once per group — and
	// that once is the run where getting it wrong reads the entire history
	// twice (see setSpotGroupExtent).
	if g.BackWatermark == 0 {
		g.BackWatermark = high
	}
	if g.HighWatermark == 0 {
		g.HighWatermark = high
	}

	// Consecutive overview failures tolerated before this group is left for the
	// next pass.
	//
	// One failure used to end the group outright, which threw away the other
	// 199 batches of budget: the first live run did four batches of two hundred
	// and stopped. frugalusenet answers "511 issue with group" intermittently
	// mid-walk, and the next attempt on a fresh connection succeeds, so a
	// hiccup must cost a retry rather than the pass. Bounded, because a range
	// that fails every time is a real fault and spinning on it would be worse
	// than stopping.
	//
	// The retry is always the SAME range. Skipping past a failure would advance
	// the watermark over articles nothing ever read, and that loss is silent
	// and permanent -- the one outcome worth more than a wasted pass.
	const maxOverviewRetries = 3

	forward := 0
	// FORWARD first, always. New spots are what a member is waiting for; the
	// history has waited years and can wait one more pass.
	mark := g.HighWatermark
	fails := 0
	for budget > 0 && ctx.Err() == nil {
		from, to, ok := spotForwardRange(mark, low, high, batch)
		if !ok {
			break
		}
		n, err := p.readSpotRange(ctx, pool, g.Name, from, to)
		if err != nil {
			if fails++; fails < maxOverviewRetries {
				continue
			}
			return forward, 0, err
		}
		fails = 0
		forward += n
		if err := p.st.advanceSpotHigh(ctx, g.Name, to); err != nil {
			return forward, 0, err
		}
		mark = to
		budget--
	}

	if g.BackfillDone || cfg.SkipBackfill {
		return forward, 0, nil
	}

	// BACKWARD with whatever budget the forward pass left. The floor is the
	// server's own low mark: below it the articles are gone, and a group that
	// reaches it is done for good rather than retried every pass.
	backfilled := 0
	back := g.BackWatermark
	fails = 0
	for budget > 0 && ctx.Err() == nil {
		from, to, done, ok := spotBackRange(back, low, batch)
		if !ok {
			break
		}
		n, err := p.readSpotRange(ctx, pool, g.Name, from, to)
		if err != nil {
			if fails++; fails < maxOverviewRetries {
				continue
			}
			return forward, backfilled, err
		}
		fails = 0
		backfilled += n
		if err := p.st.lowerSpotBack(ctx, g.Name, from, done); err != nil {
			return forward, backfilled, err
		}
		if done {
			break
		}
		back = from
		budget--
	}
	return forward, backfilled, nil
}

// spotForwardRange is the next unread range above the watermark.
//
// Pure, because this arithmetic is where a spot feed silently breaks: one off
// by one and the pass either re-reads the same page forever or steps over a
// spot that will never be looked at again. Neither shows up as an error.
func spotForwardRange(watermark, serverLow, serverHigh int64, batch int) (from, to int64, ok bool) {
	if batch <= 0 {
		batch = defaultSpotBatchSize
	}
	from = watermark + 1
	// A watermark below what the server still holds means articles expired
	// while we were away. Resume at the server's floor rather than walking a
	// range that is guaranteed empty.
	if from < serverLow {
		from = serverLow
	}
	if from > serverHigh {
		return 0, 0, false
	}
	to = from + int64(batch) - 1
	if to > serverHigh {
		to = serverHigh
	}
	return from, to, true
}

// spotBackRange is the next unread range below the back watermark.
//
// done reports that this range reaches the server's floor, which is what ends
// a backfill permanently: below server_low the articles are gone, so a group
// that gets there has read everything that still exists and must not be
// retried every pass forever.
func spotBackRange(back, serverLow int64, batch int) (from, to int64, done, ok bool) {
	if batch <= 0 {
		batch = defaultSpotBatchSize
	}
	to = back - 1
	if to < serverLow {
		// Already at or below the floor: nothing left, and it is finished.
		return 0, 0, true, false
	}
	from = to - int64(batch) + 1
	if from <= serverLow {
		from = serverLow
		done = true
	}
	return from, to, done, true
}

// readSpotRange XOVERs one range and stores every spot in it.
func (p *Plugin) readSpotRange(ctx context.Context, pool *nntp.Pool, group string, from, to int64) (int, error) {
	var rows []spotRow
	err := pool.TryDo(ctx, func(c *nntp.Conn) error {
		if _, _, _, err := c.Group(group); err != nil {
			return err
		}
		over, _, err := c.Overview(int(from), int(to))
		if err != nil {
			return err
		}
		rows = spotRowsFromOverview(group, over)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return p.st.upsertSpots(ctx, rows)
}

// spotRowsFromOverview turns an XOVER page into storable spots.
//
// Articles that are not spots are skipped silently and deliberately: free.pt
// carries ordinary posts too, and one log line per non-spot would bury the
// pass in noise on a group where they are a normal majority in places.
func spotRowsFromOverview(group string, over []nntp.MessageOverview) []spotRow {
	out := make([]spotRow, 0, len(over))
	for _, ov := range over {
		if ov.MessageId == "" {
			continue
		}
		h, err := ParseSpotFrom(ov.From)
		if err != nil {
			continue
		}
		r := spotRow{
			MessageID:  ov.MessageId,
			GroupName:  group,
			ArticleNum: int64(ov.MessageNumber),
			Poster:     h.Poster,
			Subject:    ov.Subject,
			PublicKey:  h.PublicKey,
			HeaderSig:  h.Signature,
			Category:   h.Category,
			KeyID:      h.KeyID,
			SubCats:    h.FullSubCats(),
			SizeBytes:  h.SizeBytes,
			Locale:     h.Locale,
		}
		// The header's own timestamp, not the Date header: Date is
		// poster-controlled and routinely wrong, while PostedAt is part of the
		// signed From tuple.
		if h.PostedAt > 0 {
			r.PostedAt = time.Unix(h.PostedAt, 0).UTC()
		} else if !ov.Date.IsZero() {
			r.PostedAt = ov.Date.UTC()
		}
		out = append(out, r)
	}
	return out
}

// nextSpotIndex is the pass's own interval, separate from the crawler's: the
// forward half is one round trip per thousand articles, so it can run often
// without costing anything worth measuring.
func (p *Plugin) nextSpotIndex(ctx context.Context) time.Time {
	m := p.effective(ctx).SpotIntervalMin
	if m <= 0 {
		m = defaultSpotIntervalMin
	}
	return time.Now().Add(time.Duration(m) * time.Minute)
}
