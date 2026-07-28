package usenet

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Corpus audit: runs the live junk engine over titles already IN the production
// catalogue. Every hit is a false positive by construction — a release we
// accepted and would now drop.
//
// Skips unless USENET_AUDIT_CORPUS points at a title<TAB>size file, so it costs
// nothing in CI. Run it before shipping any junk-rule change: the rules are
// heuristics over adversarial data, and reasoning about them is unreliable. This
// harness found 96 false positives in 20,000 catalogued titles and drove them to
// 27 across three fixes (CJK-as-punctuation, {Tags:} metadata blocks, repeated
// punctuation runs) — none of which were visible by inspection. Export a corpus
// with:
//
//	SELECT title, COALESCE(size,0) FROM nzbs WHERE title ~ '[^[:ascii:]]'
//
// weighted toward non-ASCII, which is where the ASCII assumptions bite.
func TestJunkCorpusAudit(t *testing.T) {
	path := os.Getenv("USENET_AUDIT_CORPUS")
	if path == "" {
		t.Skip("no corpus")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	type hit struct{ title string }
	byRule := map[string][]hit{}
	nonASCII, total := 0, 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 2)
		title := parts[0]
		if title == "" {
			continue
		}
		var size int64
		if len(parts) > 1 {
			size, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		}
		total++
		wide := false
		for _, r := range title {
			if r > 127 {
				wide = true
				break
			}
		}
		if wide {
			nonASCII++
		}
		// Sized form: the builder knows the release size, so this is the verdict
		// that actually decides whether a release survives.
		if rule := whichJunkRuleSized(title, size); rule != "" {
			byRule[rule] = append(byRule[rule], hit{title})
		}
	}

	rules := make([]string, 0, len(byRule))
	for r := range byRule {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return len(byRule[rules[i]]) > len(byRule[rules[j]]) })

	bad := 0
	for _, r := range rules {
		bad += len(byRule[r])
	}
	t.Logf("corpus %d titles (%d non-ASCII) — %d would be junked (%.2f%%)",
		total, nonASCII, bad, 100*float64(bad)/float64(max(total, 1)))
	for _, r := range rules {
		hits := byRule[r]
		t.Logf("  %-24s %5d", r, len(hits))
		for i, h := range hits {
			if i >= 4 {
				break
			}
			t.Logf("        %s", h.title)
		}
	}
}
