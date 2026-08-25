package usenet

import (
	"math/rand"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// The pre-filter's ONLY acceptable outcome is "same verdict, less work". These
// tests attack it from three directions: the derived literal must be a real
// necessary condition for every shipped rule, the gate must never change a
// verdict on realistic or adversarial input, and it must degrade to
// pass-everything on anything it cannot prove.

// gateIsNecessary is the core property: if a string matches the regex, it MUST
// contain one of the gate's literals. A counter-example here means junk stops
// being caught in production.
func gateIsNecessary(t *testing.T, name, pattern, s string) {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		return // unparseable rules are the compiler's problem, not the gate's
	}
	g := buildLiteralGate(pattern)
	if re.MatchString(s) && !g.pass(s) {
		t.Fatalf("rule %q: %q MATCHES the regex but the gate rejected it — "+
			"the pre-filter would silently drop this junk.\n  pattern: %s\n  gate: %+v",
			name, s, pattern, g)
	}
}

// Every shipped rule, against every subject the rest of the suite cares about
// plus a spread of shapes. If the gate is wrong for any real rule, this finds
// it before prod does.
func TestGateIsNecessaryForEveryShippedRule(t *testing.T) {
	specs, err := parseJunkRulesTSV(junkSeedPath)
	if err != nil {
		t.Fatalf("parse seed rules: %v", err)
	}
	subjects := []string{
		"",
		"a",
		"[Group] Show Name - 01 (1080p) [ABCD1234].mkv",
		"Some.Movie.2024.1080p.BluRay.x264-GRP",
		"aB3kZ9qL",
		"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"РУССКИЙ ФИЛЬМ 2024",
		"日本語のタイトル [1080p]",
		"file.vol000+01.par2",
		"archive.part01.rar",
		"something.r00",
		"TiKtOk compilation 2024",
		"(1/100) - \"file.mkv\" yEnc",
		"[ 1 | 100 ] Show Name",
		strings.Repeat("long ", 200),
		"UPPER lower 12345 !@#$%^&*()",
		"...   ---   ___",
	}
	gated := 0
	for _, s := range specs {
		if s.Kind != "regex" || !s.Enabled {
			continue
		}
		if len(buildLiteralGate(s.Rule).Any) > 0 {
			gated++
		}
		for _, subj := range subjects {
			gateIsNecessary(t, s.Name, s.Rule, subj)
		}
	}
	if gated == 0 {
		t.Error("no shipped rule got a literal gate — the optimisation is doing nothing")
	}
	t.Logf("%d of the shipped regex rules carry a literal gate", gated)
}

// Randomised strings over an alphabet drawn from the rules themselves, which is
// where a near-miss is most likely to hide.
func TestGateIsNecessaryUnderFuzz(t *testing.T) {
	specs, err := parseJunkRulesTSV(junkSeedPath)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "abcXYZ019 .-_[]()|+xvid1080ptiktokvolparrar\"yEnc"
	rng := rand.New(rand.NewSource(20260731))
	for _, s := range specs {
		if s.Kind != "regex" || !s.Enabled {
			continue
		}
		for i := 0; i < 400; i++ {
			n := rng.Intn(40)
			var b strings.Builder
			for j := 0; j < n; j++ {
				b.WriteByte(alphabet[rng.Intn(len(alphabet))])
			}
			gateIsNecessary(t, s.Name, s.Rule, b.String())
		}
	}
}

// The whole matcher, old path vs new: same verdict on every subject.
//
// matchNoGate re-runs the rule loop with the gate disabled, which is exactly
// the pre-change behaviour, so any divergence is the pre-filter's fault.
func (m *junkMatcher) matchNoGate(t, ts string, sizeBytes int64) string {
	for _, r := range m.rules {
		if r.params.SizedOnly && sizeBytes <= 0 {
			continue
		}
		if r.params.sized() {
			if sizeBytes <= 0 || (r.params.MaxSizeBytes > 0 && sizeBytes >= r.params.MaxSizeBytes) ||
				(r.params.MinSizeBytes > 0 && sizeBytes < r.params.MinSizeBytes) {
				continue
			}
		}
		in := t
		if r.params.LightInput {
			in = ts
		}
		if r.re != nil {
			if r.re.MatchString(in) && gatesPass(in, r.params) {
				return r.name
			}
			continue
		}
		if runJunkHeuristic(r.heuristic, in, r.params) {
			return r.name
		}
	}
	return ""
}

func TestMatcherVerdictsAreUnchangedByTheGate(t *testing.T) {
	m, err := loadEmbeddedJunkMatcher()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(4242))
	const alphabet = "abcdefXYZ019 .-_[]()|+\"'{}=:xvid1080ptiktokvolparrryEnc日本語"

	var subjects []string
	subjects = append(subjects,
		"[Group] Show Name - 01 (1080p) [ABCD1234].mkv",
		"Some.Movie.2024.1080p.BluRay.x264-GRP",
		"file.vol000+01.par2", "archive.part01.rar", "clip.r00",
		"aB3kZ9qL", "TIKTOK compilation", "(1/100) - \"a.mkv\" yEnc",
	)
	for i := 0; i < 4000; i++ {
		n := rng.Intn(60)
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}
		subjects = append(subjects, b.String())
	}

	sizes := []int64{0, 1, 900_000, 5_000_000, 900_000_000}
	for _, subj := range subjects {
		stripped := stripAllMarkers(subj)
		for _, sz := range sizes {
			want := m.matchNoGate(subj, stripped, sz)
			got := m.match(subj, stripped, sz)
			if want != got {
				t.Fatalf("gate changed the verdict for %q (size %d): was %q, now %q",
					subj, sz, want, got)
			}
		}
	}
}

