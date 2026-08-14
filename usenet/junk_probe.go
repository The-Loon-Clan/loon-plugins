package usenet

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// The junk-recovery probe: asks the BODY what a dropped posting is really called.
//
// The crawler drops a junk-titled posting outright (crawl.go, "never index it")
// on the strength of its SUBJECT. That is the only evidence it has at that
// point, and for obfuscated posts the subject is exactly the part the poster
// scrambled. The yEnc header inside the body carries the encoder's own
// filename, which the same obfuscation routinely leaves intact:
//
//	Subject: 541279675.bin
//	=ybegin part=1 line=128 size=768000 name=Some.Real.Release-GROUP.part03.rar
//
// Whether that is the common case or the rare one decides something expensive:
// 39,404 of the newest 50,000 releases had the junk shape when the rules were
// last audited (junk_obfuscated_probe_test.go). If most of those bodies name a
// real file, the crawler is discarding most of the feed on a subject line. If
// they name another random token, the rules are right and this closes.
//
// So this measures rather than fixes. It reads the drops that subject_corpus
// already sampled, records what the body called them, and re-runs the junk
// rules on THAT name -- because "recovered a name" and "recovered a name worth
// indexing" are different results and only the second one justifies a change.
// Nothing here writes to the release tables.
//
// Off by default, for the reason NFO is: this spends metered provider bytes.

// yEnc's =ybegin line puts name= LAST, running to end of line, precisely so a
// filename may contain spaces (yEnc draft §2.1). Anchored per-line because the
// header is the body's first line but not always its first byte -- some posters
// emit a blank line or a comment ahead of it.
var reYBeginName = regexp.MustCompile(`(?m)^=ybegin\b.*?\bname=(.+?)[\r\n]*$`)

// yEncName returns the filename the encoder recorded, or "" if the body has no
// yEnc header at all (plain text, a broken post, or a format we do not read).
func yEncName(body []byte) string {
	m := reYBeginName.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// junkProbeReadBytes caps what we keep in memory, not what crosses the wire.
// The header is the first line, so a few KB is always enough -- but the rest of
// the article is still transferred, because the connection drains any unread
// remainder before the next command (nntp.Conn.cmd). Dropping the connection
// instead would save those bytes and cost a re-dial plus a slot against the
// provider's account cap, which is the worse trade for a sampling job.
//
// The consequence for accounting: one probe costs about one whole segment of
// wire regardless of this number, so the budget knob below counts ARTICLES.
const junkProbeReadBytes = 8 << 10

type junkProbeRow struct {
	id        int64
	group     string
	subject   string
	junkRule  string
	messageID string
}

// runJunkProbe is the job entry point.
func (p *Plugin) runJunkProbe(ctx context.Context) {
	cfg := p.effective(ctx)
	if !cfg.JunkProbeEnabled {
		p.junkProbeJob.Log("disabled in settings")
		p.junkProbeJob.SetIdle(p.nextJunkProbe(cfg))
		return
	}
	if !p.mayWrite(ctx, p.junkProbeJob) {
		return
	}
	if !p.junkProbeMu.TryLock() {
		p.junkProbeJob.Log("junk probe already running — skipping overlap")
		return
	}
	defer p.junkProbeMu.Unlock()

	if !p.withLease(ctx, leaseScopeJob, jobNameJunkProbe, p.leaseTTL(cfg), func(ctx context.Context) {
		p.junkProbeLocked(ctx, cfg)
	}) {
		p.junkProbeJob.Log("junk probe skipped — another worker holds this job")
		p.junkProbeJob.SetIdle(p.nextJunkProbe(cfg))
	}
}

func (p *Plugin) junkProbeLocked(ctx context.Context, cfg Config) {
	p.junkProbeJob.SetRunning()

	// The fleet's primary pool, for the reason nfoLocked spells out: a private
	// pool cannot sense crawler pressure and is invisible to the account cap.
	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.junkProbeJob.Log("no server configured — add one in the admin wizard")
			p.junkProbeJob.SetIdle(p.nextJunkProbe(cfg))
			return
		}
		p.junkProbeJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/junk-probe-pool", err)
		return
	}
	if len(runs) == 0 {
		p.junkProbeJob.Log("no active server — skipping")
		p.junkProbeJob.SetIdle(p.nextJunkProbe(cfg))
		return
	}
	pool := runs[0].pool

	rows, err := p.st.junkDropsToProbe(ctx, cfg.JunkProbeBatchSize)
	if err != nil {
		p.junkProbeJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/junk-probe-candidates", err)
		return
	}
	if len(rows) == 0 {
		p.junkProbeJob.Log("no unprobed junk drops — the corpus samples 1 in %d subjects", 1<<corpusSampleShift)
		p.junkProbeJob.SetIdle(p.nextJunkProbe(cfg))
		return
	}

	var (
		real, stillJunk, noHeader, gone int
	)
	timeouts := passYield{limit: cfg.HealthTransportYield}
	fetchTimeout := time.Duration(cfg.HealthStatTimeoutSec) * time.Second

pass:
	for i, row := range rows {
		if ctx.Err() != nil {
			return
		}

		body, _, err := p.fetchArticleBody(ctx, pool, row.messageID, row.group, fetchTimeout, junkProbeReadBytes)
		switch {
		case err == nil:
			// fell through

		case errors.Is(err, nntp.ErrPoolBusy):
			// The yield, as NFO does it: pool exhaustion means the crawler
			// wants those connections, and indexing is what members notice.
			p.junkProbeJob.Log("pool busy — yielding to the crawler at %d/%d", i, len(rows))
			break pass

		case isArticleGone(err):
			// Expired or never there. Records as probed with no name: the
			// question was "what is this called", and the answer is nothing.
			gone++
			if err := p.st.recordJunkProbe(ctx, row.id, ""); err != nil {
				p.reportErr(ctx, "usenet/junk-probe-record", err)
			}
			continue

		default:
			// A transport failure says nothing about the article, so the row is
			// left unprobed for the next pass rather than written off.
			if timeouts.observe(transportOrPool(err)) {
				p.junkProbeJob.Log("too many transport failures — stopping at %d/%d", i, len(rows))
				break pass
			}
			continue
		}

		name := yEncName(body)
		if name == "" {
			noHeader++
		} else if whichJunkRule(name) != "" {
			// The body agrees with the subject: another random token. This is
			// the outcome that VINDICATES the drop, and counting it is the
			// whole point of running the rules a second time.
			stillJunk++
		} else {
			real++
		}
		if err := p.st.recordJunkProbe(ctx, row.id, name); err != nil {
			p.reportErr(ctx, "usenet/junk-probe-record", err)
		}
	}

	// The finding, in the job log, in the terms the decision needs: how often a
	// dropped posting turns out to name a real file.
	probed := real + stillJunk + noHeader + gone
	if probed > 0 {
		p.junkProbeJob.Log("probed %d drops: %d named a real file (%d%%), %d named junk too, %d had no yEnc header, %d gone",
			probed, real, real*100/probed, stillJunk, noHeader, gone)
	}
	p.junkProbeJob.SetIdle(p.nextJunkProbe(cfg))
}

func (p *Plugin) nextJunkProbe(cfg Config) time.Time {
	return time.Now().Add(time.Duration(cfg.JunkProbeIntervalMin) * time.Minute)
}
