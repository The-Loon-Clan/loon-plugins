package usenet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Proof-image extraction — the second feature built on article bodies.
//
// Same architecture as NFO (nfo.go), one structural difference: an image
// larger than one article spans SEGMENTS, and half a JPEG is garbage, so the
// candidate carries the file's whole (bounded) segment list and a fetch is
// all-or-nothing. The document walk that records the list already refused
// files past a size cap, so "bounded" is enforced before this job ever sees a
// candidate — but it is re-checked here, because the message-ids were written
// by an earlier extractor generation that may have promised differently.

// imageMaxBytesPerArticle caps one article read. Articles run to ~1MB of
// payload; yEnc overhead is a few percent. Twice that is generous, and a
// "segment" that busts it is not part of a small proof image.
const imageMaxBytesPerArticle = 2 << 20

// imageMaxDecodedBytes caps the assembled file. Must not be looser than the
// document walk's own recording cap, or a stale candidate could balloon.
const imageMaxDecodedBytes = 10 << 20

// jpegMagic / pngMagic are the only formats worth storing: every proof and
// sample in the wild is one of the two, and anything else the filename
// promised is exactly the mislabelling this check exists to catch.
var (
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
	pngMagic  = []byte{0x89, 'P', 'N', 'G'}
)

func looksLikeImage(b []byte) bool {
	return bytes.HasPrefix(b, jpegMagic) || bytes.HasPrefix(b, pngMagic)
}

// errNoImageStore is the "feature not wired" answer, distinct from a failure.
var errNoImageStore = errors.New("no image store registered")

func (p *Plugin) resolveImageBackend() (pluginapi.ReleaseImageStore, error) {
	if p.core == nil {
		return nil, errNoImageStore
	}
	if st, ok := pluginapi.LookupReleaseImageStore(p.core); ok {
		return st, nil
	}
	return nil, errNoImageStore
}

// runImage is the job entry point.
func (p *Plugin) runImage(ctx context.Context) {
	defer p.recoverPass(jobNameImage, p.imageJob)
	cfg := p.effective(ctx)
	if !cfg.ImageEnabled {
		p.imageJob.Log("disabled in settings")
		p.imageJob.SetIdle(p.nextImage(cfg))
		return
	}
	if !p.mayWrite(ctx, p.imageJob) {
		return
	}
	if !p.imageMu.TryLock() {
		p.imageJob.Log("image fetch already running — skipping overlap")
		return
	}
	defer p.imageMu.Unlock()

	if !p.withLease(ctx, leaseScopeJob, jobNameImage, p.leaseTTL(cfg), func(ctx context.Context) {
		p.imageLocked(ctx, cfg)
	}) {
		p.imageJob.Log("image fetch skipped — another worker holds this job")
		p.imageJob.SetIdle(p.nextImage(cfg))
	}
}

func (p *Plugin) imageLocked(ctx context.Context, cfg Config) {
	p.imageJob.SetRunning()

	backend, err := p.resolveImageBackend()
	if err != nil {
		p.imageJob.Log("no image store registered by the host — nothing to do")
		p.imageJob.SetIdle(p.nextImage(cfg))
		return
	}

	// The fleet's primary pool, for the reasons recorded on the NFO job: a
	// private pool can neither sense crawler pressure nor be seen by the
	// account-cap machinery.
	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.imageJob.Log("no server configured — add one in the admin wizard")
			p.imageJob.SetIdle(p.nextImage(cfg))
			return
		}
		p.imageJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/image-pool", err)
		return
	}
	if len(runs) == 0 {
		p.imageJob.Log("no active server — skipping")
		p.imageJob.SetIdle(p.nextImage(cfg))
		return
	}
	pool := runs[0].pool

	rows, err := backend.ImageCandidates(ctx, cfg.ImageBatchSize)
	if err != nil {
		p.imageJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/image-candidates", err)
		return
	}
	if len(rows) == 0 {
		p.imageJob.Log("no releases waiting for an image")
		p.imageJob.SetIdle(p.nextImage(cfg))
		return
	}

	budget := int64(cfg.ImageBudgetMB) << 20
	var (
		spent          int64
		stored, absent int
		skipped        int
	)
	timeouts := passYield{limit: cfg.HealthTransportYield}
	fetchTimeout := time.Duration(cfg.HealthStatTimeoutSec) * time.Second

