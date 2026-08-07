package rewards

import (
	"fmt"
	"sort"
	"strings"
)

// The catalogue of things an admin can pick from a dropdown.
//
// Before this, `rewards.trigger` and `achievements.metric` were free text and
// the picker was assembled from whatever was ALREADY configured plus a
// hardcoded "login". That list is empty exactly when it is most needed —
// setting up the first one — and a typo produced a reward that looked
// perfectly healthy and could never fire, because nothing anywhere knew what
// the valid names were.
//
// So the host DECLARES what it offers, once, and every picker reads the
// declaration. A name that is not in the catalogue is a configuration error
// the admin page can refuse, rather than silence discovered months later by a
// member asking where their badge went.

// SourceDef is one thing the site can reward or score, as the admin sees it.
//
// Fires and Counts are separate because most things are both and some are
// neither-both: a post can announce itself the moment it is written AND be
// counted over a lifetime, while "days registered" only ever counts and
// "password changed" only ever fires. One flag could not say that.
type SourceDef struct {
	// Key is the stable id stored in rewards.trigger / achievements.metric.
	// Dotted and lowercase by convention: "posts.created".
	Key string
	// Label is the dropdown text: "Posts created".
	Label string
	// Group buckets the dropdown — "Forum", "Uploads", "Requests". A flat
	// list of thirty entries is a list nobody reads.
	Group string
	// Fires — a surface announces this the moment it happens, so it can be a
	// reward's trigger and can drive immediate achievement evaluation.
	Fires bool
	// Counts — a running total exists, so it can score an achievement
	// threshold. A source that Counts must register a MetricSource; one that
	// only Fires need not.
	Counts bool
	// Unit and Units name what is being counted, so an achievement can be
	// named without the admin typing it: "First post", "100 posts".
	Unit  string
	Units string
}

// Valid reports whether a def is usable. A def that neither fires nor counts
// can be selected and then do nothing, which is the failure this catalogue
// exists to remove.
func (d SourceDef) Valid() error {
	switch {
	case d.Key == "":
		return fmt.Errorf("source has no key")
	case d.Label == "":
		return fmt.Errorf("source %q has no label; the dropdown would show a blank row", d.Key)
	case !d.Fires && !d.Counts:
		return fmt.Errorf("source %q neither fires nor counts, so nothing could ever use it", d.Key)
	case d.Counts && d.Unit == "":
		return fmt.Errorf("source %q counts but names no unit, so achievements on it cannot be named", d.Key)
	}
	return nil
}

// SuggestName is the achievement name an admin gets offered for a threshold on
// this source: "First post" at one, "100 posts" above.
//
// A suggestion, not a rule — the field stays editable, because "Centurion" is
// a better name than "100 posts" and no generator will think of it. But an
// empty name field is how a catalogue ends up full of achievements called
// "achievement-3".
func (d SourceDef) SuggestName(threshold int64) string {
	if d.Unit == "" {
		return ""
	}
	if threshold <= 1 {
		return "First " + d.Unit
	}
	units := d.Units
	if units == "" {
		units = d.Unit + "s"
	}
	return fmt.Sprintf("%d %s", threshold, units)
}

// SourceCatalogExtension is the registry key a host publishes its catalogue
// under. One registration, not one per entry: the set is a single editorial
// decision about what this site rewards, and assembling it from fragments
// makes "what can I pick?" unanswerable without booting.
const SourceCatalogExtension = "rewards.sources"

// SourceCatalog is what a host registers.
type SourceCatalog []SourceDef

