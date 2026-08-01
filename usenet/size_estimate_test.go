package usenet

import "testing"

func art(bytes int64, total int) stagedArticle {
	return stagedArticle{Bytes: bytes, TotalParts: total}
}

// The estimate must take the LEAST per-article figure, not the mean. At ingest
// any single article can be the one that trips a sized rule, so the set's risk
// is set by the article that under-states it the most. A mean would describe
// an estimator the ingest path cannot implement — it never sees the set.
func TestWorstCaseTakesTheLeastArticleEstimate(t *testing.T) {
	// A set of 10 parts whose articles vary: 1 MB, 1 MB, and a 100 KB tail.
	arts := []stagedArticle{art(1<<20, 10), art(1<<20, 10), art(100<<10, 10)}
	est, usable := worstCaseEstimate(arts)
	if usable != 3 {
		t.Fatalf("usable = %d, want 3", usable)
	}
	if want := int64(100 << 10 * 10); est != want {
		t.Errorf("est = %d, want the smallest article's %d", est, want)
	}
}

// An article the parser could read no part count from would not be estimated
// at ingest either, so it must not drag the estimate to zero.
func TestArticlesWithoutAPartCountAreIgnored(t *testing.T) {
	arts := []stagedArticle{art(500, 0), art(1000, 4), art(0, 4)}
	est, usable := worstCaseEstimate(arts)
	if usable != 1 {
		t.Errorf("usable = %d, want 1 (only the complete article)", usable)
	}
	if est != 4000 {
		t.Errorf("est = %d, want 4000", est)
	}
	// A set where nothing is usable reports so rather than claiming zero bytes.
	if est, usable := worstCaseEstimate([]stagedArticle{art(0, 0)}); est != 0 || usable != 0 {
		t.Errorf("unusable set = %d/%d, want 0/0", est, usable)
	}
	if est, usable := worstCaseEstimate(nil); est != 0 || usable != 0 {
		t.Errorf("empty set = %d/%d, want 0/0", est, usable)
	}
}

// The bucket labels drive the histogram an operator reads to pick the margin.
// They must sort in ratio order, and the under-estimate side — the direction
// that would junk a real release — must be resolvable.
func TestBucketsSortInRatioOrderAndSeparateTheRiskySide(t *testing.T) {
	// A perfect estimate.
	if got := estimateBucket(1000, 1000); got != "04_under_1.05" {
		t.Errorf("exact estimate bucketed as %q", got)
	}
	// Wild over-estimate: harmless, it only costs a missed rejection.
	if got := estimateBucket(100, 1000); got != "01_under_0.5" {
		t.Errorf("over-estimate bucketed as %q", got)
	}
	// 10x under-estimate: the dangerous direction, must land high in the range.
	if got := estimateBucket(10_000, 1_000); got != "11_under_16" {
		t.Errorf("10x under-estimate bucketed as %q", got)
	}
	// Past the last boundary.
	if got := estimateBucket(20_000, 1_000); got != "12_over_16" {
		t.Errorf("extreme under-estimate bucketed as %q", got)
	}
	// Unusable inputs are their own bucket, never silently a ratio.
	for _, c := range [][2]int64{{1000, 0}, {0, 1000}, {0, 0}, {-1, 5}} {
		if got := estimateBucket(c[0], c[1]); got != "00_unusable" {
			t.Errorf("estimateBucket(%d,%d) = %q, want 00_unusable", c[0], c[1], got)
		}
	}

	// Ascending ratios must produce ascending labels, since the admin card
	// sorts them as strings.
	prev := ""
	for _, ratio := range []float64{0.1, 0.6, 0.9, 1.0, 1.1, 1.3, 1.7, 2.5, 3.5, 6, 12, 100} {
		got := estimateBucket(int64(ratio*1000), 1000)
		if got <= prev {
			t.Errorf("ratio %.1f gave %q which does not sort after %q", ratio, got, prev)
		}
		prev = got
	}
}

// The whole point of the margin is that a sized rule fires BELOW its cap, so
// an under-estimate is what junks a real release. This pins the arithmetic the
// eventual gate will use, before anything depends on it.
func TestMarginProtectsAgainstUnderEstimates(t *testing.T) {
	const cap790K = 790_000 // tiny_no_space's band

	// A set truly just over the cap, under-estimated 3x, would be wrongly
	// junked by a naive check — and is correctly spared by the margin.
	trueSize, est := int64(900_000), int64(300_000)
	if est >= cap790K {
		t.Fatal("test setup: the naive check would not have fired anyway")
	}
	if est*safeSizeMargin < cap790K {
		t.Errorf("margin %d admitted an estimate that hides a %d-byte release",
			safeSizeMargin, trueSize)
	}
	// A genuinely tiny release still clears the gate, or the gate is useless.
	if est := int64(50_000); est*safeSizeMargin >= cap790K {
		t.Errorf("margin %d rejects even a clearly-tiny release", safeSizeMargin)
	}
}
