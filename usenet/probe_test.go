package usenet

import (
	"testing"

	"github.com/the-loon-clan/loon/nntp"
)

// ovs builds an ascending overview slice with message-ids "<n@test>" for the
// given numbers, the shape Overview() returns.
func ovs(nums ...int) []nntp.MessageOverview {
	out := make([]nntp.MessageOverview, 0, len(nums))
	for _, n := range nums {
		out = append(out, nntp.MessageOverview{
			MessageNumber: n,
			MessageId:     "<" + string(rune('a'+n%26)) + "@test>",
		})
	}
	return out
}

func TestCompareNumberingSameBackbone(t *testing.T) {
	over := ovs(101, 102, 103, 104)
	// Candidate agrees on every number.
	byID := map[string]int{}
	for _, o := range over {
		byID[o.MessageId] = o.MessageNumber
	}
	v := compareNumbering(over, func(id string) (int, bool) {
		n, ok := byID[id]
		return n, ok
	})
	if !v.Same || v.Compared != 4 || v.Matched != 4 {
		t.Fatalf("want Same 4/4, got %+v", v)
	}
}

func TestCompareNumberingDistinctBackbone(t *testing.T) {
	over := ovs(101, 102, 103, 104)
	// Candidate has the articles but under its own numbering: one mismatch
	// is enough to lose Same even when most numbers coincide.
	v := compareNumbering(over, func(id string) (int, bool) {
		for _, o := range over {
			if o.MessageId == id {
				if o.MessageNumber == 103 {
					return 9999, true
				}
				return o.MessageNumber, true
			}
		}
		return 0, false
	})
	if v.Same || v.Compared != 4 || v.Matched != 3 {
		t.Fatalf("want not-Same 3/4, got %+v", v)
	}
}

func TestCompareNumberingInconclusiveBelowMinimum(t *testing.T) {
	// Only two articles overlap — matching perfectly is still not a verdict.
	over := ovs(101, 102, 103, 104)
	v := compareNumbering(over, func(id string) (int, bool) {
		for _, o := range over[:2] {
			if o.MessageId == id {
				return o.MessageNumber, true
			}
		}
		return 0, false
	})
	if v.Same || v.Compared != 2 || v.Matched != 2 {
		t.Fatalf("want inconclusive 2/2, got %+v", v)
	}
}

func TestCompareNumberingSkipsJunkAndCaps(t *testing.T) {
	// 12 clean rows + a blank-id row and a zero-number row mixed in. The walk
	// is newest-first and stops at probeSampleSize comparisons.
	over := ovs(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)
	over = append(over, nntp.MessageOverview{MessageNumber: 13, MessageId: ""})
	over = append(over, nntp.MessageOverview{MessageNumber: 0, MessageId: "<zero@test>"})
	var asked []string
	v := compareNumbering(over, func(id string) (int, bool) {
		asked = append(asked, id)
		for _, o := range over {
			if o.MessageId == id {
				return o.MessageNumber, true
			}
		}
		return 0, false
	})
	if !v.Same || v.Compared != probeSampleSize || v.Matched != probeSampleSize {
		t.Fatalf("want Same %d/%d, got %+v", probeSampleSize, probeSampleSize, v)
	}
	if len(asked) != probeSampleSize {
		t.Fatalf("stat called %d times, want %d (junk rows must not consume the budget)", len(asked), probeSampleSize)
	}
	// Newest clean row first: number 12.
	if asked[0] != over[11].MessageId {
		t.Fatalf("first stat was %q, want the newest clean article %q", asked[0], over[11].MessageId)
	}
}
