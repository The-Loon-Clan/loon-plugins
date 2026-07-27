package usenet

import "testing"

// KeepaliveMin is one of the few knobs where 0 is a real setting ("no idle
// probing") rather than "unset". That makes it easy to get backwards in both
// directions, and both directions are silent: a missed default ships the
// feature disabled, and a missed zero-allowlist entry makes the off switch
// impossible to store.
func TestKeepaliveDefaultAndExplicitOff(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.KeepaliveMin != 2 {
		t.Errorf("unset keepalive_min = %d, want the default 2 — a missing default ships the feature off", c.KeepaliveMin)
	}

	// A negative would become a negative ticker interval, which panics.
	neg := Config{KeepaliveMin: -5}
	neg.applyDefaults()
	if neg.KeepaliveMin != 0 {
		t.Errorf("negative keepalive_min = %d, want 0 (off)", neg.KeepaliveMin)
	}

	// An operator-set value survives.
	set := Config{KeepaliveMin: 7}
	set.applyDefaults()
	if set.KeepaliveMin != 7 {
		t.Errorf("explicit keepalive_min = %d, want 7", set.KeepaliveMin)
	}
}

// A stored 0 must beat the non-zero default, or the knob has no off switch.
func TestKeepaliveStoredZeroDisables(t *testing.T) {
	base := Config{}
	base.applyDefaults() // KeepaliveMin == 2

	got := base.withOverrides(map[string]string{"keepalive_min": "0"})
	if got.KeepaliveMin != 0 {
		t.Errorf("stored 0 gave %d, want 0 — without the zero allowlist the off switch silently does nothing", got.KeepaliveMin)
	}

	got = base.withOverrides(map[string]string{"keepalive_min": "10"})
	if got.KeepaliveMin != 10 {
		t.Errorf("stored 10 gave %d, want 10", got.KeepaliveMin)
	}

	// An unrelated override must not disturb it.
	got = base.withOverrides(map[string]string{"connections": "30"})
	if got.KeepaliveMin != 2 {
		t.Errorf("unrelated override changed keepalive to %d, want 2", got.KeepaliveMin)
	}
}
