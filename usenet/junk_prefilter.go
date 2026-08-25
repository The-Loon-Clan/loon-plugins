package usenet

import (
	"regexp/syntax"
	"strings"
)

// A literal pre-filter for the junk engine.
//
// Measured on prod (45s CPU profile, worker at ~3.8 cores): parseOverviews is
// 70% of the worker's CPU and the junk engine is 42% of the whole process —
// 24 compiled rules run as full regexes against every subject the crawler
// reads. Most of those rules cannot possibly match most subjects, and the
// automaton is what proves it, one backtrack at a time (regexp.backtrack alone
// was 27%).
//
// So: derive from each pattern a set of literals where AT LEAST ONE must
// appear in any string the pattern can match, and check that with
// strings.Contains before paying for the regex.
//
// The whole design rests on being CONSERVATIVE. requiredLiterals returns nil —
// meaning "no pre-filter, always run the regex" — for anything it cannot prove,
// so the pre-filter can only ever skip work the regex would have declined
// anyway. A wrong literal here would silently stop junk being caught, which is
// the one outcome worse than the CPU cost.

// literalGate is the precomputed guard for one rule. Empty Any means no gate.
type literalGate struct {
	// Any holds alternatives: the subject must contain at least one. They are
	// stored lowercased when Fold is set.
	Any []string
	// Fold marks a case-insensitive pattern, so the haystack is lowercased
	// before the check.
	Fold bool
}

// pass reports whether s could possibly match. A nil/empty gate passes
// everything, which is the safe default.
func (g literalGate) pass(s string) bool {
	if len(g.Any) == 0 {
		return true
	}
	if g.Fold {
		s = strings.ToLower(s)
	}
	for _, lit := range g.Any {
		if strings.Contains(s, lit) {
			return true
		}
	}
	return false
}

// minGateLen is the shortest literal the gate will accept. Two, not three:
// a short alternative is still a CORRECT requirement, only a less selective
// one, and dropping the whole gate because of it throws away the other
// forty-eight. That is not hypothetical — software_warez is a 49-branch
// alternation costing 84% of the junk engine, and a single short branch was
// disabling its gate entirely. A one-rune literal is excluded because it
// appears in essentially every subject, so the Contains scan buys nothing.
const minGateLen = 2

// buildLiteralGate parses a rule's pattern and returns its gate.
//
// Returns an empty gate (pass-everything) whenever the analysis cannot prove a
// required literal — an unparseable pattern, an alternation with a
// literal-free branch, a top-level optional, and so on.
func buildLiteralGate(pattern string) literalGate {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return literalGate{}
	}
	re = re.Simplify()
	// Fold comes from the parse TREE, not the pattern text. A fold flag has
	// several spellings — (?i:...), (?is), (?si), mid-pattern (?i) — and the
	// parser stores every folded literal min-folded (uppercase for ASCII), so
	// a textual "(?i)" scan gave (?i:keygen) the gate {["KEYGEN"], no fold}:
	// lowercase subjects failed the Contains and the regex never ran — a
	// silently dead rule with zero filter_hits, the exact outcome the header
	// above forbids. Folding is per-literal in the tree (EXACT(?i)folded
	// folds only the tail); one tree-wide bool stays conservative, because
	// lowercasing an exact-case literal together with the haystack only ever
	// WIDENS the gate.
	fold := hasFoldedLiteral(re)
	lits := requiredLiterals(re)
	out := make([]string, 0, len(lits))
	for _, l := range lits {
		if len([]rune(l)) < minGateLen {
			// Too common to be worth scanning for, and it would pass nearly
			// everything anyway — so the gate as a whole stops paying.
			return literalGate{}
		}
		if fold {
			l = strings.ToLower(l)
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return literalGate{}
	}
	return literalGate{Any: out, Fold: fold}
}

// requiredLiterals returns literals of which at least one must appear in any
// string the expression matches, or nil when none can be proven.
//
// The rules, and why each is safe:
//
//	Literal          → itself is required.
//	Concat           → any ONE child's requirement is enough (all children must
//	                   match, so any child's requirement holds for the whole).
//	                   We take the longest, since longer is more selective.
//	Alternate        → EVERY branch must contribute, and the result is the
//	                   union: a subject matching the expression matched some
//	                   branch, so it contains that branch's literal. One
//	                   literal-free branch collapses the whole thing to nil.
//	Capture          → transparent.
//	Star/Quest/Repeat with min 0 → matches empty, so nothing is required.
//	Plus/Repeat with min >= 1    → the body must appear at least once.
//	anything else    → nil (unknown, so no gate).
// hasFoldedLiteral reports whether any literal anywhere in the tree carries
// the parser's FoldCase flag — the oracle buildLiteralGate trusts for Fold.
func hasFoldedLiteral(re *syntax.Regexp) bool {
	if re.Op == syntax.OpLiteral && re.Flags&syntax.FoldCase != 0 {
		return true
	}
	for _, sub := range re.Sub {
		if hasFoldedLiteral(sub) {
			return true
		}
	}
	return false
}

func requiredLiterals(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral:
		return []string{string(re.Rune)}

	case syntax.OpCapture:
		if len(re.Sub) == 1 {
			return requiredLiterals(re.Sub[0])
		}
		return nil

	case syntax.OpConcat:
		best := []string(nil)
		bestLen := 0
		for _, sub := range re.Sub {
			got := requiredLiterals(sub)
			// Only single-literal requirements compose safely here: an
			// alternation nested in a concat contributes a SET, and mixing
			// sets from several children would need a cross product to stay
			// correct. Take the best single literal instead.
			if len(got) == 1 && len(got[0]) > bestLen {
				best, bestLen = got, len(got[0])
			}
		}
		if best != nil {
			return best
		}
		// No single-literal child: fall back to a child that is itself a pure
		// alternation of literals, which is still a valid any-of set.
		for _, sub := range re.Sub {
			if got := requiredLiterals(sub); len(got) > 1 {
				return got
			}
		}
		return nil

	case syntax.OpAlternate:
		var all []string
		for _, sub := range re.Sub {
			got := requiredLiterals(sub)
			if len(got) == 0 {
				// A branch with no requirement means a matching subject need
				// contain none of the others.
				return nil
			}
			all = append(all, got...)
		}
		return all

	case syntax.OpPlus:
		if len(re.Sub) == 1 {
			return requiredLiterals(re.Sub[0])
		}
		return nil

	case syntax.OpRepeat:
		if re.Min >= 1 && len(re.Sub) == 1 {
			return requiredLiterals(re.Sub[0])
		}
		return nil

	default:
		// OpStar, OpQuest, OpCharClass, OpAnyChar, anchors, empty-width
		// assertions: none of them require a specific literal.
		return nil
	}
}
