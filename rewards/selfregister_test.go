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
// UnitSource, and Provision returned an error -- so web and worker
// crash-looped and the site served 502 until it was fixed forward.
//
// The bug is not really "wrong name". It is that a plugin which both SCANS a
// namespace and WRITES to it can poison itself, and neither half looks
// dangerous on its own.
//
// This reads the names docExtensions() ACTUALLY registers. The first version
// of this test computed them itself, so it asserted a property of a string in
// the test file: changing plugin.go back to the broken name left it passing.
// A mutation caught that, which is the only reason this note exists.
func TestNothingRegisteredLandsInAScannedNamespace(t *testing.T) {
	prefixes := scannedPrefixes()
	if len(prefixes) == 0 {
		t.Fatal("no scanned prefixes declared; this test would pass vacuously")
	}
	docs := docExtensions()
	if len(docs) == 0 {
		t.Fatal("no doc extensions declared; this test would pass vacuously")
	}

	for _, d := range docs {
		for _, p := range prefixes {
			if strings.HasPrefix(d.def.Name, p) {
				t.Errorf("Provision registers %q, which is inside the scanned namespace %q — "+
					"the scan will type-assert this placeholder to a real interface, fail, "+
					"and refuse to boot", d.def.Name, p)
			}
		}
	}
}

// And the definitions themselves must be acceptable to core, or Provision
// silently registers nothing and the directory is emptier than it looks.
// RegisterDef's error is discarded at the call site on purpose -- a
// documentation placeholder must never be able to fail a boot -- which is
// exactly why it has to be checked here instead.
func TestDocExtensionsAreAcceptedByCore(t *testing.T) {
	c := &core.Core{}
	for _, d := range docExtensions() {
		if err := d.def.Validate(); err != nil {
			t.Errorf("%q: %v", d.def.Name, err)
			continue
		}
		if err := c.RegisterDef(d.def, d.value); err != nil {
			t.Errorf("core refused %q: %v", d.def.Name, err)
		}
	}
	// Every one should now be visible in the directory.
	if got := len(c.ExtensionNames()); got != len(docExtensions()) {
		t.Errorf("%d of %d doc extensions reached the registry", got, len(docExtensions()))
	}
}

// The summaries carry the real, dynamic name -- "rewards.units.<slug>" -- since
// the registered key is truncated and would otherwise tell a host to register
// under a key nothing reads.
func TestDocSummariesNameTheRealKey(t *testing.T) {
	for _, d := range docExtensions() {
		if !strings.Contains(d.def.Summary, d.def.Name+".") {
			t.Errorf("%q's summary never mentions the real %s.<...> key, so a host "+
				"would register under the truncated one: %q",
				d.def.Name, d.def.Name, d.def.Summary)
		}
	}
}
