package usenet

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// NZB health checking: STAT every segment of a stored NZB to find out whether
// the release is still downloadable. Providers expire articles, so a release
// that assembled fine last month can be partly or wholly gone.
//
// Two rules make this safe to run against a live indexer:
//
//   - It leases connections with pool.TryDo, never pool.Do. Health checking is
//     bookkeeping over the whole archive; if it queued for connections it would
//     starve the crawler, which is the job that actually matters.
//   - A result is only written when it is trustworthy. STAT is tri-state
//     (present / confirmed-missing / inconclusive), and a check that came back
//     mostly inconclusive must NOT overwrite a previous definitive verdict —
//     otherwise a flaky provider silently marks a healthy archive dead.

const (
	healthUnknown = "unknown"
	healthHealthy = "healthy"
	healthBroken  = "broken"
	healthDead    = "dead"
)

// maxInconclusiveRatio: above this share of unusable STAT answers we distrust
// the whole check and keep whatever verdict we had.
const maxInconclusiveRatio = 0.20

// releaseSegments is a release's message-ids, split by role. PAR2 segments are
// recovery data: their loss is survivable, and their survival is what decides
// whether missing data segments are repairable.
type releaseSegments struct {
	Data []string
	Par2 []string
}

// nzbXML is the subset of the NZB format health checking needs.
type nzbXML struct {
	Files []struct {
		Subject  string `xml:"subject,attr"`
		Segments struct {
			Segment []struct {
				ID string `xml:",chardata"`
			} `xml:"segment"`
		} `xml:"segments"`
	} `xml:"file"`
}

// parseNzbSegments extracts message-ids from a gzipped NZB, classifying each
// file as data or PAR2 by its subject.
//
// Obfuscated uploads whose subjects don't name their files yield par2=0, which
// makes the scoring pessimistic (any loss reads as dead). That is the correct
// bias: we cannot prove repairability we can't see.
func parseNzbSegments(gzipped []byte) (releaseSegments, error) {
	var out releaseSegments
	raw, err := gunzipBytes(gzipped)
	if err != nil {
		return out, fmt.Errorf("gunzip: %w", err)
	}
	var doc nzbXML
	dec := xml.NewDecoder(bytes.NewReader(raw))
	// Real NZBs commonly declare iso-8859-1 — it is what the newzbin DTD's own
	// preamble uses — and Go's decoder refuses any non-UTF-8 declaration unless
	// a CharsetReader is supplied. Without this, every imported NZB fails to
	// parse and its health can never be determined.
	dec.CharsetReader = nzbCharsetReader
	if err := dec.Decode(&doc); err != nil {
		return out, fmt.Errorf("parse nzb: %w", err)
	}
	for _, f := range doc.Files {
		par2 := isPar2Subject(f.Subject)
		for _, s := range f.Segments.Segment {
			id := strings.TrimSpace(s.ID)
			if id == "" {
				continue
			}
			// Stored bare; STAT wants the angle-bracket form.
			if !strings.HasPrefix(id, "<") {
				id = "<" + id + ">"
			}
			if par2 {
				out.Par2 = append(out.Par2, id)
			} else {
				out.Data = append(out.Data, id)
			}
		}
	}
	return out, nil
}

func isPar2Subject(subject string) bool {
	return strings.Contains(strings.ToLower(subject), ".par2")
}

// nzbCharsetReader decodes the legacy encodings NZB files declare. Latin-1 maps
// byte-for-byte onto the first 256 code points, so the conversion is a widening.
// Windows-1252 differs only in 0x80-0x9F (typographic punctuation) and is
// accepted on the same path: the fields we read are message-ids and subjects,
// where being slightly wrong about a smart quote is far better than refusing to
// parse the file at all.
func nzbCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "us-ascii", "ascii", "":
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		var sb strings.Builder
		sb.Grow(len(b))
		for _, c := range b {
			sb.WriteRune(rune(c))
		}
		return strings.NewReader(sb.String()), nil
	}
	return nil, fmt.Errorf("unsupported charset %q", charset)
}

// statResult is one segment's tri-state answer.
type statResult int

const (
	statPresent statResult = iota
	statMissing            // the server said 430: definitively not there
	statUnknown            // no usable answer; proves nothing either way
)

