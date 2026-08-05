package usenet

import "testing"

// Does the junk engine already recognise the obfuscated posts that dominate
// the index? Run against titles taken verbatim from production, where 39,404
// of the newest 50,000 releases look like this and NONE are categorised junk.
//
// It began as a diagnostic and answered its own question: 0 of 15 caught,
// because short_alnum_token's require_digit gate demands a digit AND a
// letter, which spares pure-letter titles by design and pure-DIGIT ones by
// accident. bare_numeric_token closes that gap; this is now the assertion
// that keeps it closed.
func TestObfuscatedProductionTitles(t *testing.T) {
	// Verbatim from prod, newest-first, anime_id IS NULL.
	titles := []string{
		"541279675.bin",
		"792022149.bin",
		"697427797.bin",
		"802908147.bin",
		"599979121.bin",
		"3710578.bin",
		"901139269.bin",
		"725029818.bin",
		"443386672.bin",
		"859945603.bin",
		"949991921.bin",
		"616921374.bin",
		"82020282-n",
		"82021431-n",
		"82047270-n",
	}
	// Real releases that must NEVER match, so a rule tuned for the above
	// cannot quietly start eating content.
	real := []string{
		"When.Life.Gives.You.Tangerines.S01E04.1080p.NF.WEB-DL",
		"[SubsPlease] Sousou no Frieren - 28 (1080p) [F4A9C21B]",
		"Attack.on.Titan.S04E28.1080p.WEB.H264-SenpaiSubs",
		"Chobits.2002.Complete.BDRip.1080p.x265-GROUP",
		// The 6-digit floor's reason for existing: short numeric titles are
		// real. "86" is an anime; a bare year is a legitimate token.
		"86",
		"86.S01E01.1080p.WEB-DL",
		"2024",
		"1917.2019.1080p.BluRay.x264",
		"12345", // five digits: under the floor, deliberately safe
	}

	for _, title := range titles {
		rule := whichJunkRule(title)
		if rule == "" {
			t.Errorf("obfuscated title %q matched NO junk rule — it would be indexed and be unfindable forever", title)
			continue
		}
		t.Logf("caught %-18s by %q", title, rule)
	}

	for _, title := range real {
		if rule := whichJunkRule(title); rule != "" {
			t.Errorf("FALSE POSITIVE: real release %q matched junk rule %q", title, rule)
		}
	}
}
