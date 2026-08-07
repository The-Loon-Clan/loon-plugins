package usenet

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// TestCheckSegmentsFoldsFailedChunks pins the module's stated invariant: a
// mostly-inconclusive check must NEVER overwrite a definitive verdict. When a
// chunk's connection dies mid-way, statBatch still returns real answers for the
// ids it reached and statUnknown for the rest — and those unknowns are exactly
// what the maxInconclusiveRatio guard exists to see. Discarding the failed
// chunk wholesale counted its segments neither missing nor unknown, so a
// release where half the segments were never answered got a definitive
// "healthy" written over a real broken/dead verdict. The corpse-pool pattern
// (idle sockets die, a later chunk succeeds on a fresh dial) produces exactly
// that shape, and it happened on prod.
func TestCheckSegmentsFoldsFailedChunks(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("<%d@x>", i)
		}
		return out
	}
	transportErr := errors.New("read tcp: connection reset by peer")

	t.Run("failed chunk's unknowns must reach the ratio guard", func(t *testing.T) {
		// 400 segments, chunk 200. Chunk 1 dies with nothing answered; chunk 2
		// answers everything present. Half the release was never checked, so
		// the one thing that must NOT happen is a definitive verdict — the
		// doubt is transport-minted, so the release is abandoned and the pass
		// counts it toward deciding whether the pool is sick.
		segs := releaseSegments{Data: ids(400)}
		calls := 0
		_, _, _, _, outcome := checkSegments(context.Background(), segs, 200, 0,
			func(chunk []string) ([]statResult, error) {
				calls++
				res := make([]statResult, len(chunk))
				if calls == 1 {
					for i := range res {
						res[i] = statUnknown
					}
					return res, transportErr
				}
				for i := range res {
					res[i] = statPresent
				}
				return res, nil
			})
		if outcome == healthWritten {
			t.Fatal("a check that never answered half the segments produced a definitive verdict — " +
				"the failed chunk's unknowns were discarded instead of feeding the inconclusive guard")
		}
		if outcome != healthSkipTransport {
			t.Fatalf("outcome = %v, want healthSkipTransport", outcome)
		}
	})

	t.Run("answers received before the connection died still count", func(t *testing.T) {
		// Chunk 1: 190 confirmed missing (430s), then the socket dies with 10
		// unanswered. Chunk 2: all present. 10/400 unknown is within the ratio,
		// so a verdict IS written — and it must include the 190 real 430s the
		// old code threw away with the transport error.
		segs := releaseSegments{Data: ids(400)}
		calls := 0
		verdict, total, missing, _, outcome := checkSegments(context.Background(), segs, 200, 0,
			func(chunk []string) ([]statResult, error) {
				calls++
				res := make([]statResult, len(chunk))
				if calls == 1 {
					for i := range res {
						if i < 190 {
							res[i] = statMissing
						} else {
							res[i] = statUnknown
						}
					}
					return res, transportErr
				}
				for i := range res {
					res[i] = statPresent
				}
				return res, nil
			})
		if outcome != healthWritten {
			t.Fatalf("outcome = %v, want healthWritten (10/400 unknown is within the ratio)", outcome)
		}
		if missing != 190 {
			t.Errorf("missing = %d, want the 190 confirmed 430s from the failed chunk", missing)
		}
		if total != 400 {
			t.Errorf("total = %d, want 400", total)
		}
		if verdict != healthDead {
			t.Errorf("verdict = %q, want %q (190 missing, no par2)", verdict, healthDead)
		}
	})

	t.Run("three transport failures still abort the release", func(t *testing.T) { //nolint:dupl
		segs := releaseSegments{Data: ids(600)}
		calls := 0
		_, _, _, _, outcome := checkSegments(context.Background(), segs, 100, 0,
			func(chunk []string) ([]statResult, error) {
				calls++
				res := make([]statResult, len(chunk))
				for i := range res {
					res[i] = statUnknown
				}
				return res, transportErr
			})
		// Row-level, NOT pass-level. Three dead sockets in one release still
		// abandons that release — the corpse-pool bail-out is intact — but the
		// pass now decides for itself whether enough releases in a row failed
		// this way to mean the pool is sick. Making this end the whole pass is
		// what left the checker doing nothing for weeks against a provider
		// that times out routinely.
		if outcome != healthSkipTransport {
			t.Fatalf("outcome = %v, want healthSkipTransport (abandon the release, let the pass judge the pool)", outcome)
		}
		if calls != 3 {
			t.Errorf("stat called %d times, want 3 — the corpse-pool bail-out must survive the fix", calls)
		}
	})

	t.Run("an exhausted pool still ends the pass immediately", func(t *testing.T) {
		segs := releaseSegments{Data: ids(600)}
		calls := 0
		_, _, _, _, outcome := checkSegments(context.Background(), segs, 100, 0,
			func(chunk []string) ([]statResult, error) {
				calls++
				return nil, nntp.ErrPoolBusy
			})
		// Unchanged and load-bearing: a busy pool means the CRAWLER holds the
		// connections, and waiting for them is the crawler's loss. First
		// refusal ends it, without burning a second lease attempt.
		if outcome != healthSkipTransient {
			t.Fatalf("outcome = %v, want healthSkipTransient — a busy pool must still yield at once", outcome)
		}
		if calls != 1 {
			t.Errorf("stat called %d times, want 1 — the sweep queued behind the crawler", calls)
		}
	})

	t.Run("clean pass still writes", func(t *testing.T) {
		segs := releaseSegments{Data: ids(300), Par2: ids(50)}
		verdict, total, missing, par2, outcome := checkSegments(context.Background(), segs, 200, 0,
			func(chunk []string) ([]statResult, error) {
				return make([]statResult, len(chunk)), nil // zero value = statPresent
			})
		if outcome != healthWritten || verdict != healthHealthy || missing != 0 || total != 350 || par2 != 50 {
			t.Errorf("clean pass: verdict=%q total=%d missing=%d par2=%d outcome=%v",
				verdict, total, missing, par2, outcome)
		}
	})
}

