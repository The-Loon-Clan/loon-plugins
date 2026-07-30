//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// seedCoverage records fetched ranges for a group so walkPastCoverage finds it
// judgeable — the walk-past machinery reads real newsgroup_ranges rows.
func seedCoverage(t *testing.T, st *PGStore, group string, lo, hi int64) {
	t.Helper()
	db := st.db.DB()
	sch := st.db.Schema()
	if _, err := db.Exec(`INSERT INTO `+sch+`.newsgroups (name, active) VALUES ($1, TRUE)
		ON CONFLICT (name) DO UPDATE SET active = TRUE`, group); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO `+sch+`.newsgroup_ranges (backbone, group_name, range_start, range_end)
		VALUES ('b1', $1, $2, $3)`, group, lo, hi); err != nil {
		t.Fatal(err)
	}
}

// stageSalvageSet stages a two-file set: file 1 is data claiming dataTotal
// segments with dataHave present; file 2 is par2 with par2Have of par2Total
// (par2Total 0 = no par2 file at all). Article numbers land from 1000 up.
func stageSalvageSet(t *testing.T, r *redisStaging, group, base string, dataHave, dataTotal, par2Have, par2Total int) {
	t.Helper()
	var arts []stagedArticle
	n := 0
	for i := 1; i <= dataHave; i++ {
		n++
		arts = append(arts, stagedArticle{
			Group: group, BaseSubject: base,
			Subject:   fmt.Sprintf("%s.mkv [01/02] (%d/%d)", base, i, dataTotal),
			MessageID: fmt.Sprintf("<%s-d%d@x>", base, i),
			Poster:    "poster@example.com", Bytes: 100_000_000, Posted: time.Now(),
			PartNum: i, TotalParts: dataTotal, SegTotal: dataTotal,
			FileNum: 1, TotalFiles: 2, FileParts: true,
			ArticleNum: 1000 + n,
		})
	}
	for i := 1; i <= par2Have; i++ {
		n++
		arts = append(arts, stagedArticle{
			Group: group, BaseSubject: base,
			Subject:   fmt.Sprintf("%s.vol0+2.par2 [02/02] (%d/%d)", base, i, par2Total),
			MessageID: fmt.Sprintf("<%s-p%d@x>", base, i),
			Poster:    "poster@example.com", Bytes: 50_000_000, Posted: time.Now(),
			PartNum: i, TotalParts: par2Total, SegTotal: par2Total,
			FileNum: 2, TotalFiles: 2, FileParts: true,
			ArticleNum: 1000 + n,
		})
	}
	if _, err := r.stageArticles(context.Background(), arts); err != nil {
		t.Fatal(err)
	}
}

// stagedKeysExist reports whether a set's art:/grp: keys are both present.
func stagedKeysExist(t *testing.T, r *redisStaging, group, base string) bool {
	t.Helper()
	h := groupHashKey(group, base)
	n, err := r.rdb.Exists(context.Background(), artKey(group, h), grpKey(group, h)).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n == 2
}

// nzbHealth returns (rows, health_status) for a title in the plugin's own
// catalogue — the internal sink the fixture stores through.
func nzbHealth(t *testing.T, st *PGStore, title string) (int, string) {
	t.Helper()
	var n int
	var status string
	err := st.db.DB().QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(health_status), '') FROM `+st.db.Schema()+`.nzbs WHERE title = $1`,
		title).Scan(&n, &status)
	if err != nil {
		t.Fatal(err)
	}
	return n, status
}

// nzbHealthCounts returns the stored (total_segments, missing_segments) for a
// title — the recheck-durability half of a salvage verdict.
func nzbHealthCounts(t *testing.T, st *PGStore, title string) (total, missing int) {
	t.Helper()
	err := st.db.DB().QueryRow(
		`SELECT COALESCE(total_segments, 0), COALESCE(missing_segments, 0)
		   FROM `+st.db.Schema()+`.nzbs WHERE title = $1`, title).Scan(&total, &missing)
	if err != nil {
		t.Fatal(err)
	}
	return total, missing
}

