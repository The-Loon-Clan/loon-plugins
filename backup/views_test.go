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
		"Acks": []ackRow{{
			Source: "local-array", Generation: 26, AckedAt: now.Add(-3 * time.Hour),
			Packs: 2125, Bytes: 141341013494,
		}},
		"NoAck":  false,
		"AckLag": int64(2),
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
		"Acks": []ackRow{}, "NoAck": true,
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