// classifyStat turns a Stat() outcome into the tri-state. Only a server-issued
// 430 counts as "missing" — every other failure is inconclusive, because
// treating a timeout as a missing article is how an archive gets wrongly
// condemned.
func classifyStat(err error) statResult {
	if err == nil {
		return statPresent
	}
	var e nntp.Error
	if errors.As(err, &e) {
		if e.Code == 430 {
			return statMissing
		}
		return statUnknown
	}
	return statUnknown
}

// isProtocolError reports whether err is a server response rather than a
// transport failure. It decides whether the pool keeps the connection: a 430
// ("no such article") is a normal answer and must NOT cost us the connection,
// while an i/o error means the connection is finished.
func isProtocolError(err error) bool {
	var e nntp.Error
	return errors.As(err, &e)
}

// healthVerdict scores a check. Missing data is survivable exactly as far as the
// surviving PAR2 blocks can rebuild it.
func healthVerdict(missingData, par2Total, par2Missing int) string {
	if missingData <= 0 {
		return healthHealthy
	}
	surviving := par2Total - par2Missing
	if surviving < 0 {
		surviving = 0
	}
	if missingData <= surviving {
		return healthBroken
	}
	return healthDead
}

// statBatch STATs one chunk of message-ids on a single leased connection.
//
// Returns the per-id results and whether the connection survived. Server
// responses (including 430) are recorded and the connection kept; a transport
// failure aborts the chunk and discards the connection, and every id not yet
// answered stays unknown rather than being assumed missing.
func (p *Plugin) statBatch(ctx context.Context, pool *nntp.Pool, ids []string, statTimeout time.Duration) ([]statResult, error) {
	out := make([]statResult, len(ids))
	for i := range out {
		out[i] = statUnknown
	}
	err := pool.TryDo(ctx, func(c *nntp.Conn) error {
		for i, id := range ids {
			if ctx.Err() != nil {
				return nil // keep the connection; the rest stay unknown
			}
			// Per STAT, not per lease. The pool applied the crawler's
			// OpTimeout on the way in — sized for a 3000-article OVER — and a
			// socket the provider had quietly closed therefore cost a full
			// minute to discover, three times per release. A STAT is one
			// short line; the point of the deadline is to learn the
			// connection is dead cheaply. The pool clears whatever deadline
			// is set when the lease ends, so this cannot follow the
			// connection to the crawler's next use.
			if statTimeout > 0 {
				_ = c.SetDeadline(time.Now().Add(statTimeout))
			}
			_, _, serr := c.Stat(id)
			out[i] = classifyStat(serr)
			if serr != nil && !isProtocolError(serr) {
				// Transport failure: the connection is unusable, so hand it back
				// to be discarded rather than STATting the rest through it.
				return serr
			}
		}
		return nil
	})
	return out, err
}

// healthOutcome says what to do with a release after checking it. The
// distinction matters: prod writes nothing when a check fails, so the same rows
// come back on the next pass and the drain loop spins with no backoff. Stamping
// everything instead would be just as wrong — a release skipped because the pool
// was momentarily busy would then wait the full recheck window.
type healthOutcome int

const (
	healthWritten       healthOutcome = iota // verdict recorded
	healthSkipPermanent                      // bad data: stamp it so it stops jamming the queue
	healthSkipTransient                      // the POOL has nothing to give: end the pass
	// healthSkipTransport — we HELD a connection and the provider failed the
	// request (an i/o timeout mid-STAT). Row-level, unlike the above.
	//
	// These were the same outcome, and it made the sweep useless. The yield
	// was decided per release but ended the whole pass, so against a provider
	// that times out routinely the FIRST release tripped it on every pass and
	// the checker checked nothing — for weeks — while logging a plausible
	// "connection pool busy or failing". Whether the pool is exhausted and
	// whether one release's reads timed out are different facts and now have
	// different outcomes: the first still yields immediately so the crawler
	// keeps priority, the second moves to the next release and only yields
	// after HealthTransportYield of them in a row.
	healthSkipTransport
	// healthSkipRow — THIS row's answers were too doubtful to trust, but the
	// pool is fine. Continue to the next row. Splitting this from transient
	// was a confirmed high: a release whose ids draw deterministic non-430
	// replies is inconclusive on EVERY pass, and because candidates order
	// never-checked/recheck-requested rows first, breaking the pass on it
	// meant one pathological release starved catalogue-wide health checking
	// forever — with a log line indistinguishable from pool contention.
	healthSkipRow
)

