package backup

import (
	"database/sql"
	"html/template"
	"strings"
	"testing"
	"time"
)

// Rendering the page is the test that was missing.
//
// The page shipped without one and failed in production with "this page failed
// to render", because `{{num .Packs}}` was handed an int where the helper took
// int64 — html/template refuses the ENTIRE render on an argument type
// mismatch, so one field killed the whole page and the cause was only visible
// in a log. Every field the template touches is exercised here, in both the
// populated and the empty state.
func renderVM(t *testing.T, vm map[string]any) string {
	t.Helper()
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"bytes": fmtBytes2, "num": fmtNum, "ago": fmtAgo,
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "backup.html", vm); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestBackupPageRendersPopulated(t *testing.T) {
	now := time.Now()
	vm := map[string]any{
		"Acks": []ackLine{{
			ackRow: ackRow{Source: "local-array", Generation: 26, AckedAt: now.Add(-3 * time.Hour),
				Packs: 2125, Bytes: 141341013494},
			CompleteUntil: "2026-07-30T22:23:29Z", Pct: 99, Known: true,
			HeldBytes: 141197795750, TotalBytes: 141341013494,
			GensBehind: 2, FilesBehind: 348, BytesBehind: 143217744,
		}},
		"NoAck": false,
		"Generations": []genRow{
			{ID: 28, StartedAt: now.Add(-time.Hour), SealedAt: sql.NullTime{Time: now, Valid: true},
				Files: 424234, Bytes: 141341013494, Hashed: 53435},
			{ID: 27, StartedAt: now.Add(-2 * time.Hour), Files: 0}, // unsealed
			{ID: 26, StartedAt: now.Add(-3 * time.Hour), SealedAt: sql.NullTime{Time: now.Add(-3 * time.Hour), Valid: true},
				Files: 423886, Bytes: 141197795750, Hashed: 53397, Error: "1 class(es) shrank"},
		},
		"Latest": genRow{ID: 28, SealedAt: sql.NullTime{Time: now, Valid: true},
			Files: 424234, Bytes: 141341013494, Hashed: 53435},
		"HasSealed": true,
		// These two are ints, and the template runs them through num/bytes.
		// That mismatch is exactly what broke the page in production.
		"PackCount": 2131,
		"PackBytes": int64(141341013494),
		"PackGen":   int64(28),
		"Classes": []classLine{
			{Class: "site", Files: 14, Bytes: 4410651, PrevKnown: true, Delta: 0, Packs: 1, PackBytes: 4410651},
			{Class: "db-dumps", Files: 0, Bytes: 0, Rotates: true, Empty: true},
			{Class: "screenshots", Files: 311962, Bytes: 126661687375, PrevKnown: true, Delta: -12, Packs: 1901},
			{Class: "covers", Files: 14817, Bytes: 2275217829, Delta: 40, Packs: 34},
		},
		"Suspects": []suspectRow{
			{Path: "web/static/covers/1.jpg", Class: "covers", Reason: "size changed, mtime did not",
				Detail: "was 1024", SeenCount: 3, LastSeen: now.Add(-30 * time.Minute)},
		},
		"Dumps": []dumpLine{
			{Stamp: "20260731T054300Z", Files: 412, Bytes: 21400000000, Tables: 180, Version: "13.23", Newest: true},
		},
		"DumpDir":     "db-dumps",
		"ShrinkPct":   maxClassShrinkPct,
		"RehashDenom": rehashDenominator,
	}
	out := renderVM(t, vm)

	for _, want := range []string{
		"local-array", "2,125", "site", "screenshots", "20260731T054300Z",
		"unsealed", "shrank", "size changed, mtime did not", "2,131",
		// The progress bar and the sentence it exists to make.
		"complete until", "2026-07-30T22:23:29Z", "width:99%", "2 behind",
		"not yet collected",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page is missing %q", want)
		}
	}
	// The empty class must be called out — an unmounted volume and an empty
	// class look identical, and that is the failure the warning exists for.
	if !strings.Contains(out, "unmounted volume") {
		t.Error("an empty class rendered without its warning")
	}
}

