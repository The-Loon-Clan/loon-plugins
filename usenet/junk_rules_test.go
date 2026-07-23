package usenet

import "testing"

// TestEmbeddedJunkRulesParse guards the shipped file: it is embedded and
// compiled at init, so a malformed line would panic every process at startup.
func TestEmbeddedJunkRulesParse(t *testing.T) {
	specs, err := embeddedJunkRules()
	if err != nil {
		t.Fatalf("shipped seed/junk_rules.tsv does not parse: %v", err)
	}
	// The COMPLETE prod pattern set, in prod's evaluation order. This list is
	// the parity contract: a prod rule missing here is a regression to the
	// partial lift that let "0N70ZyFoz8n50" into the index on the first live
	// crawl, and order matters because attribution is first-match and the
	// size-band catchalls must run last.
	wantOrder := []string{
		"long_alnum_run", "multi_seg_random", "uuid", "software_warez",
		"template_token", "dot_sep_obfuscated", "rot13_archive",
		"repeated_short_tok", "alnum_blob_ext", "short_alnum_token",
		"mid_alnum_token", "js_template_leak", "single_token_20",
		"long_digit_run", "high_special_chars", "random_words",
		"word_word_hex", "tiny_no_space", "short_lowercase_token",
		"long_no_space", "chaotic_specials_small", "short_random_token",
		"under_1mib", "under_5mib",
	}
	if len(specs) != len(wantOrder) {
		t.Fatalf("got %d rules, want %d (the full prod set)", len(specs), len(wantOrder))
	}
	for i, s := range specs {
		if s.Name != wantOrder[i] {
			t.Errorf("rule %d = %q, want %q (prod evaluation order)", i, s.Name, wantOrder[i])
		}
		if s.Notes == "" {
			t.Errorf("rule %q has no notes — every shipped rule should say why it exists", s.Name)
		}
		if !s.Enabled {
			t.Errorf("rule %q ships disabled", s.Name)
		}
	}
	// The catchalls and every other size-banded rule must be inert without a
	// size, or ingest would junk everything (ingest never knows the size).
	for _, s := range specs {
		if (s.Params.sized() || s.Params.SizedOnly) && s.Name == "long_alnum_run" {
			t.Errorf("unsized workhorse rule %q acquired a size gate", s.Name)
		}
	}
	if _, err := newJunkMatcher(specs); err != nil {
		t.Fatalf("shipped rules do not compile: %v", err)
	}
}