// The full salvage path through the real round entry point: a walk-past-dead
// set whose surviving par2 covers its data gap must become a stored release
// MARKED BROKEN — not evicted, and never stored unmarked, because an unmarked
// broken release serves as complete, the one failure this feature must not
// introduce while fixing the loss.
func TestSalvageStoresBrokenRelease(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	const group, base = "a.b.group", "Kaiju.Show.S01E02.1080p.BluRay.x264-GRP"

	seedCoverage(t, p.st.(*PGStore), group, 1, 100000)
	// 3 of 4 data segments; par2 claims 3 blocks, 1 fetched: one data gap, one
	// surviving recovery block. The under-held par2 is deliberate — it is the
	// shape where the stored total went wrong.
	stageSalvageSet(t, staging, group, base, 3, 4, 1, 3)
	backdate(t, staging, group, base, time.Hour)

	p.runWalkPastSweep(ctx, p.effective(ctx))

	rows, status := nzbHealth(t, p.st.(*PGStore), base)
	if rows != 1 {
		t.Fatalf("catalogue holds %d rows for the salvaged set, want 1", rows)
	}
	if status != healthBroken {
		t.Fatalf("health_status = %q, want %q — a broken release stored unmarked serves as complete", status, healthBroken)
	}
	// The stored total must be EXACTLY the listed articles plus the missing
	// DATA (4 held + 1 gap): a health recheck reconstructs its baseline as
	// total - listed, and that baseline is scored as missing data. Storing
	// salvageTally's total here (which also counts the 2 never-fetched par2
	// claims) inflated the baseline every recheck and decayed broken releases
	// straight to dead — erasing the mark this whole path exists to write.
	if total, missing := nzbHealthCounts(t, p.st.(*PGStore), base); total != 5 || missing != 1 {
		t.Fatalf("stored total=%d missing=%d, want 5/1 — total must equal listed articles + missing data exactly", total, missing)
	}
	if stagedKeysExist(t, staging, group, base) {
		t.Error("staged keys survived the salvage — the set must drain once stored")
	}
	if n := p.tel.salvagedCount(); n != 1 {
		t.Errorf("telemetry salvaged = %d, want 1", n)
	}
}

// Gaps beyond what the surviving par2 can rebuild are not worth a row: the
// set evicts like any other walk-past death, and nothing reaches the sink.
func TestSalvageEvictsBeyondPar2Repair(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	const group, base = "a.b.group", "Gone.Show.S01E03.1080p.BluRay.x264-GRP"

	seedCoverage(t, p.st.(*PGStore), group, 1, 100000)
	// 3 of 5 data segments, no par2 at all: two gaps, zero recovery.
	stageSalvageSet(t, staging, group, base, 3, 5, 0, 0)
	backdate(t, staging, group, base, time.Hour)

	p.runWalkPastSweep(ctx, p.effective(ctx))

	if rows, _ := nzbHealth(t, p.st.(*PGStore), base); rows != 0 {
		t.Fatalf("unrepairable set reached the catalogue (%d rows)", rows)
	}
	if stagedKeysExist(t, staging, group, base) {
		t.Error("unrepairable dead set still staged — it must evict")
	}
	if n := p.tel.salvagedCount(); n != 0 {
		t.Errorf("telemetry salvaged = %d, want 0", n)
	}
}

// Salvage must never resurrect what the build path would drop: an operator-
// blacklisted set is discarded, however repairable its articles are.
func TestSalvageRespectsTheBlacklist(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	const group, base = "a.b.group", "Spam.Show.S01E04.1080p.BluRay.x264-GRP"

	withBlacklist(t, blacklistRule{Pattern: "Spam\\.Show", Field: "subject", Enabled: true})
	seedCoverage(t, p.st.(*PGStore), group, 1, 100000)
	stageSalvageSet(t, staging, group, base, 3, 4, 2, 2)
	backdate(t, staging, group, base, time.Hour)

	p.runWalkPastSweep(ctx, p.effective(ctx))

	if rows, _ := nzbHealth(t, p.st.(*PGStore), base); rows != 0 {
		t.Fatalf("blacklisted set reached the catalogue (%d rows)", rows)
	}
	if stagedKeysExist(t, staging, group, base) {
		t.Error("blacklisted dead set still staged — it must drop")
	}
}

