package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// The guard against the worst failure a backup has: a missing bind mount is
// indistinguishable from an empty class. The boot check creates the directory
// when it is absent, the walk finds nothing, the generation seals with zero
// files, and once older generations age out the only copy is gone — with every
// step reporting success.
func TestShrinkGateCatchesACollapsedClass(t *testing.T) {
	prev := map[string]classTotal{
		"mascots":     {Files: 40, Bytes: 4 << 20},
		"covers":      {Files: 16_800, Bytes: 2 << 30},
		"screenshots": {Files: 253_512, Bytes: 117 << 30},
	}

	// The exact scenario: one class vanishes entirely.
	got := detectShrink(prev, map[string]classTotal{
		"mascots":     {},
		"covers":      {Files: 16_800, Bytes: 2 << 30},
		"screenshots": {Files: 253_512, Bytes: 117 << 30},
	}, maxClassShrinkPct)
	if len(got) != 1 || got[0].Class != "mascots" {
		t.Fatalf("a class going 40 files -> 0 must be reported, got %+v", got)
	}
	if got[0].PctDropped != 100 {
		t.Errorf("PctDropped = %v, want 100", got[0].PctDropped)
	}

	// Ordinary churn must NOT trip it, or the gate gets disabled by whoever is
	// on call the third time it cries wolf.
	if got := detectShrink(prev, map[string]classTotal{
		"mascots":     {Files: 39, Bytes: 4 << 20}, // one deleted
		"covers":      {Files: 16_795, Bytes: 2 << 30},
		"screenshots": {Files: 253_500, Bytes: 117 << 30},
	}, maxClassShrinkPct); len(got) != 0 {
		t.Errorf("normal deletions tripped the gate: %+v", got)
	}

	// Growth is never a shrink.
	if got := detectShrink(prev, map[string]classTotal{
		"mascots":     {Files: 41},
		"covers":      {Files: 17_000},
		"screenshots": {Files: 260_000},
	}, maxClassShrinkPct); len(got) != 0 {
		t.Errorf("growth reported as shrink: %+v", got)
	}
}

// A brand-new class has no previous total, and must not be treated as having
// collapsed from nothing — the first run would otherwise never seal.
func TestShrinkGateIgnoresNewAndEmptyClasses(t *testing.T) {
	if got := detectShrink(nil, map[string]classTotal{"covers": {Files: 10}}, maxClassShrinkPct); len(got) != 0 {
		t.Errorf("first run reported a shrink: %+v", got)
	}
	prev := map[string]classTotal{"mascots": {Files: 0}}
	if got := detectShrink(prev, map[string]classTotal{"mascots": {Files: 0}}, maxClassShrinkPct); len(got) != 0 {
		t.Errorf("an empty class that stayed empty reported a shrink: %+v", got)
	}
}

// Ordering decides what an interrupted pass managed to protect. Screenshots are
// 117 GB of 131 GB, so a pass that leads with them and is then killed by a
// deploy has indexed nothing else — including the 30 MB that cannot be
// re-fetched from anywhere.
func TestCheapIrreplaceableClassesAreIndexedFirst(t *testing.T) {
	got := orderedClasses([]AssetClass{
		{Slug: "screenshots", Order: 90},
		{Slug: "covers", Order: 50},
		{Slug: "mascots", Order: 10},
		{Slug: "site", Order: 10},
	})
	if got[len(got)-1].Slug != "screenshots" {
		t.Errorf("screenshots must be indexed last, got order %v", slugs(got))
	}
	if got[0].Order != 10 {
		t.Errorf("the cheapest tier must be first, got %v", slugs(got))
	}
	// Equal orders keep a stable, predictable sequence.
	if got[0].Slug != "mascots" || got[1].Slug != "site" {
		t.Errorf("equal-order classes must sort by slug, got %v", slugs(got))
	}
}