// requiredLiterals must refuse anything it cannot prove. Each of these has a
// way to match WITHOUT the tempting literal, so a gate would be unsound.
func TestGateRefusesWhatItCannotProve(t *testing.T) {
	for _, pattern := range []string{
		`(?i)(foo)?bar|baz.*`, // an alternation branch with no literal floor
		`^.*$`,                // matches anything
		`(abc)*`,              // star: matches empty
		`(abc)?def?`,          // optional tail
		`[a-z]{3,}`,           // class only
		`abc|`,                // empty alternative
	} {
		if g := buildLiteralGate(pattern); len(g.Any) > 0 {
			// Prove the gate is actually wrong by finding a matching string it
			// rejects, so this test fails for the right reason.
			re := regexp.MustCompile(pattern)
			for _, probe := range []string{"", "def", "zzz", "baz", "xyzzy"} {
				if re.MatchString(probe) && !g.pass(probe) {
					t.Errorf("pattern %q got gate %+v, which rejects the matching string %q",
						pattern, g, probe)
				}
			}
		}
	}
}

// Where the length cutoff sits, and why. A two-character alternative is kept:
// it is still a correct requirement, and dropping the whole gate for it threw
// away the other forty-eight branches of software_warez. A ONE-character one
// is refused, because it appears in nearly every subject so the scan buys
// nothing.
func TestShortLiteralsDisableTheGate(t *testing.T) {
	if g := buildLiteralGate(`(?i)tiktok|a`); len(g.Any) != 0 {
		t.Errorf("a 1-char alternative must disable the gate, got %+v", g)
	}
	if g := buildLiteralGate(`(?i)tiktok|ab`); len(g.Any) != 2 {
		t.Errorf("a 2-char alternative should be kept, got %+v", g)
	}
	g := buildLiteralGate(`(?i)tiktok|douyin`)
	if len(g.Any) != 2 {
		t.Fatalf("expected both alternatives gated, got %+v", g)
	}
	if !g.Fold {
		t.Error("(?i) pattern did not set Fold — the gate would miss cased subjects")
	}
	if !g.pass("A TIKTOK CLIP") || g.pass("unrelated subject") {
		t.Errorf("fold gate behaved wrongly: %+v", g)
	}
}

// Every spelling of the fold flag must set Fold. The old oracle was a
// textual "(?i)" scan, so (?i:keygen) — a valid, compiling, enabled operator
// rule — got the gate {["KEYGEN"], no fold}: lowercase subjects failed the
// Contains, the regex never ran, and the rule sat silently dead with zero
// filter_hits.
func TestInlineFlagFormsSetFold(t *testing.T) {
	cases := []struct {
		pattern string
		subject string
	}{
		{`(?i:keygen)`, "photoshop 2026 keygen included"},
		{`(?is)keygen.title`, "free keygen\ntitle inside"},
		{`(?si)keygen`, "some keygen post"},
		{`EXACT(?i)folded`, "has EXACTfolded inside"},
	}
	for _, c := range cases {
		g := buildLiteralGate(c.pattern)
		if !g.Fold {
			t.Errorf("buildLiteralGate(%q).Fold = false — the gate would miss cased subjects", c.pattern)
		}
		gateIsNecessary(t, "inline_fold", c.pattern, c.subject)
	}
	// And a plain exact-case pattern stays exact: fold only ever widens.
	if g := buildLiteralGate(`keygen`); g.Fold || len(g.Any) != 1 || g.Any[0] != "keygen" {
		t.Errorf("exact-case literal gate changed: %+v", g)
	}
}

// End to end through the matcher: an operator-authored inline-flag rule must
// fire on lowercase subjects — the gate may only ever skip work the regex
// would have declined.
func TestInlineFoldRuleFiresThroughTheMatcher(t *testing.T) {
	m, err := newJunkMatcher([]junkRuleSpec{{
		Name: "inline_fold", Kind: "regex", Rule: `(?i:keygen)`, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.match("photoshop 2026 keygen incl crack", "photoshop 2026 keygen incl crack", 0); got != "inline_fold" {
		t.Errorf("lowercase subject: match = %q, want inline_fold — the gate starved the rule", got)
	}
}

// Sanity: the analysis agrees with the regexp package about a prefix it can
// also derive, which catches a whole class of mistakes in the walker.
func TestGateAgreesWithLiteralPrefix(t *testing.T) {
	for _, pattern := range []string{`abcdef`, `abcdef.*`, `abcdef[0-9]+`} {
		re := regexp.MustCompile(pattern)
		prefix, _ := re.LiteralPrefix()
		g := buildLiteralGate(pattern)
		if len(g.Any) != 1 || !strings.HasPrefix(prefix, g.Any[0]) {
			t.Errorf("pattern %q: gate %+v disagrees with LiteralPrefix %q", pattern, g, prefix)
		}
	}
	// And the parser we rely on is the same one regexp uses.
	if _, err := syntax.Parse(`(?i)tiktok`, syntax.Perl); err != nil {
		t.Fatal(err)
	}
}
