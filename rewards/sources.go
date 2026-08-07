package rewards

import (
	"context"
	"fmt"
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

// SourceCatalogExtension is the registry key a host publishes its SEED
// catalogue under.
//
// A seed, not the catalogue itself: the catalogue is the reward_sources table,
// and this is only what gets written into it the first time the plugin boots
// against an empty one. Code proposes, configuration disposes — after the
// first boot an operator owns the list, and a host changing its seed will not
// silently rewrite what they edited.
const SourceCatalogExtension = "rewards.sources"

// SourceCatalog is a set of source definitions.
type SourceCatalog []SourceDef

// StockSources is the set a general-purpose site is likely to want, offered so
// a host seeds its catalogue by editing a list rather than inventing the
// vocabulary from nothing.
//
// Seeded ONCE, into an empty table, and then it is configuration: an operator
// disables what this site does not have and adds what it does, without a
// deploy. A site with no forum turns the forum rows off; only the operator
// knows that, and now they can act on it.
//
// The keys are the contract — changing one orphans every reward and
// achievement already pointing at it, which is why the table makes key its
// primary key rather than something renameable.
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

// Catalogue reads the configured sources, enabled ones only, sorted for
// display.
//
// Read live rather than snapshotted at boot, unlike the metric SOURCES: those
// are code and cannot change while the process runs, but this is configuration
// and an edit must take effect on the next page render. It is an admin-page
// read of a table with a handful of rows, so the cost is not the concern —
// serving a stale dropdown after someone just fixed it is.
func (p *Plugin) Catalogue(ctx context.Context) (SourceCatalog, error) {
	if p.admin == nil {
		return nil, nil
	}
	return p.admin.ListSources(ctx)
}

// Source looks one up by key.
func (p *Plugin) Source(ctx context.Context, key string) (SourceDef, bool) {
	cat, err := p.Catalogue(ctx)
	if err != nil {
		return SourceDef{}, false
	}
	for _, d := range cat {
		if d.Key == key {
			return d, true
		}
	}
	return SourceDef{}, false
}

// seedSources writes the host's seed catalogue into an EMPTY table, once.
//
// Only when empty: a host that changes its seed must not overwrite what an
// operator edited, and re-seeding on every boot would resurrect rows they
// deliberately deleted. Returns how many were written so the boot log can say
// it happened — a silent seed is indistinguishable from a migration that did
// not run.
func (p *Plugin) seedSources(ctx context.Context, seed SourceCatalog) (int, error) {
	if len(seed) == 0 || p.admin == nil {
		return 0, nil
	}
	existing, err := p.admin.CountSources(ctx)
	if err != nil {
		return 0, err
	}
	if existing > 0 {
		return 0, nil
	}
	for _, d := range seed {
		if err := d.Valid(); err != nil {
			return 0, fmt.Errorf("seed catalogue: %w", err)
		}
	}
	return len(seed), p.admin.SeedSources(ctx, seed)
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

// sourceRow is the table's shape. Separate from SourceDef because `group` is a
// reserved word in SQL and the column is `grp`; letting that spelling leak
// into the type every caller uses would be the tail wagging the dog.
type sourceRow struct {
	Key     string `db:"key"`
	Label   string `db:"label"`
	Group   string `db:"grp"`
	Fires   bool   `db:"fires"`
	Counts  bool   `db:"counts"`
	Unit    string `db:"unit"`
	Units   string `db:"units"`
	Ordinal int    `db:"ordinal"`
	Enabled bool   `db:"enabled"`
	Stock   bool   `db:"stock"`
}

func (r sourceRow) def() SourceDef {
	return SourceDef{Key: r.Key, Label: r.Label, Group: r.Group,
		Fires: r.Fires, Counts: r.Counts, Unit: r.Unit, Units: r.Units}
}

// ListSources returns the enabled catalogue, ordered the way a dropdown wants
// it: by group, then the operator's ordinal, then label.
func (s *PGStore) ListSources(ctx context.Context) (SourceCatalog, error) {
	var rows []sourceRow
	if err := s.sel(ctx, &rows, `
		SELECT key, label, grp, fires, counts, unit, units, ordinal, enabled, stock
		  FROM reward_sources
		 WHERE enabled
		 ORDER BY grp, ordinal, label`); err != nil {
		return nil, err
	}
	out := make(SourceCatalog, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.def())
	}
	return out, nil
}

// CountSources counts every row, enabled or not. The seed asks this, and it
// must see a disabled row: an operator who turned every stock source off did
// not ask for them back on the next boot.
func (s *PGStore) CountSources(ctx context.Context) (int, error) {
	var n int
	err := s.get(ctx, &n, `SELECT count(*) FROM reward_sources`)
	return n, err
}

// SeedSources writes the initial catalogue. ON CONFLICT DO NOTHING rather than
// upsert: two workers booting together must not fight over it, and the loser
// writing nothing is the correct outcome.
func (s *PGStore) SeedSources(ctx context.Context, cat SourceCatalog) error {
	for i, d := range cat {
		if _, err := s.exec(ctx, `
			INSERT INTO reward_sources (key, label, grp, fires, counts, unit, units, ordinal, stock)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)
			ON CONFLICT (key) DO NOTHING`,
			d.Key, d.Label, d.Group, d.Fires, d.Counts, d.Unit, d.Units, i); err != nil {
			return fmt.Errorf("seed source %q: %w", d.Key, err)
		}
	}
	return nil
}
