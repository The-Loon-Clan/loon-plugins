package usenet

import (
	"strings"
	"testing"
)

// The save-path gate. A typo'd percentage (850 for 85) used to save cleanly
// and silently disable a pressure gate — pressure tops out at 100%, so the
// threshold could never fire and the backfill would stage straight into the
// eviction band while the pause logs cited gates that weren't in charge.
func TestValidateKnobs(t *testing.T) {
	var cur Config
	cur.applyDefaults()

	if msg := validateKnobs(cur, map[string]int{}); msg != "" {
		t.Fatalf("an empty save was rejected: %s", msg)
	}
	if msg := validateKnobs(cur, map[string]int{"backfill_pressure_high_pct": 88}); msg != "" {
		t.Fatalf("a sane save was rejected: %s", msg)
	}

	if msg := validateKnobs(cur, map[string]int{"backfill_pressure_high_pct": 850}); msg == "" {
		t.Error("850%% saved cleanly — the one-extra-digit typo that disables a gate")
	}
	if msg := validateKnobs(cur, map[string]int{"crawl_pressure_high_pct": 101}); msg == "" {
		t.Error("a crawl gate past 100%% saved cleanly")
	}

	// Inverted hysteresis: low >= high flaps at the threshold.
	if msg := validateKnobs(cur, map[string]int{"backfill_pressure_low_pct": 90}); msg == "" {
		t.Error("low above high saved cleanly — the hysteresis is gone")
	}
	// The relationship must be judged against the POST-save state: raising
	// high alone is fine while low keeps its current default beneath it.
	if msg := validateKnobs(cur, map[string]int{"backfill_pressure_high_pct": 90}); msg != "" {
		t.Errorf("raising high to 90 with default low 70 was rejected: %s", msg)
	}

	// High above the ceiling stages through the eviction band whenever a
	// drainable backlog exists (the ceiling only guards the empty-queue path).
	if msg := validateKnobs(cur, map[string]int{"backfill_pressure_high_pct": 99}); msg == "" {
		t.Error("high above the ceiling saved cleanly")
	}
	// ...but raising BOTH together is a legitimate operator choice.
	if msg := validateKnobs(cur, map[string]int{
		"backfill_pressure_high_pct": 94, "backfill_pressure_ceiling_pct": 96,
	}); msg != "" {
		t.Errorf("raising high and ceiling together was rejected: %s", msg)
	}
}

// The runtime backstop for values that arrive around the form: config.yml,
// hand-inserted settings rows, an older binary's writes. Repair, not refusal —
// there is nobody to show an error to at effective() time.
func TestNormalizeRepairsPressureKnobs(t *testing.T) {
	var c Config
	c.applyDefaults()
	c.BackfillPressureHighPct = 850 // the stored typo
	c.normalize()
	if c.BackfillPressureHighPct != 85 {
		t.Errorf("850 normalized to %d, want the default 85", c.BackfillPressureHighPct)
	}

	c.applyDefaults()
	c.BackfillPressureHighPct = 99 // above the ceiling (92)
	c.normalize()
	if c.BackfillPressureHighPct != c.BackfillPressureCeilingPct {
		t.Errorf("high %d not clamped to the ceiling %d — the backfill would stage in the eviction band",
			c.BackfillPressureHighPct, c.BackfillPressureCeilingPct)
	}

	c.applyDefaults()
	c.BackfillPressureLowPct = 85 // equal to high: no hysteresis
	c.normalize()
	if c.BackfillPressureLowPct >= c.BackfillPressureHighPct {
		t.Errorf("low %d not forced below high %d", c.BackfillPressureLowPct, c.BackfillPressureHighPct)
	}

	// withOverrides is the path DB rows enter through — the backstop must run
	// there, or a hand-inserted row bypasses everything.
	var base Config
	base.applyDefaults()
	out := base.withOverrides(map[string]string{"backfill_pressure_high_pct": "850"})
	if out.BackfillPressureHighPct != 85 {
		t.Errorf("a stored 850 reached the gates as %d via withOverrides", out.BackfillPressureHighPct)
	}
}

// Checkbox saves must only touch bools the form declared (has_<key> markers).
// An unchecked checkbox posts nothing, so writing false for every known bool
// force-reset the UN-rendered ones — every knob save silently flipped
// backfill_no_catchup, the catch-up loop's emergency brake, back off.
func TestPostedBoolsHonorsPresenceMarkers(t *testing.T) {
	var cfg Config
	form := map[string]string{
		"has_skip_backfill":    "1",
		"skip_backfill":        "on",
		"has_crawl_no_catchup": "1",
		// crawl_no_catchup absent = declared and unchecked
		// backfill_no_catchup: no marker = not on this form at all
	}
	got := postedBools(func(k string) string { return form[k] }, cfg.boolFields())

	if got["skip_backfill"] != "true" {
		t.Errorf("declared+checked = %q, want true", got["skip_backfill"])
	}
	if got["crawl_no_catchup"] != "false" {
		t.Errorf("declared+unchecked = %q, want false", got["crawl_no_catchup"])
	}
	if _, wrote := got["backfill_no_catchup"]; wrote {
		t.Error("an undeclared bool was written — the emergency-brake clobber is back")
	}
}

// Every bool the config knows must be DECLARED on the settings form, and the
// two knobs that were invisible must be rendered — an emergency brake you can
// only reach via SQL is not a brake.
func TestSettingsFormDeclaresEveryBoolAndKnob(t *testing.T) {
	raw, err := viewFS.ReadFile("templates/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	var cfg Config
	for key := range cfg.boolFields() {
		if !strings.Contains(page, `name="`+key+`"`) {
			t.Errorf("bool %s has no checkbox on the settings form", key)
		}
		if !strings.Contains(page, `name="has_`+key+`"`) {
			t.Errorf("bool %s has no presence marker — unchecked saves cannot be told from absent", key)
		}
	}
	// The knob list must cover every knobFields key, or a setting is
	// admin-overridable only by hand-inserting rows — the hidden-knob trap:
	// five knobs (including the eviction ceiling) lived that way, and every
	// one of them confused a diagnosis.
	rendered := map[string]bool{}
	for _, k := range knobList(Config{}) {
		rendered[k.Key] = true
	}
	for key := range cfg.knobFields() {
		if !rendered[key] {
			t.Errorf("knob %s is settable via knobFields but has no row in knobList — "+
				"invisible knobs get force-kept or hand-edited, never understood", key)
		}
	}
}
