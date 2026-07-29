package backup

import (
	"context"
	"fmt"
	"time"
)

// The "Backup Index" job. Separate from the archive job because the two run on
// different clocks: the index is cheap enough for daily, the full archive is
// not — and a shorter index interval is what keeps the window in which an
// in-place overwrite can hide down to a day.

// maxClassShrinkPct is how much a class may lose against the previous sealed
// generation before the pass refuses to seal.
//
// This guards the worst failure mode a backup has, and it is not hypothetical:
// a missing bind mount is indistinguishable from an empty class. The boot check
// creates the directory when it is absent, the walk finds nothing, the
// generation seals with zero files, and once older generations age out the only
// copy is gone — with every step reporting success. Refusing to seal turns
// silent data loss into a loud, boring alert.
const maxClassShrinkPct = 10.0

// rehashDenominator is the fraction of the inventory re-read per run regardless
// of stat. 8 gives full coverage every eight runs — daily, so roughly weekly —
// and is what catches bit-rot and a torn write whose mtime never moved again.
const rehashDenominator = 8

func (p *Plugin) runIndex(ctx context.Context) {
	if !p.indexMu.TryLock() {
		p.indexJob.Log("index already running — skipping overlap")
		return
	}
	defer p.indexMu.Unlock()
	p.indexJob.SetRunning()
	started := time.Now()

	if deps == nil || len(deps.Classes) == 0 {
		p.indexJob.Log("no asset classes configured — the host must pass Deps.Classes")
		p.indexJob.SetIdle(time.Now().Add(time.Duration(indexIntervalMin) * time.Minute))
		return
	}

	// The previous sealed generation's per-class totals, read BEFORE the walk so
	// the comparison is against a known-good baseline rather than against
	// whatever this run happens to produce.
	var prev map[string]classTotal
	if lastGen, err := p.st.lastSealedGeneration(ctx); err == nil && lastGen > 0 {
		if t, err := p.st.classTotals(ctx, lastGen); err == nil {
			prev = t
		}
	}

	res, err := p.indexPass(ctx, deps.Root, deps.Classes, rehashDenominator)
	if err != nil {
		p.indexJob.SetError(err.Error())
		p.indexJob.Log("index failed: %v", err)
		return
	}

	// Compare AFTER sealing the row but BEFORE anything downstream treats it as
	// authoritative. A quarantined generation stays on disk for diagnosis; what
	// it must never do is license a retention pass to delete older packs.
	if shrunk := detectShrink(prev, res.PerClass, maxClassShrinkPct); len(shrunk) > 0 {
		for _, s := range shrunk {
			p.indexJob.Log("REFUSING to trust this generation: class %s went from %d files to %d (-%.0f%%). "+
				"A class collapsing usually means a missing bind mount, not a deletion — "+
				"check the mount before anything prunes.",
				s.Class, s.WasFiles, s.NowFiles, s.PctDropped)
		}
		p.indexJob.SetError(fmt.Sprintf("%d class(es) shrank beyond %.0f%%", len(shrunk), maxClassShrinkPct))
		return
	}

	p.indexJob.Log("indexed %s file(s), %s — %s hashed, %s carried forward, %s suspect, %s cleared (%.1fs)",
		fmtComma(res.Files), fmtBytes(res.Bytes),
		fmtComma(res.Hashed), fmtComma(res.Skipped), fmtComma(res.Suspect), fmtComma(res.Cleared),
		time.Since(started).Seconds())
	for _, c := range orderedClasses(deps.Classes) {
		if t, ok := res.PerClass[c.Slug]; ok {
			p.indexJob.Log("  %-16s %8s file(s)  %10s", c.Slug, fmtComma(t.Files), fmtBytes(t.Bytes))
		}
	}
	p.indexJob.SetIdle(time.Now().Add(time.Duration(indexIntervalMin) * time.Minute))
}

// fmtComma renders a count with thousands separators. Six-figure file counts
// are unreadable otherwise, and this log is read while something is wrong.
func fmtComma(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// fmtBytes renders a byte count in the largest unit that keeps it readable.
func fmtBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}
