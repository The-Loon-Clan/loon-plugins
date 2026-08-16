package usenet

// The spot fetch pass: turn indexed spot headers into releases.
//
// This is the expensive half, and it is deliberately decoupled from the index
// pass by a worklist (spots WHERE fetched_at IS NULL) rather than run inline.
// The index reads a thousand spots per round trip; this reads TWO articles per
// spot — the document, then the NZB it points at — so it is three orders of
// magnitude more work and must be free to lag by months without holding the
// catalogue of what exists up behind it.
//
// TWO ARTICLES, IN TWO DIFFERENT GROUPS. The document lives with the spot in
// free.pt; the NZB lives in alt.binaries.ftd and is fetched BY MESSAGE-ID, so
// that group never has to be crawled. Dropping it from the crawl list, which
// was the right call, costs this pass nothing.

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// SpotNZBGroup carries the NZB and image payloads a spot points at. A fixed
// property of the protocol; fetched by message-id, never crawled.
const SpotNZBGroup = "alt.binaries.ftd"

// unspecialZipStr reverses Spotnet's binary escaping.
//
// Article bodies are line-based and cannot carry a raw newline, NUL or '=', so
// those four bytes are escaped. The ORDER matters and is Spotweb's: '=D' is
// undone LAST, so that an escaped '=' cannot be re-read as the introducer of
// the escape that follows it. Doing '=D' first would turn "=D" + "C" into
// "=C" and then into a newline — corrupting exactly the payloads that contain
// a literal '=', which for base64-adjacent data is most of them.
func unspecialZipStr(s string) string {
	s = strings.ReplaceAll(s, "=C", "\n")
	s = strings.ReplaceAll(s, "=B", "\r")
	s = strings.ReplaceAll(s, "=A", "\x00")
	return strings.ReplaceAll(s, "=D", "=")
}

// decodeSpotBinary turns a spot's raw article body into the bytes it encodes.
//
// Body lines are joined with NO separator: the newlines that mattered were
// escaped as '=C' before posting, so re-adding the transport's own line breaks
// would inject bytes that were never in the payload.
//
// The result is raw DEFLATE (PHP's gzinflate), not zlib and not gzip — neither
// has a header here, so compress/flate is the exact counterpart.
//
// CONSECUTIVE STREAMS, not one. Posting tools disagree about how a multi-
// article NZB is compressed: some deflate the whole document and cut the
// compressed bytes across articles (one stream), others cut the DOCUMENT and
// deflate each piece on its own (one stream per article, concatenated on the
// wire). flate stops at the first final block and says nothing about what
// follows, so reading a single stream silently drops every piece after the
// first for the second kind of poster — an 89GB Euphoria pack decoded to 3.5MB
// of XML ending mid-attribute, with no error anywhere. The loop keeps inflating
// while bytes remain; a remainder that is not a stream (trailing padding) ends
// the loop and the XML validation in fetchSpotNZB is what judges the result.
func decodeSpotBinary(lines []string, compressed bool) ([]byte, error) {
	raw := unspecialZipStr(strings.Join(lines, ""))
	if !compressed {
		return []byte(raw), nil
	}
	// bytes.Reader implements io.ByteReader, so flate consumes EXACTLY its
	// stream and the position after EOF is the start of whatever follows —
	// which is what makes restarting on the remainder correct.
	br := bytes.NewReader([]byte(raw))
	// Bounded: a corrupt or hostile length field should not become an
	// out-of-memory. 32MB is far past any real NZB.
	const maxOut = 32 << 20
	var out []byte
	for br.Len() > 0 {
		r := flate.NewReader(br)
		chunk, err := io.ReadAll(io.LimitReader(r, int64(maxOut-len(out))))
		r.Close()
		if err != nil {
			// The FIRST stream failing fails the decode, bytes or no bytes.
			//
			// This used to keep the partial output whenever some had been
			// produced, and that is how a truncated stream became a published
			// release: half an NZB inflates fine, parses far enough to look
			// real, and describes a fraction of the file. Nothing downstream
			// can tell "the first 9GB of an 89GB release" from a small
			// release, so the judgement has to be made here, where the EOF is
			// visible.
			if len(out) == 0 {
				return nil, err
			}
			// A CONTINUATION failing is different: at least one whole stream
			// decoded, and the remainder may simply be padding rather than a
			// second stream. The partial chunk is discarded — appending bytes
			// from a stream that did not finish would corrupt the document —
			// and the caller's XML validation decides whether what we have is
			// complete.
			break
		}
		out = append(out, chunk...)
		if len(out) >= maxOut {
			break
		}
	}
	return out, nil
}