// checkOne STATs an entire release and returns its verdict plus what should be
// persisted.
func (p *Plugin) checkOne(ctx context.Context, pool *nntp.Pool, row healthRow, chunk int, statTimeout time.Duration) (verdict string, totalSegs, missingData, par2Total int, outcome healthOutcome) {
	segs, err := parseNzbSegments(row.Data)
	if err != nil {
		// A blob we can't parse is a storage problem, not evidence about the
		// articles — never let it downgrade a verdict.
		p.reportErr(ctx, "usenet/health-parse", fmt.Errorf("nzb %d: %w", row.ID, err))
		return "", 0, 0, 0, healthSkipPermanent
	}
	return checkSegments(ctx, segs, chunk, row.Total, func(ids []string) ([]statResult, error) {
		return p.statBatch(ctx, pool, ids, statTimeout)
	})
}

// checkSegments runs the STAT tally over a release's segments and decides the
// verdict. Split from checkOne with the batch function injected so the
// transport-failure accounting is testable without a live NNTP pool — the
// accounting is where this went wrong in production.
//
// claimedTotal is the release's RECORDED segment count (0 = unknown). When it
// exceeds what the NZB lists, the difference is missing DATA by definition:
// a salvaged release's blob lists only the segments that were ever fetched,
// every one of which still exists on the server, so a listed-segments-only
// tally would flip its broken verdict to healthy on the first recheck —
// erasing the one mark that keeps an incomplete release from serving as
// complete. The baseline keeps the verdict honest until nzb-heal replaces
// the row with a genuinely complete copy (whose claimed total matches its
// listing again).
func checkSegments(ctx context.Context, segs releaseSegments, chunk, claimedTotal int, stat func(ids []string) ([]statResult, error)) (verdict string, totalSegs, missingData, par2Total int, outcome healthOutcome) {
	total := len(segs.Data) + len(segs.Par2)
	if total == 0 {
		return "", 0, 0, 0, healthSkipPermanent
	}
	baseline := 0
	if claimedTotal > total {
		baseline = claimedTotal - total
		total = claimedTotal
	}

	var missData, missPar2, unknown int
	transportFails := 0
	count := func(ids []string, missing *int) error {
		for start := 0; start < len(ids); start += chunk {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			end := min(start+chunk, len(ids))
			res, err := stat(ids[start:end])
			if err != nil && (errors.Is(err, nntp.ErrPoolBusy) || errors.Is(err, nntp.ErrPoolEmpty)) {
				// The crawler needs the connections; stop and try again later
				// rather than waiting for them.
				return err
			}
			// The chunk's results count even when its connection died partway.
			// statBatch pre-fills every id statUnknown and classifies the ids
			// answered before the failure, so the answered ones are real
			// verdicts (including confirmed-missing 430s) and the unanswered
			// remainder is exactly the doubt the inconclusive guard below must
			// see. Discarding a failed chunk counted its segments neither
			// missing nor unknown while total still included them — a
			// mostly-unchecked release then sailed past the guard and had a
			// definitive "healthy" written over a real broken/dead verdict.
			for _, r := range res {
				switch r {
				case statMissing:
					*missing++
				case statUnknown:
					unknown++
				}
			}
			if err != nil {
				// Transport failure: the pool discarded that connection, and the
				// next chunk would draw another. One bad socket is a blip, but a
				// run of them means the POOL is sick (providers kill idle NNTP
				// sessions, so after an idle gap every socket is a corpse) — and
				// grinding on costs an op-timeout per corpse. A sweep on prod
				// spent 10+ silent minutes doing exactly that. Yield; the rows
				// stay untouched and the next sweep gets a refreshed pool.
				transportFails++
				if transportFails >= 3 {
					return err
				}
			}
		}
		return nil
	}
	if err := count(segs.Data, &missData); err != nil {
		return "", 0, 0, 0, transportOrPool(err)
	}
	if err := count(segs.Par2, &missPar2); err != nil {
		return "", 0, 0, 0, transportOrPool(err)
	}

	// Too much doubt: keep whatever verdict this release already had. The
	// baseline is excluded from the doubt ratio — those segments are KNOWN
	// missing, not unanswered. WHERE the doubt came from decides who skips:
	// unknowns minted by dying connections mean the POOL is sick (yield the
	// pass, same as the corpse-pool bail-out), but unknowns the server
	// actually answered are a property of this release — it will be exactly
	// as inconclusive next pass, and candidates sort it first, so ending the
	// pass on it starved every other check forever. Row-level: move on.
	if float64(unknown)/float64(total-baseline) > maxInconclusiveRatio {
		if transportFails > 0 {
			// Doubt minted by dying sockets, not by the server's answers — so
			// this says nothing about the release. Skip the ROW; the pass
			// decides for itself whether enough of these in a row mean the
			// pool is sick.
			return "", 0, 0, 0, healthSkipTransport
		}
		return "", 0, 0, 0, healthSkipRow
	}
	missData += baseline
	return healthVerdict(missData, len(segs.Par2), missPar2), total, missData, len(segs.Par2), healthWritten
}

