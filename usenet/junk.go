package usenet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
)

// Junk-title detection — THE canonical engine (ported from prod's
// whichJunkPattern/whichJunkPatternSized before the legacy crawler's deletion;
// the host now keeps MIRRORS of these rules: pkg/services/junk_title.go and
// the SQL sweep, per its CLAUDE.md "Junk-title rules"). Obfuscated Usenet
// posts use random-token subjects
// ("0N70ZyFoz8n50", "Pzz8CzBPoBNsCu8oRPpDYwESRkpq5UU3jGlz…") that would
// otherwise assemble into garbage "releases". We drop them at ingest (before
// staging) and again at build (with the release size in hand), and sweep
// already-staged/built junk in the prune job.
//
// HISTORY: the first lift carried only 6 of prod's ~26 patterns, and the first
// live crawl proved it immediately — "0N70ZyFoz8n50" was indexed because
// short_alnum_token (prod's single largest junk class, 34k+ rows there) had
// never been ported. This file is now rule-for-rule, threshold-for-threshold,
// ORDER-for-order identical to prod; the reported names match prod's pattern
// names so hit counters are comparable across both.
//
// The rules are DATA: seed/junk_rules.tsv is embedded (so the filter works
// before any database exists), seeds the junk_rules table, and the live set is
// then loaded from that table and compiled ONCE into an immutable junkMatcher
// held in an atomic pointer. Rules run in TSV/table order — which ships as
// prod's evaluation order, so attribution matches too. The check runs per
// article on the ingest hot path, so it must never touch the database; reload
// swaps the whole matcher in one atomic store.
//
// Two kinds of rule:
//   - kind=regex: the pattern lives in the data and is operator-editable.
//   - kind=heuristic: the rule names a built-in algorithm below (shapes RE2
//     cannot express: backreferences, ratios, per-segment analysis). Operators
//     can still disable or re-order them; the thresholds are prod's constants.
//
// SIZED rules only fire when the caller knows the release size (size > 0) —
// the build path does, the per-article ingest path does not. That mirrors prod,
// where isJunkTitle is the unsized subset and the assembler calls the sized
// form with the summed release size.

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
	MaxLen       int  `json:"max_len,omitempty"`
	MinSegLen    int  `json:"min_seg_len,omitempty"`
	MinChaotic   int  `json:"min_chaotic,omitempty"`
	// Size gates. A rule with MaxSizeBytes > 0 fires only when the caller
	// supplied a size AND size < MaxSizeBytes; MinSizeBytes additionally
	// requires size >= MinSizeBytes. Rules with either set are skipped
	// entirely when the size is unknown (ingest path).
	MinSizeBytes int64 `json:"min_size_bytes,omitempty"`
	MaxSizeBytes int64 `json:"max_size_bytes,omitempty"`
	// SizedOnly marks a rule with no size BOUND that still belongs to the
	// sized section: it fires at any size, yet never on the unsized ingest
	// path. Prod's short_random_token works exactly this way — size-agnostic
	// since May 2026, but only ever consulted by the assembler.
	SizedOnly bool `json:"sized_only,omitempty"`
	// LightInput feeds the rule the LIGHTLY-normalised title (trim +
	// quote/bracket strip, NO extension strip) — prod's sized section works on
	// that form; e.g. word_word_hex deliberately tolerates a trailing
	// extension. Rule DATA, not a hardcoded name list: the eight shipped rules
	// set it in the seed TSV, and an operator's own rule gets the same input
	// by setting the same param instead of silently receiving a different
	// normalisation than the rules it sits beside.
	LightInput bool `json:"light_input,omitempty"`
}

func (p junkParams) sized() bool { return p.MinSizeBytes > 0 || p.MaxSizeBytes > 0 }

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