// validateNZBDocument parses an NZB to completion and reports its distinct
// segment count.
//
// This is the check the decode alone cannot make: a poster whose own tooling
// truncated the document posts a VALID deflate stream of HALF an NZB, and the
// only way to notice is to read what came out. xml.Decoder errors on a
// document cut mid-token, and requiring at least one file with one segment
// rejects the technically-well-formed-but-empty shell.
func validateNZBDocument(doc []byte) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	// Real NZBs declare iso-8859-1 (the DTD's own example does); Go's decoder
	// refuses any non-UTF-8 label unless told how to read it. Byte-for-rune is
	// exact for latin-1 and close enough for the windows-1252 mislabels, and
	// message-ids — the only thing counted — are ASCII by RFC 5536 either way.
	dec.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		return &latin1Reader{r: input}, nil
	}
	seen := make(map[string]struct{})
	files := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("nzb document does not parse (truncated?): %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "file":
			files++
		case "segment":
			var mid string
			if err := dec.DecodeElement(&mid, &se); err != nil {
				return 0, fmt.Errorf("nzb segment does not parse: %w", err)
			}
			if mid != "" {
				seen[mid] = struct{}{}
			}
		}
	}
	if files == 0 || len(seen) == 0 {
		return 0, fmt.Errorf("nzb document lists %d files / %d segments", files, len(seen))
	}
	return len(seen), nil
}

// latin1Reader maps every input byte onto the code point with its value, which
// is the whole of ISO 8859-1. Streaming, so a multi-megabyte document is not
// buffered twice just to satisfy the decoder's charset check.
type latin1Reader struct {
	r   io.Reader
	buf [2048]byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	// Each latin-1 byte expands to at most 2 UTF-8 bytes.
	max := len(p) / 2
	if max == 0 {
		return 0, nil
	}
	if max > len(l.buf) {
		max = len(l.buf)
	}
	n, err := l.r.Read(l.buf[:max])
	w := 0
	for _, c := range l.buf[:n] {
		if c < 0x80 {
			p[w] = c
			w++
			continue
		}
		p[w] = 0xC0 | c>>6
		p[w+1] = 0x80 | c&0x3F
		w += 2
	}
	return w, err
}

// runSpotFetch works the unfetched-spot worklist.
func (p *Plugin) runSpotFetch(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.mayWrite(ctx, p.spotFetchJob) {
		return
	}
	if !p.spotFetchMu.TryLock() {
		p.spotFetchJob.Log("spot fetch already running — skipping overlap")
		return
	}
	defer p.spotFetchMu.Unlock()
	p.spotFetchJob.SetRunning()
	cfg := p.effective(ctx)

	batch := cfg.SpotFetchBatch
	if batch <= 0 {
		batch = defaultSpotFetchBatch
	}
	work, err := p.st.unfetchedSpots(ctx, batch)
	if err != nil {
		p.spotFetchJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/spot-fetch-worklist", err)
		return
	}
	if len(work) == 0 {
		p.spotFetchJob.Log("no spots awaiting a document fetch")
		p.spotFetchJob.SetIdle(p.nextSpotFetch(ctx))
		return
	}

	runs, err := p.activeFleet(ctx, cfg)
	if err != nil || len(runs) == 0 {
		p.spotFetchJob.Log("no server configured")
		p.spotFetchJob.SetIdle(p.nextSpotFetch(ctx))
		return
	}
	pool := runs[0].pool

	sink, err := p.resolveSink()
	if err != nil {
		p.spotFetchJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/spot-fetch-sink", err)
		return
	}

	var fetched, stored, refused, gone int
	for _, s := range work {
		if ctx.Err() != nil {
			break
		}
		res := p.fetchOneSpot(ctx, pool, sink, s)
		fetched++
		switch {
		case res.refused:
			refused++
		case res.gone:
			gone++
		case res.stored:
			stored++
		}
	}
	p.flushOpStats(ctx)
	p.spotFetchJob.Log("fetched %d spots: %d released, %d expired, %d refused (bad signature)",
		fetched, stored, gone, refused)
	p.spotFetchJob.SetIdle(p.nextSpotFetch(ctx))
}

type spotFetchResult struct {
	stored  bool
	refused bool // signature checked and FAILED — never indexed
	gone    bool // the articles have expired; normal on old history
}

