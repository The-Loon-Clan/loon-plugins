package usenet

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline differential probe for a candidate CAMPAIGN rule, run against real
// production data pulled read-only:
//
//	flood.txt         base_subjects of live flood sets (evicted, held=1, span=0)
//	real_titles.txt   indexed titles, status=completed AND anime_id IS NOT NULL
//	real_all.txt      indexed titles, status=completed (any category)
//	elys_indexed.txt  EVERY title in public.nzbs containing "elys-" (full-table
//	                  trigram scan, not a sample) — the highest-risk FP set
//
// A junk rule is a DELETE (TagJunkTitlesBatch -> JunkCleanupBatch, and the
// crawler never re-offers a subject it drops at ingest), so the number that
// decides this is the false-positive count, not the catch rate.
const elysCampaignRule = `^elys-[0-9a-f]{16} - \[[A-Za-z0-9]{8}\] [A-Za-z0-9]{16}$`

// The corpora are pulled from production read-only and are NOT committed —
// they are real member-facing titles. Point JUNK_PROBE_CORPUS at a directory
// holding them to run this; without it every case skips, so CI stays green and
// the probe stays re-runnable whenever a rule is proposed or tuned.
func probeDir() string { return os.Getenv("JUNK_PROBE_CORPUS") }

func loadProbeCorpus(t *testing.T, name string) []string {
	t.Helper()
	dir := probeDir()
	if dir == "" {
		t.Skip("set JUNK_PROBE_CORPUS to a directory of production corpora to run this probe")
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Skipf("corpus %s unavailable: %v", name, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", name, err)
	}
	return out
}

// withElysRule installs the shipped rule set PLUS the candidate, using the real
// compile path (newJunkMatcher + setJunkMatcher), so the probe exercises
// production normalisation, the literal prefilter and rule ordering — not a
// bare regexp.MatchString that would miss all three.
func withElysRule(t *testing.T, first bool) func() {
	t.Helper()
	specs, err := embeddedJunkRules()
	if err != nil {
		t.Fatalf("embeddedJunkRules: %v", err)
	}
	cand := junkRuleSpec{
		Name: "elys_campaign_tag", Kind: "regex", Rule: elysCampaignRule, Enabled: true,
	}
	// Strip any shipped copy first, so this stays a single-rule experiment
	// whether or not the rule has already landed in the seed TSV.
	kept := make([]junkRuleSpec, 0, len(specs))
	for _, s := range specs {
		if s.Name != cand.Name {
			kept = append(kept, s)
		}
	}
	if first {
		specs = append([]junkRuleSpec{cand}, kept...)
	} else {
		specs = append(kept, cand)
	}
	m, err := newJunkMatcher(specs)
	if err != nil {
		t.Fatalf("newJunkMatcher: %v", err)
	}
	prev := activeJunk.Load()
	setJunkMatcher(m)
	return func() { setJunkMatcher(prev) }
}

// withoutElysRule installs the shipped rule set MINUS the campaign rule, so the
// baseline below keeps measuring the pre-change world after the rule ships.
func withoutElysRule(t *testing.T) func() {
	t.Helper()
	specs, err := embeddedJunkRules()
	if err != nil {
		t.Fatalf("embeddedJunkRules: %v", err)
	}
	kept := make([]junkRuleSpec, 0, len(specs))
	for _, s := range specs {
		if s.Name != "elys_campaign_tag" {
			kept = append(kept, s)
		}
	}
	m, err := newJunkMatcher(kept)
	if err != nil {
		t.Fatalf("newJunkMatcher: %v", err)
	}
	prev := activeJunk.Load()
	setJunkMatcher(m)
	return func() { setJunkMatcher(prev) }
}

// TestElysCampaign_BaselineNothingCatchesIt establishes that NO OTHER rule
// catches this flood — so the campaign rule's catch is genuinely new work and
// not a reattribution of drops that were already happening.
func TestElysCampaign_BaselineNothingCatchesIt(t *testing.T) {
	defer withoutElysRule(t)()
	flood := loadProbeCorpus(t, "flood.txt")
	var elys, caught int
	byRule := map[string]int{}
	for _, s := range flood {
		if !strings.HasPrefix(s, "elys-") {
			continue
		}
		elys++
		if r := whichJunkRule(s); r != "" {
			caught++
			byRule[r]++
		}
	}
	t.Logf("BASELINE: flood=%d elys=%d already-caught=%d %v", len(flood), elys, caught, byRule)
	if caught != 0 {
		t.Errorf("expected the elys flood to pass the current engine entirely, %d were caught", caught)
	}
}

// TestElysCampaign_CatchRate measures the half that is cheap to be wrong about.
func TestElysCampaign_CatchRate(t *testing.T) {
	defer withElysRule(t, true)()
	flood := loadProbeCorpus(t, "flood.txt")
	var total, caught, elysTotal, elysCaught int
	for _, s := range flood {
		total++
		hit := whichJunkRule(s) == "elys_campaign_tag"
		if hit {
			caught++
		}
		if strings.HasPrefix(s, "elys-") {
			elysTotal++
			if hit {
				elysCaught++
			}
		}
	}
	t.Logf("CATCH: %d/%d of ALL sampled flood = %.1f%%", caught, total, 100*float64(caught)/float64(total))
	t.Logf("CATCH: %d/%d of the elys-prefixed flood = %.2f%%", elysCaught, elysTotal,
		100*float64(elysCaught)/float64(elysTotal))
	if elysCaught != elysTotal {
		for _, s := range flood {
			if strings.HasPrefix(s, "elys-") && whichJunkRule(s) != "elys_campaign_tag" {
				t.Errorf("MISSED elys flood subject: %q", s)
				break
			}
		}
	}
}

// TestElysCampaign_NoFalsePositives is the one that decides it. Every corpus is
// real indexed data; a single hit here is a real release permanently deleted.
func TestElysCampaign_NoFalsePositives(t *testing.T) {
	defer withElysRule(t, true)()
	for _, name := range []string{"real_titles.txt", "real_all.txt", "elys_indexed.txt"} {
		corpus := loadProbeCorpus(t, name)
		var fp int
		for _, title := range corpus {
			if whichJunkRule(title) == "elys_campaign_tag" {
				fp++
				if fp <= 5 {
					t.Errorf("FALSE POSITIVE in %s: %q would be DELETED", name, title)
				}
			}
		}
		t.Logf("FP: %s — %d titles, %d matched the campaign rule", name, len(corpus), fp)
	}
}

// TestElysCampaign_NoCollateralChange proves the new rule changes NO other
// verdict: every title the engine judged before must get the same answer after,
// unless the campaign rule itself is the one firing.
func TestElysCampaign_NoCollateralChange(t *testing.T) {
	var corpora []string
	for _, name := range []string{"real_titles.txt", "real_all.txt", "elys_indexed.txt", "flood.txt"} {
		corpora = append(corpora, loadProbeCorpus(t, name)...)
	}
	restore := withoutElysRule(t)
	before := make([]string, len(corpora))
	for i, s := range corpora {
		before[i] = whichJunkRule(s)
	}
	restore()
	defer withElysRule(t, true)()
	var changed, newlyCaught int
	for i, s := range corpora {
		after := whichJunkRule(s)
		if after == before[i] {
			continue
		}
		if before[i] == "" && after == "elys_campaign_tag" {
			newlyCaught++
			continue
		}
		changed++
		if changed <= 5 {
			t.Errorf("VERDICT CHANGED for %q: %q -> %q", s, before[i], after)
		}
	}
	t.Logf("COLLATERAL: %d titles, %d newly caught by the campaign rule, %d other verdicts changed",
		len(corpora), newlyCaught, changed)
}

// TestElysCampaign_ShapeBoundaries pins the exact discriminator by hand. The
// BARE form is the one that grouped correctly and produced 1,205 indexed
// multi-GB releases; the BRACKETED form is the one that can never group.
func TestElysCampaign_ShapeBoundaries(t *testing.T) {
	defer withElysRule(t, true)()
	mustCatch := []string{
		"elys-f6623bd71b85ed5e - [GfacQ4XT] 18sssNEGZUpepNAd",
		"elys-52e84036b4adcac5 - [Aazz4yKT] MpD8dBMHoorvDBFx",
		"elys-3acdaa6be45137bf - [0Dgro5Pz] g016LmoxN52SuXCE",
		"elys-1907283e54a1cae9 - [KXPSbTzl] qoS5w5nwO0OvHnwj",
		// The $-anchor sees the title AFTER stripReleaseExts, so the campaign's
		// archive/media-suffixed variants are covered by the same pattern. This
		// widens nothing: the head still has to be the full campaign shape.
		"elys-f6623bd71b85ed5e - [GfacQ4XT] 18sssNEGZUpepNAd.mkv",
		"elys-f6623bd71b85ed5e - [GfacQ4XT] 18sssNEGZUpepNAd.part01.rar",
		"elys-f6623bd71b85ed5e - [GfacQ4XT] 18sssNEGZUpepNAd.vol03+04.par2",
	}
	for _, s := range mustCatch {
		if got := whichJunkRule(s); got != "elys_campaign_tag" {
			t.Errorf("whichJunkRule(%q) = %q, want elys_campaign_tag", s, got)
		}
	}
	// Must NOT fire. The bare form is 1,205 real indexed releases; the rest are
	// near-misses that prove the rule is not reaching past its own shape.
	mustSurvive := []string{
		"elys-eaa738c0cd6b0bde",                                // indexed, 2.1 GB
		"elys-f21c4eff3dfdbc43",                                // indexed, 1.7 GB
		"elys-8146f22e8f74b8d8",                                // indexed, 7.2 GB
		"Elysium.2013.1080p.BluRay.x264-SPARKS",                // real word
		"Elysia.no.Uta.S01E03.1080p.WEB-DL.AAC2.0.H.264-VARYG", // real word
		"elys-f6623bd71b85ed5e - [GfacQ4XT]",                   // truncated
		"elys-f6623bd71b85ed5e [GfacQ4XT] 18sssNEGZUpepNAd",    // no " - " separator
		"prefix elys-f6623bd71b85ed5e - [GfacQ4XT] 18sssNEGZUpepNAd",
		"elys-f6623bd71b85ed5eAA - [GfacQ4XT] 18sssNEGZUpepNAd", // 18-char id
		"elys-zzzzzzzzzzzzzzzz - [GfacQ4XT] 18sssNEGZUpepNAd",   // non-hex id
		// The head anchor is load-bearing, and this is the proof. A full scan of
		// all 833,409 public.nzbs rows for the TAIL alone —
		// ` - \[[A-Za-z0-9]{8}\] [A-Za-z0-9]{16}$` — returns exactly one row, and
		// it is a real 621 MB anime release: "Memesubs" is 8 alphanumerics and
		// "Koyomimonogatari" is 16, so the campaign's tail shape occurs verbatim
		// in an ordinary fansub naming convention. Anchoring on the tail alone
		// would have deleted it. (The head alone is equally unsafe — it is the
		// 1,205 bare-form releases above — so BOTH halves earn their place.)
		"[Memesubs] Koyomimonogatari - 08 - [Memesubs] Koyomimonogatari",
	}
	for _, s := range mustSurvive {
		if got := whichJunkRule(s); got == "elys_campaign_tag" {
			t.Errorf("OVERREACH: %q matched the campaign rule", s)
		}
	}
}
