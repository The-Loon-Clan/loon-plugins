package rewards

import (
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// Provision scans the registry by PREFIX and type-asserts whatever it finds.
// It also registers documentation placeholders. Those two facts collided in
// production on 2026-08-07: a placeholder named "rewards.units.<reward-slug>"
// matched HasPrefix(name, "rewards.units."), failed the assertion to
// UnitSource, and Provision returned an error -- so web and worker crash-looped
// and the site served 502 until it was fixed forward.
//
// The bug is not really "wrong name". It is that a plugin which both SCANS a
// namespace and WRITES to it can poison itself, and nothing about either half
// looks dangerous alone.
func TestDocPlaceholdersCannotMatchTheDiscoveryScans(t *testing.T) {
	// Exactly the predicates Provision uses.
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{strings.TrimSuffix(UnitSourcePrefix, "."), UnitSourcePrefix},
		{strings.TrimSuffix(MetricSourcePrefix, "."), MetricSourcePrefix},
	} {
		if strings.HasPrefix(tc.name, tc.prefix) {
			t.Errorf("placeholder %q matches the scan for %q — Provision will type-assert "+
				"it, fail, and refuse to boot", tc.name, tc.prefix)
		}
	}
}

// The stronger version of the same guarantee, stated as a rule rather than
// checked case by case: nothing this plugin registers for documentation may
// live inside a namespace it scans.
func TestNoRegisteredNameIsBothScannedAndPlaceheld(t *testing.T) {
	scanned := []string{UnitSourcePrefix, MetricSourcePrefix}

	c := &core.Core{}
	// The two placeholders, registered exactly as Provision does.
	for _, n := range []string{
		strings.TrimSuffix(UnitSourcePrefix, "."),
		strings.TrimSuffix(MetricSourcePrefix, "."),
	} {
		if err := c.RegisterDef(core.ExtensionDef{
			Name: n, Summary: "placeholder", Kind: core.ExtCallback,
		}, unitSourceDoc{}); err != nil {
			t.Fatalf("register %q: %v", n, err)
		}
	}

	for _, name := range c.ExtensionNames() {
		for _, p := range scanned {
			if strings.HasPrefix(name, p) {
				t.Errorf("this plugin registered %q inside %q, which it also scans — "+
					"the scan will assert the placeholder to a real interface and Boot dies",
					name, p)
			}
		}
	}
}
