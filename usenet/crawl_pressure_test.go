package usenet

import (
	"context"
	"errors"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// The forward crawl's pressure gate. Its thresholds must sit ABOVE the
// backfill's, or the two jobs yield together and nothing distinguishes "pause
// history to let the builder catch up" from "stop storing because storing
// destroys".
func TestCrawlYieldsLaterThanBackfill(t *testing.T) {
	var c Config
	c.applyDefaults()

	if c.CrawlPressureHighPct <= c.BackfillPressureHighPct {
		t.Errorf("crawl gate %d%% must sit above the backfill gate %d%% — new articles "+
			"matter more than history, so the crawl yields only when storing would "+
			"actively destroy what is already staged",
			c.CrawlPressureHighPct, c.BackfillPressureHighPct)
	}
	if c.CrawlPressureHighPct >= 100 {
		t.Errorf("crawl gate %d%% never fires before the backend is completely full",
			c.CrawlPressureHighPct)
	}
}

// The crawl's EFFECTIVE stop is clamped to the eviction ceiling. The raw
// default (95) sits above the ceiling (92) that exists because past it Redis
// evicts rather than refuses — the crawl was permitted to begin staging inside
// the exact band the backfill treats as a hard stop. The clamp must preserve
// the crawl-beats-backfill ordering: effective stop still above the backfill
// high gate, so the crawl keeps the last of the room.
func TestCrawlStopClampsToTheEvictionCeiling(t *testing.T) {
	var c Config
	c.applyDefaults()

	if got := c.crawlStopPct(); got != c.BackfillPressureCeilingPct {
		t.Errorf("default crawl stop = %d%%, want the ceiling %d%% — the crawl may not "+
			"stage inside the eviction band", got, c.BackfillPressureCeilingPct)
	}
	if c.crawlStopPct() <= c.BackfillPressureHighPct {
		t.Errorf("clamped crawl stop %d%% fell to or below the backfill gate %d%% — "+
			"the crawl no longer wins the last of the room", c.crawlStopPct(), c.BackfillPressureHighPct)
	}

	// An operator running the crawl gate BELOW the ceiling keeps their number.
	c.CrawlPressureHighPct = 80
	if got := c.crawlStopPct(); got != 80 {
		t.Errorf("crawl gate 80 with ceiling %d: stop = %d, want 80", c.BackfillPressureCeilingPct, got)
	}
	// No ceiling configured (pg staging): the crawl gate stands alone.
	c.CrawlPressureHighPct, c.BackfillPressureCeilingPct = 95, 0
	if got := c.crawlStopPct(); got != 95 {
		t.Errorf("ceiling 0: stop = %d, want the raw crawl gate 95", got)
	}
}

// The decision itself, as the round loop makes it. Pinned as a table because
// the boundary matters: production sat at 99.8%, so a gate that only fired at a
// hard 100%% would have watched 640 releases a minute be destroyed and done
// nothing.
func TestCrawlPressureGateBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pressure float64
		pct      int
		want     bool
	}{
		{"healthy backend keeps crawling", 0.42, 95, false},
		{"busy but not dangerous", 0.90, 95, false},
		{"exactly at the threshold pauses", 0.95, 95, true},
		{"production's actual reading", 0.998, 95, true},
		{"completely full", 1.0, 95, true},
		// 0 disables the gate: an operator on a backend that cannot evict
		// (pg staging, or redis with noeviction) may not want it at all.
		{"zero disables the gate", 1.0, 0, false},
		{"unbounded backend reports 0 pressure", 0.0, 95, false},
	} {
		if got := shouldPauseForPressure(tc.pressure, tc.pct); got != tc.want {
			t.Errorf("%s: pressure %.3f against %d%% -> pause=%v, want %v",
				tc.name, tc.pressure, tc.pct, got, tc.want)
		}
	}
}

// An operator can raise or lower it live, like every other knob.
func TestCrawlPressureIsConfigurable(t *testing.T) {
	var c Config
	c.applyDefaults()
	if _, ok := c.knobFields()["crawl_pressure_high_pct"]; !ok {
		t.Error("crawl_pressure_high_pct is not admin-editable; a hardcoded operational " +
			"value cannot be tuned on the box where it is wrong")
	}
}

// stubStaging reports a fixed pressure. It EMBEDS stagingStore so the other
// methods exist without bodies; any call to one would nil-panic, which is the
// right outcome for a test that has wandered past what it means to cover.
type stubStaging struct {
	stagingStore
	pressureVal float64
	pressureErr error
}

func (s stubStaging) pressure(context.Context) (float64, error) {
	return s.pressureVal, s.pressureErr
}

// The wiring, not just the arithmetic: the crawl round must actually consult
// the gate. Mutation testing showed a correct shouldPauseForPressure and a
// crawl loop that ignored it produced a fully green suite.
func TestCrawlRoundConsultsThePressureGate(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()

	for _, tc := range []struct {
		name      string
		staging   stagingStore
		wantPause bool
	}{
		{"healthy backend crawls", stubStaging{pressureVal: 0.40}, false},
		{"full backend pauses", stubStaging{pressureVal: 0.998}, true},
		// Inside the eviction band: above the ceiling (92), below the raw
		// crawl gate (95). The gate must consult the CLAMPED stop — a gate
		// wired to the raw pct crawls straight through the band where Redis
		// evicts forming sets to make room.
		{"eviction band pauses via the clamp", stubStaging{pressureVal: 0.93}, true},
		// Fail OPEN: an unreadable gauge must not idle the crawler.
		{"unreadable pressure keeps crawling", stubStaging{pressureErr: errStubPressure}, false},
		// No staging wired at all (tests, boot ordering) must not panic.
		{"no staging backend", nil, false},
	} {
		p := &Plugin{
			staging: tc.staging,
			tel:     newTelemetry(),
			core:    &core.Core{Errors: core.NewErrorReporter(core.ErrorAdapter{})},
		}
		got, _ := p.pauseForStagingPressure(context.Background(), cfg)
		if got != tc.wantPause {
			t.Errorf("%s: pause=%v, want %v", tc.name, got, tc.wantPause)
		}
	}
}

var errStubPressure = errors.New("backend unreachable")