pass:
	for i, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if len(row.MessageIDs) == 0 {
			continue
		}
		// Worst case for THIS candidate, checked before any of its fetches: a
		// budget one whole file can exceed is not a ceiling, and stopping
		// mid-file wastes every byte already read for it.
		worst := int64(len(row.MessageIDs)) * imageMaxBytesPerArticle
		if budget > 0 && spent+worst > budget {
			p.imageJob.Log("byte budget reached (%d MB) — stopping at %d/%d",
				cfg.ImageBudgetMB, i, len(rows))
			break pass
		}

		parts := make([][]byte, 0, len(row.MessageIDs))
		var readTotal int64
		for _, mid := range row.MessageIDs {
			body, n, err := p.fetchArticleBody(ctx, pool, mid, row.Group, fetchTimeout, imageMaxBytesPerArticle)
			spent += n
			readTotal += n
			switch {
			case err == nil:
				parts = append(parts, body)
				continue

			case errors.Is(err, nntp.ErrPoolBusy):
				// The yield, exactly as NFO does it: pool exhaustion means the
				// crawler wants these connections. This candidate's partial
				// reads are abandoned, the row stays untouched for next pass.
				p.imageJob.Log("pool busy — yielding to the crawler at %d/%d", i, len(rows))
				p.imageJob.Log("image pass: %d stored, %d unavailable, %.1f MB read",
					stored, absent, float64(spent)/(1<<20))
				p.imageJob.SetIdle(p.nextImage(cfg))
				return

			case isArticleGone(err):
				// ANY segment gone means the whole image is unrecoverable —
				// there are no partial JPEGs. Written off at once.
				if merr := backend.MarkImageUnavailable(ctx, row.ID, "article not on server"); merr != nil {
					p.reportErr(ctx, "usenet/image-mark", merr)
				}
				absent++
				continue pass

			default:
				if n, rerr := backend.RecordImageAttemptFailure(ctx, row.ID); rerr != nil {
					p.reportErr(ctx, "usenet/image-attempt", rerr)
				} else if cfg.ImageMaxRetries > 0 && n >= cfg.ImageMaxRetries {
					if merr := backend.MarkImageUnavailable(ctx, row.ID,
						fmt.Sprintf("unreachable after %d attempts", n)); merr != nil {
						p.reportErr(ctx, "usenet/image-mark", merr)
					}
					absent++
				}
				if timeouts.observe(healthSkipTransport) {
					p.imageJob.Log("provider unhealthy — yielding at %d/%d", i, len(rows))
					break pass
				}
				skipped++
				continue pass
			}
		}

		img, ok := assembleImage(parts)
		if !ok {
			// Decoded, joined, and it is not a JPEG or PNG — the document walk
			// trusted a filename and the filename lied. Permanent for this id.
			if merr := backend.MarkImageUnavailable(ctx, row.ID, "not decodable as an image"); merr != nil {
				p.reportErr(ctx, "usenet/image-mark", merr)
			}
			absent++
			continue
		}
		if err := backend.StoreReleaseImage(ctx, row.ID, img); err != nil {
			p.reportErr(ctx, "usenet/image-store", err)
			skipped++
			continue
		}
		stored++
	}

	p.imageJob.Log("image pass: %d stored, %d unavailable, %d skipped, %.1f MB read",
		stored, absent, skipped, float64(spent)/(1<<20))
	p.imageJob.SetIdle(p.nextImage(cfg))
}

// assembleImage yEnc-decodes each article and joins the parts into one file.
//
// Order: the segment list is in document order, which is nearly always
// posting order — but =ypart carries each part's own byte offset, and when
// every part declares one, that authority wins. A document listing parts out
// of order is exactly the case where trusting it produces a scrambled file
// that still starts with a valid magic number.
func assembleImage(parts [][]byte) ([]byte, bool) {
	type piece struct {
		begin int64
		data  []byte
	}
	pieces := make([]piece, 0, len(parts))
	haveOffsets := true
	for _, body := range parts {
		hdr, herr := parseYencHeader(body)
		dec, derr := yencDecode(body)
		if derr != nil || len(dec) == 0 {
			// A part that is not yEnc: a single-part image posted as plain
			// text does not exist in practice, so treat as undecodable.
			return nil, false
		}
		if herr != nil || hdr.Begin <= 0 {
			haveOffsets = false
		}
		pieces = append(pieces, piece{begin: hdr.Begin, data: dec})
	}
	if haveOffsets {
		sort.Slice(pieces, func(i, j int) bool { return pieces[i].begin < pieces[j].begin })
	}
	var out []byte
	for _, pc := range pieces {
		out = append(out, pc.data...)
		if len(out) > imageMaxDecodedBytes {
			return nil, false
		}
	}
	if !looksLikeImage(out) {
		return nil, false
	}
	return out, true
}

// nextImage is the next scheduled run.
func (p *Plugin) nextImage(cfg Config) time.Time {
	m := cfg.ImageIntervalMin
	if m <= 0 {
		m = 60
	}
	return time.Now().Add(time.Duration(m) * time.Minute)
}
