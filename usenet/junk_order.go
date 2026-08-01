package usenet

import "sort"

// Recommending an evaluation order for the junk rules.
//
// `match` returns on the first rule that fires, and on a real feed the great
// majority of ingested articles ARE junk — measured at ~96% on this install.
// So the rules that catch the most should be reached first: every article a
// late rule eventually catches has paid for every rule above it.
//
// That is not hypothetical. Production ran with a rule catching 3.5 BILLION
// articles at position 130, behind one costing 81% of the engine's measured
// CPU for 0.3% of the catches. Nothing surfaced the mismatch, because hit
// counts and evaluation order were never shown together.
//
// The recommendation is deliberately naive — rank by hits, descending — and
// deliberately advisory. It is not applied automatically, for two reasons:
// order decides which rule is CREDITED in filter_hits when two rules both
// match, so reordering silently rewrites the history an operator tunes
// against; and hit counts are lifetime totals, so a rule that mattered last
// year can outrank one that matters now.

// junkOrderRow is one rule with its recommendation attached.
type junkOrderRow struct {
	junkRuleStat
	// Rank is the position this rule WOULD hold if ordered purely by hits,
	// 1-based among enabled rules.
	Rank int
	// Share is this rule's percentage of all junk hits.
	Share float64
	// Drift is how far the rule sits from its recommended slot: positive
	// means it runs LATER than its catch rate justifies, which is the
	// expensive direction.
	Drift int
	// Sized marks a rule that only runs on the build path (it needs the
	// release size), so its position relative to ingest rules is moot.
	Sized bool
	// HitsFmt is the thousands-separated count. Formatted in Go rather than
	// via a template helper: these admin fragments are parsed without a
	// FuncMap, so a {{num .Hits}} would fail the WHOLE render at runtime with
	// nothing failing at compile time.
	HitsFmt string
}

// rankJunkRules pairs each rule with its hit-ranked position.
//
// Disabled rules keep their place in the listing but take no rank: they are
// not evaluated, so recommending a position for them would be noise. Sized
// rules are ranked among themselves — they never run on the ingest path, so
// comparing them to ingest rules would suggest moves that change nothing.
func rankJunkRules(stats []junkRuleStat, sized map[string]bool) []junkOrderRow {
	var total int64
	for _, s := range stats {
		if s.Enabled {
			total += s.Hits
		}
	}

	rows := make([]junkOrderRow, len(stats))
	for i, s := range stats {
		rows[i] = junkOrderRow{junkRuleStat: s, Sized: sized[s.Name], HitsFmt: fmtComma(s.Hits)}
		if total > 0 {
			rows[i].Share = 100 * float64(s.Hits) / float64(total)
		}
	}

	// Rank the ingest rules by hits, then the sized ones, each among their own
	// kind and in their existing relative band.
	for _, band := range []bool{false, true} {
		idx := make([]int, 0, len(rows))
		for i, r := range rows {
			if r.Enabled && r.Sized == band {
				idx = append(idx, i)
			}
		}
		sort.SliceStable(idx, func(a, b int) bool {
			return rows[idx[a]].Hits > rows[idx[b]].Hits
		})
		for rank, i := range idx {
			rows[i].Rank = rank + 1
		}
	}

	// Drift compares where a rule sits among evaluated rules to where its
	// catch rate says it should sit. Computed on ORDINAL position, not the
	// stored `position` number, because those are spaced (10, 20, …) and the
	// gaps are not meaningful.
	ordinal := map[string]int{}
	for _, band := range []bool{false, true} {
		n := 0
		for _, r := range rows {
			if r.Enabled && r.Sized == band {
				n++
				ordinal[r.Name] = n
			}
		}
	}
	for i := range rows {
		if rows[i].Rank > 0 {
			rows[i].Drift = ordinal[rows[i].Name] - rows[i].Rank
		}
	}
	return rows
}

// recommendedOrder returns the positions that would sort the rules by hits.
//
// Spaced by 10 so a later rule can be slotted between two without renumbering
// everything, and sized rules keep a trailing band of their own so they never
// interleave with ingest rules. Disabled rules keep their current position:
// re-enabling one should put it back where the operator left it, not wherever
// a rank computed while it was off happens to fall.
func recommendedOrder(rows []junkOrderRow) map[string]int {
	out := make(map[string]int, len(rows))
	for _, band := range []struct {
		sized bool
		base  int
	}{{false, 10}, {true, 900}} {
		idx := make([]int, 0, len(rows))
		for i, r := range rows {
			if r.Enabled && r.Sized == band.sized {
				idx = append(idx, i)
			}
		}
		sort.SliceStable(idx, func(a, b int) bool {
			return rows[idx[a]].Hits > rows[idx[b]].Hits
		})
		for n, i := range idx {
			out[rows[i].Name] = band.base + n*10
		}
	}
	for _, r := range rows {
		if !r.Enabled {
			out[r.Name] = r.Position
		}
	}
	return out
}

// moveJunkRule returns the order with one rule shifted one slot up or down
// among the rules it actually competes with.
//
// Swapping ordinals rather than nudging the stored number keeps the spacing
// intact and makes the move a no-op at the ends, instead of silently
// colliding two rules onto one position.
func moveJunkRule(rows []junkOrderRow, name string, up bool) map[string]int {
	cur := make(map[string]int, len(rows))
	for _, r := range rows {
		cur[r.Name] = r.Position
	}
	var band []junkOrderRow
	var target junkOrderRow
	found := false
	for _, r := range rows {
		if r.Name == name {
			target, found = r, true
		}
	}
	if !found || !target.Enabled {
		return cur
	}
	for _, r := range rows {
		if r.Enabled && r.Sized == target.Sized {
			band = append(band, r)
		}
	}
	sort.SliceStable(band, func(a, b int) bool { return band[a].Position < band[b].Position })
	for i, r := range band {
		if r.Name != name {
			continue
		}
		j := i + 1
		if up {
			j = i - 1
		}
		if j < 0 || j >= len(band) {
			return cur // already at the end of its band
		}
		cur[band[i].Name], cur[band[j].Name] = band[j].Position, band[i].Position
		return cur
	}
	return cur
}
