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
func (p *Plugin) statBatch(ctx context.Context, pool *nntp.Pool, ids []string) ([]statResult, error) {
	out := make([]statResult, len(ids))
	for i := range out {
		out[i] = statUnknown
	}
	err := pool.TryDo(ctx, func(c *nntp.Conn) error {
		for i, id := range ids {
			if ctx.Err() != nil {
				return nil // keep the connection; the rest stay unknown
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
	healthSkipTransient                      // pool busy / too much doubt: leave it, retry soon
)

// checkOne STATs an entire release and returns its verdict plus what should be
// persisted.
func (p *Plugin) checkOne(ctx context.Context, pool *nntp.Pool, row healthRow, chunk int) (verdict string, totalSegs, missingData, par2Total int, outcome healthOutcome) {
	segs, err := parseNzbSegments(row.Data)
	if err != nil {
		// A blob we can't parse is a storage problem, not evidence about the
		// articles — never let it downgrade a verdict.
		p.core.Errors.Report(ctx, "usenet/health-parse", fmt.Errorf("nzb %d: %w", row.ID, err))
		return "", 0, 0, 0, healthSkipPermanent
	}
	total := len(segs.Data) + len(segs.Par2)
	if total == 0 {
		return "", 0, 0, 0, healthSkipPermanent
	}

	var missData, missPar2, unknown int
	count := func(ids []string, missing *int) error {
		for start := 0; start < len(ids); start += chunk {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			end := min(start+chunk, len(ids))
			res, err := p.statBatch(ctx, pool, ids[start:end])
			if err != nil && (errors.Is(err, nntp.ErrPoolBusy) || errors.Is(err, nntp.ErrPoolEmpty)) {
				// The crawler needs the connections; stop and try again later
				// rather than waiting for them.
				return err
			}
			for _, r := range res {
				switch r {
				case statMissing:
					*missing++
				case statUnknown:
					unknown++
				}
			}
		}
		return nil
	}
	if err := count(segs.Data, &missData); err != nil {
		return "", 0, 0, 0, healthSkipTransient
	}
	if err := count(segs.Par2, &missPar2); err != nil {
		return "", 0, 0, 0, healthSkipTransient
	}

	// Too much doubt: keep whatever verdict this release already had.
	if float64(unknown)/float64(total) > maxInconclusiveRatio {
		return "", 0, 0, 0, healthSkipTransient
	}
	return healthVerdict(missData, len(segs.Par2), missPar2), total, missData, len(segs.Par2), healthWritten
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
}

type internalHealth struct{ st Store }

func (b internalHealth) candidates(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error) {
	return b.st.nzbsNeedingHealthCheck(ctx, limit, recheckDays, minAgeHours)
}
func (b internalHealth) setVerdict(ctx context.Context, id int64, status string, total, missing, par2 int) error {
	return b.st.updateNzbHealth(ctx, id, status, total, missing, par2)
}
func (b internalHealth) touch(ctx context.Context, id int64) error {
	return b.st.touchHealthChecked(ctx, id)
}

type hostHealth struct{ hs pluginapi.ReleaseHealthStore }

func (b hostHealth) candidates(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error) {
	cands, err := b.hs.HealthCandidates(ctx, limit, recheckDays, minAgeHours)
	if err != nil {
		return nil, err
	}
	rows := make([]healthRow, len(cands))
	for i, c := range cands {
		rows[i] = healthRow{ID: c.ID, Data: c.NZBGz}
	}
	return rows, nil
}
func (b hostHealth) setVerdict(ctx context.Context, id int64, status string, total, missing, par2 int) error {
	return b.hs.SetHealthVerdict(ctx, id, status, total, missing, par2)
}
func (b hostHealth) touch(ctx context.Context, id int64) error {
	return b.hs.TouchHealthChecked(ctx, id)
}

// resolveHealthBackend mirrors storeRelease's sink rule: host mode without the
// capability refuses loudly — silently sweeping the plugin's (empty) table
// while the host catalogue rots unchecked is the worse failure.
func (p *Plugin) resolveHealthBackend() (healthBackend, error) {
	if p.cfg.Sink == "host" {
		hs, ok := pluginapi.LookupReleaseHealthStore(p.core)
		if !ok {
			return nil, fmt.Errorf("sink=host but no %q capability is registered", pluginapi.UsenetHealthStoreName)
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
	if !p.healthMu.TryLock() {
		p.healthJob.Log("health check already running — skipping overlap")
		return
	}
	defer p.healthMu.Unlock()
	cfg := p.effective(ctx)
	// Health competes for the same idle connections the crawler wants; running
	// it on two workers at once doubles that pressure for no extra coverage.
	if !p.withLease(ctx, leaseScopeJob, "NZB Health Check", p.leaseTTL(cfg), func() {
		p.healthLocked(ctx, cfg)
	}) {
		p.healthJob.Log("health check skipped — another worker holds this job")
		p.healthJob.SetIdle(p.nextHealth(cfg))
	}
}

func (p *Plugin) healthLocked(ctx context.Context, cfg Config) {
	p.healthJob.SetRunning()

	pool, err := p.ensurePool(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.healthJob.Log("no server configured")
			p.healthJob.SetIdle(p.nextHealth(cfg))
			return
		}
		p.healthJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/health-pool", err)
		return
	}

	backend, err := p.resolveHealthBackend()
	if err != nil {
		p.healthJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/health-backend", err)
		return
	}
	rows, err := backend.candidates(ctx, cfg.HealthBatchSize, cfg.HealthRecheckDays, cfg.HealthMinAgeHours)
	if err != nil {
		p.healthJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/health-candidates", err)
		return
	}
	if len(rows) == 0 {
		p.healthJob.Log("no releases due a health check")
		p.healthJob.SetIdle(p.nextHealth(cfg))
		return
	}

	var checked, unreadable int
	var yielded bool
	tally := map[string]int{}
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		verdict, total, missing, par2, outcome := p.checkOne(ctx, pool, row, cfg.HealthStatChunk)
		switch outcome {
		case healthWritten:
			if err := backend.setVerdict(ctx, row.ID, verdict, total, missing, par2); err != nil {
				p.core.Errors.Report(ctx, "usenet/health-update", fmt.Errorf("nzb %d: %w", row.ID, err))
				continue
			}
			checked++
			tally[verdict]++
		case healthSkipPermanent:
			// Bad data, not bad luck. Stamp it so an unreadable row doesn't sit at
			// the head of the queue forever, but leave its verdict untouched.
			if err := backend.touch(ctx, row.ID); err != nil {
				p.core.Errors.Report(ctx, "usenet/health-touch", err)
			}
			unreadable++
		case healthSkipTransient:
			// The connections are wanted elsewhere, or the provider is flaky.
			// Stop the pass instead of grinding through the rest for the same
			// answer, and leave the row untouched so it is retried promptly.
			yielded = true
		}
		if yielded {
			break
		}
	}

	msg := fmt.Sprintf("health check: %d checked (%d healthy, %d broken, %d dead), %d unreadable",
		checked, tally[healthHealthy], tally[healthBroken], tally[healthDead], unreadable)
	if yielded {
		msg += " — yielded early (connections busy or results inconclusive)"
	}
	p.healthJob.Log("%s", msg)
	p.healthJob.SetIdle(p.nextHealth(cfg))
}

func (p *Plugin) nextHealth(cfg Config) time.Time {
	return time.Now().Add(time.Duration(cfg.HealthIntervalMin) * time.Minute)
}
