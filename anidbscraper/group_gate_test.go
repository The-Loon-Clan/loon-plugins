package anidbscraper

import (
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The README says this package ships no tests, deliberately, because a test
// over a stub pins the stub. The gate is the exception and the reason is the
// bug that produced it: group_gate.go is a MIRROR of the host's
// pkg/services/anidb_scan_group_gate.go, and the two copies had already
// diverged once — silently, because nothing on either side asserted what the
// decision was. These cases are the host's own, ported with it, so a change
// that lands on one side and not the other shows up as a red build rather
// than as a site tagging "Adobe Audition 2026" as an anime.

// fakeMatcher answers with distinguishable ids so a test can tell WHICH
// lookup a verdict handed back: 1 = the fuzzy Find, 2 = the exact FindExact.
type fakeMatcher struct{}

func (fakeMatcher) Find(string) (int, bool)      { return 1, true }
func (fakeMatcher) Rebuild(map[string]int)       {}
func (fakeMatcher) FindExact(string) (int, bool) { return 2, true }

// fuzzyOnlyMatcher is a host matcher that does NOT implement
// pluginapi.ExactTitleMatcher — the wiring gate mode "exact" must refuse.
type fuzzyOnlyMatcher struct{}

func (fuzzyOnlyMatcher) Find(string) (int, bool) { return 1, true }
func (fuzzyOnlyMatcher) Rebuild(map[string]int)  {}

func row(groups ...string) pluginapi.NzbRow {
	return pluginapi.NzbRow{ID: 1, Title: "whatever", Groups: groups}
}

// ── the pattern language (mirrors the host's TestGlobMatch) ────────────────

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*anime*", "alt.binaries.multimedia.anime.highspeed", true},
		{"*anime*", "alt.binaries.anime", true},
		{"*anime*", "alt.binaries.dvd.anime.repost", true},
		{"*anime*", "alt.binaries.cartoons.french.animes-fansub", true},
		{"*anime*", "alt.binaries.teevee", false},
		{"*anime*", "alt.binaries.mp3", false},
		// anchoring
		{"alt.binaries.anime", "alt.binaries.anime", true},
		{"alt.binaries.anime", "alt.binaries.anime.repost", false},
		{"alt.binaries.anime*", "alt.binaries.anime.repost", true},
		{"*.anime", "alt.binaries.dvd.anime", true},
		{"*.anime", "alt.binaries.dvd.anime.repost", false},
		// multiple wildcards, including a greedy middle that must not eat
		// the suffix
		{"alt.*.anime.*", "alt.binaries.anime.raws", true},
		{"*ab*ab", "abab", true},
		{"*ab*ab", "ab", false},
		{"*", "anything", true},
		{"*", "", true},
		{"", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestParseGroupPatterns(t *testing.T) {
	got := parseGroupPatterns("  *ANIME*  \n\n# a comment\nalt.binaries.multimedia.japanese\r\nfoo,bar\n#\n")
	want := []string{"*anime*", "alt.binaries.multimedia.japanese", "foo", "bar"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parseGroupPatterns = %q, want %q", got, want)
	}
	if p := parseGroupPatterns("   \n # only a comment \n"); len(p) != 0 {
		t.Fatalf("comment-only allowlist parsed to %q, want empty", p)
	}
}

// ── the shipped default is a no-op ─────────────────────────────────────────

// The whole point of the port: a host that upgrades past group_gate.go and
// configures nothing must tag exactly what it tagged yesterday.
func TestUnconfiguredGateIsInert(t *testing.T) {
	g, warn, err := newGroupGate(Config{}, nil)
	if err != nil || warn != "" {
		t.Fatalf("empty config: err=%v warn=%q, want a clean inert gate", err, warn)
	}
	if g.active() {
		t.Error("an unconfigured gate must be inactive")
	}
	if !g.allows(row("alt.binaries.teevee")) {
		t.Error("an unconfigured gate must allow a non-anime group — that is the pre-gate behaviour")
	}
	if !strings.Contains(g.describe(), "inert") {
		t.Errorf("describe() = %q, want it to say the gate is inert", g.describe())
	}

	p := &Plugin{gate: g}
	SetDeps(Deps{Matcher: fakeMatcher{}})
	v := p.verdict(g, row("alt.binaries.teevee"))
	if v.match == nil || v.offGate {
		t.Fatal("an inert gate must hand back the normal matcher and gate nothing")
	}
	if id, _ := v.match("x"); id != 1 {
		t.Errorf("inert gate used lookup %d, want the fuzzy Find (1)", id)
	}
}

// ── the allow decision (mirrors the host's TestScanGroupGateAllows) ────────

func TestGroupGateAllows(t *testing.T) {
	def, _, err := newGroupGate(Config{GroupAllowlist: "*anime*"}, nil)
	if err != nil {
		t.Fatalf("newGroupGate: %v", err)
	}
	if def.mode != gateModeRefuse {
		t.Errorf("mode = %q, want the shipped default %q for an unset mode", def.mode, gateModeRefuse)
	}
	if !def.allows(row("alt.binaries.multimedia.anime.highspeed")) {
		t.Error("an anime group must be allowed by an *anime* allowlist")
	}
	if def.allows(row("alt.binaries.teevee")) {
		t.Error("alt.binaries.teevee must not be allowed by an *anime* allowlist")
	}
	if !def.allows(row("ALT.BINARIES.ANIME")) {
		t.Error("group matching must be case-insensitive")
	}
	// A crosspost carried in an anime group is evidence about the posting
	// even when it is not the row's first group.
	if !def.allows(row("alt.binaries.boneless", "alt.binaries.multimedia.anime")) {
		t.Error("a crosspost into an anime group must be allowed")
	}
	// No newsgroup at all = not a crawled posting (upload / scrape), and
	// also what an un-populated NzbRow.Groups looks like.
	if !def.allows(row()) {
		t.Error("a release with no newsgroup must be allowed")
	}
	if !def.allows(row("", "  ")) {
		t.Error("blank newsgroup names must read as 'no newsgroup', not as a refusal")
	}

	// mode=off is the kill switch even with an allowlist configured.
	off, _, err := newGroupGate(Config{GroupAllowlist: "*anime*", GroupGateMode: "OFF"}, nil)
	if err != nil {
		t.Fatalf("newGroupGate: %v", err)
	}
	if off.active() || !off.allows(row("alt.binaries.teevee")) {
		t.Error("mode=off must disable the gate")
	}
}

// ── mode dispatch (mirrors the host's TestScanGateVerdictByMode) ───────────

func TestVerdictByMode(t *testing.T) {
	SetDeps(Deps{Matcher: fakeMatcher{}})
	p := &Plugin{exactMatcher: fakeMatcher{}}
	on, offGroup := row("alt.binaries.multimedia.anime.highspeed"), row("alt.binaries.teevee")

	mk := func(mode string) groupGate {
		g, _, err := newGroupGate(Config{GroupAllowlist: "*anime*", GroupGateMode: mode}, nil)
		if err != nil {
			t.Fatalf("newGroupGate(%q): %v", mode, err)
		}
		return g
	}

	if v := p.verdict(mk(gateModeRefuse), on); v.match == nil || v.offGate {
		t.Error("an in-jurisdiction release must be matched normally in every mode")
	}
	if v := p.verdict(mk(gateModeRefuse), offGroup); v.match != nil || !v.offGate {
		t.Error("refuse: an out-of-jurisdiction release must get no title match at all")
	}
	v := p.verdict(mk(gateModeExact), offGroup)
	if v.match == nil || !v.offGate {
		t.Fatal("exact: an out-of-jurisdiction release must still get the exact lookup")
	}
	if id, _ := v.match("x"); id != 2 {
		t.Errorf("exact mode used lookup %d, want FindExact (2) — the fuzzy steps are what it switches off", id)
	}
	if v := p.verdict(mk(gateModeReport), offGroup); v.match == nil || !v.offGate || !v.report {
		t.Error("report: an out-of-jurisdiction release must behave normally and be counted")
	}

	// A typo must fall back to the strict default, not silently disable the
	// gate — that is the failure the gate exists to prevent.
	g, warn, err := newGroupGate(Config{GroupAllowlist: "*anime*", GroupGateMode: "refus"}, nil)
	if err != nil {
		t.Fatalf("newGroupGate: %v", err)
	}
	if warn == "" {
		t.Error("an unrecognised mode must warn")
	}
	if g.mode != gateModeRefuse {
		t.Errorf("unrecognised mode resolved to %q, want %q", g.mode, gateModeRefuse)
	}
	if vv := p.verdict(g, offGroup); vv.match != nil {
		t.Error("an unrecognised mode must behave as refuse")
	}
}

// ── the optional host override ─────────────────────────────────────────────

func TestAllowTitleGuessOverride(t *testing.T) {
	SetDeps(Deps{Matcher: fakeMatcher{}})
	// A host with no newsgroups at all deciding jurisdiction its own way.
	g, _, err := newGroupGate(Config{}, func(r pluginapi.NzbRow) bool { return r.ID == 7 })
	if err != nil {
		t.Fatalf("newGroupGate: %v", err)
	}
	if !g.active() {
		t.Fatal("an AllowTitleGuess override must make the gate active with no allowlist")
	}
	p := &Plugin{}
	if v := p.verdict(g, pluginapi.NzbRow{ID: 7}); v.match == nil || v.offGate {
		t.Error("the override said yes; the row must be matched normally")
	}
	if v := p.verdict(g, pluginapi.NzbRow{ID: 8}); v.match != nil || !v.offGate {
		t.Error("the override said no; refuse mode must give no title match")
	}

	// Two ways of deciding jurisdiction at once is a wiring contradiction:
	// the plugin would have to ignore one, and no answer to that is right.
	if _, _, err := newGroupGate(Config{GroupAllowlist: "*anime*"}, func(pluginapi.NzbRow) bool { return true }); err == nil {
		t.Error("an allowlist AND an override must be refused at Provision")
	}
}

// ── the run-end report ─────────────────────────────────────────────────────

func TestGateOutcomeLine(t *testing.T) {
	patterns, _, _ := newGroupGate(Config{GroupAllowlist: "*anime*"}, nil)
	counts := map[string]int{"alt.binaries.teevee": 40, "alt.binaries.mp3": 9}

	if line := gateOutcomeLine(groupGate{}, 100, 0, 0, 0, nil); line != "" {
		t.Errorf("an inert gate must say nothing, got %q", line)
	}
	if line := gateOutcomeLine(patterns, 100, 40, 0, 100, counts); !strings.Contains(line, "40 rows refused") ||
		!strings.Contains(line, "alt.binaries.teevee=40") {
		t.Errorf("refuse line = %q, want the count and the busiest gated group", line)
	}
	// The guard that catches a host which configured the gate but does not
	// populate NzbRow.Groups: nothing gated, nothing seen, work done.
	line := gateOutcomeLine(patterns, 100, 0, 0, 0, nil)
	if !strings.Contains(line, "INERT") || !strings.Contains(line, "NzbRow.Groups") {
		t.Errorf("silent-inert warning = %q, want it to name the unpopulated field", line)
	}
	// An empty scan is not evidence of a wiring problem.
	if line := gateOutcomeLine(patterns, 0, 0, 0, 0, nil); line != "" {
		t.Errorf("a scan with no rows must not accuse the host, got %q", line)
	}
	// Nor is a run where the gate plainly saw newsgroups and allowed them.
	if line := gateOutcomeLine(patterns, 100, 0, 0, 100, nil); line != "" {
		t.Errorf("an all-allowed run must say nothing, got %q", line)
	}
}

func TestTopGatedGroups(t *testing.T) {
	got := topGatedGroups(map[string]int{"c": 5, "a": 9, "b": 5}, 2)
	if got != "a=9 b=5" {
		t.Errorf("topGatedGroups = %q, want %q (count desc, then name for a stable log line)", got, "a=9 b=5")
	}
}

// ── the exact-mode wiring contract ─────────────────────────────────────────

// A host that asks for exact-only with a matcher that cannot do it must be
// refused at boot, not quietly given the fuzzy matcher the mode exists to
// switch off — it would believe it had a gate it does not have.
func TestExactModeNeedsExactMatcher(t *testing.T) {
	exact, _, err := newGroupGate(Config{GroupAllowlist: "*anime*", GroupGateMode: gateModeExact}, nil)
	if err != nil {
		t.Fatalf("newGroupGate: %v", err)
	}
	if _, err := resolveExactMatcher(exact, fuzzyOnlyMatcher{}); err == nil {
		t.Error("exact mode with a Find-only matcher must refuse at Provision")
	}
	em, err := resolveExactMatcher(exact, fakeMatcher{})
	if err != nil || em == nil {
		t.Fatalf("exact mode with an ExactTitleMatcher: em=%v err=%v, want it wired", em, err)
	}

	// Every other mode needs nothing, including from a matcher that cannot
	// do exact — the requirement belongs to the one mode that uses it.
	refuse, _, _ := newGroupGate(Config{GroupAllowlist: "*anime*"}, nil)
	if em, err := resolveExactMatcher(refuse, fuzzyOnlyMatcher{}); em != nil || err != nil {
		t.Errorf("refuse mode wanted an exact matcher: em=%v err=%v", em, err)
	}
	// ...and an inert gate must not fail a boot over a mode nothing reads.
	inert, _, _ := newGroupGate(Config{GroupGateMode: gateModeExact}, nil)
	if _, err := resolveExactMatcher(inert, fuzzyOnlyMatcher{}); err != nil {
		t.Errorf("an inert gate must not block boot: %v", err)
	}
}