// par2-only gaps mean every data segment is present: the release stores as a
// NORMAL row (no broken verdict) — the completeness check alone was holding a
// perfectly good release hostage to its recovery files.
func TestSalvageStoresDataCompleteAsNormal(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	const group, base = "a.b.group", "Fine.Show.S01E05.1080p.BluRay.x264-GRP"

	seedCoverage(t, p.st.(*PGStore), group, 1, 100000)
	// All 4 data segments, 1 of 3 par2 segments: data complete.
	stageSalvageSet(t, staging, group, base, 4, 4, 1, 3)
	backdate(t, staging, group, base, time.Hour)

	p.runWalkPastSweep(ctx, p.effective(ctx))

	rows, status := nzbHealth(t, p.st.(*PGStore), base)
	if rows != 1 {
		t.Fatalf("data-complete set not stored (%d rows)", rows)
	}
	if status == healthBroken {
		t.Error("data-complete release marked broken — all its data segments are present")
	}
	if stagedKeysExist(t, staging, group, base) {
		t.Error("stored set still staged")
	}
}

// Every resolved set leaves a completion-distance record — the measured
// basis the position-based staging window will be derived from. Driven
// through the real paths: a build records 'built', a salvage 'salvaged',
// a beyond-repair judgment 'salvage_dead', each with its article span and
// the group's watermarks attached at flush.
func TestResolutionsRecordBuildAndSalvage(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	st := p.st.(*PGStore)
	const group = "a.b.group"

	// Watermarks the flush should snapshot.
	if _, err := st.db.DB().Exec(`INSERT INTO ` + st.db.Schema() +
		`.newsgroup_state (backbone, group_name, high_watermark, back_watermark) VALUES ('b1', 'a.b.group', 90000, 40000)
		 ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	seedCoverage(t, st, group, 1, 100000)

	// One buildable set (completes and builds normally).
	for i := 1; i <= 2; i++ {
		if _, err := staging.stageArticles(ctx, []stagedArticle{{
			Group: group, BaseSubject: "Kaiju.Show.S01E08.1080p.BluRay.x264-GRP",
			Subject:   fmt.Sprintf("Kaiju.Show.S01E08.1080p.BluRay.x264-GRP (%d/2)", i),
			MessageID: fmt.Sprintf("<r8-%d@x>", i), Poster: "p", Bytes: 50_000_000,
			Posted:  time.Now(),
			PartNum: i, TotalParts: 2, SegTotal: 2, ArticleNum: 50000 + i,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// One salvageable dead set and one beyond-repair dead set.
	stageSalvageSet(t, staging, group, "Rescue.Show.S01E09.1080p.BluRay.x264-GRP", 3, 4, 2, 2)
	backdate(t, staging, group, "Rescue.Show.S01E09.1080p.BluRay.x264-GRP", time.Hour)
	stageSalvageSet(t, staging, group, "Gone.Show.S01E10.1080p.BluRay.x264-GRP", 3, 5, 0, 0)
	backdate(t, staging, group, "Gone.Show.S01E10.1080p.BluRay.x264-GRP", time.Hour)

	p.buildLocked(ctx)

	type resRow struct {
		kind           string
		lo, back, high int64
	}
	var got []resRow
	res, err := st.db.DB().Query(`SELECT kind, art_lo, back_watermark, high_watermark FROM ` +
		st.db.Schema() + `.set_resolutions ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for res.Next() {
		var r resRow
		if err := res.Scan(&r.kind, &r.lo, &r.back, &r.high); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	kinds := map[string]int{}
	for _, r := range got {
		kinds[r.kind]++
		if r.lo <= 0 {
			t.Errorf("%s recorded without a span (art_lo=%d)", r.kind, r.lo)
		}
		if r.back != 40000 || r.high != 90000 {
			t.Errorf("%s recorded watermarks (%d,%d), want (40000,90000)", r.kind, r.back, r.high)
		}
	}
	if kinds["built"] != 1 || kinds["salvaged"] != 1 || kinds["salvage_dead"] != 1 {
		t.Errorf("resolutions = %v (rows: %+v), want built:1 salvaged:1 salvage_dead:1", kinds, got)
	}
}
