package achievements

import (
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// metricSourceDoc exists ONLY to be registered against the callback
// convention, so the extension directory can list it.
//
// The real registrations are dynamic — one per metric — and are made by the
// HOST, so there is no single value to describe. A placeholder under the
// pattern name is how a directory built from a flat registry can still say
// "this shape exists, and you are the one who supplies it". Registering
// nothing would leave the most easily-missed seam in the plugin invisible: a
// callback nobody knows to implement simply never fires, and nothing reports
// it.
type metricSourceDoc struct{}

// docExtension is one documented convention: the definition and the
// placeholder value registered under it.
type docExtension struct {
	def   core.ExtensionDef
	value any
}

// docExtensions is the single source of truth for the callback conventions
// this plugin publishes to the extension directory. A function rather than
// inline in Provision so a TEST can read the names Provision will actually
// register.
//
// The name deliberately stops BEFORE the dot. Provision scans the registry
// with HasPrefix(name, "achievements.metrics.") and type-asserts what it
// finds, so a placeholder inside that namespace would match the scan, fail
// the assertion and take Boot down — rewards shipped exactly that crash-loop
// on 2026-08-07, which is why the truncated-name trick and the test guarding
// it (docext_test.go) came across with the move.
func docExtensions() []docExtension {
	return []docExtension{
		{core.ExtensionDef{
			Name:    strings.TrimSuffix(MetricSourcePrefix, "."),
			Summary: "HOST SUPPLIES, one per metric as achievements.metrics.<metric>: the current counter value per member for scoring achievements",
			Kind:    core.ExtCallback,
		}, metricSourceDoc{}},
	}
}

// scannedPrefixes are the namespaces Provision walks and type-asserts.
// Nothing this plugin registers may land inside one.
func scannedPrefixes() []string {
	return []string{MetricSourcePrefix}
}
