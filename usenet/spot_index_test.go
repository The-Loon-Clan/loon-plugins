package usenet

import (
	"testing"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// The forward walk. Every case here is a way to silently lose spots rather
// than to fail: a range that repeats forever, one that skips, or one that
// walks numbers the server no longer holds.
func TestSpotForwardRange(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mark, low, high  int64
		batch            int
		wantFrom, wantTo int64
		wantOK           bool
	}{
		// A watermark of 0 never reaches here in practice — setSpotGroupExtent
		// seeds it to server_high on first sight, precisely so the forward pass
		// does not walk the history the backfill owns.
		{"unseeded watermark would walk history", 0, 3118, 5906734, 1000, 3118, 4117, true},
		{"resumes one past the watermark", 4117, 3118, 5906734, 1000, 4118, 5117, true},
		{"clamps to the server high", 5906000, 3118, 5906734, 1000, 5906001, 5906734, true},
		{"caught up entirely", 5906734, 3118, 5906734, 1000, 0, 0, false},
		{"watermark past the high (server rolled back)", 5906999, 3118, 5906734, 1000, 0, 0, false},
		// Articles expired while we were away: resuming at the stale watermark
		// would XOVER a range that is guaranteed empty, every pass, forever.
		{"watermark below the floor skips the gap", 100, 3118, 5906734, 1000, 3118, 4117, true},
		{"zero batch falls back to the default", 0, 1, 100000, 0, 1, int64(defaultSpotBatchSize), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to, ok := spotForwardRange(tc.mark, tc.low, tc.high, tc.batch)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (from != tc.wantFrom || to != tc.wantTo) {
				t.Errorf("range = %d-%d, want %d-%d", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

// The backward walk, and specifically how it ENDS. A backfill that never
// reports done is retried every pass forever; one that reports done early
// leaves history unread and no way to notice.
func TestSpotBackRange(t *testing.T) {
	for _, tc := range []struct {
		name             string
		back, low        int64
		batch            int
		wantFrom, wantTo int64
		wantDone, wantOK bool
	}{
		{"first step down from the high", 5906734, 3118, 1000, 5905734, 5906733, false, true},
		{"continues below", 5905734, 3118, 1000, 5904734, 5905733, false, true},
		// The last partial page must be READ and marked done in the same step,
		// not read now and finished on some later pass that may never come.
		{"final partial range finishes it", 3500, 3118, 1000, 3118, 3499, true, true},
		{"exactly one batch to the floor", 4118, 3118, 1000, 3118, 4117, true, true},
		{"already at the floor is done", 3118, 3118, 1000, 0, 0, true, false},
		{"below the floor is done", 3000, 3118, 1000, 0, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to, done, ok := spotBackRange(tc.back, tc.low, tc.batch)
			if ok != tc.wantOK || done != tc.wantDone {
				t.Fatalf("ok/done = %v/%v, want %v/%v", ok, done, tc.wantOK, tc.wantDone)
			}
			if ok && (from != tc.wantFrom || to != tc.wantTo) {
				t.Errorf("range = %d-%d, want %d-%d", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

// Walking the whole history must terminate, cover every article exactly once,
// and never read the same page twice. Asserted by running the real steppers to
// exhaustion rather than by reasoning about them.
func TestSpotBackfillCoversEverythingAndTerminates(t *testing.T) {
	const low, high = 3118, 20117 // 17,000 articles
	seen := map[int64]int{}
	back := int64(high)
	steps := 0
	for {
		from, to, done, ok := spotBackRange(back, low, 1000)
		if !ok {
			break
		}
		steps++
		if steps > 1000 {
			t.Fatal("backfill did not terminate")
		}
		for i := from; i <= to; i++ {
			seen[i]++
		}
		if done {
			break
		}
		back = from
	}
	for i := int64(low); i < high; i++ {
		switch seen[i] {
		case 1:
		case 0:
			t.Fatalf("article %d was never read", i)
		default:
			t.Fatalf("article %d was read %d times", i, seen[i])
		}
	}
}

func TestSpotRowsFromOverview(t *testing.T) {
	const spotFrom = "Paaldanser <KEYDATA@27a02b00c08d13z00.3365188124.20.1786812549.1.NL.SIGDATA>"
	over := []nntp.MessageOverview{
		{MessageNumber: 10, MessageId: "<a@spot.net>", From: spotFrom, Subject: "Some Release"},
		// An ordinary post. free.pt carries them and they are not a fault.
		{MessageNumber: 11, MessageId: "<b@example.com>", From: "Bob <bob@example.com>", Subject: "hello"},
		// No message-id: unusable, since it is the key and the signed content.
		{MessageNumber: 12, MessageId: "", From: spotFrom},
	}
	rows := spotRowsFromOverview("free.pt", over)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.MessageID != "<a@spot.net>" || r.GroupName != "free.pt" || r.ArticleNum != 10 {
		t.Errorf("identity = %q %q %d", r.MessageID, r.GroupName, r.ArticleNum)
	}
	if r.Poster != "Paaldanser" || r.Subject != "Some Release" {
		t.Errorf("poster/subject = %q / %q", r.Poster, r.Subject)
	}
	if r.SizeBytes != 3365188124 {
		t.Errorf("size = %d — a value past int4, which is why the column is bigint", r.SizeBytes)
	}
	if r.PostedAt.Unix() != 1786812549 {
		t.Errorf("posted = %v", r.PostedAt)
	}
	if len(r.SubCats) == 0 || r.SubCats[0] != "02a02" {
		t.Errorf("subcats = %v, want the XML form the category table is keyed on", r.SubCats)
	}
}

// Date is poster-controlled and routinely wrong; the From tuple's timestamp is
// part of what the header signature covers. The header wins where both exist,
// and Date is the fallback rather than the source.
func TestSpotRowsPreferTheSignedTimestamp(t *testing.T) {
	wrong := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := spotRowsFromOverview("free.pt", []nntp.MessageOverview{{
		MessageNumber: 1, MessageId: "<a@spot.net>", Date: wrong,
		From: "P <K@27a02.100.20.1786812549.1.NL.SIG>",
	}})
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].PostedAt.Year() == 1999 {
		t.Error("the Date header won over the signed timestamp")
	}
	if rows[0].PostedAt.Unix() != 1786812549 {
		t.Errorf("posted = %v", rows[0].PostedAt)
	}
}

// The division of labour on a group's FIRST pass, which is where the two
// halves can silently do each other's work.
//
// setSpotGroupExtent seeds both watermarks to server_high on first sight. If
// only back_watermark were seeded, high_watermark would stay 0 and the forward
// pass would treat all 5.9M articles as new — walking the history from the
// bottom while the backfill walked the same history from the top. Every
// article read twice, newest spots arriving last, and no error anywhere.
func TestFreshSpotGroupSplitsForwardFromBackfill(t *testing.T) {
	const low, high = 3118, 5906734
	// Seeded state, as the store writes it on first sight.
	seededHigh, seededBack := int64(high), int64(high)

	if _, _, ok := spotForwardRange(seededHigh, low, high, 1000); ok {
		t.Error("the forward pass has work to do on a fresh group — it would walk the history backfill owns")
	}
	from, to, done, ok := spotBackRange(seededBack, low, 1000)
	if !ok || done {
		t.Fatalf("backfill on a fresh group: ok=%v done=%v, want work to do", ok, done)
	}
	if to != high-1 {
		t.Errorf("backfill starts at %d, want just below the server high (%d)", to, high-1)
	}
	if to-from+1 != 1000 {
		t.Errorf("range holds %d articles, want the full batch", to-from+1)
	}

	// And once real articles arrive, forward picks them up and backfill is
	// unaffected — the two never touch the same number.
	newHigh := int64(high + 500)
	ffrom, fto, ok := spotForwardRange(seededHigh, low, newHigh, 1000)
	if !ok {
		t.Fatal("forward found no work after new articles arrived")
	}
	if ffrom != high+1 || fto != newHigh {
		t.Errorf("forward range = %d-%d, want %d-%d", ffrom, fto, high+1, newHigh)
	}
	if ffrom <= to {
		t.Errorf("forward (%d) overlaps backfill (%d)", ffrom, to)
	}
}
