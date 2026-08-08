package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The announce path's arithmetic, exercised through the store rather than
// asserted about in isolation.
//
// Three behaviours the tracker's whole ratio system rests on, and each is wrong in
// a way that looks fine until somebody checks a member's numbers:
//
//   - byte counts ADD (a client reports cumulative totals, the delta is what is
//     credited)
//   - left_bytes REPLACES (it is a current position, not an accumulation)
//   - completed is STICKY (a re-announce after finishing must not undo a snatch)
func TestAnnounceDeltaAccumulatesAndCompletionSticks(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	m.Now = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if err := m.UpsertTorrent(ctx, &Torrent{InfoHash: hash, Name: "Release", Size: 1000, InfoBytes: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	// First announce: 100 up, 200 down, 800 left, not done.
	if err := m.ApplyAnnounceDelta(ctx, 7, hash, 100, 200, 800, false); err != nil {
		t.Fatal(err)
	}
	// Second: another 50 up, 300 down, now finished.
	if err := m.ApplyAnnounceDelta(ctx, 7, hash, 50, 300, 0, true); err != nil {
		t.Fatal(err)
	}

	rows, err := m.ListUserStats(ctx, 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d stat rows, want 1 — the upsert created a second instead of updating", len(rows))
	}
	s := rows[0]
	if s.Uploaded != 150 || s.Downloaded != 500 {
		t.Errorf("uploaded=%d downloaded=%d, want 150/500 — deltas must ADD", s.Uploaded, s.Downloaded)
	}
	if s.LeftBytes != 0 {
		t.Errorf("left=%d, want 0 — left_bytes must REPLACE, not accumulate", s.LeftBytes)
	}
	if !s.Completed {
		t.Error("completed did not stick")
	}
	// The join the "my stats" page depends on.
	if s.Name != "Release" || s.TSize != 1000 {
		t.Errorf("torrent name/size not joined: %q/%d", s.Name, s.TSize)
	}

	// A LATER announce with completed=false must not un-complete it. This is the
	// one that costs a member their snatch if it is wrong, and a client that keeps
	// announcing after finishing sends exactly this.
	if err := m.ApplyAnnounceDelta(ctx, 7, hash, 10, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	rows, _ = m.ListUserStats(ctx, 7, 10)
	if !rows[0].Completed {
		t.Error("a later announce with completed=false cleared the snatch")
	}
	if rows[0].Uploaded != 160 {
		t.Errorf("uploaded=%d, want 160 — seeding after completion still credits upload", rows[0].Uploaded)
	}
}

// A stat row for a torrent that does not exist must be refused, because Postgres
// refuses it. A double that accepts it lets a test pass on a foreign-key violation.
func TestAnnounceForUnknownTorrentIsRefused(t *testing.T) {
	m := NewMemStore()
	if err := m.ApplyAnnounceDelta(context.Background(), 7, "deadbeef", 1, 1, 0, false); err == nil {
		t.Error("accepted stats for an unregistered torrent; the FK would have rejected it")
	}
}

// Totals are summed over the stat rows rather than kept as their own counter, and
// seeding/leeching depend on BOTH left_bytes and recency — a peer that stopped
// announcing a week ago is neither.
func TestTotalsDeriveSeedingFromRecencyNotJustBytes(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	add := func(hash, name string, at time.Time, left int64) {
		m.Now = at
		if err := m.UpsertTorrent(ctx, &Torrent{InfoHash: hash, Name: name, Size: 100, InfoBytes: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		if err := m.ApplyAnnounceDelta(ctx, 7, hash, 10, 20, left, left == 0); err != nil {
			t.Fatal(err)
		}
	}

	// The clock is set BEFORE each write, because last_seen is stamped at write
	// time. The first version of this test moved m.Now back and then forward again
	// before writing anything, so every row ended up stamped "now" and the stale
	// case was never exercised -- and the assertion below was a t.Logf, which is
	// how it passed anyway. Both fixed.
	add("a00000000000000000000000000000000000000a", "seeding-now", now, 0)
	add("b00000000000000000000000000000000000000b", "leeching-now", now, 50)
	add("c00000000000000000000000000000000000000c", "finished-but-gone", now.Add(-72*time.Hour), 0)

	m.Now = now
	tot, err := m.Totals(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if tot.Uploaded != 30 || tot.Downloaded != 60 {
		t.Errorf("uploaded=%d downloaded=%d, want 30/60", tot.Uploaded, tot.Downloaded)
	}
	// THE point of the test. Rows "a" and "c" both have left_bytes 0, so a check
	// that looked only at bytes would report 2 seeding. Only "a" announced
	// recently, and a peer that has not been seen for three days is seeding
	// nothing -- reporting it would tell a member they are seeding a torrent their
	// client stopped days ago.
	if tot.Seeding != 1 {
		t.Errorf("seeding=%d, want 1 — a stale complete peer is not seeding, however empty its left_bytes", tot.Seeding)
	}
	if tot.Leeching != 1 {
		t.Errorf("leeching=%d, want 1", tot.Leeching)
	}
	// Snatched counts history, not activity, so the stale one still counts.
	if tot.Snatched != 2 {
		t.Errorf("snatched=%d, want 2 — completion is history and does not expire", tot.Snatched)
	}
}

// Ratio's both-zero case returns 0, not +Inf, and the reason is a sort order.
//
// An inactive member must sort to the BOTTOM of a ratio-ordered admin table. +Inf
// would put every member who has never downloaded anything at the top, which is
// the opposite of what the table is for.
func TestRatioSortsInactiveMembersLast(t *testing.T) {
	if r := (Totals{}).Ratio(); r != 0 {
		t.Errorf("a member with no activity has ratio %v, want 0 so they sort last", r)
	}
	if r := (Totals{Uploaded: 500}).Ratio(); r != 500 {
		t.Errorf("upload-only ratio = %v, want the upload figure rather than +Inf", r)
	}
	if r := (Totals{Uploaded: 300, Downloaded: 100}).Ratio(); r != 3 {
		t.Errorf("ratio = %v, want 3", r)
	}
}

// A passkey is UNIQUE across members, because an announce carries nothing else to
// say who it is from. Two members sharing one makes every announce ambiguous.
func TestPasskeyIsUniqueAndRotationReplaces(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	if err := m.SetPasskey(ctx, 7, "key-one"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPasskey(ctx, 8, "key-one"); err == nil {
		t.Error("two members were allowed the same passkey; every announce from it is ambiguous")
	}
	// Rotation replaces rather than adding a second key for the same member.
	if err := m.SetPasskey(ctx, 7, "key-two"); err != nil {
		t.Fatal(err)
	}
	if id, ok, _ := m.UserByPasskey(ctx, "key-one"); ok {
		t.Errorf("the rotated-away passkey still resolves, to %d", id)
	}
	if id, ok, _ := m.UserByPasskey(ctx, "key-two"); !ok || id != 7 {
		t.Errorf("the new passkey resolves to %d/%v, want 7/true", id, ok)
	}
	// An empty passkey must never match anything — it is what a request with no
	// passkey at all looks like.
	if _, ok, _ := m.UserByPasskey(ctx, ""); ok {
		t.Error("an empty passkey resolved to a member")
	}
	if err := m.SetPasskey(ctx, 9, ""); err == nil {
		t.Error("stored an empty passkey")
	}
}

// The gate fails CLOSED when the host wired no entitlements service.
//
// A host that has not wired it has not decided everyone may use the tracker — it
// has not wired the thing that decides. Answering yes would open a private tracker
// to anyone holding a passkey.
func TestGateFailsClosedWithoutServices(t *testing.T) {
	// A bare Core: no Entitlements, no Users. Exactly what a host that forgot to
	// wire them hands the plugin.
	g := NewGate(&core.Core{})
	ctx := context.Background()
	if g.Entitled(ctx, 7) {
		t.Error("entitled with no entitlements service — a private tracker just went public")
	}
	if g.Active(ctx, 7) {
		t.Error("active with no users service")
	}
}