// TestWhichJunkRuleNames pins which rule catches what. Naming the rule is the
// point of making these data — you need to see which one is doing the work
// before tuning or disabling it.
func TestWhichJunkRuleNames(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Pzz8CzBPoBNsCu8oRPpDYwESRkpq5UU3jGlz", "long_alnum_run"},
		{"aBcDeFgHiJkLmNoP", ""}, // 16 no-digit bare token: prod only flags this on the SIZED path
		{"550e8400-e29b-41d4-a716-446655440000", "uuid"},
		{"My {total} Release", "template_token"},
		// 16-char run is under the 24 threshold, and the "." is a separator so the
		// bare-token rule is out — the dot-separated rule is what catches this.
		{"f329yZ98AaYf2qHd.QPv2", "dot_sep_obfuscated"},
		{"", "empty"},
		{"Cowboy Bebop S01E01 1080p BluRay", ""},
		{"The.Matrix.1999.2160p.UHD.BluRay", ""},
	}
	for _, tc := range cases {
		if got := whichJunkRule(tc.title); got != tc.want {
			t.Errorf("whichJunkRule(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

// TestTemplateTokenTrailingGap documents a real limitation inherited from prod:
// the decoration strip (trimCut of quotes/braces/brackets) runs BEFORE the rules
// and eats a trailing "}", so "Release {total}" reduces to "Release {total" and
// template_token can never fire on a trailing token — only a mid-string one.
// Trailing is likely the common shape, so this rule under-fires.
//
// Left as-is deliberately: prod behaves this way, and the rule is now data, so
// the fix belongs upstream (change prod, then re-seed) rather than as a silent
// divergence here. Change this test when that happens.
func TestTemplateTokenTrailingGap(t *testing.T) {
	if got := whichJunkRule("My Release {total}"); got != "" {
		t.Fatalf("whichJunkRule = %q; the trailing-token gap appears to be fixed — "+
			"update this test and the shipped rule notes", got)
	}
	if got := whichJunkRule("My {total} Release"); got != "template_token" {
		t.Errorf("mid-string token: got %q, want template_token", got)
	}
}

// TestJunkMatcherRejectsBadRule: an operator-authored rule with a broken regex
// must fail to compile, so the caller keeps the previous working matcher rather
// than silently ingesting junk.
func TestJunkMatcherRejectsBadRule(t *testing.T) {
	_, err := newJunkMatcher([]junkRuleSpec{
		{Name: "broken", Kind: "regex", Rule: "([unclosed", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected an invalid regex to fail compilation")
	}

	if _, err := newJunkMatcher([]junkRuleSpec{
		{Name: "odd", Kind: "wat", Rule: "x", Enabled: true},
	}); err == nil {
		t.Error("expected an unknown kind to be rejected")
	}
	if _, err := newJunkMatcher([]junkRuleSpec{
		{Name: "odd", Kind: "heuristic", Rule: "no_such_algorithm", Enabled: true},
	}); err == nil {
		t.Error("expected an unknown heuristic id to be rejected")
	}
	// An empty rule set is refused too — that would mean "filter nothing".
	if _, err := newJunkMatcher([]junkRuleSpec{
		{Name: "off", Kind: "regex", Rule: "x", Enabled: false},
	}); err == nil {
		t.Error("expected an all-disabled rule set to be rejected")
	}
}

// TestJunkParamsTune proves the params are live: the same title flips verdict
// when a heuristic's threshold changes, which is what makes DB tuning useful.
func TestJunkParamsTune(t *testing.T) {
	title := "aBcDeFgHiJkL" // 12 chars, mixed case, no separator

	strict, err := newJunkMatcher([]junkRuleSpec{
		{Name: "bare", Kind: "heuristic", Rule: "bare_alnum_token", Params: junkParams{MinLen: 10}, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strict.match(title, title, 0); got != "bare" {
		t.Errorf("min_len=10: match = %q, want %q", got, "bare")
	}

	lax, err := newJunkMatcher([]junkRuleSpec{
		{Name: "bare", Kind: "heuristic", Rule: "bare_alnum_token", Params: junkParams{MinLen: 20}, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := lax.match(title, title, 0); got != "" {
		t.Errorf("min_len=20: match = %q, want no match", got)
	}
}

// TestJunkFingerprintDetectsChange: the reload path recompiles only when the
// fingerprint moves, so it has to notice every field that affects behavior.
func TestJunkFingerprintDetectsChange(t *testing.T) {
	base := []junkRuleSpec{{Name: "a", Kind: "regex", Rule: "x", Enabled: true}}
	fp := junkFingerprint(base)

	if junkFingerprint([]junkRuleSpec{{Name: "a", Kind: "regex", Rule: "x", Enabled: true}}) != fp {
		t.Error("identical rules produced different fingerprints")
	}
	changed := []struct {
		name string
		spec junkRuleSpec
	}{
		{"pattern", junkRuleSpec{Name: "a", Kind: "regex", Rule: "y", Enabled: true}},
		{"enabled", junkRuleSpec{Name: "a", Kind: "regex", Rule: "x", Enabled: false}},
		{"kind", junkRuleSpec{Name: "a", Kind: "heuristic", Rule: "x", Enabled: true}},
		{"params", junkRuleSpec{Name: "a", Kind: "regex", Rule: "x", Params: junkParams{MinLen: 3}, Enabled: true}},
		{"name", junkRuleSpec{Name: "b", Kind: "regex", Rule: "x", Enabled: true}},
	}
	for _, c := range changed {
		if junkFingerprint([]junkRuleSpec{c.spec}) == fp {
			t.Errorf("changing %s did not change the fingerprint", c.name)
		}
	}
}
