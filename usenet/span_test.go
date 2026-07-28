package usenet

import "testing"

// Article numbers ascend with posting time, so a set's span is how far apart
// its articles sit on the server. One upload happens in a single run, so a real
// release is near-contiguous even with other posters interleaved. A span orders
// of magnitude wider than that is not interleaving — it is a base subject
// generic enough to have merged unrelated posts into one set, which can never
// complete because it waits on files belonging to somebody else's upload.
func TestSpanDetectsBaseCollisions(t *testing.T) {
	// A real release: 2,000 articles posted in a run, other posters interleaved
	// so it covers rather more article numbers than it owns.
	real := pendingSet{Have: 2000, ArtLo: 1_000_000, ArtHi: 1_020_000}
	if real.Span() != 20_001 {
		t.Errorf("span = %d, want 20,001", real.Span())
	}
	if real.Collided() {
		t.Error("a normal release with interleaved posters read as a collision")
	}

	// A set mid-arrival holds few articles while already covering a real range.
	// The first version of this scaled the threshold with articles held and
	// flagged exactly these — the sets an operator is actively watching.
	early := pendingSet{Have: 4, ArtLo: 4_100, ArtHi: 5_900}
	if early.Collided() {
		t.Errorf("a 4-article release spanning %d numbers read as a collision; "+
			"the threshold is scaling with articles held", early.Span())
	}

	// A collision: articles spread across two million article numbers, which is
	// most of a day of posting. These cannot be one upload.
	collided := pendingSet{Have: 16, ArtLo: 1_000_000, ArtHi: 3_000_000}
	if !collided.Collided() {
		t.Errorf("16 articles across %d article numbers must read as a collision",
			collided.Span())
	}
}

// A merged set inherits somebody else's declared file count, so an absurd Need
// must neither excuse nor manufacture a collision verdict — only the span
// decides.
func TestCollisionIgnoresDeclaredNeed(t *testing.T) {
	p := pendingSet{Have: 10, Need: 5_000_000, ArtLo: 1, ArtHi: 2_000_000}
	if !p.Collided() {
		t.Error("an absurd declared Need must not excuse an absurd span")
	}
}

// Unknown bounds must read as "no information", never as a collision: sets
// staged before span tracking existed have none, and flagging them would fill
// the card with false alarms on exactly the sets an operator is watching.
func TestUnknownSpanIsNotACollision(t *testing.T) {
	for _, p := range []pendingSet{
		{Have: 10},                            // no bounds at all
		{Have: 10, ArtLo: 500},                // lo only
		{Have: 10, ArtLo: 900, ArtHi: 100},    // inverted
		{Have: 0, ArtLo: 1, ArtHi: 5_000_000}, // no articles held
		// hi WITHOUT lo is the dangerous one: article numbers run into the
		// billions, so treating a missing lo as zero yields a span of the whole
		// group and flags a perfectly healthy set as a collision. Every set
		// staged before span tracking existed looks exactly like this.
		{Have: 10, ArtHi: 1_800_000_000},
	} {
		if p.Collided() {
			t.Errorf("%+v flagged as a collision on incomplete information", p)
		}
		if p.Span() < 0 {
			t.Errorf("%+v produced a negative span", p)
		}
	}
}

// A single-article set is the commonest shape in the whole pipeline (obfuscated
// spam), and its span is 1. It must never trip the ratio.
func TestSingleArticleSetIsNeverACollision(t *testing.T) {
	p := pendingSet{Have: 1, ArtLo: 42, ArtHi: 42}
	if p.Span() != 1 {
		t.Errorf("span = %d, want 1", p.Span())
	}
	if p.Collided() {
		t.Error("a one-article set read as a collision")
	}
}
