//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The stored half. Everything asserted here is a property of the SQL — the
// day-bucketed primary key, the additive upsert, and the sample's
// don't-overwrite-with-empty rule — so it needs a real Postgres rather than a
// fake that would only restate the assumptions.

func outcomeRow(t *testing.T, s *PGStore, reason buildOutcome) (count int64, sample string, ok bool) {
	t.Helper()
	err := s.db.DB().QueryRow(
		`SELECT total_count, last_sample FROM `+s.db.Schema()+
			`.build_outcomes WHERE day = CURRENT_DATE AND reason = $1`,
		string(reason)).Scan(&count, &sample)
	if err != nil {
		return 0, "", false
	}
	return count, sample, true
}

func TestRecordBuildOutcomes_AccumulatesIntoTodaysRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := map[buildOutcome]*outcomeVal{
		outcomeIncomplete: {count: 400, sample: "waiting.on.parts"},
		outcomeBuilt:      {count: 3, sample: "a.real.release"},
	}
	if err := s.recordBuildOutcomes(ctx, first); err != nil {
		t.Fatalf("first flush: %v", err)
	}

	// A second pass on the same day must ADD, not replace — otherwise the
	// day's total is just whatever the last pass happened to see.
	second := map[buildOutcome]*outcomeVal{
		outcomeIncomplete: {count: 350, sample: "later.sample"},
	}
	if err := s.recordBuildOutcomes(ctx, second); err != nil {
		t.Fatalf("second flush: %v", err)
	}

	n, sample, ok := outcomeRow(t, s, outcomeIncomplete)
	if !ok {
		t.Fatal("no incomplete row")
	}
	if n != 750 {
		t.Errorf("incomplete = %d, want 750 (400 + 350)", n)
	}
	// The later non-empty sample wins, matching filter_hits: the newest
	// evidence is the more useful one when a bucket is being investigated.
	if sample != "later.sample" {
		t.Errorf("sample = %q, want later.sample", sample)
	}
	if n, _, _ := outcomeRow(t, s, outcomeBuilt); n != 3 {
		t.Errorf("built = %d, want 3 — a flush must not disturb other reasons", n)
	}
}

