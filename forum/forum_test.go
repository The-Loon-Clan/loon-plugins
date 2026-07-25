package forum

import "testing"

// isAllowedReactionEmoji is the server-side allowlist that guards the
// forum_post_reactions.emoji column against arbitrary unicode/HTML
// smuggled through a hand-crafted POST (the handler rejects anything
// it returns false for). It must accept exactly the six picker emoji
// in community_thread.html and nothing else — including near-miss
// look-alikes that differ only by a variation selector.
func TestIsAllowedReactionEmoji(t *testing.T) {
	cases := []struct {
		name  string
		emoji string
		want  bool
	}{
		{"thumbs up", "👍", true},
		{"heart (with VS16)", "❤️", true},
		{"joy", "😂", true},
		{"open mouth", "😮", true},
		{"cry", "😢", true},
		{"tada", "🎉", true},

		{"empty string", "", false},
		{"plain text", "like", false},
		{"heart without variation selector", "❤", false},
		{"emoji not in picker", "🔥", false},
		{"whitespace-padded (handler trims before calling)", " 👍", false},
		{"html injection attempt", "<img src=x onerror=alert(1)>", false},
		{"two allowed emoji concatenated", "👍👍", false},
		{"trailing text after emoji", "👍x", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowedReactionEmoji(tc.emoji); got != tc.want {
				t.Errorf("isAllowedReactionEmoji(%q) = %v, want %v", tc.emoji, got, tc.want)
			}
		})
	}
}

// TestAllowedReactionEmojisCount pins the allowlist size so a future
// edit to the picker that forgets to update the map (or vice versa)
// trips a test rather than silently drifting from the template.
func TestAllowedReactionEmojisCount(t *testing.T) {
	if len(allowedReactionEmojis) != 6 {
		t.Errorf("allowedReactionEmojis has %d entries, want 6 (keep in sync with community_thread.html picker)", len(allowedReactionEmojis))
	}
	for emoji, ok := range allowedReactionEmojis {
		if !ok {
			t.Errorf("allowlist entry %q maps to false — every listed emoji must be true", emoji)
		}
	}
}
