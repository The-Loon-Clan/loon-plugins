package usenet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// NFO extraction — the first feature built on article bodies.
//
// The crawler indexes from OVERVIEW lines and has never read an article body.
// That one fact blocks NFO text, sample images, RAR password detection, yEnc
// verification and part repair: five features that look unrelated and are the
// same missing primitive. This is the cheapest of them and the most visible to
// a member, which is why it goes first (docs/BODY-FETCH.md §5).
//
// The whole cost model rests on fetching FEW articles rather than parts of
// them. NNTP has no byte-ranged read — RFC 3977 offers ARTICLE, HEAD, BODY and
// STAT, and BODY streams to a lone "." — so stopping early desyncs a pipelined
// connection and costs a reconnect instead of saving bytes. An NFO is almost
// always one small article, so one whole fetch answers the question.

// nfoMaxBytes caps what we will read from a single article.
//
// An NFO is a text file, typically 1-50 KB. A megabyte of it is not an NFO,
// and the message-id was recorded by a document walk that could have been
// wrong about which file it pointed at. Reading a 1 GB video segment into
// memory because of a mislabelled id is the failure this prevents.
const nfoMaxBytes = 1 << 20 // 1 MB

// Size bounds on what counts as an NFO, matching newznab's NfoService
// (MIN_NFO_SIZE / MAX_NFO_SIZE).
//
// Separate from nfoMaxBytes, which is a memory guard on the READ. These are a
// judgement about the CONTENT: below the floor there is nothing worth showing,
// and above the ceiling it is not an NFO — a 900 KB wall of text passes every
// "is this printable" check and is still not the thing we went looking for.
const (
	nfoMinSize = 12
	nfoMaxSize = 65535
)

// Formats an NFO is not, lifted from newznab's _nonNfoHeaderRegex.
//
// It earns its place: the message-id was chosen because a filename ended
// ".nfo", so whatever a poster mislabelled can arrive. Several of these — PDF,
// PNG, GIF, gzip, MZ — are partly printable and would otherwise survive the
// character test as a blob of mojibake on a release page.
//
// SPLIT IN TWO, because a regex cannot do the binary half in Go. `\x89` in a
// Go regexp means the RUNE U+0089, which is 0xC2 0x89 in UTF-8 — two bytes,
// neither of them the single 0x89 a PNG actually starts with. Written as one
// regex this silently matched nothing for exactly the signatures that most
// needed it. The byte prefixes are therefore compared as bytes.
var nonNFOBytePrefixes = [][]byte{
	{0x89, 'P', 'N', 'G'},  // PNG
	{0x1f, 0x8b, 0x08},     // gzip
	{'P', 'K', 0x03, 0x04}, // zip / office / jar
	{'M', 'Z'},             // DOS/Windows executable
	{'%', 'P', 'D', 'F'},   // PDF
	{'G', 'I', 'F', '8', '7', 'a'},
	{'G', 'I', 'F', '8', '9', 'a'},
	{'R', 'I', 'F', 'F'}, // wav / avi
	{'I', 'D', '3'},      // mp3 with a tag
}

// The text-shaped half, where a regex is the right tool.
var nonNFOHeader = regexp.MustCompile(`(?i)\A(\s*<\?xml|=newz\[NZB\]=|\s*[RP]AR|.{0,10}(JFIF|matroska|ftyp))`)

// looksLikeAnotherFormat reports whether the body starts with a signature that
// rules out its being an NFO.
func looksLikeAnotherFormat(raw []byte) bool {
	for _, sig := range nonNFOBytePrefixes {
		if bytes.HasPrefix(raw, sig) {
			return true
		}
	}
	return nonNFOHeader.Match(raw)
}

// nfoBackend is where candidates come from and where text goes.
//
// Same shape as healthBackend: the plugin owns the fetching, the catalogue may
// belong to either side. In sink=host mode the releases are the host's, so
// without a seam the job would have nothing to work on.
type nfoBackend interface {
	candidates(ctx context.Context, limit int) ([]pluginapi.NFOCandidate, error)
	setNFO(ctx context.Context, id int64, nfo string) error
	markUnavailable(ctx context.Context, id int64, reason string) error
	recordFailure(ctx context.Context, id int64) (int, error)
}

type hostNFO struct{ st pluginapi.ReleaseNFOStore }

