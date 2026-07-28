package usenet

import (
	"strconv"
	"testing"
)

// meta for a release of one large media file plus small par2s — the shape of
// essentially every real multi-file post.
func metaOneBigManySmall(totalFiles, bigSegs, smallSegs int) (map[string]string, []string) {
	meta := map[string]string{
		"file_parts":  "1",
		"total_files": strconv.Itoa(totalFiles),
		"total_parts": strconv.Itoa(bigSegs),
		"seg_total":   strconv.Itoa(bigSegs), // set-wide MAX, as the writer stores it
	}
	var fields []string
	// File 1: the media file, all its segments present.
	meta[segFieldKey(1)] = strconv.Itoa(bigSegs)
	for p := 1; p <= bigSegs; p++ {
		fields = append(fields, formatFieldKey(1, p))
	}
	// Files 2..n: par2 volumes, all their (few) segments present.
	for f := 2; f <= totalFiles; f++ {
		meta[segFieldKey(f)] = strconv.Itoa(smallSegs)
		for p := 1; p <= smallSegs; p++ {
			fields = append(fields, formatFieldKey(f, p))
		}
	}
	return meta, fields
}

// The bug that produced built=0 on production for a whole day. Every file was
// judged against the SET-WIDE maximum segment count, so only the largest could
// ever qualify — a release of one media file plus twelve par2s counted 1
// complete file out of 13 and sat pending until it expired.
func TestGroupCompleteJudgesEachFileByItsOwnSegmentTotal(t *testing.T) {
	meta, fields := metaOneBigManySmall(13, 400, 3)
	if !isGroupComplete(meta, fields) {
		t.Error("a fully-present release read as incomplete — every file is being " +
			"judged against the largest file's segment count")
	}

	// Genuinely missing the tail of the media file: must stay incomplete.
	_, short := metaOneBigManySmall(13, 400, 3)
	short = short[:len(short)-1] // drop one par2 segment
	metaShort, _ := metaOneBigManySmall(13, 400, 3)
	if isGroupComplete(metaShort, short) {
		t.Error("a set missing a segment read as complete")
	}

	// A missing FILE entirely must stay incomplete.
	metaMissing, fieldsMissing := metaOneBigManySmall(13, 400, 3)
	metaMissing["total_files"] = "14" // one more file than we have
	if isGroupComplete(metaMissing, fieldsMissing) {
		t.Error("a set missing a whole file read as complete")
	}
}

// groupNeededParts gates whether the exact check runs at all, so it must be a
// LOWER bound. Overestimating does not delay a set, it withholds it forever.
func TestGroupNeededPartsIsALowerBound(t *testing.T) {
	meta, fields := metaOneBigManySmall(13, 400, 3)
	actual := len(fields) // 400 + 12*3 = 436
	need := groupNeededParts(meta)
	if need > actual {
		t.Errorf("needed %d for a set of %d articles — the gate would never open", need, actual)
	}
	if need <= 0 {
		t.Errorf("needed %d; the gate must still require something", need)
	}

	// Production's numbers: the old totalFiles*seg_total gave 2,016,196 for a
	// set holding 156,882 articles, a factor of ~13.
	big := map[string]string{
		"file_parts": "1", "total_files": "13",
		"seg_total": "155092", "total_parts": "155092",
	}
	if got := groupNeededParts(big); got > 200000 {
		t.Errorf("pre-upgrade fallback needed %d for a ~157k-article set — still an over-estimate", got)
	}

	// Partially-seen set: the bound rises with what is known but never exceeds
	// the truth, so the gate opens as soon as the set could plausibly be whole.
	partial := map[string]string{"file_parts": "1", "total_files": "5"}
	partial[segFieldKey(1)] = "100"
	partial[segFieldKey(2)] = "4"
	if got := groupNeededParts(partial); got != 100+4+3 {
		t.Errorf("partial bound = %d, want 107 (known files + one article each for the 3 unseen)", got)
	}
}

// Single-file sets were never affected; pinned so the multi-file fix does not
// disturb them.
func TestGroupCompleteSingleFileUnchanged(t *testing.T) {
	meta := map[string]string{"file_parts": "0", "total_parts": "45"}
	var fields []string
	for p := 1; p <= 45; p++ {
		fields = append(fields, formatFieldKey(0, p))
	}
	if !isGroupComplete(meta, fields) {
		t.Error("complete single-file set read as incomplete")
	}
	if isGroupComplete(meta, fields[:44]) {
		t.Error("single-file set missing a segment read as complete")
	}
	if got := groupNeededParts(meta); got != 45 {
		t.Errorf("single-file needed = %d, want 45", got)
	}
}

// A large release is tens of thousands of segments over dozens of batches, and
// a bulk re-read fetches those out of order across many parallel connections.
// Judging "hopeless" from when the set was CREATED deleted exactly those
// releases while they were still filling — production shed ~128 sets/minute
// during a re-read and built nothing.
func TestEvictionMeasuresStalenessNotAge(t *testing.T) {
	now := int64(1_000_000)

	// Created 20 minutes ago but touched 10 seconds ago: still arriving.
	growing := map[string]string{
		"created_at": strconv.FormatInt(now-1200, 10),
		"touched_at": strconv.FormatInt(now-10, 10),
	}
	age, ok := evictionStaleness(growing, now)
	if !ok {
		t.Fatal("no timestamp resolved")
	}
	if age > 300 {
		t.Errorf("a set touched %ds ago reads as %ds stale — it is still growing "+
			"and would be evicted mid-fill", 10, age)
	}

	// Created and last touched 20 minutes ago: genuinely stalled.
	stalled := map[string]string{
		"created_at": strconv.FormatInt(now-1200, 10),
		"touched_at": strconv.FormatInt(now-1200, 10),
	}
	if age, _ := evictionStaleness(stalled, now); age <= 300 {
		t.Errorf("a set untouched for 20 minutes reads as %ds stale — eviction "+
			"would never shed genuinely dead sets", age)
	}

	// Pre-upgrade set with no touched_at falls back to created_at.
	legacy := map[string]string{"created_at": strconv.FormatInt(now-600, 10)}
	if age, ok := evictionStaleness(legacy, now); !ok || age != 600 {
		t.Errorf("legacy fallback: age=%d ok=%v, want 600/true", age, ok)
	}

	// No timestamps at all must not evict on a zero age.
	if _, ok := evictionStaleness(map[string]string{}, now); ok {
		t.Error("a set with no timestamps must not resolve an age")
	}
}
