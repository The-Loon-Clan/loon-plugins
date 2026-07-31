package usenet

import (
	"regexp/syntax"
	"testing"
)

func TestWhyNoGate(t *testing.T) {
	specs, _ := parseJunkRulesTSV(junkSeedPath)
	for _, s := range specs {
		if s.Name != "software_warez" {
			continue
		}
		re, _ := syntax.Parse(s.Rule, syntax.Perl)
		re = re.Simplify()
		alt := re.Sub[0]
		for i, b := range alt.Sub {
			if got := requiredLiterals(b); len(got) == 0 {
				t.Logf("branch %d has NO literal: op=%v %s", i, b.Op, b.String())
			} else if len(got[0]) < 2 {
				t.Logf("branch %d literal too short: %q", i, got[0])
			}
		}
	}
}
