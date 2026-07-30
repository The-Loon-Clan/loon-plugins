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
	// 3 of 4 data segments, both par2 segments: one gap, two recovery blocks.
	stageSalvageSet(t, staging, group, base, 3, 4, 2, 2)
	backdate(t, staging, group, base, time.Hour)

	p.runWalkPastSweep(ctx, p.effective(ctx))

	rows, status := nzbHealth(t, p.st.(*PGStore), base)
	if rows != 1 {
		t.Fatalf("catalogue holds %d rows for the salvaged set, want 1", rows)
	}
	if status != healthBroken {
		t.Fatalf("health_status = %q, want %q — a broken release stored unmarked serves as complete", status, healthBroken)
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