// The state a fresh install is in, and the state that matters most: nothing has
// ever been collected. It must render, and it must say so plainly.
func TestBackupPageRendersEmpty(t *testing.T) {
	out := renderVM(t, map[string]any{
		"Acks": []ackLine{}, "NoAck": true,
		"Generations": []genRow{}, "Latest": genRow{}, "HasSealed": false,
		"Classes": []classLine{}, "Suspects": []suspectRow{}, "Dumps": []dumpLine{},
		"DumpDir": "", "ShrinkPct": maxClassShrinkPct, "RehashDenom": rehashDenominator,
	})
	if !strings.Contains(out, "No puller has ever reported") {
		t.Error("the never-backed-up state must say so, loudly")
	}
	if !strings.Contains(out, "not in the backup at all") {
		t.Error("an unconfigured dump directory must be called out")
	}
}

// The helpers accept whatever the view model actually carries. int vs int64 is
// not a distinction worth losing a page over.
func TestFormatHelpersAcceptIntAndInt64(t *testing.T) {
	if got := fmtNum(2131); got != "2,131" {
		t.Errorf("fmtNum(int) = %q", got)
	}
	if got := fmtNum(int64(2131)); got != "2,131" {
		t.Errorf("fmtNum(int64) = %q", got)
	}
	if got := fmtBytes2(1536); got != "1.5 KB" {
		t.Errorf("fmtBytes2(int) = %q", got)
	}
	if got := fmtAgo(time.Time{}); got != "never" {
		t.Errorf("fmtAgo(zero) = %q, want never", got)
	}
}

// The bar's arithmetic. Its whole job is to be believed, so the clamps matter
// more than the happy path: a bar reading 103% is embarrassing, and one reading
// 100% while a third of the corpus is uncollected is dangerous.
func TestAckCoverage(t *testing.T) {
	latest := genRow{ID: 28, Files: 424234, Bytes: 141341013494}
	meta26 := genMeta{SealedAt: "2026-07-30T22:23:29Z", Files: 423886, Bytes: 141197795750}

	t.Run("behind the newest generation", func(t *testing.T) {
		l := ackCoverage(ackRow{Generation: 26, Bytes: 141197795750}, latest, meta26, true)
		if !l.Known || l.CompleteUntil != "2026-07-30T22:23:29Z" {
			t.Errorf("complete-until not carried: %+v", l)
		}
		if l.Pct != 99 {
			t.Errorf("Pct = %d, want 99", l.Pct)
		}
		if l.Current {
			t.Error("Current = true while two generations behind")
		}
		if l.GensBehind != 2 || l.FilesBehind != 348 {
			t.Errorf("behind = %d gens / %d files, want 2 / 348", l.GensBehind, l.FilesBehind)
		}
	})

	t.Run("holding the newest generation reads current", func(t *testing.T) {
		meta := genMeta{SealedAt: "2026-07-31T04:30:36Z", Files: latest.Files, Bytes: latest.Bytes}
		l := ackCoverage(ackRow{Generation: 28, Bytes: latest.Bytes}, latest, meta, true)
		if !l.Current || l.Pct != 100 || l.GensBehind != 0 || l.BytesBehind != 0 {
			t.Errorf("a current target reported %+v", l)
		}
	})

	t.Run("a target ahead of the newest sealed generation caps at 100", func(t *testing.T) {
		// Acked generation 29 while 28 is the newest SEALED one: the index
		// sealed 29 after the ack was written, or is mid-pass.
		meta := genMeta{SealedAt: "2026-07-31T05:00:00Z", Files: 500000, Bytes: latest.Bytes * 2}
		l := ackCoverage(ackRow{Generation: 29}, latest, meta, true)
		if l.Pct != 100 {
			t.Errorf("Pct = %d, want it capped at 100", l.Pct)
		}
		if l.FilesBehind != 0 || l.BytesBehind != 0 {
			t.Errorf("behind went negative: %d files / %d bytes", l.FilesBehind, l.BytesBehind)
		}
	})

	t.Run("an inventory that shrank never reports negative lag", func(t *testing.T) {
		bigger := genMeta{SealedAt: "x", Files: latest.Files + 5000, Bytes: latest.Bytes + 999}
		l := ackCoverage(ackRow{Generation: 27}, latest, bigger, true)
		if l.FilesBehind != 0 || l.BytesBehind != 0 {
			t.Errorf("negative lag leaked through: %+v", l)
		}
	})

	t.Run("an aged-out generation is unknown, not confidently zero", func(t *testing.T) {
		l := ackCoverage(ackRow{Generation: 3, Bytes: 100}, latest, genMeta{}, false)
		if l.Known {
			t.Error("Known = true for a generation with no row")
		}
		if l.CompleteUntil != "" {
			t.Errorf("CompleteUntil = %q, want empty — there is no honest date to show", l.CompleteUntil)
		}
	})
}
