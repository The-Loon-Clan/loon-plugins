package usenet

// The read side of the news server, published for whoever is planning a
// repair. See pluginapi.ArticleProbe for why it exists and why it lives here
// rather than in an upload agent.
//
// Everything below leans on machinery the health job already runs: the same
// fleet, the same pool, the same classifyStat that decides whether a 430 means
// "gone" or "we could not ask". Repair asking its own questions through a
// second connection path would be a second thing to configure and a second
// thing to get wrong about which failures are absences.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// articleProbeMaxHead caps ArticleHead regardless of what a caller asks for.
// The yEnc header is two short lines; anything beyond a few KB is the payload,
// and a caller that wants the payload wants a download rather than a probe.
const articleProbeMaxHead = 8 << 10

type articleProbe struct{ p *Plugin }

var _ pluginapi.ArticleProbe = articleProbe{}

func (a articleProbe) Available() bool {
	if a.p == nil {
		return false
	}
	ctx := context.Background()
	cfg := a.p.effective(ctx)
	runs, err := a.p.activeFleet(ctx, cfg)
	if err != nil || len(runs) == 0 {
		return false
	}
	// activeFleet hands back live pools; releasing them here would close
	// connections the crawler is about to want. The fleet owns their
	// lifetime, so this only reports what it found.
	return true
}

func (a articleProbe) StatMissing(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cfg := a.p.effective(ctx)
	runs, err := a.p.activeFleet(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no usenet server configured")
	}
	statTimeout := time.Duration(cfg.HealthStatTimeoutSec) * time.Second

	var missing []string
	// Chunked through the same batching the health job uses, so one lease does
	// not hold a connection for thousands of round trips while the crawler
	// waits behind it.
	const chunk = 200
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		res, err := a.p.statBatch(ctx, runs[0].pool, batch, statTimeout)
		if err != nil {
			return nil, err
		}
		for i, r := range res {
			switch r {
			case statMissing:
				missing = append(missing, batch[i])
			case statUnknown:
				// NOT an absence. "We could not ask" and "it is gone" look the
				// same to a caller that only counts, and a repair built on
				// that difference would re-post articles that are still there
				// — wasted upload, and a duplicate segment in the spliced NZB.
				// One unknown makes the whole batch untrustworthy, because a
				// partial answer is indistinguishable from a smaller outage.
				return nil, fmt.Errorf("stat inconclusive for %s: the provider "+
					"neither confirmed nor denied it, so the missing set cannot be trusted", batch[i])
			}
		}
	}
	return missing, nil
}

func (a articleProbe) ArticleHead(ctx context.Context, id string, maxBytes int) ([]byte, error) {
	if id == "" {
		return nil, fmt.Errorf("empty message id")
	}
	if maxBytes <= 0 || maxBytes > articleProbeMaxHead {
		maxBytes = articleProbeMaxHead
	}
	cfg := a.p.effective(ctx)
	runs, err := a.p.activeFleet(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no usenet server configured")
	}
	statTimeout := time.Duration(cfg.HealthStatTimeoutSec) * time.Second

	var out []byte
	err = runs[0].pool.TryDo(ctx, func(c *nntp.Conn) error {
		if statTimeout > 0 {
			// Generous relative to a STAT: this reads a body rather than one
			// status line, and the deadline is here to notice a dead socket
			// cheaply rather than to bound a large transfer.
			_ = c.SetDeadline(time.Now().Add(statTimeout * 3))
		}
		r, err := c.Body(id)
		if err != nil {
			return err
		}
		// LimitReader, then DRAIN. The article is a dot-terminated block on a
		// pooled connection: stopping early leaves the rest of it in the
		// stream, and the next lease reads a yEnc payload as its command
		// response. Copying the tail to io.Discard costs one article of
		// bandwidth and keeps the connection usable.
		buf, rerr := io.ReadAll(io.LimitReader(r, int64(maxBytes)))
		_, _ = io.Copy(io.Discard, r)
		if rerr != nil {
			return rerr
		}
		out = buf
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("article %s returned an empty body", id)
	}
	return out, nil
}
