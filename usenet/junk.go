package usenet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync/atomic"
)

// Junk-title detection, ported from the prod site's isJunkTitle
// (indexer-site/pkg/services/nzb_assembler.go). Obfuscated Usenet posts use
// random-token subjects ("Pzz8CzBPoBNsCu8oRPpDYwESRkpq5UU3jGlz…") that would
// otherwise assemble into garbage "releases". We drop them at ingest (before
// staging) and again at build (defensive), and sweep already-staged/built junk
// in the prune job.
//
// The rules are DATA, not code: seed/junk_rules.tsv is embedded (so the filter
// works before any database exists), seeds the junk_rules table, and the live
// set is then loaded from that table and compiled ONCE into an immutable
// junkMatcher held in an atomic pointer. The check runs per article on the
// ingest hot path, so it must never touch the database — reload swaps the whole
// matcher in one atomic store.

const junkSeedPath = "seed/junk_rules.tsv"

// junkRuleSpec is one rule as stored/shipped, before compilation.
type junkRuleSpec struct {
	Name    string
	Kind    string // "regex" | "heuristic"
	Rule    string // regex source, or the built-in heuristic id
	Params  junkParams
	Notes   string
	Enabled bool
}

// junkParams are the gates (regex rules) and tuning knobs (heuristics).
type junkParams struct {
	RequireUpper bool `json:"require_upper,omitempty"`
	RequireLower bool `json:"require_lower,omitempty"`
	RequireDigit bool `json:"require_digit,omitempty"`
	MinLen       int  `json:"min_len,omitempty"`
	MinSegLen    int  `json:"min_seg_len,omitempty"`
	MinChaotic   int  `json:"min_chaotic,omitempty"`
}

// compiledRule is the runtime form: regexes already compiled, no allocation or
// parsing on the hot path.
type compiledRule struct {
	name      string
	re        *regexp.Regexp // nil for heuristics
	heuristic string         // "" for regex rules
	params    junkParams
}

// junkMatcher is an immutable compiled rule set. Replace it wholesale; never
// mutate one that is in use.
type junkMatcher struct{ rules []compiledRule }

// activeJunk holds the live matcher. Reads are lock-free; a reload stores a new
// matcher in one atomic operation.
var activeJunk atomic.Pointer[junkMatcher]

func init() {
	m, err := loadEmbeddedJunkMatcher()
	if err != nil {
		// The embedded file ships with the binary, so this is a build-time bug.
		panic("usenet: embedded junk rules are invalid: " + err.Error())
	}
	activeJunk.Store(m)
}

// loadEmbeddedJunkMatcher compiles the shipped defaults.
func loadEmbeddedJunkMatcher() (*junkMatcher, error) {
	specs, err := parseJunkRulesTSV(junkSeedPath)
	if err != nil {
		return nil, err
	}
	return newJunkMatcher(specs)
}

// embeddedJunkRules returns the shipped rules — used by the seeder.
func embeddedJunkRules() ([]junkRuleSpec, error) {
	return parseJunkRulesTSV(junkSeedPath)
}

// setJunkMatcher swaps the live rule set. Safe to call while crawling.
func setJunkMatcher(m *junkMatcher) {
	if m != nil && len(m.rules) > 0 {
		activeJunk.Store(m)
	}
}

// parseJunkRulesTSV reads the shipped rule file.
func parseJunkRulesTSV(path string) ([]junkRuleSpec, error) {
	recs, err := seedRecords(seedData, path, 4)
	if err != nil {
		return nil, err
	}
	out := make([]junkRuleSpec, 0, len(recs))
	for _, rec := range recs {
		spec := junkRuleSpec{
			Name:    col(rec, 0),
			Kind:    col(rec, 1),
			Rule:    rec[2], // not trimmed: a pattern may end in meaningful space
			Notes:   col(rec, 4),
			Enabled: true,
		}
		if raw := col(rec, 3); raw != "" && raw != "{}" {
			if err := json.Unmarshal([]byte(raw), &spec.Params); err != nil {
				return nil, fmt.Errorf("%s: rule %q: params: %w", path, spec.Name, err)
			}
		}
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no rules", path)
	}
	return out, nil
}

// newJunkMatcher compiles specs. Disabled rules are dropped here so the hot path
// never has to check a flag.
func newJunkMatcher(specs []junkRuleSpec) (*junkMatcher, error) {
	m := &junkMatcher{}
	for _, s := range specs {
		if !s.Enabled {
			continue
		}
		switch s.Kind {
		case "regex":
			re, err := regexp.Compile(s.Rule)
			if err != nil {
				return nil, fmt.Errorf("junk rule %q: %w", s.Name, err)
			}
			m.rules = append(m.rules, compiledRule{name: s.Name, re: re, params: s.Params})
		case "heuristic":
			switch s.Rule {
			case "bare_token", "multi_segment_chaos":
			default:
				return nil, fmt.Errorf("junk rule %q: unknown heuristic %q", s.Name, s.Rule)
			}
			m.rules = append(m.rules, compiledRule{name: s.Name, heuristic: s.Rule, params: s.Params})
		default:
			return nil, fmt.Errorf("junk rule %q: unknown kind %q", s.Name, s.Kind)
		}
	}
	if len(m.rules) == 0 {
		return nil, fmt.Errorf("junk rules: nothing enabled")
	}
	return m, nil
}

// structural gate for the multi-segment random-token check: only alphanumerics,
// underscores, spaces (real titles carry other punctuation).
var reMultiSegSegmented = regexp.MustCompile(`^[A-Za-z0-9_ ]+$`)

