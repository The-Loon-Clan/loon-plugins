package usenet

import "fmt"

// Estimating a release's size from a single article, so the SIZED junk rules
// can eventually run at ingest instead of at the end of the pipeline.
//
// The waste this targets is measured, not theoretical. The sized rules need
// the release's total bytes, which is only known once every article is loaded
// — the last step. So junk only a sized rule can catch is staged into Redis,
// completes, waits behind a ready queue millions deep, gets drawn, has its
// articles loaded (~16 ms), and is only THEN recognised. On 2026-07-31 that
// path ended in `junk` for 99.97% of the sets that walked it.
//
// An OVER line already carries the article's :bytes (RFC 3977) and its subject
// already carries the claimed part count, so a set's size is estimable from
// any ONE of its articles:
//
//	estimated_total_bytes ~= article_bytes * claimed_total_parts
//
// The catch is that a false junk verdict at ingest is UNRECOVERABLE: the
// article is never staged, so nothing downstream can correct it. A sized rule
// fires when the size is BELOW its cap, which means the dangerous error is an
// UNDER-estimate — it makes a large release look small enough to junk.
//
// So this file ships the measurement first, and only the measurement. It
// computes the estimate the ingest path COULD have made and compares it to the
// true summed size, at build time where both numbers are already in hand. That
// needs no ingest change, no staging change, and takes no risk. The margin for
// the real gate comes out of the resulting distribution rather than a guess.

// worstCaseEstimate returns the LEAST per-article estimate for a set, computed
// two ways, with the count of articles that could contribute.
//
// The least, not the mean: at ingest any single article can be the one that
// triggers a sized rule, so the risk is set by the article that under-states
// the release the most. Averaging would describe an estimator the ingest path
// cannot implement — it sees one article at a time, not the set.
//
// perFile is `bytes * claimed parts`. whole additionally multiplies by the
// claimed FILE count where the subject carries one.
//
// Both are measured because the first day of data said the difference matters
// enormously. `bytes * parts` was right within 5% for 98.7% of sets and
// under-stated the rest by MORE than 16x — 42,088 of them. A subject's
// "(1/100)" counts parts within one FILE, so for a multi-file release the
// naive figure describes one file and misses the release by roughly the file
// count. Which is fatal for the intended use: a sized rule fires BELOW its
// cap, so an under-estimate is what junks a real release, and no fixed margin
// covers a tail that open-ended.
func worstCaseEstimate(arts []stagedArticle) (perFile, whole int64, usable int) {
	for _, a := range arts {
		if a.Bytes <= 0 || a.TotalParts <= 0 {
			continue
		}
		usable++
		e := a.Bytes * int64(a.TotalParts)
		if perFile == 0 || e < perFile {
			perFile = e
		}
		// FileParts distinguishes a FILE counter from a part counter; without
		// it the marker means something else and multiplying would invent
		// size. A single-file release keeps the same figure either way.
		w := e
		if a.FileParts && a.TotalFiles > 1 {
			w = e * int64(a.TotalFiles)
		}
		if whole == 0 || w < whole {
			whole = w
		}
	}
	return perFile, whole, usable
}

// estimateBuckets are the ratio boundaries of actual/estimate, ascending.
//
// Ratios ABOVE 1 are the under-estimates — the direction that would junk a
// real release — so the buckets are finer there. The lowest bucket is
// everything under 0.5 (a wild over-estimate, which only costs a missed
// rejection).
var estimateBuckets = []float64{0.5, 0.8, 0.95, 1.05, 1.25, 1.5, 2, 3, 4, 8, 16}

// estimateBucket labels a ratio for the counter table. Labels sort
// lexicographically in ratio order (zero-padded index prefix) so the admin
// list reads as a histogram rather than an alphabetised jumble.
func estimateBucket(actual, est int64) string {
	if est <= 0 || actual <= 0 {
		return "00_unusable"
	}
	r := float64(actual) / float64(est)
	for i, b := range estimateBuckets {
		if r < b {
			return fmt.Sprintf("%02d_under_%g", i+1, b)
		}
	}
	return fmt.Sprintf("%02d_over_%g", len(estimateBuckets)+1, estimateBuckets[len(estimateBuckets)-1])
}

// noteSizeEstimate records how wrong each estimator would have been for one
// set, bucketed by ratio, under both kinds.
//
// Called on the build path with the articles already in memory, so it costs a
// multiply per article against a ~16 ms load. It counts EVERY set it sees,
// including the ones about to be junked: those are precisely the sets an
// ingest-time gate would reject, so their error is the error that matters.
//
// Two counters rather than one because the choice between the estimators has
// to be made from data. The first day's naive figures had a 1.29% tail beyond
// 16x, and a gate cannot ship until one of these has a bounded one.
func (p *Plugin) noteSizeEstimate(arts []stagedArticle, base string) {
	perFile, whole, usable := worstCaseEstimate(arts)
	if usable == 0 {
		p.hits.noteN("size_estimate", "00_unusable", 1, base)
		return
	}
	actual, _ := summarize(arts)
	p.hits.noteN("size_estimate", estimateBucket(actual, perFile), 1, base)
	p.hits.noteN("size_estimate_files", estimateBucket(actual, whole), 1, base)
}

// safeSizeMargin is the factor a sized rule's cap must exceed the estimate by
// before the rule could be trusted at ingest.
//
// It is deliberately NOT wired into any decision yet. It exists so the
// measurement has something to answer: once the histogram shows the ratio's
// upper tail, the margin is set above that tail and the gate becomes
// `estimate * margin < cap`. Shipping the constant with the instrument keeps
// the two in one place instead of rediscovering the reasoning later.
const safeSizeMargin = 4