// An empty sample must not blank a stored one. A pass can note a reason with no
// subject available; losing the existing evidence to that would defeat the
// column's purpose.
func TestRecordBuildOutcomes_EmptySampleDoesNotOverwrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.recordBuildOutcomes(ctx, map[buildOutcome]*outcomeVal{
		outcomeJunk: {count: 1, sample: "keep.this.one"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordBuildOutcomes(ctx, map[buildOutcome]*outcomeVal{
		outcomeJunk: {count: 1, sample: ""},
	}); err != nil {
		t.Fatal(err)
	}

	n, sample, _ := outcomeRow(t, s, outcomeJunk)
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if sample != "keep.this.one" {
		t.Errorf("sample = %q, want the earlier one preserved", sample)
	}
}

func TestRecordBuildOutcomes_EmptyMapIsANoOp(t *testing.T) {
	s := testStore(t)
	if err := s.recordBuildOutcomes(context.Background(), nil); err != nil {
		t.Errorf("nil map returned %v, want nil — an idle pass writes nothing", err)
	}
	if _, _, ok := outcomeRow(t, s, outcomeBuilt); ok {
		t.Error("a no-op flush created a row")
	}
}

// The outcome ledger's whole point: the reasons must sum to the candidates
// examined, or a branch was added to buildLocked without accounting for it.
// build_outcomes.go claimed "the test asserts it" since the ledger was born;
// this is that test, driven through a real pass over one candidate per major
// branch. (outcomeSalvaged is deliberately outside this sum — salvage
// candidates come from the walk-past sweep, not the ready-queue draw.)
func TestBuildOutcomesSumToCandidates(t *testing.T) {
	ctx := context.Background()
	p, staging := buildPassPlugin(t)
	rdb := staging.rdb
	const group = "a.b.group"

	stagePair := func(base string) {
		t.Helper()
		for i := 1; i <= 2; i++ {
			if _, err := staging.stageArticles(ctx, []stagedArticle{{
				Group: group, BaseSubject: base,
				Subject:   fmt.Sprintf("%s (%d/2)", base, i),
				MessageID: fmt.Sprintf("<%s-%d@x>", base, i),
				Poster:    "p", Bytes: 50_000_000, Posted: time.Now(),
				PartNum: i, TotalParts: 2, SegTotal: 2,
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// One candidate per branch: buildable, blocked extension (title fast
	// path), and ready-but-refused (corrupted after queueing → incomplete).
	stagePair("Kaiju.Show.S01E06.1080p.BluRay.x264-GRP")
	stagePair("malware.exe")
	stagePair("Kaiju.Show.S01E07.1080p.BluRay.x264-GRP")
	if err := rdb.HDel(ctx, artKey(group, groupHashKey(group, "Kaiju.Show.S01E07.1080p.BluRay.x264-GRP")),
		formatFieldKey(0, 2)).Err(); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 3 {
		t.Fatalf("fixture: %d candidates queued, want 3", n)
	}

	p.buildLocked(ctx)

	rows := map[string]int{}
	res, err := p.st.(*PGStore).db.DB().Query(
		`SELECT reason, SUM(total_count) FROM ` + p.st.(*PGStore).db.Schema() + `.build_outcomes GROUP BY reason`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for res.Next() {
		var reason string
		var n int
		if err := res.Scan(&reason, &n); err != nil {
			t.Fatal(err)
		}
		rows[reason] = n
	}
	if rows["built"] != 1 || rows["blocked_ext"] != 1 || rows["incomplete"] != 1 {
		t.Errorf("outcomes = %v, want built:1 blocked_ext:1 incomplete:1", rows)
	}
	sum := 0
	for reason, n := range rows {
		if reason == string(outcomeSalvaged) {
			continue
		}
		sum += n
	}
	if sum != 3 {
		t.Errorf("outcome reasons sum to %d for 3 candidates — a buildLocked branch is unaccounted (%v)", sum, rows)
	}
}

// Re-seeding must never resurrect what an operator turned off: the upsert
// updates rule/params/notes (so shipped fixes deploy) while `enabled` is
// deliberately absent from the SET list. A future "enabled = EXCLUDED.enabled"
// cleanup would re-enable every locally disabled rule on every boot — this
// pins the invariant so that cleanup fails loudly instead of shipping.
func TestSeedJunkRulesPreservesOperatorState(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	specs := []junkRuleSpec{
		{Name: "seed_a", Kind: "regex", Rule: "^a$", Enabled: true},
		{Name: "seed_b", Kind: "regex", Rule: "^b$", Enabled: true},
	}
	if _, err := s.seedJunkRules(ctx, specs); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.DB().Exec(
		`UPDATE ` + s.db.Schema() + `.junk_rules SET enabled = FALSE WHERE name = 'seed_a'`); err != nil {
		t.Fatal(err)
	}

	// The next deploy re-seeds with a fixed pattern for the disabled rule.
	specs[0].Rule = "^a2$"
	if _, err := s.seedJunkRules(ctx, specs); err != nil {
		t.Fatal(err)
	}

	var enabled bool
	var rule string
	if err := s.db.DB().QueryRow(
		`SELECT enabled, rule FROM `+s.db.Schema()+`.junk_rules WHERE name = 'seed_a'`).
		Scan(&enabled, &rule); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("re-seed re-enabled a rule the operator disabled")
	}
	if rule != "^a2$" {
		t.Errorf("re-seed did not deploy the fixed pattern (rule = %q) — preserving enabled must not freeze the rule body", rule)
	}
}

// The corpus store: batch insert lands rows with their residue flag, and the
// prune trims by age — the rolling window that keeps a sampled table small
// however long the install runs.
func TestSubjectCorpusInsertAndPrune(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	rows := []corpusRow{
		{group: "a.b.group", subject: "Show { 1 | 100 } yEnc", residue: true},
		{group: "a.b.group", subject: "Fine Release (1/45) yEnc", residue: false},
	}
	if err := s.insertSubjectCorpus(ctx, rows); err != nil {
		t.Fatal(err)
	}
	var n, res int
	if err := s.db.DB().QueryRow(
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE residue) FROM `+s.db.Schema()+`.subject_corpus`).
		Scan(&n, &res); err != nil {
		t.Fatal(err)
	}
	if n != 2 || res != 1 {
		t.Errorf("corpus holds %d rows / %d residues, want 2/1", n, res)
	}

	if _, err := s.db.DB().Exec(
		`UPDATE ` + s.db.Schema() + `.subject_corpus SET seen_at = now() - interval '30 days'`); err != nil {
		t.Fatal(err)
	}
	pruned, err := s.pruneSubjectCorpus(ctx, 14)
	if err != nil || pruned != 2 {
		t.Errorf("pruned %d (err=%v), want 2 — the rolling window must trim", pruned, err)
	}
}

// The resolutions store in isolation: batch insert with watermarks attached,
// and the prune.
func TestSetResolutionsStore(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	rows := []setResolution{{group: "a.b.g", base: "Some.Release", kind: "built", artLo: 100, artHi: 200, held: 40}}
	marks := map[string]groupMarks{"a.b.g": {Back: 50, High: 900}}
	if err := s.insertSetResolutions(ctx, rows, marks); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var n int
	if err := s.db.DB().QueryRow(`SELECT COUNT(*) FROM ` + s.db.Schema() +
		`.set_resolutions WHERE kind='built' AND base_subject='Some.Release' AND art_lo=100 AND back_watermark=50 AND high_watermark=900`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stored %d matching rows, want 1", n)
	}
}
