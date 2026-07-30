//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// buildPassPlugin wires the minimum real machinery a build pass touches:
// a schema-isolated Postgres store, real Redis staging, and inert scheduler
// stubs for the job. Mirrors Provision's field setup (plugin.go) — if
// Provision grows a field the pass needs, this fixture fails loudly at the
// nil dereference rather than silently diverging.
func buildPassPlugin(t *testing.T) (*Plugin, *redisStaging) {
	t.Helper()
	rdb := testRedis(t)
	staging := newTestStaging(rdb)
	sched := core.NewScheduler(core.SchedulerAdapter{})
	p := &Plugin{
		core:        &core.Core{Errors: core.NewErrorReporter(core.ErrorAdapter{})},
		st:          testStore(t),
		staging:     staging,
		tel:         newTelemetry(),
		hits:        newFilterHits(),
		posterHits:  newPosterHits(),
		posterWatch: newPosterWatch(nil),
		outcomes:    newBuildOutcomes(),
		buildJob:    sched.RegisterJob("build-test", ""),
	}
	p.cfg.applyDefaults()
	return p, staging
}

// The WIRING, not just the mechanics: a candidate the builder's verification
// refuses must be withdrawn from the ready queue by the build pass itself,
// with its articles left staged — and a set that later truly completes must
// build normally on a following pass. Without the withdrawal the refused
// entry is immortal: re-drawn, re-loaded and re-refused every pass while its
// articles pin staging memory until the TTL (the 2026-07-30 jam: 157 sets,
// ~7 GB, backfill paused waiting on a drain that could never drain).
func TestBuildPassDemotesRefusedReadySets(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	rdb := staging.rdb

	const group, base = "a.b.group", "Kaiju.Show.S01E01.1080p.BluRay.x264-GRP"
	stage := func(part int, id string) {
		t.Helper()
		if _, err := staging.stageArticles(ctx, []stagedArticle{{
			Group: group, BaseSubject: base,
			Subject:   fmt.Sprintf("%s (%d/2)", base, part),
			MessageID: id, Poster: "poster@example.com", Bytes: 100_000_000,
			Posted:  time.Now(),
			PartNum: part, TotalParts: 2, SegTotal: 2,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	stage(1, "<k1@x>")
	stage(2, "<k2@x>")
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 1 {
		t.Fatalf("fixture: complete set not queued (%d)", n)
	}

	// Break the set AFTER it queued — the divergence population (merged
	// re-posts, pre-monotonic meta) that staging admitted and the builder
	// refuses.
	hash := groupHashKey(group, base)
	if err := rdb.HDel(ctx, artKey(group, hash), formatFieldKey(0, 2)).Err(); err != nil {
		t.Fatal(err)
	}

	built, _ := p.buildLocked(ctx)
	if built != 0 {
		t.Fatalf("built %d from a refused set, want 0", built)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 0 {
		t.Fatal("refused candidate still queued after the pass — the demote call is not wired into the incomplete branch")
	}
	if n, _ := rdb.Exists(ctx, artKey(group, hash), grpKey(group, hash)).Result(); n != 2 {
		t.Fatalf("demote destroyed staged data (%d of 2 keys left) — it must withdraw, not delete", n)
	}
	if n := p.tel.demotedCount(); n != 1 {
		t.Errorf("telemetry demoted = %d, want 1", n)
	}

	// The missing part arrives: the set re-queues and the next pass builds it.
	stage(2, "<k2b@x>")
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 1 {
		t.Fatal("re-completed set not re-queued")
	}
	built, _ = p.buildLocked(ctx)
	if built != 1 {
		t.Fatalf("built %d after the set re-completed, want 1", built)
	}
	var nzbs int
	if err := p.st.(*PGStore).db.DB().QueryRow(
		`SELECT COUNT(*) FROM ` + p.st.(*PGStore).db.Schema() + `.nzbs`).Scan(&nzbs); err != nil {
		t.Fatal(err)
	}
	if nzbs != 1 {
		t.Fatalf("nzbs table holds %d rows, want 1", nzbs)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 0 {
		t.Error("built set still queued")
	}
}