// The stat gate decides what gets re-read, so anything it treats as "unchanged"
// is content the backup will carry forward without looking at.
func TestStatGateNoticesEveryKindOfChange(t *testing.T) {
	base := fileRow{Size: 1000, MtimeNS: 5_000, CtimeNS: 6_000, Inode: 42}
	for _, tc := range []struct {
		name string
		row  fileRow
	}{
		{"size changed", fileRow{Size: 1001, MtimeNS: 5_000, CtimeNS: 6_000, Inode: 42}},
		{"mtime changed", fileRow{Size: 1000, MtimeNS: 5_001, CtimeNS: 6_000, Inode: 42}},
		// The clock-skew case: an NTP step back plus a same-size rewrite can
		// repeat an mtime. ctime cannot be moved backward from userspace.
		{"ctime changed, mtime repeated", fileRow{Size: 1000, MtimeNS: 5_000, CtimeNS: 6_001, Inode: 42}},
		// Replace-by-rename can preserve size and mtime but never the inode.
		{"inode changed", fileRow{Size: 1000, MtimeNS: 5_000, CtimeNS: 6_000, Inode: 43}},
	} {
		if tc.row.statKey() == base.statKey() {
			t.Errorf("%s: the stat gate saw no change, so the file would never be re-hashed", tc.name)
		}
	}
	same := fileRow{Size: 1000, MtimeNS: 5_000, CtimeNS: 6_000, Inode: 42}
	if same.statKey() != base.statKey() {
		t.Error("an unchanged file must not be re-hashed every run — that is the whole cost saving")
	}
}

// The rolling re-hash is the only thing that catches bit-rot and a torn write
// whose mtime never moved again. It must cover everything, deterministically,
// rather than sampling and hoping.
func TestRollingRehashCoversEverything(t *testing.T) {
	const denom = 8
	paths := make([]string, 500)
	for i := range paths {
		paths[i] = filepath.ToSlash(filepath.Join("web/static/covers", string(rune('a'+i%26))+itoa(i)+".jpg"))
	}

	seen := map[string]bool{}
	for gen := int64(0); gen < denom; gen++ {
		for _, p := range paths {
			if rehashDue(p, gen, denom) {
				seen[p] = true
			}
		}
	}
	if len(seen) != len(paths) {
		t.Errorf("%d of %d files would never be re-verified in a full cycle", len(paths)-len(seen), len(paths))
	}

	// Deterministic: the same path and generation always agree, or coverage is
	// a coincidence rather than a guarantee.
	for _, p := range paths[:20] {
		if rehashDue(p, 3, denom) != rehashDue(p, 3, denom) {
			t.Fatalf("rehashDue is not deterministic for %s", p)
		}
	}

	// And it must not re-hash everything every run, or the cost saving is gone.
	due := 0
	for _, p := range paths {
		if rehashDue(p, 0, denom) {
			due++
		}
	}
	if due == 0 || due > len(paths)/2 {
		t.Errorf("%d of %d due in one generation; want roughly a %d-th", due, len(paths), denom)
	}
}

// A truncated image must be flagged. Five writers used to create files at their
// final path, so torn files predate the fix and are still on disk — and once
// written, their mtime never changes again, so nothing else will ever notice.
func TestTruncatedImagesAreDetected(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.jpg")
	if err := os.WriteFile(good, append([]byte{0xFF, 0xD8, 0xFF}, 0xFF, 0xD9), 0o644); err != nil {
		t.Fatal(err)
	}
	torn := filepath.Join(dir, "torn.jpg")
	if err := os.WriteFile(torn, []byte{0xFF, 0xD8, 0xFF, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path     string
		wantTorn bool
	}{
		{good, false},
		{torn, true},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		sum, gotTorn, err := hashFile(tc.path, info.Size())
		if err != nil {
			t.Fatal(err)
		}
		if sum == "" {
			t.Errorf("%s: no hash returned", tc.path)
		}
		if gotTorn != tc.wantTorn {
			t.Errorf("%s: truncated=%v, want %v", filepath.Base(tc.path), gotTorn, tc.wantTorn)
		}
	}

	// An unknown format must never be flagged: being unable to check is not
	// evidence of damage, and a false positive here means a real file gets
	// reported as suspect forever.
	webp := filepath.Join(dir, "shot.webp")
	if err := os.WriteFile(webp, []byte("RIFF....WEBPVP8 whatever"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(webp)
	if _, torn, err := hashFile(webp, info.Size()); err != nil || torn {
		t.Errorf("unknown format flagged as truncated (torn=%v, err=%v)", torn, err)
	}
}

func slugs(cs []AssetClass) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Slug
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
