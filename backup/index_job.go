package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	if shrunk := detectShrink(prev, res.PerClass, maxClassShrinkPct, rotatingClasses(deps.Classes)); len(shrunk) > 0 {
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
	if empty := emptyClasses(deps.Classes, res.PerClass); len(empty) > 0 {
		p.indexJob.Log("NOTE: %d class(es) hold no files: %s. Confirm each is genuinely empty "+
			"rather than an unmounted volume — they are listed cheapest-first, so the earliest "+
			"names are the ones that cannot be re-fetched from anywhere.",
			len(empty), strings.Join(empty, ", "))
		// The note above asks the operator to confirm something the kernel can
		// answer. An empty class whose directory sits on the container's
		// writable layer is not an empty class — it is a missing mount, and it
		// is the difference between "nothing to back up" and "backing up
		// nothing". That distinction earns an error, not a line in a log: the
		// note existed while prod lost four database dumps.
		for _, slug := range empty {
			c, ok := classBySlug(deps.Classes, slug)
			if !ok {
				continue
			}
			w := ephemeralWarning("Class "+slug, filepath.Join(deps.Root, c.Dir))
			if w == "" {
				continue
			}
			p.indexJob.Log("WARNING: %s", w)
			if p.core != nil && p.core.Errors != nil {
				p.core.Errors.Report(ctx, "backup/class-dir-ephemeral", errors.New(w))
			}
		}
	}
	p.indexJob.SetIdle(time.Now().Add(time.Duration(indexIntervalMin) * time.Minute))
}

// emptyClasses names the classes that indexed nothing, cheapest-first.
//
// The shrink gate cannot cover this case and it is worth being precise about
// why: it compares against the previous sealed generation, so it fires when a
// class LOSES files. A class that was never mounted has always been zero, never
// shrank, and is therefore waved through every run for as long as it stays
// broken — the backup reporting success while protecting nothing, which is the
// exact failure the gate was written to prevent, in its other form.
//
// This cannot be resolved automatically. From inside the container an unmounted
// class and a genuinely empty one are indistinguishable: the boot check creates
// the directory either way, and Docker silently creates a missing bind source
// as an empty directory too. Several classes here really are empty — no wiki
// uploads yet, no music covers yet — so refusing to seal would cry wolf and get
// the gate switched off. Naming them every run is the honest middle: it costs
// one line and makes the difference discoverable before a restore needs them.
func emptyClasses(classes []AssetClass, perClass map[string]classTotal) []string {
	var out []string
	for _, c := range orderedClasses(classes) {
		if t, ok := perClass[c.Slug]; !ok || t.Files == 0 {
			out = append(out, c.Slug)
		}
	}
	return out
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
