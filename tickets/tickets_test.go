package tickets

import (
	"strings"
	"testing"
)

func TestNormalizePriority(t *testing.T) {
	cases := map[string]string{
		"low":     "low",
		"normal":  "normal",
		"high":    "high",
		"":        "normal", // absent form field
		"LOW":     "normal", // case-sensitive: not in the CHECK set
		"urgent":  "normal", // unknown value
		" high ":  "normal", // not trimmed by this helper
		"normal;": "normal", // injection-ish garbage
	}
	for in, want := range cases {
		if got := normalizePriority(in); got != want {
			t.Errorf("normalizePriority(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClampSubject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int // expected byte length
	}{
		{"empty", "", 0},
		{"short", "hello", 5},
		{"exactly 200", strings.Repeat("a", 200), 200},
		{"over 200 truncates", strings.Repeat("a", 250), 200},
		{"way over", strings.Repeat("x", 10000), 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clampSubject(tc.in)
			if len(got) != tc.want {
				t.Errorf("clampSubject(len %d) → len %d, want %d", len(tc.in), len(got), tc.want)
			}
			// Result must be a prefix of the input (pure truncation, no rewrite).
			if !strings.HasPrefix(tc.in, got) {
				t.Errorf("clampSubject not a prefix of input: %q", got)
			}
		})
	}
}

func TestTicketVisibleTo(t *testing.T) {
	const owner = 42
	tests := []struct {
		name     string
		ownerID  int
		viewerID int
		isAdmin  bool
		want     bool
	}{
		{"owner sees own ticket", owner, owner, false, true},
		{"admin sees others' tickets", owner, 7, true, true},
		{"stranger blocked", owner, 7, false, false},
		{"admin owns it", owner, owner, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tk := &SupportTicket{UserID: tc.ownerID}
			if got := ticketVisibleTo(tk, tc.viewerID, tc.isAdmin); got != tc.want {
				t.Errorf("ticketVisibleTo(owner=%d, viewer=%d, admin=%v) = %v, want %v",
					tc.ownerID, tc.viewerID, tc.isAdmin, got, tc.want)
			}
		})
	}
}
