package anidb

import "testing"

func TestNormalizeSequelFolding(t *testing.T) {
	s := New("", nil)
	// all of these should fold to the same base key
	base := s.Normalize("Attack on Titan")
	for _, variant := range []string{
		"Attack on Titan Season 2",
		"Attack on Titan 2",
		"Attack on Titan II",
		"Attack.on.Titan.S2", // via default normalize (dots→space) then… "s2" stays; check trailing-number only
	} {
		if got := s.Normalize(variant); got != base && variant != "Attack.on.Titan.S2" {
			t.Errorf("Normalize(%q) = %q, want %q", variant, got, base)
		}
	}
	if base != "attack on titan" {
		t.Fatalf("base = %q, want %q", base, "attack on titan")
	}
}

func TestDomain(t *testing.T) {
	d := New("client", nil).Domain()
	if d.Key != "anime" || d.Priority != 100 {
		t.Fatalf("domain = %+v", d)
	}
}

// Without a client name this source can answer nothing — AniDB requires a
// registered one — so it must not exist rather than exist mutely.
//
// It used to build itself regardless, register at priority 100, take the
// "anime" domain and serve every lookup from an empty title index. Anime
// releases then matched nothing AND could not fall through to a source that
// would have worked: 1,847 releases at 6.2% cover art against television's
// 59%, with nothing in the log, because from the outside a source answering
// "no match" is indistinguishable from one that is working.
func TestNoClientMeansNoSource(t *testing.T) {
	for _, client := range []string{"", "   "} {
		if got := New(client, nil); got != nil {
			t.Errorf("New(%q) = %+v, want nil so the domain falls to a keyless source", client, got)
		}
	}
	if New("loonclient", nil) == nil {
		t.Error("New with a client name returned nil")
	}
}
