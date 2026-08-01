package usenet

import "testing"

func stat(name string, pos int, hits int64, enabled bool) junkRuleStat {
	return junkRuleStat{Name: name, Position: pos, Hits: hits, Enabled: enabled}
}

// The readout exists to make one specific mismatch visible: a high-volume rule
// sitting late. Drift is the number that says so, and its SIGN has to be right
// — positive means "runs later than its catch rate justifies", which is the
// direction that costs CPU.
func TestRankHighlightsRulesThatRunTooLate(t *testing.T) {
	// The real shape prod was running: the 3.5B rule thirteenth, behind an
	// expensive one catching almost nothing.
	rows := rankJunkRules([]junkRuleStat{
		stat("long_alnum_run", 10, 6_000_000_000, true),
		stat("software_warez", 40, 20_000_000, true),
		stat("single_token_20", 130, 3_500_000_000, true),
	}, nil)

	by := map[string]junkOrderRow{}
	for _, r := range rows {
		by[r.Name] = r
	}

	if by["long_alnum_run"].Rank != 1 || by["single_token_20"].Rank != 2 || by["software_warez"].Rank != 3 {
		t.Fatalf("ranks wrong: %+v", by)
	}
	// single_token_20 runs 3rd but deserves 2nd → drift +1, the expensive way.
	if got := by["single_token_20"].Drift; got != 1 {
		t.Errorf("single_token_20 drift = %d, want +1 (runs later than it should)", got)
	}
	// software_warez runs 2nd but deserves 3rd → drift -1, running too early.
	if got := by["software_warez"].Drift; got != -1 {
		t.Errorf("software_warez drift = %d, want -1 (runs earlier than it should)", got)
	}
	if by["long_alnum_run"].Drift != 0 {
		t.Errorf("the top rule is already first; drift should be 0")
	}
	// Share is of total junk hits, so the biggest rule dominates.
	if s := by["long_alnum_run"].Share; s < 60 || s > 65 {
		t.Errorf("share = %.1f%%, want ~63%%", s)
	}
}

// Disabled rules are not evaluated, so ranking them would invent advice about
// a rule that costs nothing — and worse, would shift the ranks of the rules
// that DO run.
func TestDisabledRulesAreNotRanked(t *testing.T) {
	rows := rankJunkRules([]junkRuleStat{
		stat("live_a", 10, 100, true),
		stat("retired", 20, 999_999, false),
		stat("live_b", 30, 50, true),
	}, nil)
	for _, r := range rows {
		if r.Name == "retired" {
			if r.Rank != 0 || r.Drift != 0 {
				t.Errorf("disabled rule was ranked: %+v", r)
			}
			continue
		}
	}
	// And a disabled rule with huge hits must not displace the live ones.
	by := map[string]junkOrderRow{}
	for _, r := range rows {
		by[r.Name] = r
	}
	if by["live_a"].Rank != 1 || by["live_b"].Rank != 2 {
		t.Errorf("disabled rule distorted live ranks: %+v", by)
	}
}

// Sized rules only run on the build path, so ranking them against ingest rules
// would recommend moves that change nothing at ingest.
func TestSizedRulesRankInTheirOwnBand(t *testing.T) {
	sized := map[string]bool{"under_1mib": true, "under_5mib": true}
	rows := rankJunkRules([]junkRuleStat{
		stat("ingest_big", 10, 1_000_000, true),
		stat("under_1mib", 923, 4_000, true),
		stat("under_5mib", 924, 25_000, true),
	}, sized)

	by := map[string]junkOrderRow{}
	for _, r := range rows {
		by[r.Name] = r
	}
	if by["ingest_big"].Rank != 1 {
		t.Errorf("ingest rule should rank 1 in its own band, got %d", by["ingest_big"].Rank)
	}
	// under_5mib out-catches under_1mib, so it ranks first AMONG SIZED rules.
	if by["under_5mib"].Rank != 1 || by["under_1mib"].Rank != 2 {
		t.Errorf("sized band ranked wrongly: %+v", by)
	}

	order := recommendedOrder(rows)
	if order["ingest_big"] >= 900 {
		t.Errorf("an ingest rule was placed in the sized band: %d", order["ingest_big"])
	}
	if order["under_5mib"] < 900 || order["under_1mib"] < 900 {
		t.Errorf("sized rules must stay in the trailing band: %+v", order)
	}
	if order["under_5mib"] >= order["under_1mib"] {
		t.Errorf("the better-catching sized rule should come first: %+v", order)
	}
}

func TestRecommendedOrderIsSpacedAndKeepsDisabledInPlace(t *testing.T) {
	rows := rankJunkRules([]junkRuleStat{
		stat("a", 10, 300, true),
		stat("off", 20, 999, false),
		stat("b", 30, 200, true),
		stat("c", 40, 100, true),
	}, nil)
	order := recommendedOrder(rows)

	if order["a"] != 10 || order["b"] != 20 || order["c"] != 30 {
		t.Errorf("expected spacing of 10 by hit order, got %+v", order)
	}
	// A disabled rule keeps its stored position, so re-enabling puts it back
	// where the operator left it.
	if order["off"] != 20 {
		t.Errorf("disabled rule moved: %d, want its stored 20", order["off"])
	}
}

// Moving swaps positions rather than nudging a number, so spacing survives and
// the ends are no-ops instead of collisions.
func TestMoveSwapsWithinTheBand(t *testing.T) {
	rows := rankJunkRules([]junkRuleStat{
		stat("first", 10, 5, true),
		stat("second", 20, 4, true),
		stat("third", 30, 3, true),
	}, nil)

	up := moveJunkRule(rows, "third", true)
	if up["third"] != 20 || up["second"] != 30 {
		t.Errorf("move up did not swap: %+v", up)
	}
	if up["first"] != 10 {
		t.Errorf("an unrelated rule moved: %+v", up)
	}

	down := moveJunkRule(rows, "first", false)
	if down["first"] != 20 || down["second"] != 10 {
		t.Errorf("move down did not swap: %+v", down)
	}

	// Ends are no-ops — never a collision.
	if top := moveJunkRule(rows, "first", true); top["first"] != 10 {
		t.Errorf("moving the first rule up changed something: %+v", top)
	}
	if bot := moveJunkRule(rows, "third", false); bot["third"] != 30 {
		t.Errorf("moving the last rule down changed something: %+v", bot)
	}
	// An unknown or disabled rule leaves the order untouched.
	if un := moveJunkRule(rows, "nope", true); len(un) != 3 || un["first"] != 10 {
		t.Errorf("unknown rule perturbed the order: %+v", un)
	}
}

// Positions must stay unique: two rules sharing one makes the loader's
// tie-break the rule NAME, an ordering nobody chose and nothing displays.
func TestRecommendedOrderIsCollisionFree(t *testing.T) {
	var in []junkRuleStat
	for i, n := range []string{"a", "b", "c", "d", "e", "f"} {
		in = append(in, stat(n, (i+1)*10, int64(100-i), true))
	}
	in = append(in, stat("s1", 923, 5, true), stat("s2", 924, 9, true))
	rows := rankJunkRules(in, map[string]bool{"s1": true, "s2": true})

	seen := map[int]string{}
	for name, pos := range recommendedOrder(rows) {
		if other, dup := seen[pos]; dup {
			t.Fatalf("position %d assigned to both %s and %s", pos, other, name)
		}
		seen[pos] = name
	}
}
