package usenet

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
		// the only defensible outcome is a transient skip.
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
		if outcome != healthSkipTransient {
			t.Fatalf("outcome = %v, want healthSkipTransient", outcome)
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

	t.Run("three transport failures still abort the release", func(t *testing.T) {
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
		if outcome != healthSkipTransient {
			t.Fatalf("outcome = %v, want healthSkipTransient (the pool is sick, yield)", outcome)
		}
		if calls != 3 {
			t.Errorf("stat called %d times, want 3 — the corpse-pool bail-out must survive the fix", calls)
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

// TestHealthOutcomeSemantics pins the three-way outcome, which exists because
// prod's two-way version misbehaves in both directions: it writes nothing when a
// check fails, so the same rows return next pass and the drain loop spins with
// no backoff — while stamping everything instead would make a release skipped
// for a momentary busy pool wait the whole recheck window.
func TestHealthOutcomeSemantics(t *testing.T) {
	if healthWritten == healthSkipPermanent || healthSkipPermanent == healthSkipTransient {
		t.Fatal("outcomes must be distinct")
	}
	// Permanent = bad data; the row gets stamped so it stops jamming the queue.
	// Transient = bad luck; the row is left alone so it is retried promptly.
	// This test documents the contract the sweep loop relies on; the loop's
	// branches are asserted by the switch in runHealthCheck.
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