// trailing release extensions to peel before checks (compound first).
var reReleaseExtDynamic = regexp.MustCompile(`(?i)\.(vol\d+\+\d+\.par2|part\d+\.rar|r\d{2,3}|\d{3})$`)

var staticReleaseExts = []string{
	".par2", ".rar", ".7z", ".zip", ".tar", ".gz", ".nzb", ".enc",
	".sfv", ".nfo", ".mkv", ".mp4", ".avi", ".iso", ".bin", ".001",
}

// stripReleaseExts peels trailing release/archive extensions so a title like
// "f2c8b393559540cfb9e33471cfda340c.par2" reduces to the bare hash before the
// pattern checks run. Repeats until stable (handles ".part01.rar" etc.).
func stripReleaseExts(s string) string {
	s = trimSpace(s)
	for {
		n := len(s)
		if n == 0 {
			return s
		}
		if next := reReleaseExtDynamic.ReplaceAllString(s, ""); next != s {
			s = trimSpace(next)
			continue
		}
		stripped := false
		low := toLowerTail(s)
		for _, ext := range staticReleaseExts {
			if hasSuffix(low, ext) {
				s = trimSpace(s[:n-len(ext)])
				stripped = true
				break
			}
		}
		if !stripped {
			return s
		}
	}
}

// isJunkTitle reports whether a parsed release name is machine-generated junk.
func isJunkTitle(title string) bool { return whichJunkRule(title) != "" }

// whichJunkRule returns the name of the first rule that fires, or "" if the
// title looks real. Naming the rule is what makes the filter tunable — you can
// see which rule is doing the work before changing it.
func whichJunkRule(title string) string {
	t := trimSpace(stripReleaseExts(title))
	// strip wrapping decoration posters put around hashes: 'x', {x}, [x], - x
	t = trimCut(t, "'\"{}[]- ")
	if len(t) == 0 {
		return "empty" // nothing left after stripping
	}
	return activeJunk.Load().match(t)
}

// match runs the compiled rules in order against an already-normalised title.
func (m *junkMatcher) match(t string) string {
	for _, r := range m.rules {
		if r.re != nil {
			if r.re.MatchString(t) && gatesPass(t, r.params) {
				return r.name
			}
			continue
		}
		switch r.heuristic {
		case "bare_token":
			if bareMixedCaseToken(t, r.params) {
				return r.name
			}
		case "multi_segment_chaos":
			if multiSegmentChaos(t, r.params) {
				return r.name
			}
		}
	}
	return ""
}

// gatesPass applies the optional character-class requirements. A rule with no
// gates passes trivially.
func gatesPass(t string, p junkParams) bool {
	if p.RequireUpper && !containsAny(t, 'A', 'Z') {
		return false
	}
	if p.RequireLower && !containsAny(t, 'a', 'z') {
		return false
	}
	if p.RequireDigit && !containsAny(t, '0', '9') {
		return false
	}
	return true
}

// bareMixedCaseToken: a naked run with no separator that mixes upper and lower
// at min_len+ chars. Real releases ALWAYS carry a separator (space/dot/dash/
// bracket), so a bare mixed-case run is machine junk. Catches the tokens that
// slip under the long-alphanumeric-run rule.
func bareMixedCaseToken(t string, p junkParams) bool {
	minLen := p.MinLen
	if minLen <= 0 {
		minLen = 16
	}
	return len(t) >= minLen && !hasSeparator(t) &&
		containsAny(t, 'A', 'Z') && containsAny(t, 'a', 'z')
}

// multiSegmentChaos: min_chaotic+ segments (split on _ or space) of min_seg_len+
// chars that EACH mix upper, lower and digit. Real tokens rarely do all three,
// almost never in two segments.
func multiSegmentChaos(t string, p junkParams) bool {
	minLen, minSegLen, minChaotic := p.MinLen, p.MinSegLen, p.MinChaotic
	if minLen <= 0 {
		minLen = 24
	}
	if minSegLen <= 0 {
		minSegLen = 5
	}
	if minChaotic <= 0 {
		minChaotic = 2
	}
	if len(t) < minLen || !reMultiSegSegmented.MatchString(t) {
		return false
	}
	chaotic := 0
	seg := make([]rune, 0, 16)
	flush := func() bool {
		defer func() { seg = seg[:0] }()
		if len(seg) < minSegLen {
			return false
		}
		var u, l, d bool
		for _, c := range seg {
			switch {
			case c >= 'A' && c <= 'Z':
				u = true
			case c >= 'a' && c <= 'z':
				l = true
			case c >= '0' && c <= '9':
				d = true
			}
		}
		return u && l && d
	}
	for _, c := range t {
		if c == '_' || c == ' ' {
			if flush() {
				chaotic++
			}
			continue
		}
		seg = append(seg, c)
	}
	if flush() {
		chaotic++
	}
	return chaotic >= minChaotic
}

// ── small string helpers (avoid importing strings twice across files) ──

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func trimCut(s, cutset string) string {
	in := func(b byte) bool {
		for i := 0; i < len(cutset); i++ {
			if cutset[i] == b {
				return true
			}
		}
		return false
	}
	for len(s) > 0 && in(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && in(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func toLowerTail(s string) string {
	start := len(s) - 8
	if start < 0 {
		start = 0
	}
	b := []byte(s[start:])
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}

func hasSeparator(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '.', '_', '-', '[', ']', '(', ')', '{', '}', '\'', '"', '|', '/', '\\':
			return true
		}
	}
	return false
}

func containsAny(s string, lo, hi byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= lo && s[i] <= hi {
			return true
		}
	}
	return false
}
