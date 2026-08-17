package achievements

import (
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// Provision scans the registry by PREFIX and type-asserts whatever it finds.
// It also registers a documentation placeholder. Those two facts collided in
// production once (rewards, 2026-08-07): a placeholder landed inside the
// scanned namespace, failed the type assertion, and both processes
// crash-looped. The guard came across with the metric-source convention.
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
// RegisterDef's error is discarded at the call site on purpose — a
// documentation placeholder must never be able to fail a boot — which is
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
	if got := len(c.ExtensionNames()); got != len(docExtensions()) {
		t.Errorf("%d of %d doc extensions reached the registry", got, len(docExtensions()))
	}
}

// The summary carries the real, dynamic name — "achievements.metrics.<metric>"
// — since the registered key is truncated and would otherwise tell a host to
// register under a key nothing reads.
func TestDocSummariesNameTheRealKey(t *testing.T) {
	for _, d := range docExtensions() {
		if !strings.Contains(d.def.Summary, d.def.Name+".") {
			t.Errorf("%q's summary never mentions the real %s.<...> key, so a host "+
				"would register under the truncated one: %q",
				d.def.Name, d.def.Name, d.def.Summary)
		}
	}
}

// The declared event must be acceptable to core, for the same reason the doc
// extensions are checked: DeclareEvent refusing it would leave the event
// invisible in the directory, and the announcement half of every completion
// silently absent.
func TestCompletedEventDeclarationIsAccepted(t *testing.T) {
	c := &core.Core{}
	p := &Plugin{}
	if err := p.declareEvents(c); err != nil {
		t.Fatalf("declareEvents: %v", err)
	}
	defs := c.EventDefs()
	if len(defs) != 1 || defs[0].Name != EventCompleted {
		t.Fatalf("declared %+v, want exactly %q", defs, EventCompleted)
	}
	if !defs[0].Countable || defs[0].Kind != core.EventMember {
		t.Errorf("achievements.completed must be a countable member event — "+
			"completing achievements is itself a legitimate per-member total (got %+v)", defs[0])
	}
}
