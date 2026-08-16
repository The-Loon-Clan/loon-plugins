package usenet

// Outcome counting: how often each operation succeeded, alongside how often it
// failed and why.
//
// The failures are already in error_logs, in more detail than this. What this
// adds is the DENOMINATOR, which is what turns a scary number into a decision:
// 1,435 overview failures is an outage at ten thousand attempts and noise at
// ten million, and error_logs cannot tell those apart because it only ever sees
// the bad half.
//
// Counted in memory and flushed once per pass, for the same reason filter hits
// and poster hits are: the hot path must not take a database round trip per
// article, and a pass that produces ten thousand outcomes should produce one
// statement, not ten thousand.

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// opStatKey identifies one counter.
type opStatKey struct {
	Op      string
	Outcome string
}

// opCounter accumulates outcomes between flushes.
type opCounter struct {
	mu sync.Mutex
	n  map[opStatKey]int64
}

func newOpCounter() *opCounter { return &opCounter{n: map[opStatKey]int64{}} }

// note records one outcome.
func (c *opCounter) note(op, outcome string) {
	if c == nil || op == "" || outcome == "" {
		return
	}
	c.mu.Lock()
	c.n[opStatKey{Op: op, Outcome: outcome}]++
	c.mu.Unlock()
}

// noteErr records success or a normalised failure reason, whichever applies.
//
// One call site instead of two, because the pattern it replaces -- count the
// failure at the error branch and forget to count the success -- produces
// exactly the numerator-without-denominator this file exists to fix.
func (c *opCounter) noteErr(op string, err error) {
	if err == nil {
		c.note(op, outcomeOK)
		return
	}
	c.note(op, classifyOutcome(err))
}

// drain returns and clears the counters.
func (c *opCounter) drain() map[opStatKey]int64 {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.n) == 0 {
		return nil
	}
	out := c.n
	c.n = map[opStatKey]int64{}
	return out
}

const (
	outcomeOK      = "ok"
	outcomeUnknown = "other"
)

// classifyOutcome reduces an error to a BOUNDED label.
//
// Bounded is the requirement, not descriptiveness: this is a primary key
// column, and an outcome carrying an ephemeral port or an article number would
// turn a few hundred rows a month into a few million. The detail already lives
// in error_logs; what belongs here is the class.
func classifyOutcome(err error) string {
	if err == nil {
		return outcomeOK
	}
	s := strings.ToLower(err.Error())

	// An NNTP status code is the most useful label there is, and it is exactly
	// three digits. Prefer the code the SERVER chose over any guess made here.
	if code := nntpStatusCode(err.Error()); code != "" {
		return code
	}
	switch {
	case strings.Contains(s, "no usable connection"), strings.Contains(s, "all connections busy"),
		strings.Contains(s, "pool"):
		return "pool"
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "connection reset"), strings.Contains(s, "broken pipe"),
		strings.Contains(s, "eof"):
		return "reset"
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "context canceled"):
		return "cancelled"
	case strings.Contains(s, "auth"):
		return "auth"
	}
	return outcomeUnknown
}

// reAddrPort matches an IPv4 address with a port, which is the shape every Go
// transport error carries: "read tcp 172.18.0.6:45686->23.182.120.44:443".
var reAddrPort = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}:\d+`)

// nntpStatusCode finds a three-digit NNTP response code in an error.
//
// Scans for the FIRST standalone three-digit token rather than only the prefix:
// these errors are wrapped several layers deep ("overview failed: XOVER: 511
// issue with group"), and anchoring at the start would classify every one of
// them as "other" — which is the same as not classifying them at all.
//
// Addresses are stripped FIRST, and that is not defensive tidying. A usenet
// port is three digits in the status-code range: 119 and 443 both are. Without
// this, every transport error is classified by the port it was talking to, so
// "i/o timeout" became "443" and "connection reset" became "119" — labels that
// look like server responses, are not, and would have made the whole metric
// lie in the direction of "the server rejected us".
func nntpStatusCode(msg string) string {
	msg = reAddrPort.ReplaceAllString(msg, " ")
	for _, field := range strings.FieldsFunc(msg, func(r rune) bool {
		return r == ' ' || r == ':' || r == '(' || r == ')' || r == ','
	}) {
		if len(field) != 3 {
			continue
		}
		n, err := strconv.Atoi(field)
		// 1xx-5xx: the RFC 3977 response range. A bare "200" inside a byte
		// count would be a false positive, which is why only the first match
		// wins and why the surrounding text is already known to be an error.
		if err == nil && n >= 100 && n <= 599 {
			return field
		}
	}
	return ""
}

// flushOpStats writes the accumulated counters.
//
// Best-effort by design: these are trend counters, and losing one pass's worth
// to a transient database error is not worth failing a crawl over. It is
// reported, because a flush that fails EVERY pass is a broken metric and a
// silent one would be worse than no metric at all.
func (p *Plugin) flushOpStats(ctx context.Context) {
	stats := p.opstats.drain()
	if len(stats) == 0 {
		return
	}
	if err := p.st.recordOpStats(ctx, stats); err != nil {
		p.reportErr(ctx, "usenet/op-stats-flush", err)
	}
}
