package magic

import "math"

// The level curve, alone in a file so the tests can hold it still.
//
// Experience is points spent casting — practice, not luck. The curve is
// square-root so early levels come quickly and the tail stretches: level 1
// at 200 xp, 2 at 800, 3 at 1,800, 5 at 5,000, 10 (the cap) at 20,000.
//
// What a level buys:
//
//	discount   2% off every cast per level, capped at 20% — mastery makes
//	           magic cheaper, never free
//	reach      the duration cap grows from 48h by 48h per level, inside
//	           the genre's hard ceiling of 360h (15 days)
//	custom     ratio pairs beyond the preset buffs unlock at level 1 —
//	           you learn the classics before you improvise
const (
	levelCap     = 10
	maxHoursCap  = 360
	minHours     = 24
	customFromLv = 1
)

// levelFor converts experience to a level.
func levelFor(xp int64) int {
	if xp <= 0 {
		return 0
	}
	lv := int(math.Sqrt(float64(xp) / 200))
	if lv > levelCap {
		return levelCap
	}
	return lv
}

// discountPct is the level's price break.
func discountPct(level int) int {
	d := level * 2
	if d > 20 {
		return 20
	}
	return d
}

// maxHours is the level's duration reach.
func maxHours(level int) int {
	h := 48 + level*48
	if h > maxHoursCap {
		return maxHoursCap
	}
	return h
}

// customAllowed reports whether the level may name its own ratio pair.
func customAllowed(level int) bool { return level >= customFromLv }
