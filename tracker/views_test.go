package tracker

import (
	"strings"
	"testing"
	"time"
)

func renderFixture(t *testing.T) *Plugin {
	t.Helper()
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return p
}

// The admin page renders to completion with real rows.
//
// html/template STREAMS, so a field the view model lacks truncates the page at
// that row rather than erroring — which is why the assertions include content from
// the LAST table on the page.
func TestAdminPageRenders(t *testing.T) {
	p := renderFixture(t)
	seen := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	added := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	out, err := p.exec(adminVM{
		Now: seen,
		Members: []memberVM{
			{Aggregate: &Aggregate{UserID: 7, Uploaded: 5 << 30, Downloaded: 1 << 30,
				ActiveCount: 2, TorrentCount: 9, SnatchedCount: 4, LastSeen: &seen},
				Username: "ameNZB", Entitled: true, Ratio: 5},
			// No access and no activity: the row that must render "—" for the
			// ratio rather than "0.00", and a secondary badge rather than blank.
			{Aggregate: &Aggregate{UserID: 8}, Username: "lurker"},
		},
		Torrents: []*Torrent{
			{InfoHash: strings.Repeat("a", 40), Name: "Some.Release-GRP",
				Size: 3 << 30, FileCount: 12, Seeders: 4, Leechers: 1, Snatches: 20, AddedAt: added},
		},
		Total: 1,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)

	for _, want := range []string{
		"Members", "ameNZB", "lurker",
		"5.0 GiB", "1.0 GiB", // bytes rendered, not raw integers
		"5.00", // the ratio
		"entitled", "no access",
		// From the LAST table, so its presence proves the render reached the end.
		"Torrents", "Some.Release-GRP", "3.0 GiB", "1 registered",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q — if the earlier rows rendered, the template aborted mid-stream", want)
		}
	}
	// A zero ratio must read as "—", never "0.00": an inactive member has no
	// ratio rather than a ratio of nothing.
	if strings.Contains(html, "0.00") {
		t.Error("a zero ratio rendered as 0.00 instead of —")
	}
	// Raw byte integers must not leak through the formatter.
	if strings.Contains(html, "5368709120") {
		t.Error("a raw byte count reached the page")
	}
}

// The idle case must SAY it is idle. Provision registers no routes without Redis,
// so an empty table would read as "a tracker nobody uses" when the truth is "a
// tracker that cannot run" — and those need different actions.
func TestIdlePageExplainsItself(t *testing.T) {
	p := renderFixture(t)
	out, err := p.exec(adminVM{Now: time.Now(), Idle: true})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)
	if !strings.Contains(html, "idle") || !strings.Contains(html, "Redis") {
		t.Error("the idle page does not say why the tracker is not running")
	}
	// And it must not also render empty tables underneath, which would muddle the
	// message it just gave.
	if strings.Contains(html, "No torrents registered") {
		t.Error("the idle page also rendered the empty tables")
	}
}

// An empty-but-working tracker says what makes a row appear, rather than showing a
// blank table an operator has to interpret.
func TestEmptyPageSaysWhatToExpect(t *testing.T) {
	p := renderFixture(t)
	out, err := p.exec(adminVM{Now: time.Now()})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)
	if !strings.Contains(html, "tracker.access") {
		t.Error("the empty members table does not mention the entitlement that populates it")
	}
	if !strings.Contains(html, "No torrents registered") {
		t.Error("the empty torrents table has no message")
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {-1, "0 B"}, {512, "512 B"},
		{1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{1 << 30, "1.0 GiB"}, {1 << 40, "1.0 TiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