// StockSources is the set a general-purpose site is likely to want, offered so
// a host declares its catalogue by editing a list rather than inventing the
// vocabulary. It is NOT registered automatically: a site with no forum should
// not show forum achievements in its dropdowns, and only the host knows.
//
// A host takes these, drops what it does not have, adds its own, and registers
// the result. The keys are the contract — changing one orphans every reward
// and achievement already pointing at it.
func StockSources() SourceCatalog {
	return SourceCatalog{
		{Key: "login", Label: "Logged in", Group: "Account",
			Fires: true, Counts: true, Unit: "login", Units: "logins"},
		{Key: "posts.created", Label: "Posts created", Group: "Forum",
			Fires: true, Counts: true, Unit: "post", Units: "posts"},
		{Key: "threads.created", Label: "Threads started", Group: "Forum",
			Fires: true, Counts: true, Unit: "thread", Units: "threads"},
		{Key: "comments.created", Label: "Comments posted", Group: "Comments",
			Fires: true, Counts: true, Unit: "comment", Units: "comments"},
		{Key: "uploads.created", Label: "Uploads", Group: "Uploads",
			Fires: true, Counts: true, Unit: "upload", Units: "uploads"},
		{Key: "requests.created", Label: "Requests opened", Group: "Requests",
			Fires: true, Counts: true, Unit: "request", Units: "requests"},
		{Key: "requests.filled", Label: "Requests filled", Group: "Requests",
			Fires: true, Counts: true, Unit: "fill", Units: "fills"},
	}
}

// Catalogue returns the host's declared sources, sorted for display: by group,
// then label. Empty when the host declared none, which every picker must
// tolerate — an install can run without one, it just cannot offer a dropdown.
func (p *Plugin) Catalogue() SourceCatalog {
	out := make(SourceCatalog, len(p.sources))
	copy(out, p.sources)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// Source looks one up by key.
func (p *Plugin) Source(key string) (SourceDef, bool) {
	for _, d := range p.sources {
		if d.Key == key {
			return d, true
		}
	}
	return SourceDef{}, false
}

// Triggers is the reward trigger picker's options: everything that fires.
func (c SourceCatalog) Triggers() SourceCatalog {
	return c.filter(func(d SourceDef) bool { return d.Fires })
}

// Metrics is the achievement metric picker's options: everything that counts.
func (c SourceCatalog) Metrics() SourceCatalog {
	return c.filter(func(d SourceDef) bool { return d.Counts })
}

func (c SourceCatalog) filter(keep func(SourceDef) bool) SourceCatalog {
	out := make(SourceCatalog, 0, len(c))
	for _, d := range c {
		if keep(d) {
			out = append(out, d)
		}
	}
	return out
}

// Keys is the flat list, for callers that only need the strings.
func (c SourceCatalog) Keys() []string {
	out := make([]string, 0, len(c))
	for _, d := range c {
		out = append(out, d.Key)
	}
	return out
}

// ── Schedules ───────────────────────────────────────────────────────────────

// ScheduleDef is one named cadence a job can run on, so an operator picks
// "Hourly" rather than typing a cron expression whose mistakes are silent —
// a stray field in `0 0 * * *` does not fail, it runs at the wrong time.
type ScheduleDef struct {
	Key   string
	Label string
	// Minutes is the interval. The job machinery takes a duration, not a cron
	// string, so this catalogue speaks the same language rather than
	// introducing a parser and the class of bug that comes with one.
	Minutes int
}

// Schedules is the fixed set. Deliberately short: these are the cadences that
// make sense for scoring counters, and an operator who needs "every 7 minutes"
// wants the interval override, not a new dropdown entry.
func Schedules() []ScheduleDef {
	return []ScheduleDef{
		{Key: "15m", Label: "Every 15 minutes", Minutes: 15},
		{Key: "hourly", Label: "Hourly", Minutes: 60},
		{Key: "6h", Label: "Every 6 hours", Minutes: 360},
		{Key: "daily", Label: "Daily", Minutes: 1440},
	}
}

// Schedule resolves a key, reporting whether it is known.
func Schedule(key string) (ScheduleDef, bool) {
	for _, s := range Schedules() {
		if strings.EqualFold(s.Key, key) {
			return s, true
		}
	}
	return ScheduleDef{}, false
}
