package usenet

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/nntp"
)

// A pool with no connections fails acquire immediately — no dial, no network —
// which drives fetchBatch down its error path and exercises the accounting
// without a server.
func deadPool() *nntp.Pool { return nntp.NewPool(nntp.PoolConfig{Addr: "127.0.0.1:1", Size: 0}) }

func trackerPlugin() *Plugin {
	return &Plugin{
		tel:  newTelemetry(),
		core: &core.Core{Errors: core.NewErrorReporter(core.ErrorAdapter{})},
	}
}

// Backfill shares runBatches/fetchBatch with the forward crawl. When the fetch
// path hardcoded p.tel.crawl, every backfill batch incremented the FORWARD
// crawl's counters while only the forward planner incremented its denominator,
// so the progress bar read "21,000 / 20,000 batches (100.0%)" and the pass's
// article/staged totals silently included the backfill's work.
func TestBackfillBatchesDoNotCountAgainstTheForwardCrawl(t *testing.T) {
	p := trackerPlugin()
	p.tel.crawl.passStart(1)
	p.tel.crawl.roundStart()
	p.tel.crawl.notePlanned("a.b.forward", 2)
	p.tel.backfill.passStart(1)
	p.tel.backfill.roundStart()

	// The forward pass runs its two planned batches.
	fwd := []batchJob{{group: "a.b.forward", lo: 1, hi: 10}, {group: "a.b.forward", lo: 11, hi: 20}}
	p.runBatches(context.Background(), []providerRun{{pool: deadPool(), size: 1}}, fwd, Config{}, &p.tel.crawl, nil)

	// Backfill then runs three of its own.
	back := []batchJob{
		{group: "a.b.history", lo: 1, hi: 10},
		{group: "a.b.history", lo: 11, hi: 20},
		{group: "a.b.history", lo: 21, hi: 30},
	}
	p.runBatches(context.Background(), []providerRun{{pool: deadPool(), size: 1}}, back, Config{}, &p.tel.backfill, nil)

	crawl, _ := p.tel.crawl.snapshot()
	if crawl.Batches != 2 {
		t.Errorf("forward crawl counted %d batches, want 2 — backfill work is bleeding into it", crawl.Batches)
	}
	if crawl.Batches > crawl.BatchesTotal {
		t.Errorf("progress reads %d/%d — the bar cannot exceed 100%%",
			crawl.Batches, crawl.BatchesTotal)
	}
	if bf, _ := p.tel.backfill.snapshot(); bf.Batches != 3 {
		t.Errorf("backfill counted %d batches, want 3", bf.Batches)
	}
}

// The forward pass's "reading" field names the group the FORWARD crawl is on.
// Sharing the tracker let a concurrent backfill overwrite it, so the live view
// pointed at a newsgroup the forward crawl was not touching — which is exactly
// the readout an operator uses to decide whether a group is progressing.
func TestReadingGroupBelongsToItsOwnPass(t *testing.T) {
	p := trackerPlugin()
	p.tel.crawl.passStart(1)
	p.tel.crawl.roundStart()

	p.runBatches(context.Background(), []providerRun{{pool: deadPool(), size: 1}},
		[]batchJob{{group: "a.b.forward", lo: 1, hi: 10}}, Config{}, &p.tel.crawl, nil)
	p.runBatches(context.Background(), []providerRun{{pool: deadPool(), size: 1}},
		[]batchJob{{group: "a.b.history", lo: 1, hi: 10}}, Config{}, &p.tel.backfill, nil)

	if cur, _ := p.tel.crawl.snapshot(); cur.Reading != "a.b.forward" {
		t.Errorf("forward pass reports reading %q, want a.b.forward", cur.Reading)
	}
}