// passYield decides when a RUN of provider timeouts means the pool is sick
// rather than one release being unlucky.
//
// Split out of the sweep loop for the same reason checkSegments was split out
// of checkOne: the loop around it needs a live NNTP pool to run, and the
// accounting is exactly where this went wrong. The old version had no
// accounting at all — one bad release ended the pass — so the sweep checked
// nothing for weeks against a provider that times out routinely.
type passYield struct {
	limit int // consecutive failures before giving up; 0 disables the yield
	run   int // consecutive transport-failed releases right now
	total int // transport-failed releases this pass, for the log
}

// observe folds one release's outcome in and reports whether the pass should
// stop. Any release that reaches an answer — a verdict, an unreadable blob, or
// server-answered doubt — resets the run: those prove the pool is working.
func (y *passYield) observe(outcome healthOutcome) (yield bool) {
	if outcome != healthSkipTransport {
		y.run = 0
		return false
	}
	y.total++
	y.run++
	return y.limit > 0 && y.run >= y.limit
}

// transportOrPool separates "the pool had nothing to lend" from "we held a
// connection and the provider failed the request". Only the pool sentinels end
// a pass: they mean the crawler is using the connections, and waiting for them
// is the crawler's loss. A ctx error counts as pool-level too — that is
// shutdown, and there is no next release to try.
func transportOrPool(err error) healthOutcome {
	if errors.Is(err, nntp.ErrPoolBusy) || errors.Is(err, nntp.ErrPoolEmpty) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return healthSkipTransient
	}
	return healthSkipTransport
}

// healthBackend is where candidates come from and verdicts go. Internal mode
// is the plugin's own nzbs table; host mode is the ReleaseHealthStore
// capability, so with sink=host the plugin sweeps the HOST'S catalogue — the
// releases it delivered through the ReleaseSink — with its own pool and
// verdict logic.
type healthBackend interface {
	candidates(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error)
	setVerdict(ctx context.Context, id int64, status string, total, missing, par2 int) error
	touch(ctx context.Context, id int64) error
	// clearRecheck drops a user's recheck request without stamping
	// checked-at. Only reachable for a row whose UserRequested is set, which
	// internal mode never produces.
	clearRecheck(ctx context.Context, id int64) error
}

type internalHealth struct{ st HealthStore }

func (b internalHealth) candidates(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error) {
	return b.st.nzbsNeedingHealthCheck(ctx, limit, recheckDays, minAgeHours)
}
func (b internalHealth) setVerdict(ctx context.Context, id int64, status string, total, missing, par2 int) error {
	return b.st.updateNzbHealth(ctx, id, status, total, missing, par2)
}
func (b internalHealth) touch(ctx context.Context, id int64) error {
	return b.st.touchHealthChecked(ctx, id)
}

// clearRecheck is a no-op in internal mode: there is no site to request a
// recheck from, so the plugin's own table carries no request column and no row
// it yields is ever UserRequested.
func (b internalHealth) clearRecheck(context.Context, int64) error { return nil }

type hostHealth struct{ hs pluginapi.ReleaseHealthStore }

