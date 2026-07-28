package usenet

import "testing"

// high_special_chars means "the title is mostly spam-grade punctuation". It
// decided that with an ASCII-only alnum test, so every CJK ideograph, kana,
// Hangul syllable and Cyrillic letter counted as punctuation and a native-script
// title scored ~100% special. On an anime indexer that is not an edge case: it
// dropped the Japanese-titled releases at ingest, before staging, and the only
// evidence was a junk counter climbing — which reads as the rule working.
func TestNativeScriptTitlesAreNotPunctuation(t *testing.T) {
	for _, title := range []string{
		"FLsnow 名探偵プリキュア！/Star Detective Precure！ 1080p 26 CHS/CHT&JP", // the production sample
		"名探偵プリキュア 01 1080p",
		"進撃の巨人 The Final Season - 29 [1080p]",
		"한국어 애니메이션 - 01 1080p",
		"Аниме Атака Титанов - S04E29 1080p",
		"百日成王 Bai Ri Cheng Wang - 11 (1080p)",
	} {
		if rule := whichJunkRule(title); rule != "" {
			t.Errorf("junked a native-script release as %q: %s", rule, title)
		}
	}
}

// The other half of the fix: loosening the rule must not neuter it. These are
// the shapes it exists for.
func TestPunctuationSoupIsStillJunk(t *testing.T) {
	for _, title := range []string{
		`~!@#$%^&*()_+~!@#$%^&*(){}|:"<>?`,
		`!!!!!!!!!!!!!!!!!!!!`,
		`$&!@#=$%^&*=!@`, // the parity-suite vector
		// Native script is no defence when the title really is mostly marks.
		`名探偵プリキュア！！！！！！！！！！`,
	} {
		if whichJunkRule(title) == "" {
			t.Errorf("punctuation soup passed the filter: %s", title)
		}
	}
}

// The ratio counts runes on both sides. It used to divide a rune count by a
// BYTE length, so the verdict depended on how the title was encoded rather than
// on what it said — a multi-byte script got a third of the divisor it should
// have had. Two titles with the same structure must be judged the same way.
func TestVerdictDoesNotDependOnEncoding(t *testing.T) {
	// Ten letters, four punctuation marks: identical shape, different scripts.
	const latin = `abcdefghij!@#$`
	const cjk = `名探偵推理事件簿記録!@#$`

	if got, want := whichJunkRule(cjk), whichJunkRule(latin); got != want {
		t.Errorf("same shape, different verdict by script: latin=%q cjk=%q\n"+
			"  the ratio is being computed against byte length, not rune count",
			want, got)
	}
	if whichJunkRule(latin) == "" {
		t.Fatal("fixture is wrong: the latin form should be junk for this to mean anything")
	}
}