// fetchOneSpot reads a spot's document, verifies it, and releases it.
func (p *Plugin) fetchOneSpot(ctx context.Context, pool *nntp.Pool, sink releaseSink, s spotWork) spotFetchResult {
	var out spotFetchResult

	hdrs, err := p.headSpot(ctx, pool, s.MessageID)
	p.opstats.noteErr("spot-head", err)
	if err != nil {
		// Expired, cancelled, or briefly unreachable. Below the retention
		// horizon this is the NORMAL outcome for old history and must not be
		// logged as a fault, or a working backfill fills the error log.
		out.gone = true
		_ = p.st.markSpotFetched(ctx, s.MessageID, spotDocument{FetchError: truncErr(err)})
		return out
	}

	// Verify BEFORE parsing anything into the catalogue. A signature that was
	// checked and failed is a positive claim of authorship that did not hold
	// up — the one outcome that must never become a release.
	trust := SpotTrustUnsigned
	if key, kerr := ParseSpotKey(hdrs.get("x-user-key")); kerr == nil {
		if sig, serr := SpotSignatureBytes(hdrs.get("x-user-signature")); serr == nil {
			label, ok := SpotTrust(VerifySpot(key, s.MessageID, sig))
			if !ok {
				out.refused = true
				p.opstats.note("spot-trust", "bad-signature")
				_ = p.st.markSpotFetched(ctx, s.MessageID, spotDocument{
					Trust: "", FetchError: "signature does not match the spot's key"})
				return out
			}
			trust = label
			p.opstats.note("spot-trust", label)
		}
	}

	doc, err := ParseSpotXML(hdrs.all("x-xml"))
	if err != nil {
		_ = p.st.markSpotFetched(ctx, s.MessageID, spotDocument{Trust: trust, FetchError: truncErr(err)})
		return out
	}

	d := spotDocument{
		Title:        strings.TrimSpace(doc.Posting.Title),
		Description:  doc.Posting.Description,
		NZBSegment:   doc.NZBSegment(),
		ImageSegment: strings.TrimSpace(doc.Posting.Image.Segment),
		Trust:        trust,
	}
	if d.Title == "" {
		d.Title = s.Subject
	}
	if err := p.st.markSpotFetched(ctx, s.MessageID, d); err != nil {
		p.reportErr(ctx, "usenet/spot-mark-fetched", err)
	}
	// An announcement with no NZB is legal and is not a release. Recorded and
	// left alone rather than retried forever.
	if d.NZBSegment == "" {
		return out
	}

	nzbXML, listed, err := p.fetchSpotNZB(ctx, pool, doc.NZBSegments())
	p.opstats.noteErr("spot-nzb", err)
	if err != nil {
		out.gone = true
		_ = p.st.markSpotFetched(ctx, s.MessageID, spotDocument{
			Title: d.Title, Description: d.Description, NZBSegment: d.NZBSegment,
			ImageSegment: d.ImageSegment, Trust: trust, FetchError: truncErr(err)})
		return out
	}

	// The sink stores gzipped XML; the wire form is raw DEFLATE, so this is a
	// re-compress rather than a pass-through. Cheap, and it keeps one storage
	// format across crawled and spotted releases.
	gz, err := gzipBytes(nzbXML)
	if err != nil {
		p.reportErr(ctx, "usenet/spot-gzip", err)
		return out
	}
	rel := spotRelease(s, d, doc, gz)
	// The validated distinct segment count. Without it the sink cannot tell a
	// complete re-fetch from the partial copy it may already hold, and every
	// repair of a truncated spot would be refused as "no better than what we
	// have" — which is exactly what happened to the first repair pass.
	rel.Segments = listed
	if _, created, err := sink.store(ctx, rel); err != nil {
		p.reportErr(ctx, "usenet/spot-store", err)
	} else if created {
		out.stored = true
	}
	return out
}

// headSpot reads a spot's headers.
func (p *Plugin) headSpot(ctx context.Context, pool *nntp.Pool, msgID string) (spotHeaders, error) {
	var h spotHeaders
	err := pool.TryDo(ctx, func(c *nntp.Conn) error {
		// Some providers require a group before an article can be addressed by
		// message-id. Best-effort: most do not.
		_, _, _, _ = c.Group(SpotGroup)
		r, err := c.HeadText(msgID)
		if err != nil {
			return err
		}
		h = readSpotHeadersMulti(r)
		return nil
	})
	return h, err
}

