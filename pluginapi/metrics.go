// metrics.go declares how a plugin contributes to the host's /metrics.
//
// The host cannot know what is worth measuring in a plugin. It knows about
// requests, jobs and its own tables; it does not know that the usenet plugin
// has a staging backlog, that the tracker counts announces, or that a scraper
// has a per-source failure rate. Those are the numbers somebody is actually
// paged about, and every one of them lives behind a schema the host does not
// read.
//
// So the same shape as every other extension point here: a prefix, a typed
// contract, and a host that scans rather than lists (CHECKLIST section 1).
//
// COUNTERS AND GAUGES ONLY, deliberately. A distribution — how long something
// took, spread over buckets — is a different kind of object with a different
// contract, and the one place that genuinely needs one is HTTP request
// duration, which is the host's own. Keeping distributions out of this
// interface keeps a plugin's side to "here is a number and what it means",
// which is what almost every plugin actually has.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// MeasurementKind is what a value MEANS, which decides how it may be read.
type MeasurementKind string

const (
	// MetricCounter only ever goes up (or resets to zero on restart). A rate
	// over it is meaningful; its absolute value usually is not.
	MetricCounter MeasurementKind = "counter"

	// MetricGauge is a level that moves both ways: a queue depth, a member
	// count, a timestamp.
	MetricGauge MeasurementKind = "gauge"
)

// Measurement is one number a plugin exports.
//
// Named Measurement rather than Metric because optimization.go already has a
// Metric — a pre-formatted string for a human reading a recommendation, which
// is a different thing entirely from a float a scraper reads. Two types called
// Metric in one package would have been a coin flip at every call site.
type Measurement struct {
	// Name is the full metric name, and it is the plugin's to get right:
	// prefix it with the plugin, suffix it with the unit, and use base units.
	//
	//	usenet_staged_articles          a gauge, no unit suffix needed
	//	usenet_releases_built_total     a counter, hence _total
	//	tracker_announce_seconds_total  seconds, never milliseconds
	//
	// Base units matter because a dashboard cannot tell from the wire whether
	// a number is ms or s, and mixing them is how a graph ends up a thousand
	// times wrong with nothing to say so.
	Name string

	// Help is one line describing what this counts, shown in the exposition
	// and read by whoever is looking at an unfamiliar metric at 3am.
	Help string

	Kind MeasurementKind

	// Labels are the dimensions. KEEP THEM SMALL AND BOUNDED: every distinct
	// combination is a separate stored series forever, so a label carrying a
	// user id, a release id or a raw path does not make a richer metric, it
	// makes an unusable one and takes the monitoring system with it. Bounded
	// sets — a job name, an outcome, a source — are what belongs here.
	Labels map[string]string

	Value float64
}

// MetricSourcePrefix is where a plugin registers: "metrics.source.<plugin>".
const MetricSourcePrefix = "metrics.source."

// MetricSource is implemented by a plugin with something to measure.
type MetricSource interface {
	// Metrics is called ON SCRAPE, so it must be cheap and must not fail the
	// scrape: return what is known and omit what is not. A source that ran an
	// expensive query would make the monitoring system a load generator, and
	// one that blocked on a dependency would make a scrape time out — which
	// looks exactly like the whole process being down.
	//
	// Prefer reading counters the plugin already keeps in memory over asking
	// the database. Where a query is unavoidable, it should be one the plugin
	// would be happy running every fifteen seconds forever.
	Metrics(ctx context.Context) []Measurement
}

// MetricSources collects every registered source, keyed by plugin name.
func MetricSources(c *core.Core) []Contribution[MetricSource] {
	return Contributions[MetricSource](c, MetricSourcePrefix)
}