// TestHealthVerdict pins the scoring rule: missing data is survivable exactly as
// far as the SURVIVING par2 blocks can rebuild it.
func TestHealthVerdict(t *testing.T) {
	cases := []struct {
		name                                string
		missingData, par2Total, par2Missing int
		want                                string
	}{
		{"nothing missing", 0, 100, 0, healthHealthy},
		{"nothing missing, no par2 either", 0, 0, 0, healthHealthy},
		{"healthy even with par2 gone", 0, 50, 50, healthHealthy},
		{"repairable: fewer missing than par2", 10, 50, 0, healthBroken},
		{"repairable: exactly as many as par2", 50, 50, 0, healthBroken},
		{"unrepairable: one more than par2", 51, 50, 0, healthDead},
		{"par2 losses shrink the budget", 30, 50, 40, healthDead}, // only 10 survive
		{"par2 losses still leave enough", 10, 50, 40, healthBroken},
		{"no par2 at all: any loss is fatal", 1, 0, 0, healthDead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthVerdict(tc.missingData, tc.par2Total, tc.par2Missing)
			if got != tc.want {
				t.Errorf("healthVerdict(%d, %d, %d) = %q, want %q",
					tc.missingData, tc.par2Total, tc.par2Missing, got, tc.want)
			}
		})
	}
}