func (b hostHealth) candidates(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error) {
	cands, err := b.hs.HealthCandidates(ctx, limit, recheckDays, minAgeHours)
	if err != nil {
		return nil, err
	}
	rows := make([]healthRow, len(cands))
	for i, c := range cands {
		rows[i] = healthRow{ID: c.ID, Data: c.NZBGz, Total: c.TotalSegments,
			UserRequested: c.UserRequested}
	}
	return rows, nil
}
func (b hostHealth) setVerdict(ctx context.Context, id int64, status string, total, missing, par2 int) error {
	return b.hs.SetHealthVerdict(ctx, id, status, total, missing, par2)
}
func (b hostHealth) touch(ctx context.Context, id int64) error {
	return b.hs.TouchHealthChecked(ctx, id)
}
func (b hostHealth) clearRecheck(ctx context.Context, id int64) error {
	return b.hs.ClearHealthRecheckRequest(ctx, id)
}

// resolveHealthBackend mirrors resolveSink: host mode without the capability
// refuses loudly — silently sweeping the plugin's (empty) table while the host
// catalogue rots unchecked is the worse failure.
func (p *Plugin) resolveHealthBackend() (healthBackend, error) {
	if p.cfg.Sink == SinkHost {
		hs, ok := pluginapi.LookupReleaseHealthStore(p.core)
		if !ok {
			return nil, fmt.Errorf(
				"sink=host but this host registered no ReleaseHealthStore — deploy a host build that wires the health store, or set plugins.usenet.sink=internal for a standalone catalogue")
		}
		return hostHealth{hs: hs}, nil
	}
	return internalHealth{st: p.st}, nil
}

// runHealthCheck sweeps releases that are due a check.
func (p *Plugin) runHealthCheck(ctx context.Context) {
	if ctx == nil {
		return
	}
	// The read-only write gate (writegate.go). Every pass asks, because this
	// pipeline has four different ways to be started and only one of them ever
	// reached schedule.WriteGate.
	if !p.mayWrite(ctx, p.healthJob) {
		return
	}
	if !p.healthMu.TryLock() {
		p.healthJob.Log("health check already running — skipping overlap")
		return
	}
	defer p.healthMu.Unlock()
	cfg := p.effective(ctx)
	// Health competes for the same idle connections the crawler wants; running
	// it on two workers at once doubles that pressure for no extra coverage.
	if !p.withLease(ctx, leaseScopeJob, jobNameHealth, p.leaseTTL(cfg), func(ctx context.Context) {
		p.healthLocked(ctx, cfg)
	}) {
		p.healthJob.Log("health check skipped — another worker holds this job")
		p.healthJob.SetIdle(p.nextHealth(cfg))
	}
}

