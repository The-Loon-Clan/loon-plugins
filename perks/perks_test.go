package perks

import (
	"testing"
	"time"
)

func TestFactorsDefaultToPlainAccounting(t *testing.T) {
	tab := NewTable()
	// Nobody has anything: 1:1, which is what the tracker does with no
	// multiplier wired at all. A perk table that answered anything else would
	// change every announce on the site the moment it was installed.
	if up, down := tab.Factors(1, "aa", time.Now()); up != 1 || down != 1 {
		t.Errorf("empty table gave (%v,%v), want (1,1)", up, down)
	}
	// And a member holding a perk on a DIFFERENT torrent gets nothing here.
	tab.Replace([]Active{{UserID: 1, InfoHash: "bb", Kind: Freeleech}})
	if up, down := tab.Factors(1, "aa", time.Now()); up != 1 || down != 1 {
		t.Errorf("perk on another torrent leaked: (%v,%v)", up, down)
	}
}

func TestTheTwoPerks(t *testing.T) {
	now := time.Now()
	tab := NewTable()

	tab.Replace([]Active{{UserID: 1, InfoHash: "aa", Kind: Freeleech}})
	if up, down := tab.Factors(1, "aa", now); up != 1 || down != 0 {
		t.Errorf("freeleech = (%v,%v), want (1,0) — upload still counts", up, down)
	}
	tab.Replace([]Active{{UserID: 1, InfoHash: "aa", Kind: UploadDouble}})
	if up, down := tab.Factors(1, "aa", now); up != 2 || down != 1 {
		t.Errorf("upload2x = (%v,%v), want (2,1) — download unaffected", up, down)
	}
	// Both at once is the obvious combination and must compose, not conflict.
	tab.Replace([]Active{
		{UserID: 1, InfoHash: "aa", Kind: Freeleech},
		{UserID: 1, InfoHash: "aa", Kind: UploadDouble},
	})
	if up, down := tab.Factors(1, "aa", now); up != 2 || down != 0 {
		t.Errorf("both = (%v,%v), want (2,0)", up, down)
	}
}

// Two upload tokens on one torrent is 2x, never 4x. The unique index stops a
// member buying that; this makes it harmless if one ever slips through.
func TestUploadDoesNotCompound(t *testing.T) {
	tab := NewTable()
	tab.Replace([]Active{
		{UserID: 1, InfoHash: "aa", Kind: UploadDouble},
		{UserID: 1, InfoHash: "aa", Kind: UploadDouble},
		{UserID: 1, InfoHash: "aa", Kind: UploadDouble},
	})
	if up, _ := tab.Factors(1, "aa", time.Now()); up != 2 {
		t.Errorf("three upload tokens gave %vx, want 2x", up)
	}
}

// The table is refreshed on a timer, so between refreshes it can still be
// holding a token that lapsed a minute ago. Crediting a perk somebody no
// longer has is worse than the cost of checking the clock.
func TestExpiredPerksStopApplying(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	tab := NewTable()
	tab.Replace([]Active{
		{UserID: 1, InfoHash: "aa", Kind: Freeleech, ExpiresAt: now.Add(-time.Minute)},
	})
	if up, down := tab.Factors(1, "aa", now); up != 1 || down != 1 {
		t.Errorf("expired perk still applied: (%v,%v)", up, down)
	}
	// A zero expiry means no expiry, not "expired at the epoch".
	tab.Replace([]Active{{UserID: 1, InfoHash: "aa", Kind: Freeleech}})
	if _, down := tab.Factors(1, "aa", now); down != 0 {
		t.Error("a token with no expiry was treated as expired")
	}
}

// Hit-and-run asks this: a site that told somebody a download was free has
// already said what that download owes.
func TestFreeleechIsVisibleToHitAndRun(t *testing.T) {
	now := time.Now()
	tab := NewTable()
	tab.Replace([]Active{
		{UserID: 1, InfoHash: "aa", Kind: Freeleech},
		{UserID: 1, InfoHash: "bb", Kind: UploadDouble},
	})
	if !tab.HasFreeleech(1, "aa", now) {
		t.Error("HasFreeleech = false on a freeleech torrent")
	}
	// An upload perk is not a freeleech perk — it says nothing about what was
	// taken, so it must not excuse a snatch.
	if tab.HasFreeleech(1, "bb", now) {
		t.Error("HasFreeleech = true for an upload-only perk")
	}
	if tab.HasFreeleech(2, "aa", now) {
		t.Error("one member's freeleech leaked to another")
	}
}

func TestOnlyKnownKindsDoAnything(t *testing.T) {
	if !Known(Freeleech) || !Known(UploadDouble) {
		t.Fatal("a real kind was rejected")
	}
	if Known("freeleach") || Known("") {
		t.Error("an unknown kind was accepted — a typo in a store item would mint dead tokens")
	}
	// And an unknown kind in the table changes nothing, rather than everything.
	tab := NewTable()
	tab.Replace([]Active{{UserID: 1, InfoHash: "aa", Kind: "mystery"}})
	if up, down := tab.Factors(1, "aa", time.Now()); up != 1 || down != 1 {
		t.Errorf("unknown kind gave (%v,%v), want (1,1)", up, down)
	}
}
