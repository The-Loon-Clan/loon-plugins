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
	} {
		if whichJunkRule(title) == "" {
			t.Errorf("punctuation soup passed the filter: %s", title)
		}
	}
}

// Real releases from the production corpus that the rule used to junk. The
// full-width brackets and separators here are ordinary structure in their own
// script, and a run of exclamation marks is a title style, not garble —
// "Keijo!!!!!!!!" / "競女!!!!!!!!" is a real anime, which is why a repeated-mark
// vector cannot be used as a soup fixture.
func TestCataloguedReleasesSurviveTheFilter(t *testing.T) {
	for _, title := range []string{
		`[Audio > Lossless] 【ASMR】雙生蘿莉魅魔♪～在您耳畔舔舐、嬌喘、呢喃、絕頂、高潮瘋狂～[WAV/MP3]`,
		`【さめラジ！】第1回 Same RADIO│MC：花澤香菜（子ザメちゃん役）ゲスト：潘めぐみ（あんこうちゃん役）`,
		`[HorchataScans] Keijo!!!!!!!! - Las Musas de la Calipigia - 01 [競女!!!!!!!!] [Sub Español]`,
		// The {Tags:...} metadata block several groups append. Almost entirely
		// ';' '=' ',' — structure, not garble.
		`[LbE3L] BLACK TORCH S01E01–E02 [1080p CR WEBRip AV1 Opus 2.0 Multi-Audio MSubs] ` +
			`{Tags:L0;V7;C3;A=ja,en,ar,de,es419,eses,frfr,it,pl,ptbr;S=en,ar,zhhans,zhhant,frfr,de,id,it,ms,pl,ptbr,ru,eses,es419,th,vi;}`,
	} {
		if rule := whichJunkRule(title); rule != "" {
			t.Errorf("junked a catalogued release as %q:\n  %s", rule, title)
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
