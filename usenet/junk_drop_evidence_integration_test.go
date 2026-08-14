//go:build integration

package usenet

import (
	"context"
	"testing"
)

// The drop evidence is a property of the SQL, not of the Go struct, and that
// distinction shipped a bug.
//
// Every reader of subject_corpus asks "junk_rule IS NOT NULL" to mean "this
// subject was dropped". The Go side is honest about it — a kept row carries
// junkRule == "" and a unit test pins that. But "" is not NULL once it reaches
// Postgres, so the keeps answered IS NOT NULL too: the debug list read 100% of
// sampled subjects as junk, and the recovery probe would have spent provider
// bytes fetching the bodies of releases that were indexed correctly.
//
// A mock cannot catch that. It is the same shape as the \b-versus-\y regex pair
// that starved the title-cleaner worklist for a year while its Go mirror stayed
// green (CLAUDE.md, "Lessons learned"), so it gets the same treatment: a test
// against a real database, asserting the predicate the readers actually use.
func TestSubjectCorpusSeparatesDropsFromKeepsInSQL(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	rows := []corpusRow{
		{group: "a.b.test", subject: "541279675.bin", residue: false,
			junkRule: "bare_numeric_token", messageID: "<drop@example>"},
		// A keep: the crawler indexed this one, so there is no rule and no
		// article to go back to.
		{group: "a.b.test", subject: "[Judas] Real Show - 04 [1080p].mkv", residue: false},
	}
	if err := s.insertSubjectCorpus(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// The predicate every reader uses, asserted directly rather than through a
	// helper — the point is what Postgres thinks, not what Go thinks.
	var drops, keeps int
	q := `SELECT count(*) FILTER (WHERE junk_rule IS NOT NULL),
	             count(*) FILTER (WHERE junk_rule IS NULL)
	        FROM ` + s.db.Schema() + `.subject_corpus`
	if err := s.db.DB().QueryRow(q).Scan(&drops, &keeps); err != nil {
		t.Fatalf("count: %v", err)
	}
	if drops != 1 {
		t.Errorf("junk_rule IS NOT NULL matched %d rows, want 1 — a keep stored as '' reads as a drop, and the probe would fetch its body", drops)
	}
	if keeps != 1 {
		t.Errorf("junk_rule IS NULL matched %d rows, want 1", keeps)
	}

	// message_id gets the same treatment for the same reason: the probe's
	// candidate query requires one, and "" is not a message-id.
	var withArticle int
	if err := s.db.DB().QueryRow(
		`SELECT count(*) FROM ` + s.db.Schema() + `.subject_corpus WHERE message_id IS NOT NULL`).Scan(&withArticle); err != nil {
		t.Fatalf("count message_id: %v", err)
	}
	if withArticle != 1 {
		t.Errorf("message_id IS NOT NULL matched %d rows, want 1", withArticle)
	}

	// And the probe's own candidate query must see exactly the drop.
	cands, err := s.junkDropsToProbe(ctx, 10)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("junkDropsToProbe returned %d rows, want 1", len(cands))
	}
	if cands[0].subject != "541279675.bin" || cands[0].messageID != "<drop@example>" {
		t.Errorf("probe picked the wrong row: %+v", cands[0])
	}
}

// The debug list's counts come from the same predicate, so they inherit the
// same failure. Report the drop as unprobed until a body is read, and count a
// recovered name against the rules rather than against a SQL mirror of them.
func TestJunkDropsReportCountsOnlyDrops(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.insertSubjectCorpus(ctx, []corpusRow{
		{group: "a.b.test", subject: "541279675.bin",
			junkRule: "bare_numeric_token", messageID: "<a@example>"},
		{group: "a.b.test", subject: "xKq9x2.part01.rar",
			junkRule: "short_alnum_token", messageID: "<b@example>"},
		{group: "a.b.test", subject: "[Judas] Real Show - 04 [1080p].mkv"},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rep, err := s.junkDropsReport(ctx, 10)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.Sampled != 2 {
		t.Errorf("Sampled = %d, want 2 (the keep must not be counted as a drop)", rep.Sampled)
	}
	if rep.Probed != 0 {
		t.Errorf("Probed = %d, want 0 — nothing has been asked yet", rep.Probed)
	}

	// Now answer one of them with a real filename and one with junk. The split
	// is the finding; if both landed in the same bucket the card would report a
	// settled question either way.
	cands, err := s.junkDropsToProbe(ctx, 10)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	for _, c := range cands {
		name := "Some.Real.Release-GROUP.part03.rar"
		if c.subject == "xKq9x2.part01.rar" {
			name = "9GdEl4K3h8w68mZSgWm8ciq.part0019.rar"
		}
		if err := s.recordJunkProbe(ctx, c.id, name); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rep, err = s.junkDropsReport(ctx, 10)
	if err != nil {
		t.Fatalf("report after probe: %v", err)
	}
	if rep.Probed != 2 {
		t.Errorf("Probed = %d, want 2", rep.Probed)
	}
	if rep.Real != 1 || rep.Junk != 1 {
		t.Errorf("Real/Junk = %d/%d, want 1/1 — the two recovered names must land in different buckets", rep.Real, rep.Junk)
	}

	// A probed row must not come back as a candidate, or the job re-reads the
	// same article every pass and the bytes are spent forever.
	again, err := s.junkDropsToProbe(ctx, 10)
	if err != nil {
		t.Fatalf("candidates after probe: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("%d rows still queued after probing — probed_at is not excluding them", len(again))
	}
}
