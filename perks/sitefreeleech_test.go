package perks

import (
	"testing"
	"time"
)

// Site-wide freeleech reaches a member who holds no token at all.
//
// That is the whole point of doing it as a state: nothing was granted to
// anybody, and the announce path already asks this function what a member's
// traffic is worth.
func TestSiteFreeleechAppliesToEveryoneWithoutAGrant(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	tbl := NewTable()

	if _, down := tbl.Factors(7, "abc", now); down != 1 {
		t.Fatalf("before any window, download factor = %v; want 1", down)
	}
	tbl.SetSiteFreeleech(now.Add(7 * 24 * time.Hour))
	if _, down := tbl.Factors(7, "abc", now); down != 0 {
		t.Errorf("during the window a member with no token pays %v; want 0", down)
	}
	// A member nobody has ever heard of, on a torrent nobody has ever seen.
	if _, down := tbl.Factors(999999, "zzz", now); down != 0 {
		t.Errorf("a member with no rows at all pays %v; want 0", down)
	}
}

// A closed window must stop applying, and a cleared one must not linger.
//
// The lingering case is the dangerous one: refreshSiteFreeleech reads a map
// that OMITS slugs with no open window, so a version that only ever SET the end
// when one was present would leave the last window in force until the process
// restarted — a permanently free site, with nothing in the logs and no row to
// look at.
func TestSiteFreeleechStopsWhenTheWindowDoes(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	tbl := NewTable()

	tbl.SetSiteFreeleech(now.Add(time.Hour))
	if _, down := tbl.Factors(7, "abc", now.Add(2*time.Hour)); down != 1 {
		t.Errorf("an hour after the window closed the site is still free (%v)", down)
	}
	// The zero time is what the map's missing entry yields.
	tbl.SetSiteFreeleech(time.Time{})
	if _, down := tbl.Factors(7, "abc", now); down != 1 {
		t.Errorf("clearing the window left the site free (%v)", down)
	}
	if tbl.SiteFreeleech(now) {
		t.Error("SiteFreeleech reports true with no window")
	}
}

// An upload-double token survives a freeleech week.
//
// The two perks answer different questions and a member paid points for this
// one. Short-circuiting on the site window would silently take it away, and the
// member would see their ratio grow slower during the week the site was being
// generous — which is the opposite of what was announced.
func TestSiteFreeleechDoesNotSwallowAnUploadToken(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	tbl := NewTable()
	tbl.Replace([]Active{{
		UserID: 7, InfoHash: "abc", Kind: UploadDouble,
		ExpiresAt: now.Add(24 * time.Hour),
	}})
	tbl.SetSiteFreeleech(now.Add(7 * 24 * time.Hour))

	up, down := tbl.Factors(7, "abc", now)
	if up != 2 {
		t.Errorf("upload factor %v during site freeleech; the paid-for token was swallowed", up)
	}
	if down != 0 {
		t.Errorf("download factor %v during site freeleech; want 0", down)
	}
}