func (b hostNFO) candidates(ctx context.Context, limit int) ([]pluginapi.NFOCandidate, error) {
	return b.st.NFOCandidates(ctx, limit)
}
func (b hostNFO) setNFO(ctx context.Context, id int64, nfo string) error {
	return b.st.SetReleaseNFO(ctx, id, nfo)
}
func (b hostNFO) markUnavailable(ctx context.Context, id int64, reason string) error {
	return b.st.MarkNFOUnavailable(ctx, id, reason)
}
func (b hostNFO) recordFailure(ctx context.Context, id int64) (int, error) {
	return b.st.RecordNFOAttemptFailure(ctx, id)
}

// errNoNFOStore is the "feature not wired" answer, distinct from a failure.
var errNoNFOStore = errors.New("no NFO store registered")

func (p *Plugin) resolveNFOBackend() (nfoBackend, error) {
	if p.core == nil {
		return nil, errNoNFOStore
	}
	if st, ok := pluginapi.LookupReleaseNFOStore(p.core); ok {
		return hostNFO{st: st}, nil
	}
	return nil, errNoNFOStore
}

// runNFO is the job entry point.
func (p *Plugin) runNFO(ctx context.Context) {
	cfg := p.effective(ctx)
	if !cfg.NFOEnabled {
		p.nfoJob.Log("disabled in settings")
		p.nfoJob.SetIdle(p.nextNFO(cfg))
		return
	}
	// The read-only write gate (writegate.go). Every pass asks, because this
	// pipeline has four different ways to be started and only one of them ever
	// reached schedule.WriteGate. This pass writes NFO text and unavailability
	// markers, so read-only has to stop it.
	if !p.mayWrite(ctx, p.nfoJob) {
		return
	}
	if !p.nfoMu.TryLock() {
		p.nfoJob.Log("NFO fetch already running — skipping overlap")
		return
	}
	defer p.nfoMu.Unlock()

	// One worker at a time, like health: this competes for the crawler's idle
	// connections, and two copies double that pressure for no extra coverage.
	if !p.withLease(ctx, leaseScopeJob, jobNameNFO, p.leaseTTL(cfg), func(ctx context.Context) {
		p.nfoLocked(ctx, cfg)
	}) {
		p.nfoJob.Log("NFO fetch skipped — another worker holds this job")
		p.nfoJob.SetIdle(p.nextNFO(cfg))
	}
}

func (p *Plugin) nfoLocked(ctx context.Context, cfg Config) {
	p.nfoJob.SetRunning()

	backend, err := p.resolveNFOBackend()
	if err != nil {
		// Not an error state: a host that registered no NFO store has not
		// asked for this feature.
		p.nfoJob.Log("no NFO store registered by the host — nothing to do")
		p.nfoJob.SetIdle(p.nextNFO(cfg))
		return
	}

	// The FLEET's primary pool, deliberately. A private pool here would break
	// both halves of the design, and this codebase already found that out —
	// the health checker tried it and reverted: a private pool's TryDo tests
	// an otherwise-idle pool and so never senses crawler pressure, and its
	// dials are invisible to the account-cap machinery, so the provider sees
	// fleet-size PLUS a second pool. "Small and separate" is not smaller.
	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.nfoJob.Log("no server configured — add one in the admin wizard")
			p.nfoJob.SetIdle(p.nextNFO(cfg))
			return
		}
		p.nfoJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/nfo-pool", err)
		return
	}
	if len(runs) == 0 {
		p.nfoJob.Log("no active server — skipping")
		p.nfoJob.SetIdle(p.nextNFO(cfg))
		return
	}
	pool := runs[0].pool

	rows, err := backend.candidates(ctx, cfg.NFOBatchSize)
	if err != nil {
		p.nfoJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/nfo-candidates", err)
		return
	}
	if len(rows) == 0 {
		p.nfoJob.Log("no releases waiting for an NFO")
		p.nfoJob.SetIdle(p.nextNFO(cfg))
		return
	}

	budget := int64(cfg.NFOBudgetMB) << 20
	var (
		spent            int64
		stored, absent   int
		yielded, skipped int
	)
	timeouts := passYield{limit: cfg.HealthTransportYield}
	// Reuses the health checker's per-operation deadline for the same reason
	// it exists there: the pool hands out connections carrying the crawler's
	// OpTimeout, sized for a 3000-article OVER, so a socket the provider had
	// quietly closed would cost a full minute to discover.
	fetchTimeout := time.Duration(cfg.HealthStatTimeoutSec) * time.Second

