package feeds

import (
	"errors"
	"testing"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

func sourceByName(t *testing.T, snap lpapi.FeedsSnapshot, name string) lpapi.FeedsSource {
	t.Helper()
	for _, s := range snap.Sources {
		if s.Source == name {
			return s
		}
	}
	t.Fatalf("source %q missing from snapshot: %+v", name, snap.Sources)
	return lpapi.FeedsSource{}
}

func TestStatusBookPollTransitions(t *testing.T) {
	b := newStatusBook(true)

	b.pollOK("nyaa", 75)
	s := sourceByName(t, b.FeedsStatus(), "nyaa")
	if s.LastItems != 75 || s.LastOKAt == nil || s.LastError != "" {
		t.Fatalf("after pollOK: %+v", s)
	}

	b.pollFailed("nyaa", errors.New("status 503"))
	s = sourceByName(t, b.FeedsStatus(), "nyaa")
	if s.LastError != "status 503" || s.LastErrorAt == nil {
		t.Fatalf("after pollFailed: %+v", s)
	}
	// The failure must NOT erase when the source last worked — that gap is
	// exactly what an operator reads to see how long it has been broken.
	if s.LastOKAt == nil {
		t.Error("pollFailed erased LastOKAt")
	}
	if s.LastItems != 75 {
		t.Errorf("pollFailed changed LastItems to %d", s.LastItems)
	}

	// Recovery clears the error.
	b.pollOK("nyaa", 80)
	s = sourceByName(t, b.FeedsStatus(), "nyaa")
	if s.LastError != "" || s.LastErrorAt != nil {
		t.Fatalf("recovery did not clear the error: %+v", s)
	}
}

func TestStatusBookNekoBTEnabledFollowsTheKey(t *testing.T) {
	if s := sourceByName(t, newStatusBook(false).FeedsStatus(), "nekobt"); s.Enabled {
		t.Error("nekobt enabled without an API key")
	}
	if s := sourceByName(t, newStatusBook(true).FeedsStatus(), "nekobt"); !s.Enabled {
		t.Error("nekobt disabled despite an API key")
	}
}

func TestStatusBookRunFinished(t *testing.T) {
	b := newStatusBook(false)
	b.runFinished(&lpapi.FeedsTotals{Fetched: 100, Created: 4}, "")
	snap := b.FeedsStatus()
	if snap.LastRunAt == nil || snap.Totals.Fetched != 100 || snap.Totals.Created != 4 {
		t.Fatalf("run not recorded: %+v", snap)
	}

	// A failed run records the error but keeps the last good totals — an
	// operator comparing against yesterday must not lose the numbers.
	b.runFinished(nil, "get existing keys: connection refused")
	snap = b.FeedsStatus()
	if snap.LastRunError == "" {
		t.Error("run error not recorded")
	}
	if snap.Totals.Fetched != 100 {
		t.Errorf("failed run zeroed the totals: %+v", snap.Totals)
	}
}

func TestStatusBookSnapshotIsIsolated(t *testing.T) {
	b := newStatusBook(false)
	b.pollOK("nyaa", 10)
	snap := b.FeedsStatus()

	b.pollOK("nyaa", 999)
	if s := sourceByName(t, snap, "nyaa"); s.LastItems != 10 {
		t.Errorf("a later update mutated an earlier snapshot: LastItems = %d", s.LastItems)
	}
}