// fetchSpotNZB reads every article the spot names, inflates the whole, and
// validates that what came out is a complete NZB. Returns the document and its
// distinct segment count — the count is what lets the sink judge a re-fetch
// against the copy it already holds.
//
// The DEFLATE payload spans the concatenation, so the bodies are joined BEFORE
// decoding rather than decoded one at a time and appended: with a single
// stream cut across articles, an individual article after the first is not a
// valid stream on its own, and the boundary falls at an arbitrary byte.
// (Per-article streams also exist in the wild; decodeSpotBinary handles both.)
//
// Order is the posting order in the document and must be preserved. A missing
// or reordered middle segment fails the inflate or the validation, which is
// the outcome we want — a partial NZB is worse than none, because it publishes
// as a working release describing a fraction of the file.
func (p *Plugin) fetchSpotNZB(ctx context.Context, pool *nntp.Pool, segments []string) ([]byte, int, error) {
	if len(segments) == 0 {
		return nil, 0, errors.New("spot has no nzb segment")
	}
	var lines []string
	for _, segment := range segments {
		id := segment
		if !strings.HasPrefix(id, "<") {
			id = "<" + id + ">"
		}
		var part []string
		err := pool.TryDo(ctx, func(c *nntp.Conn) error {
			// The payload group, selected only so providers that demand it
			// will serve the message-id. It is never crawled.
			_, _, _, _ = c.Group(SpotNZBGroup)
			body, err := c.Body(id)
			if err != nil {
				return err
			}
			part, err = readBodyLines(body)
			return err
		})
		if err != nil {
			// One missing piece means no NZB, not most of one.
			return nil, 0, fmt.Errorf("segment %d/%d: %w", len(lines)+1, len(segments), err)
		}
		lines = append(lines, part...)
	}
	doc, err := decodeSpotBinary(lines, true)
	if err != nil {
		return nil, 0, err
	}
	// A valid stream can still carry a truncated document — the poster's own
	// tooling cut it before it was ever compressed. Only a full parse can tell,
	// and refusing here records a fetch_error instead of publishing a release
	// that describes a fraction of the file.
	listed, err := validateNZBDocument(doc)
	if err != nil {
		return nil, 0, err
	}
	return doc, listed, nil
}

// spotRelease maps a fetched spot onto the sink's release shape.
func spotRelease(s spotWork, d spotDocument, doc *SpotXML, nzbXML []byte) pluginapi.AssembledRelease {
	size := doc.Posting.Size
	if size <= 0 {
		size = s.SizeBytes
	}
	posted := s.PostedAt
	if doc.Posting.Created > 0 {
		posted = time.Unix(doc.Posting.Created, 0).UTC()
	}
	return pluginapi.AssembledRelease{
		Title:  d.Title,
		Group:  s.GroupName,
		Groups: []string{s.GroupName},
		Poster: s.Poster,
		// The message-id of the SPOT is the stable identity of the release:
		// one spot describes one posting, and re-fetching the same spot must
		// not create a second row.
		ContentHash: spotContentHash(s.MessageID),
		SizeBytes:   size,
		PostedAt:    posted,
		NZBGz:       nzbXML,
		Origin:      "spot",
		OriginTrust: d.Trust,
	}
}

// spotHeaders keeps every value of a repeated header, in order.
//
// A plain map would silently keep only the LAST X-Xml fragment, which is the
// exact failure ParseSpotXML exists to catch — reintroduced one layer up, where
// the parser could no longer see it.
type spotHeaders map[string][]string

func (h spotHeaders) all(name string) []string { return h[name] }

func (h spotHeaders) get(name string) string {
	v := h[name]
	if len(v) == 0 {
		return ""
	}
	return strings.Join(v, "")
}

// readSpotHeadersMulti reads a HEAD response, preserving repeats and folds.
func readSpotHeadersMulti(r io.Reader) spotHeaders {
	out := spotHeaders{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	last := ""
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "	")) && last != "" {
			v := out[last]
			v[len(v)-1] += strings.TrimLeft(line, " 	")
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		last = strings.ToLower(strings.TrimSpace(name))
		out[last] = append(out[last], strings.TrimSpace(val))
	}
	return out
}

// readBodyLines reads an article body into its lines, undoing dot-stuffing.
func readBodyLines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		// A body line beginning with '.' is sent doubled (RFC 3977 s3.1.1).
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// spotContentHash is the release identity for a spotted posting.
//
// The SPOT's message-id, not the article set: one spot describes one posting,
// and the crawler's message-id-set hash cannot be computed here because we
// never see those articles. Re-fetching the same spot must not create a second
// row, and this is what the sink's unique index keys on.
func spotContentHash(messageID string) string {
	sum := sha256.Sum256([]byte("spot:" + messageID))
	return hex.EncodeToString(sum[:])
}

// truncErr bounds an error stored in a text column.
func truncErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 400 {
		// Rune-safe: a byte slice on a UTF-8 column can MANUFACTURE invalid
		// bytes from a perfectly valid string, which Postgres then rejects.
		r := []rune(s)
		if len(r) > 400 {
			r = r[:400]
		}
		s = string(r)
	}
	return s
}

// nextSpotFetch is the pass's cadence.
func (p *Plugin) nextSpotFetch(ctx context.Context) time.Time {
	m := p.effective(ctx).SpotFetchIntervalMin
	if m <= 0 {
		m = defaultSpotFetchIntervalMin
	}
	return time.Now().Add(time.Duration(m) * time.Minute)
}