pass:
	for i, row := range rows {
		if ctx.Err() != nil {
			return
		}
		// The byte ceiling is checked BEFORE the fetch, not after: a budget
		// that can be exceeded by one whole article is not a ceiling. Providers
		// meter bytes and block accounts get consumed, which is the one control
		// this job genuinely needs that the crawler does not.
		if budget > 0 && spent+nfoMaxBytes > budget {
			p.nfoJob.Log("daily byte budget reached (%d MB) — stopping at %d/%d",
				cfg.NFOBudgetMB, i, len(rows))
			break pass
		}

		body, n, err := p.fetchArticleBody(ctx, pool, row.MessageID, row.Group, fetchTimeout)
		spent += n
		switch {
		case err == nil:
			// fell through

		case errors.Is(err, nntp.ErrPoolBusy):
			// THE YIELD. Pool exhaustion is not a failure to retry: it means
			// the crawler wants those connections, and indexing is the thing
			// members notice. Stop the pass and leave the row untouched.
			p.nfoJob.Log("pool busy — yielding to the crawler at %d/%d", i, len(rows))
			yielded++
			p.nfoJob.SetIdle(p.nextNFO(cfg))
			p.nfoJob.Log("NFO pass: %d stored, %d unavailable, %.1f MB read",
				stored, absent, float64(spent)/(1<<20))
			return

		case isArticleGone(err):
			// The server no longer holds it. Re-fetching cannot help, so
			// record that rather than offering the same dead article forever.
			if merr := backend.markUnavailable(ctx, row.ID, "article not on server"); merr != nil {
				p.reportErr(ctx, "usenet/nfo-mark", merr)
			}
			absent++
			continue

		default:
			// Transport trouble. Says nothing about the article, so this is
			// NOT a write-off — but it is counted, because an article that
			// fails this way every time would otherwise be retried forever and
			// a few of them sit at the head of the queue on every pass.
			// Newznab bounds the same thing by decrementing nfostatus toward a
			// floor; this counts up to a ceiling.
			if n, rerr := backend.recordFailure(ctx, row.ID); rerr != nil {
				p.reportErr(ctx, "usenet/nfo-attempt", rerr)
			} else if cfg.NFOMaxRetries > 0 && n >= cfg.NFOMaxRetries {
				if merr := backend.markUnavailable(ctx, row.ID,
					fmt.Sprintf("unreachable after %d attempts", n)); merr != nil {
					p.reportErr(ctx, "usenet/nfo-mark", merr)
				}
				absent++
			}
			// A provider going bad should end the pass rather than be ground
			// out one timeout at a time.
			//
			// LABELLED break. A bare one here leaves the SWITCH and carries on
			// into the decode below with a nil body — which decodeNFO rejects,
			// so the release would be marked "NFO unavailable" permanently on
			// the strength of one provider timeout. That is the worst possible
			// outcome of a transient failure: it is recorded as a fact.
			if timeouts.observe(healthSkipTransport) {
				p.nfoJob.Log("provider unhealthy — yielding at %d/%d", i, len(rows))
				break pass
			}
			skipped++
			continue
		}

		text, ok := decodeNFO(body)
		if !ok {
			// The id pointed at something that is not readable text. Marking
			// it stops the same article being paid for on every pass.
			if merr := backend.markUnavailable(ctx, row.ID, "not decodable as text"); merr != nil {
				p.reportErr(ctx, "usenet/nfo-mark", merr)
			}
			absent++
			continue
		}
		if err := backend.setNFO(ctx, row.ID, text); err != nil {
			p.reportErr(ctx, "usenet/nfo-store", err)
			skipped++
			continue
		}
		stored++
	}

	p.nfoJob.Log("NFO pass: %d stored, %d unavailable, %d skipped, %.1f MB read",
		stored, absent, skipped, float64(spent)/(1<<20))
	p.nfoJob.SetIdle(p.nextNFO(cfg))
}