func (p *Plugin) healthLocked(ctx context.Context, cfg Config) {
	p.healthJob.SetRunning()

	// The sweep borrows the FLEET's primary pool. A private ensurePool here
	// broke both halves of the design: its TryDo tested an otherwise-unused
	// pool (always "idle", never sensing crawler pressure) and its dials were
	// invisible to the account-cap machinery — the provider saw fleet-size
	// PLUS a whole second pool.
	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.healthJob.Log("no server configured — add one in the admin wizard")
			p.healthJob.SetIdle(p.nextHealth(cfg))
			return
		}
		p.healthJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/health-pool", err)
		return
	}
	pool := runs[0].pool

	backend, err := p.resolveHealthBackend()
	if err != nil {
		p.healthJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/health-backend", err)
		return
	}
	rows, err := backend.candidates(ctx, cfg.HealthBatchSize, cfg.HealthRecheckDays, cfg.HealthMinAgeHours)
	if err != nil {
		p.healthJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/health-candidates", err)
		return
	}
	if len(rows) == 0 {
		p.healthJob.Log("no releases due a health check")
		p.healthJob.SetIdle(p.nextHealth(cfg))
		return
	}
	// Log the sweep's start, not just its end: a big release is thousands of
	// STATs and a sweep can legitimately run for minutes — "running with no
	// logs" reads as hung (and once, grinding a corpse pool, it effectively
	// was).
	p.healthJob.Log("health check: sweeping %d release(s) due a check", len(rows))

	var checked, unreadable int
	var yielded, poolExhausted bool
	var inconclusive int
	var inconclusiveRequests int
	var inconclusiveIDs []int64
	timeouts := passYield{limit: cfg.HealthTransportYield}
	tally := map[string]int{}
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		verdict, total, missing, par2, outcome := p.checkOne(ctx, pool, row, cfg.HealthStatChunk,
			time.Duration(cfg.HealthStatTimeoutSec)*time.Second)
		switch outcome {
		case healthWritten:
			if err := backend.setVerdict(ctx, row.ID, verdict, total, missing, par2); err != nil {
				p.reportErr(ctx, "usenet/health-update", fmt.Errorf("nzb %d: %w", row.ID, err))
				continue
			}
			checked++
			tally[verdict]++
		case healthSkipPermanent:
			// Bad data, not bad luck. Stamp it so an unreadable row doesn't sit at
			// the head of the queue forever, but leave its verdict untouched.
			if err := backend.touch(ctx, row.ID); err != nil {
				p.reportErr(ctx, "usenet/health-touch", err)
			}
			unreadable++
		case healthSkipRow:
			// This release's answers were too doubtful to trust, but the pool
			// is fine — move on. The row keeps its prior verdict and stays
			// unstamped (retried promptly), but it must never end the pass:
			// candidates order these first, so breaking here let one
			// pathological release starve every other check forever.
			inconclusive++
			if len(inconclusiveIDs) < 3 {
				inconclusiveIDs = append(inconclusiveIDs, row.ID)
			}
			// A USER asked for this one, and this is the only ending that
			// writes nothing — a verdict clears their request, so does a
			// touch, and an inconclusive result does neither. Left alone the
			// request stays set forever: the row is re-STATted every pass and
			// the page they are watching says "queued" indefinitely. Drop the
			// request, not the checked-at stamp: the release genuinely has not
			// been checked, and stamping would hide a release nobody can get
			// an answer about at the back of the rotation.
			if row.UserRequested {
				if err := backend.clearRecheck(ctx, row.ID); err != nil {
					p.reportErr(ctx, "usenet/health-clear-recheck",
						fmt.Errorf("nzb %d: %w", row.ID, err))
				} else {
					inconclusiveRequests++
				}
			}
		case healthSkipTransport:
			// We held a connection and the provider failed the request. Say
			// nothing about this release — its verdict and its checked-at are
			// both untouched, so it is retried promptly — and try the next
			// one. Only a RUN of these means the pool itself is sick.
		case healthSkipTransient:
			// The pool has nothing to lend: the crawler is using every
			// connection. Stop rather than queue behind it — those
			// connections are better spent indexing — and leave the row
			// untouched so it is retried promptly.
			poolExhausted = true
			yielded = true
		}
		if timeouts.observe(outcome) {
			yielded = true
		}
		if yielded {
			break
		}
	}

	msg := fmt.Sprintf("health check: %d checked (%d healthy, %d broken, %d dead), %d unreadable",
		checked, tally[healthHealthy], tally[healthBroken], tally[healthDead], unreadable)
	if inconclusive > 0 {
		// Named, not folded into the yield message: "which release keeps
		// refusing to answer" is the first question of the wedge diagnosis.
		msg += fmt.Sprintf(", %d inconclusive (e.g. nzb %v)", inconclusive, inconclusiveIDs)
		if inconclusiveRequests > 0 {
			// Worth its own number: these are people waiting on an answer the
			// checker cannot give, and the count is how an operator sees that
			// the "Recheck" button is producing nothing for somebody.
			msg += fmt.Sprintf(" — %d of them user-requested, request cleared", inconclusiveRequests)
		}
	}
	if timeouts.total > 0 {
		// Named separately from the yield, because the two used to be the same
		// line and that is what hid a checker doing no work at all: "pool busy
		// or failing" reads like contention even when every release is dying
		// on provider timeouts.
		msg += fmt.Sprintf(", %d skipped on provider timeouts", timeouts.total)
	}
	switch {
	case poolExhausted:
		msg += " — yielded early (every connection is with the crawler)"
	case yielded:
		msg += fmt.Sprintf(" — yielded early (%d releases in a row failed on provider timeouts; the pool looks sick)",
			timeouts.run)
	}
	p.healthJob.Log("%s", msg)
	p.healthJob.SetIdle(p.nextHealth(cfg))
}

func (p *Plugin) nextHealth(cfg Config) time.Time {
	return time.Now().Add(time.Duration(cfg.HealthIntervalMin) * time.Minute)
}
