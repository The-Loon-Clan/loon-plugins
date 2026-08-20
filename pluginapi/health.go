// health.go declares how a plugin says whether it is actually WORKING, as
// opposed to merely loaded.
//
// /admin/plugins answers "what is running" and stops there, which is the
// smaller half of the question. A plugin can be provisioned, started, mounted
// and completely unable to do its job: a scraper with no API key, an IRC bot
// that has not connected since Tuesday, a backup that has never once completed,
// a mailer whose SMTP credentials were rotated. Every one of those looks
// identical to a healthy plugin from outside, and the way an operator finds out
// is that somebody complains about the thing it was supposed to be doing.
//
// The framework already knows about failures that happen at BOOT — Provision
// returns an error and the site does not start. This is for the other kind: the
// ones that arrive later, or that were always true but only matter when
// somebody uses the feature.
//
// DELIBERATELY NOT A MONITORING SYSTEM. No history, no thresholds, no alerting;
// those belong to whatever is already watching the process. This is one
// sentence per plugin on a page an operator already visits, which is the thing
// that was missing and the thing nothing else provides.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// HealthState is how a plugin is doing. Three values, because two is not
// enough: most of what goes wrong with a plugin is not an error, it is a
// capability quietly absent.
type HealthState string

const (
	// HealthOK is working as intended.
	HealthOK HealthState = "ok"

	// HealthDegraded is running but not doing all of its job — a soft
	// dependency absent, an optional credential unset, a queue backing up.
	// The site is fine and the operator has something worth knowing.
	//
	// This is the state that earns the whole contract. A plugin that degrades
	// gracefully (which CHECKLIST section 1 requires of every one of them) is
	// by construction a plugin that can be silently useless, and until now the
	// graceful part meant nobody ever heard about it.
	HealthDegraded HealthState = "degraded"

	// HealthFailing is not doing its job at all.
	HealthFailing HealthState = "failing"
)

// Health is one plugin's answer.
type Health struct {
	State HealthState

	// Summary is one line an operator can act on, in their terms. "no API key"
	// rather than "config error"; "last connected 3 days ago" rather than
	// "disconnected". Required for anything that is not HealthOK — a degraded
	// plugin with no explanation is a worry with no next step, which is worse
	// than not being told.
	Summary string

	// Detail is optional extra shown when the row is expanded: the failing
	// host, the config key that is empty, the last error.
	Detail string
}

// HealthReporterName is the Core extension-registry key prefix a plugin
// publishes under: "health.<plugin>".
//
// A PREFIX rather than one key, because every plugin may have an answer — this
// is the shape CHECKLIST section 1 calls a set that another plugin can append
// to, and the host scans it with Contributions.
const HealthReporterName = "health."

// HealthReporter is implemented by a plugin that can say how it is doing.
//
// Called PER REQUEST of the admin page, so it must be cheap: read state the
// plugin already holds, not the network. A reporter that dialled its upstream
// would make an admin page as slow and as flaky as the thing it is reporting
// on — and would turn one broken integration into a page that will not load.
type HealthReporter interface {
	Health(ctx context.Context) Health
}

// PluginHealth collects every reporter, keyed by the plugin name from the
// registry suffix.
func PluginHealth(c *core.Core) []Contribution[HealthReporter] {
	return Contributions[HealthReporter](c, HealthReporterName)
}