// TestClassifyStat: ONLY a server-issued 430 means "missing". Everything else is
// inconclusive — treating a timeout as a missing article is how a healthy
// archive gets wrongly condemned.
func TestClassifyStat(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want statResult
	}{
		{"no error = present", nil, statPresent},
		{"430 = definitively missing", nntp.Error{Code: 430, Msg: "no such article"}, statMissing},
		{"423 = inconclusive", nntp.Error{Code: 423, Msg: "no such article number"}, statUnknown},
		{"400 = inconclusive", nntp.Error{Code: 400, Msg: "service discontinued"}, statUnknown},
		{"timeout = inconclusive, NOT missing", errors.New("i/o timeout"), statUnknown},
		{"wrapped 430 still counts", fmt.Errorf("stat: %w", nntp.Error{Code: 430}), statMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStat(tc.err); got != tc.want {
				t.Errorf("classifyStat(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsProtocolError decides whether a failed STAT costs us the connection. A
// 430 is a normal answer and must not; an i/o error means the connection is
// finished. Getting this backwards would either destroy the pool one missing
// article at a time, or keep using a broken socket.
func TestIsProtocolError(t *testing.T) {
	if !isProtocolError(nntp.Error{Code: 430}) {
		t.Error("430 should be a protocol error (keep the connection)")
	}
	if !isProtocolError(fmt.Errorf("wrapped: %w", nntp.Error{Code: 423})) {
		t.Error("wrapped protocol errors should still be recognised")
	}
	if isProtocolError(errors.New("connection reset by peer")) {
		t.Error("transport failures must NOT be treated as protocol errors")
	}
}

func TestIsPar2Subject(t *testing.T) {
	cases := map[string]bool{
		`Some.Release.vol000+01.par2`: true,
		`Some.Release.PAR2`:           true,
		`"Some.Release.par2" yEnc`:    true,
		`Some.Release.part01.rar`:     false,
		`Some.Release.mkv`:            false,
	}
	for subject, want := range cases {
		if got := isPar2Subject(subject); got != want {
			t.Errorf("isPar2Subject(%q) = %v, want %v", subject, got, want)
		}
	}
}

// TestParseNzbSegments checks the split that the whole verdict rests on, plus
// message-id normalisation (stored bare, STAT wants angle brackets).
func TestParseNzbSegments(t *testing.T) {
	raw := `<?xml version="1.0" encoding="iso-8859-1" ?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1" subject="Show.S01E01.mkv (1/2)">
    <groups><group>alt.test</group></groups>
    <segments>
      <segment bytes="100" number="1">data1@example.com</segment>
      <segment bytes="100" number="2">&lt;data2@example.com&gt;</segment>
    </segments>
  </file>
  <file poster="p" date="1" subject="Show.S01E01.vol000+01.par2 (1/1)">
    <groups><group>alt.test</group></groups>
    <segments>
      <segment bytes="50" number="1">par1@example.com</segment>
    </segments>
  </file>
</nzb>`
	gz, err := gzipBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := parseNzbSegments(gz)
	if err != nil {
		t.Fatalf("parseNzbSegments: %v", err)
	}
	if len(segs.Data) != 2 {
		t.Errorf("data segments = %d, want 2 (%v)", len(segs.Data), segs.Data)
	}
	if len(segs.Par2) != 1 {
		t.Errorf("par2 segments = %d, want 1 (%v)", len(segs.Par2), segs.Par2)
	}
	for _, id := range append(append([]string{}, segs.Data...), segs.Par2...) {
		if len(id) < 2 || id[0] != '<' || id[len(id)-1] != '>' {
			t.Errorf("message-id %q is not in angle-bracket form", id)
		}
	}
	if segs.Data[0] != "<data1@example.com>" {
		t.Errorf("bare id was not normalised: %q", segs.Data[0])
	}
	if segs.Data[1] != "<data2@example.com>" {
		t.Errorf("already-bracketed id was double-wrapped: %q", segs.Data[1])
	}
}

func TestParseNzbSegmentsRejectsGarbage(t *testing.T) {
	if _, err := parseNzbSegments([]byte("not gzip")); err == nil {
		t.Error("expected an error for a non-gzip blob")
	}
	gz, err := gzipBytes([]byte("<nzb><unclosed>"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseNzbSegments(gz); err == nil {
		t.Error("expected an error for malformed XML")
	}
}

// TestParseNzbSegmentsLegacyCharset is a regression guard for a real failure:
// the standard NZB preamble declares iso-8859-1, and Go's XML decoder refuses
// any non-UTF-8 declaration unless a CharsetReader is supplied. Our own NZBs are
// written UTF-8, so only IMPORTED files hit this — and a parse failure means the
// release's health can never be determined.
func TestParseNzbSegmentsLegacyCharset(t *testing.T) {
	for _, charset := range []string{"iso-8859-1", "ISO-8859-1", "windows-1252", "us-ascii", "UTF-8"} {
		raw := `<?xml version="1.0" encoding="` + charset + `" ?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1" subject="Caf&#233;.S01E01.mkv (1/1)">
    <segments><segment bytes="1" number="1">a@b</segment></segments>
  </file>
</nzb>`
		gz, err := gzipBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		segs, err := parseNzbSegments(gz)
		if err != nil {
			t.Errorf("charset %s: %v", charset, err)
			continue
		}
		if len(segs.Data) != 1 {
			t.Errorf("charset %s: got %d data segments, want 1", charset, len(segs.Data))
		}
	}
}

func TestNzbCharsetReaderRejectsUnknown(t *testing.T) {
	if _, err := nzbCharsetReader("shift_jis", nil); err == nil {
		t.Error("expected an unsupported charset to be reported, not silently mangled")
	}
}

// TestHealthOutcomeSemantics pins the four-way outcome, which exists because
// prod's two-way version misbehaves in both directions: it writes nothing when a
// check fails, so the same rows return next pass and the drain loop spins with
// no backoff — while stamping everything instead would make a release skipped
// for a momentary busy pool wait the whole recheck window.
func TestHealthOutcomeSemantics(t *testing.T) {
	if healthWritten == healthSkipPermanent || healthSkipPermanent == healthSkipTransient ||
		healthSkipTransient == healthSkipRow || healthSkipRow == healthWritten {
		t.Fatal("outcomes must be distinct")
	}
	// Permanent = bad data; the row gets stamped so it stops jamming the queue.
	// Transient = bad luck with the POOL; end the pass, retry promptly.
	// Row = doubt about THIS release only; skip it and keep the pass going.
	// This test documents the contract the sweep loop relies on; the loop's
	// branches are asserted by the switch in runHealthCheck.
}

// A release the SERVER answers ambiguously is deterministically inconclusive:
// it will stat exactly the same next pass, and the candidate query sorts
// requested rechecks first — so ending the whole pass on it (the old
// transient handling) let one pathological release starve every other health
// check forever. Server-answered doubt must skip the ROW; only doubt minted
// by dying connections may end the pass.
func TestCheckSegmentsRowDoubtVsPoolDoubt(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("<%d@x>", i)
		}
		return out
	}
	allUnknown := func(chunk []string) ([]statResult, error) {
		res := make([]statResult, len(chunk))
		for i := range res {
			res[i] = statUnknown
		}
		return res, nil // the server ANSWERED — no transport failure
	}
	segs := releaseSegments{Data: ids(100)}
	if _, _, _, _, outcome := checkSegments(context.Background(), segs, 200, 0, allUnknown); outcome != healthSkipRow {
		t.Fatalf("outcome = %v, want healthSkipRow — server-answered doubt is a property of the release, not the pool", outcome)
	}

	// Same doubt ratio, but produced by a died connection. That says nothing
	// about the release, so the ROW is skipped — and it is the pass, counting
	// consecutive ones, that decides whether the pool is sick.
	transportErr := errors.New("read tcp: connection reset by peer")
	failing := func(chunk []string) ([]statResult, error) {
		res := make([]statResult, len(chunk))
		for i := range res {
			res[i] = statUnknown
		}
		return res, transportErr
	}
	if _, _, _, _, outcome := checkSegments(context.Background(), segs, 200, 0, failing); outcome != healthSkipTransport {
		t.Fatalf("outcome = %v, want healthSkipTransport — connection-minted doubt is not evidence about the release", outcome)
	}

	// And an exhausted pool is a third, distinct answer. Collapsing these two
	// into one outcome is the bug: the sweep reported "pool busy or failing"
	// and checked nothing, for weeks, while the pool was fine and the provider
	// was merely slow.
	if _, _, _, _, outcome := checkSegments(context.Background(), segs, 200, 0,
		func([]string) ([]statResult, error) { return nil, nntp.ErrPoolEmpty }); outcome != healthSkipTransient {
		t.Fatalf("outcome = %v, want healthSkipTransient — an empty pool is not a provider timeout", outcome)
	}
}

// A salvaged release's NZB lists only the segments that were ever fetched —
// every one of which still exists on the server — so a listed-segments-only
// tally flipped its broken verdict to healthy on the first routine recheck,
// erasing the one mark that keeps an incomplete release from serving as
// complete. The recorded claimed total is the antidote: the shortfall counts
// as missing data before scoring, and only nzb-heal replacing the row with a
// complete copy (claimed == listed again) releases it.
func TestCheckSegmentsClaimedTotalKeepsBrokenDurable(t *testing.T) {
	mkIDs := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("<%s-%d@x>", prefix, i)
		}
		return out
	}
	segs := releaseSegments{Data: mkIDs("d", 30), Par2: mkIDs("p", 10)}
	allPresent := func(batch []string) ([]statResult, error) {
		return make([]statResult, len(batch)), nil // zero value = statPresent
	}

	// 40 listed, 45 claimed: 5 never-fetched data segments outstanding, and
	// the 10 surviving par2 can rebuild them — broken, never healthy.
	verdict, total, missing, _, outcome := checkSegments(context.Background(), segs, 200, 45, allPresent)
	if outcome != healthWritten || verdict != healthBroken || missing != 5 || total != 45 {
		t.Errorf("claimed 45 / listed 40, all present: verdict=%q total=%d missing=%d outcome=%v, want broken/45/5/written",
			verdict, total, missing, outcome)
	}

	// Claimed 60: 20 outstanding, beyond the 10 par2 — dead.
	if verdict, _, _, _, _ := checkSegments(context.Background(), segs, 200, 60, allPresent); verdict != healthDead {
		t.Errorf("claimed 60 / listed 40: verdict=%q, want %q", verdict, healthDead)
	}

	// A normal release (claimed == listed): no baseline, healthy as before.
	if verdict, _, _, _, _ := checkSegments(context.Background(), segs, 200, 40, allPresent); verdict != healthHealthy {
		t.Errorf("claimed == listed, all present: verdict=%q, want %q", verdict, healthHealthy)
	}
}

// The fix for a sweep that checked nothing for weeks.
//
// A release that fails on a provider timeout used to end the WHOLE pass. On a
// provider that times out routinely the first release tripped it every time,
// so the checker did no work at all — while logging "connection pool busy or
// failing", which reads like ordinary contention. Whether the pool is
// exhausted and whether one release's reads timed out are different facts, and
// only the first should stop a pass.
func TestPassYieldTakesARunNotOneBadRelease(t *testing.T) {
	t.Run("one timeout does not end the pass", func(t *testing.T) {
		y := passYield{limit: 5}
		if y.observe(healthSkipTransport) {
			t.Fatal("a single provider timeout ended the pass — this is the bug: " +
				"against a flaky provider the sweep never gets past its first release")
		}
	})

	t.Run("a run of them does", func(t *testing.T) {
		y := passYield{limit: 3}
		for i := 1; i <= 2; i++ {
			if y.observe(healthSkipTransport) {
				t.Fatalf("yielded after %d, want 3", i)
			}
		}
		if !y.observe(healthSkipTransport) {
			t.Error("three in a row did not yield — a pool full of dead sockets " +
				"costs an op-timeout per release, so grinding on is not free")
		}
		if y.total != 3 {
			t.Errorf("total = %d, want 3", y.total)
		}
	})

	t.Run("any release that reaches an answer resets the run", func(t *testing.T) {
		// This is what makes the difference between "flaky provider" and "sick
		// pool": if releases are still coming back with answers in between, the
		// connections plainly work.
		for _, ok := range []healthOutcome{healthWritten, healthSkipPermanent, healthSkipRow} {
			y := passYield{limit: 2}
			y.observe(healthSkipTransport)
			y.observe(ok)
			if y.observe(healthSkipTransport) {
				t.Errorf("outcome %v did not reset the run; two failures either side "+
					"of a success were treated as consecutive", ok)
			}
			if y.total != 2 {
				t.Errorf("outcome %v: total = %d, want 2 — resets must not lose the count",
					ok, y.total)
			}
		}
	})

	t.Run("a zero limit disables the yield rather than yielding at once", func(t *testing.T) {
		// Config coerces this to a default, so a zero can only arrive from a
		// caller that skipped that — and "yield immediately, forever" would be
		// the worst possible reading of it.
		y := passYield{limit: 0}
		for i := 0; i < 50; i++ {
			if y.observe(healthSkipTransport) {
				t.Fatalf("yielded at %d with the yield disabled", i)
			}
		}
	})
}

// The saving that makes the sweep viable, measured rather than asserted.
//
// The sweep borrows the crawler's pool and inherited its OpTimeout, which is
// sized for a 3000-article OVER. A socket the provider had quietly closed
// therefore cost a full minute to discover — and up to three times per release
// before the release was abandoned. A measured production pass spent 19
// minutes to check ONE release of fifty.
//
// A STAT is a single short line. If it has not answered in seconds the
// connection is dead, and the entire value of learning that is learning it
// cheaply.
func TestStatBatchUsesItsOwnDeadlineNotThePoolsOpTimeout(t *testing.T) {
	s := newStallingNNTP(t)
	pool := nntp.NewPool(nntp.PoolConfig{
		Addr: s.ln.Addr().String(), Size: 1,
		DialTimeout: 2 * time.Second,
		// Stand in for the crawler's 60s: long enough that falling back to it
		// is unmistakable in the elapsed time.
		OpTimeout: 10 * time.Second,
	})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	p := &Plugin{}
	const statTimeout = 300 * time.Millisecond
	start := time.Now()
	_, err := p.statBatch(context.Background(), pool, []string{"<a@x>"}, statTimeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a STAT that was never answered returned no error")
	}
	// Generous ceiling: the point is 300ms-ish versus the pool's 10s, not a
	// tight bound that would flake on a loaded CI box.
	if elapsed > 3*time.Second {
		t.Errorf("statBatch took %v — it fell back to the pool's OpTimeout instead of "+
			"its own per-STAT deadline, which is what made a sweep spend a minute "+
			"per dead socket", elapsed)
	}
}

// And zero must mean "no deadline of our own", not "expire immediately" — a
// caller that skipped config defaulting would otherwise fail every STAT.
func TestStatBatchZeroTimeoutFallsBackToThePool(t *testing.T) {
	s := newFakeNNTP(t, false)
	pool := nntp.NewPool(nntp.PoolConfig{
		Addr: s.ln.Addr().String(), Size: 1,
		DialTimeout: 2 * time.Second, OpTimeout: 5 * time.Second,
	})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	p := &Plugin{}
	res, err := p.statBatch(context.Background(), pool, []string{"<a@x>"}, 0)
	if err != nil {
		t.Fatalf("statBatch with no deadline of its own: %v", err)
	}
	if len(res) != 1 || res[0] != statPresent {
		t.Errorf("res = %v, want one statPresent — the server answered 223", res)
	}
}
