package usenet

import "testing"

// The title an operator watched reach the site on 2026-08-07. 111 characters,
// 18 spam-grade punctuation marks, not one whitespace — and it matched NO rule
// at all above 5 MiB.
//
// Two near-misses let it through, which is why the fix is a new rule rather
// than a tuned threshold:
//
//   - long_no_space wants exactly this shape (60+ chars, no whitespace, 5+
//     marks) but carries max_size_bytes 2 MiB, so a big post is exempt. It is
//     also size-gated, meaning it never runs on the unsized ingest path.
//   - high_special_chars wants 15% punctuation. This title scores 14.4%.
//
// Neither threshold was moved. Both were measured against real catalogue data
// and tuned to spare real releases (see the {Tags:...} note in
// highSpecialChars, 71 of 96 false positives), so widening either to reach one
// title would risk the thing that is invisible when it goes wrong.
const operatorJunkTitle = "u9M?k*w&IS{oCoWsPK*swu!t^j%BYO>u=-231XibwuQON2hBO2Q(*UD8LJN8_[g33Bw0av*O[)ck)X?<rjAU8q_lsG}VgRv}2%QFQ6_-Kqae>L3"

func TestGarbledNoSpace_CatchesTheTitleThatGotThrough(t *testing.T) {
	// Every size, including 0 — the ingest path, where the size is not yet
	// known and every size-gated rule is skipped. That gap is half the reason
	// this rule is unsized.
	for _, size := range []int64{0, 5 << 20, 50 << 20, 900 << 20, 8 << 30} {
		if rule := whichJunkRuleSized(operatorJunkTitle, size); rule == "" {
			t.Errorf("size=%d: still matches no rule", size)
		}
	}
	if rule := whichJunkRuleSized(operatorJunkTitle, 900<<20); rule != "garbled_no_space" {
		t.Errorf("at 900 MiB the rule is %q, want garbled_no_space", rule)
	}
}

// Order check. long_no_space must keep winning inside its own band, so every
// hit counter that existed before this rule keeps reporting the same name and
// the change is strictly additive.
func TestGarbledNoSpace_DoesNotStealFromTheRulesBeforeIt(t *testing.T) {
	// Under ~790 KB, tiny_no_space owns any no-whitespace title.
	if rule := whichJunkRuleSized(operatorJunkTitle, 500<<10); rule != "tiny_no_space" {
		t.Errorf("at 500 KiB the rule is %q, want tiny_no_space — the new rule jumped the queue", rule)
	}
	// A long no-space title with 5-7 marks stays long_no_space in its band:
	// below the new rule's bar of 8, and ahead of it in the order anyway.
	mid := "a?b&c%d=e" + "0123456789012345678901234567890123456789012345678901234567890"
	if got := countGarbledPunct(mid); got < 4 || got >= 8 {
		t.Fatalf("fixture carries %d marks; it must sit in the 4-7 gap to test the boundary", got)
	}
	if rule := whichJunkRuleSized(mid, 1<<20); rule == "garbled_no_space" {
		t.Error("a 5-mark title inside long_no_space's band was claimed by garbled_no_space")
	}
}

// The bar is 8 marks, and below it this rule must do nothing. Verified against
// the live catalogue before the rule shipped: of 200 titles with 60+ chars and
// no whitespace, zero carried 8+ marks and zero carried even 4 — so the bar
// sits well clear of anything real, with the 4-7 band as headroom.
func TestGarbledNoSpace_LeavesRealLongTitlesAlone(t *testing.T) {
	survivors := []string{
		// Dot-and-dash separated names score ZERO: '.' and '-' are not
		// spam-grade punctuation. This is the shape long_no_space's own comment
		// worries about (Beyonce.-.I.Am...), and it is safe at any size.
		"Sousou.no.Frieren.S01E12.1080p.WEB-DL.DDP2.0.H.264-VARYG.Multi.Subs.Complete",
		"Mushoku.Tensei.Isekai.Ittara.Honki.Dasu.S02E12.1080p.CR.WEB-DL.AAC2.0.H.264",
		"The.Legend.of.Heroes.Sen.no.Kiseki.Northern.War.S01.1080p.BluRay.x265-DSNP",
		// Underscored no-space titles: '_' is not spam-grade either.
		"Kidou_Senshi_Gundam_Suisei_no_Majo_S02E12_1080p_WEB-DL_AAC_H264_Multi_Sub",
	}
	for _, title := range survivors {
		if len(title) < 60 {
			t.Fatalf("fixture %q is under 60 chars, so it never reaches this rule", title)
		}
		if !containsNoWhitespace(title) {
			t.Fatalf("fixture %q has whitespace, so it never reaches this rule", title)
		}
		for _, size := range []int64{0, 50 << 20, 4 << 30} {
			if rule := whichJunkRuleSized(title, size); rule == "garbled_no_space" {
				t.Errorf("REAL RELEASE DROPPED at size=%d: %q", size, title)
			}
		}
	}
}

func containsNoWhitespace(s string) bool {
	for _, c := range s {
		switch c {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			return false
		}
	}
	return true
}