// fetchArticleBody is THE PRIMITIVE the other four body features will share.
//
// Returns the raw body, the bytes read (for the budget, counted even on a
// partial read — the provider metered them whether or not we could use them),
// and an error.
//
// TryDo, never Do: blocking on the pool would queue behind the crawler and
// hold the slot the moment one frees, which is the opposite of yielding.
func (p *Plugin) fetchArticleBody(ctx context.Context, pool *nntp.Pool, messageID, group string, opTimeout time.Duration) ([]byte, int64, error) {
	var (
		out  []byte
		read int64
	)
	err := pool.TryDo(ctx, func(c *nntp.Conn) error {
		// Per-fetch deadline, as statBatch does. The pool applies the
		// crawler's OpTimeout on the way in, sized for a 3000-article OVER; a
		// socket the provider quietly closed would otherwise cost a full
		// minute to discover.
		if opTimeout > 0 {
			_ = c.SetDeadline(time.Now().Add(opTimeout))
		}
		// Some providers require a group to be selected before an article can
		// be retrieved by message-id. Best-effort: most do not, and a failure
		// here should not stop us trying the fetch.
		if group != "" {
			_, _, _, _ = c.Group(group)
		}
		r, err := c.Body(messageID)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(r, nfoMaxBytes))
		read = int64(len(b))
		if err != nil {
			return err
		}
		out = b
		return nil
	})
	return out, read, err
}

// isArticleGone reports whether the server said the article does not exist,
// as opposed to the connection failing.
//
// The distinction decides whether retrying can ever work: a 430 is permanent
// and re-fetching is wasted bytes, whereas a transport failure says nothing
// about the article at all.
func isArticleGone(err error) bool {
	if err == nil {
		return false
	}
	if !isProtocolError(err) {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "430") || strings.Contains(s, "423") || strings.Contains(s, "421")
}

// decodeNFO turns an article body into storable text.
//
// NFOs arrive either yEnc-encoded or as plain text, and both are common, so
// this tries the encoding and falls back rather than requiring the caller to
// know which it has. Returns ok=false when the result is not text — the
// message-id came from a document walk that could have pointed at the wrong
// file, and storing a decoded video segment as an NFO would put binary on a
// release page.
func decodeNFO(body []byte) (string, bool) {
	raw := body
	if decoded, err := yencDecode(body); err == nil && len(decoded) > 0 {
		raw = decoded
	}
	if len(raw) == 0 {
		return "", false
	}
	// Signature check before anything else, on the RAW bytes — several of
	// these formats are partly printable and would otherwise survive the
	// character test below as mojibake.
	if looksLikeAnotherFormat(raw) {
		return "", false
	}
	if len(raw) < nfoMinSize || len(raw) > nfoMaxSize {
		return "", false
	}
	// NFOs are traditionally CP437 for the box-drawing art. Anything that is
	// already valid UTF-8 is left alone; otherwise the high bytes are mapped
	// so the art survives instead of becoming replacement characters.
	var text string
	if utf8.Valid(raw) {
		text = string(raw)
	} else {
		text = decodeCP437(raw)
	}
	text = strings.ReplaceAll(text, "\x00", "")
	text = stripANSI(text)
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	// A binary segment that survived the checks above would be mostly
	// unprintable. Requiring the bulk of it to be printable is the cheap
	// version of "is this actually text".
	printable := 0
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' || (r >= 0x20 && r != utf8.RuneError) {
			printable++
		}
	}
	if printable*100 < utf8.RuneCountInString(text)*90 {
		return "", false
	}
	return text, true
}

// decodeCP437 maps code page 437 to UTF-8, which is what NFO box art is drawn
// in. Without it the borders every NFO is made of arrive as replacement
// characters and the file looks corrupt on the release page.
func decodeCP437(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
			continue
		}
		sb.WriteRune(cp437[c-0x80])
	}
	return sb.String()
}

// nextNFO is the next scheduled run.
func (p *Plugin) nextNFO(cfg Config) time.Time {
	m := cfg.NFOIntervalMin
	if m <= 0 {
		m = 60
	}
	return time.Now().Add(time.Duration(m) * time.Minute)
}

// reANSI matches ANSI escape sequences — CSI (colour, cursor moves) and the
// short two-character escapes.
var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-Z\-_]`)

// stripANSI removes terminal escape sequences from NFO text.
//
// NFOs are drawn in CP437 block and box characters, and a minority add ANSI
// colour on top. We render into a <pre>, which shows an escape sequence as the
// literal characters it is made of — so an ANSI-coloured NFO arrives as art
// interrupted every few characters by "[1;36m". Unreadable, and worse than the
// same art in one colour.
//
// Removing them loses the colour, which is a real loss, but the alternative is
// either shipping an ANSI-to-HTML renderer or showing garbage, and the art
// itself — which is the part people look at — survives intact either way.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s // the common case: no escapes, no allocation
	}
	return reANSI.ReplaceAllString(s, "")
}