// junkHeuristics are the built-in algorithm ids a kind=heuristic rule may name.
var junkHeuristics = map[string]bool{
	"multi_segment_chaos": true, // 2+ chaotic segments (prod: multi_seg_random)
	"bare_alnum_token":    true, // bare alnum run, length-banded, optional digit gate
	"repeated_short_tok":  true, // "rtNJ rtNJ" — same short token twice
	"high_special_chars":  true, // 15%+ garbled punctuation
	"random_words":        true, // 70%+ of non-punct chars from random-looking words
	"tiny_no_space":       true, // sized: no whitespace at all under the size cap
	"long_no_space":       true, // sized: 60+ chars, no whitespace, 5+ garbled
	"chaotic_specials":    true, // sized: 30+ chars, HAS whitespace, 3+ garbled
	"size_catchall":       true, // sized: everything in a size band is junk
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
			if !junkHeuristics[s.Rule] {
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
	s = strings.TrimSpace(s)
	for {
		n := len(s)
		if n == 0 {
			return s
		}
		if next := reReleaseExtDynamic.ReplaceAllString(s, ""); next != s {
			s = strings.TrimSpace(next)
			continue
		}
		stripped := false
		low := toLowerTail(s)
		for _, ext := range staticReleaseExts {
			if strings.HasSuffix(low, ext) {
				s = strings.TrimSpace(s[:n-len(ext)])
				stripped = true
				break
			}
		}
		if !stripped {
			return s
		}
	}
}

// isJunkTitle reports whether a parsed release name is machine-generated junk,
// judged on the title alone (prod's isJunkTitle — the unsized subset).
func isJunkTitle(title string) bool { return whichJunkRule(title) != "" }

// isJunkTitleSized additionally applies the size-gated rules; sizeBytes is the
// whole release's payload (prod's isJunkTitleSized).
func isJunkTitleSized(title string, sizeBytes int64) bool {
	return whichJunkRuleSized(title, sizeBytes) != ""
}

// whichJunkRule is whichJunkRuleSized with the size unknown.
func whichJunkRule(title string) string { return whichJunkRuleSized(title, 0) }

// whichJunkRuleSized returns the name of the first rule that fires, or "" if
// the title looks real. Naming the rule is what makes the filter tunable — you
// can see which rule is doing the work before changing it.
func whichJunkRuleSized(title string, sizeBytes int64) string {
	// Full normalisation for the title-shape rules: extensions peeled, then
	// wrapping decoration posters put around hashes: 'x', {x}, [x], - x.
	t := strings.TrimSpace(stripReleaseExts(title))
	t = strings.Trim(t, "'\"{}[]- ")
	if len(t) == 0 {
		return "empty" // nothing left after stripping
	}
	// Light normalisation for the sized rules — prod's sized section does NOT
	// strip extensions (word_word_hex tolerates one on purpose) and does not
	// trim dashes/spaces beyond the outer whitespace.
	ts := strings.Trim(strings.TrimSpace(title), "'\"{}[]")
	return activeJunk.Load().match(t, ts, sizeBytes)
}

// match runs the compiled rules in order. t is the fully-normalised title, ts
// the lightly-normalised one for sized-section rules, sizeBytes 0 when unknown.
func (m *junkMatcher) match(t, ts string, sizeBytes int64) string {
	for _, r := range m.rules {
		// Size gates: a size-banded (or sized-section) rule never fires on an
		// unknown size — the per-article ingest path.
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

// runJunkHeuristic dispatches a built-in algorithm.
func runJunkHeuristic(id, t string, p junkParams) bool {
	switch id {
	case "multi_segment_chaos":
		return multiSegmentChaos(t, p)
	case "bare_alnum_token":
		return bareAlnumToken(t, p)
	case "repeated_short_tok":
		return isRepeatedShortTokenJunk(t)
	case "high_special_chars":
		return highSpecialChars(t, p)
	case "random_words":
		return randomWordsJunk(t)
	case "tiny_no_space":
		return !strings.ContainsAny(t, " \t\r\n\v\f")
	case "long_no_space":
		return len(t) >= max(p.MinLen, 1) && !strings.ContainsAny(t, " \t\r\n\v\f") &&
			countGarbledPunct(t) >= 5
	case "chaotic_specials":
		return len(t) >= max(p.MinLen, 1) && strings.ContainsAny(t, " \t") &&
			countGarbledPunct(t) >= 3
	case "size_catchall":
		return true // the size gate in match() is the whole rule
	}
	return false
}

// gatesPass applies the optional character-class requirements. A rule with no
// gates passes trivially.
func gatesPass(t string, p junkParams) bool {
	if p.RequireUpper && !hasByteInRange(t, 'A', 'Z') {
		return false
	}
	if p.RequireLower && !hasByteInRange(t, 'a', 'z') {
		return false
	}
	if p.RequireDigit && !hasByteInRange(t, '0', '9') {
		return false
	}
	return true
}

// bareAlnumToken: the whole title is one bare alphanumeric run inside the
// configured length band, optionally required to carry at least one digit and
// one letter. Three shipped bands mirror prod exactly:
//
//	short_alnum_token  6-15, digit+letter — prod's single largest junk class
//	mid_alnum_token   16-19, digit+letter — "pE9F9wMjNxWZqpq9V"
//	single_token_20   20+,   any mix      — dashless UUIDs, hashes, base64
//
// The digit gate on the short bands is what spares pure-letter real titles
// ("Lamune", "Heroman", "Gunbuster"); at 20+ no real title is a bare run at
// all, so the gate drops away.
func bareAlnumToken(t string, p junkParams) bool {
	if len(t) < p.MinLen || (p.MaxLen > 0 && len(t) > p.MaxLen) {
		return false
	}
	var hasDigit, hasLetter bool
	for _, c := range t {
		if !isAlnum(c) {
			return false
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
		} else {
			hasLetter = true
		}
	}
	if p.RequireDigit && !(hasDigit && hasLetter) {
		return false
	}
	return true
}

// isRepeatedShortTokenJunk: the same 2-20 char alphanumeric token repeated
// twice ("rtNJ rtNJ", "PoYS PoYS"). Programmatic because RE2 has no
// backreferences. Tokens over 5 chars must mix digits and letters — repeated
// real words ("New York New York" style) stay safe.
func isRepeatedShortTokenJunk(title string) bool {
	chunks := strings.FieldsFunc(title, func(r rune) bool { return !isAlnum(r) })
	if len(chunks) != 2 || chunks[0] != chunks[1] {
		return false
	}
	n := len(chunks[0])
	if n < 2 || n > 20 {
		return false
	}
	if n <= 5 {
		return true
	}
	var hasDigit, hasLetter bool
	for _, c := range chunks[0] {
		if c >= '0' && c <= '9' {
			hasDigit = true
		} else if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

// highSpecialChars: 15%+ of the title is "garbled" punctuation. Release-style
// separators (.-_()[] and space) are allowed, and so are angle brackets —
// legitimate posters wrap signatures in them ("<<<Nimue>>>< file >") and they
// over-caught real rips before prod excluded them.
// reTrailingTagBlock matches a trailing "{...}" metadata block — the
// {Tags:L0;V7;A=ja,en;S=en,ar;} convention several release groups append.
//
// The closing brace is OPTIONAL because whichJunkRuleSized normalises with
// strings.Trim(t, "'\"{}[]- ") first, which strips it: by the time a rule sees
// the title the block is already unterminated. Requiring the brace made this
// pattern silently never match — the audit reported the same 71 false
// positives before and after adding it.
//
// The block must still contain a ":" or "=" so it reads as structured metadata
// rather than a stray brace in genuine garble.
var reTrailingTagBlock = regexp.MustCompile(`\s*\{[^{}]*[:=][^{}]*\}?\s*$`)

func highSpecialChars(t string, p junkParams) bool {
	minLen := p.MinLen
	if minLen <= 0 {
		minLen = 12
	}
	// A trailing {Tags:...} block is metadata, not garble. Several major groups
	// (Erai-raws, VARYG, Gecko, Himejoshi) append one, and it is almost entirely
	// ';' '=' ',' ':' — so a release with a long language list scored as
	// punctuation soup on the strength of its own structured metadata. Measured
	// against 20,000 catalogued titles this was 71 of the 96 false positives,
	// every one a real release. Stripped for the RATIO only; the rest of the
	// title is judged as it stands, and a genuinely garbled title does not
	// become clean by having braces at the end.
	t = reTrailingTagBlock.ReplaceAllString(t, "")

	// Runes, not bytes, on BOTH sides of the ratio. The numerator has always
	// counted runes; leaving the denominator as len(t) made the verdict depend
	// on how the title was encoded rather than on what it said — a multi-byte
	// script silently got a third of the divisor it should have had.
	// A RUN of the same mark is one piece of emphasis, not N pieces of garble.
	// Anime naming uses it constantly — "Keijo!!!!!!!!", "New Game!!",
	// "Ore Monogatari!!" — and counting each mark separately junked
	// "[HorchataScans] Keijo!!!!!!!! … [競女!!!!!!!!]" on the strength of the
	// show's own name. Garble is VARIED punctuation; "$&!@#=$%^&*=!@" survives
	// this collapse untouched because no two of its marks are adjacent twins.
	//
	// The length gate stays on the RAW rune count: collapsing first would let a
	// title that is nothing but one repeated mark shrink below minLen and escape
	// the rule entirely.
	var special, total, raw int
	prev := rune(-1)
	for _, c := range t {
		raw++
		if c == prev {
			continue
		}
		prev = c
		total++
		// unicode.IsLetter/IsDigit, not the ASCII isAlnum this used to call.
		// This rule means "the title is mostly spam-grade punctuation", and
		// with an ASCII-only test every CJK ideograph, kana and Hangul
		// syllable counted as punctuation — so a Japanese title scored
		// ~100% special and was dropped at ingest. On an anime indexer that
		// is not an edge case: it silently excluded the native-language
		// releases the catalogue most wants, and did it inside a rule whose
		// counter climbing looked like evidence it was working.
		//
		// Full-width punctuation (！ ／ 、) still counts as special, which is
		// correct — it IS punctuation, and a title carrying a couple of marks
		// stays far below the threshold.
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			continue
		}
		// Only ASCII punctuation counts. What this rule exists to catch is
		// keyboard soup — "$&!@#=$%^&*=!@", "${tmFKdpM6G(3^HdolJ[56NHAa|" —
		// and the obfuscation bots that generate it work in ASCII. Non-ASCII
		// punctuation is structure: 【】（）〔〕 are brackets, ・／ are
		// separators, │ divides fields, ： introduces one. We already allow
		// the ASCII []() forms, so counting their full-width equivalents as
		// garble junked real releases for using the punctuation of their own
		// script — 7 of the 8 remaining false positives against the
		// production corpus, including two audio releases and a radio show.
		if c > 127 {
			continue
		}
		switch c {
		case ' ', '.', '-', '_', '[', ']', '(', ')', '<', '>':
			continue
		}
		special++
	}
	if raw < minLen || total == 0 {
		return false
	}
	return float64(special)/float64(total) > 0.15
}

// randomWordsJunk: random alphanumeric words make up 70%+ of the non-punct
// characters — a single 12+ char random word, or several totalling 16+.
func randomWordsJunk(t string) bool {
	words := strings.Fields(t)
	if len(words) == 0 {
		return false
	}
	var junkChars, totalNonPunct int
	for _, w := range words {
		if isAllPunct(w) {
			continue
		}
		totalNonPunct += len(w)
		if isRandomWord(w) {
			junkChars += len(w)
		}
	}
	minChars := 16
	if len(words) <= 2 {
		minChars = 12 // catch short single-word junk like "EXxxUeCnKNXD"
	}
	return junkChars >= minChars && totalNonPunct > 0 &&
		float64(junkChars)/float64(totalNonPunct) >= 0.7
}

// isRandomWord: an 8+ char pure-alnum word mixing digits and letters. Real
// words are letters; real numbers are digits; the mix is machine output.
func isRandomWord(w string) bool {
	if len(w) < 8 {
		return false
	}
	var hasDigit, hasLetter bool
	for _, c := range w {
		if !isAlnum(c) {
			return false
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
		} else {
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

// isAllPunct reports whether every character in w is punctuation/separator.
func isAllPunct(w string) bool {
	for _, c := range w {
		if isAlnum(c) {
			return false
		}
	}
	return true
}

// countGarbledPunct counts characters from the "spam-grade" punctuation set —
// distinct from the release-style separators (.-_()[]) real titles use.
func countGarbledPunct(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case '|', '@', '#', '$', '%', '^', '&', '*', '<', '>', '=', '+', '~', '?', '{', '}', '!', '\\', ';', ':':
			n++
		}
	}
	return n
}

// multiSegmentChaos: min_chaotic+ segments (split on _ or space) of min_seg_len+
// chars that EACH mix upper, lower and digit. Real tokens rarely do all three,
// almost never in two segments. Gated on the alnum+underscore+space-only
// structural shape — real titles always carry other punctuation somewhere.
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

// ── small string helpers ──

func isAlnum(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// toLowerTail lowercases only the last 24 bytes — enough to match any release
// extension without allocating a lowercase copy of a long subject.
func toLowerTail(s string) string {
	const tail = 24
	if len(s) > tail {
		return strings.ToLower(s[len(s)-tail:])
	}
	return strings.ToLower(s)
}

// hasByteInRange reports whether any byte of s falls in [lo, hi]. Byte-range,
// NOT strings.ContainsAny (which takes a char set) — the gate checks are
// character-class membership (has-an-uppercase, has-a-digit).
func hasByteInRange(s string, lo, hi byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= lo && s[i] <= hi {
			return true
		}
	}
	return false
}
