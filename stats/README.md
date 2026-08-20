# stats plugin

Collects every plugin's `StatContributor` hook into one snapshot on a job, and
serves it back as a page and a small home-page widget. A site with a dozen
plugins has a dozen interesting numbers and nowhere to see them together; this
is that place, and it needs no knowledge of any plugin to do it.

**Registry-driven, so nothing here changes when a plugin is added.** A plugin
opts in by implementing `pluginapi.StatContributor` and calling
`pluginapi.RegisterStats` in its `Provision`; this plugin discovers it by
prefix-scanning the core extension registry. There is no list of plugins in
this package and there should never be one.

**Deliberately absent:** history. Every number here is *current* — how many
releases, how many members — and none of it is kept over time, so the page
cannot draw a graph or answer "how fast is this growing". Adding that is a
table and a retention policy, and a snapshot cache is the wrong shape to grow
it from. `/metrics` on the host is the seam that already exposes these for a
scraper that *does* keep history, which is the better place for it.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `/p/stats` — "Site stats" | any signed-in member | `Public:false` with a zero `MinRole`. Nav group *Community*. |
| Site widget `stats` | as the host places it | The top 5 rows only, with a link to the full page. |
| Job **Stats Cache** | operator | Worker only. Hourly, one minute after boot. `MarkWrites()`, so scheduled runs hold off while the site is read-only — it only writes cache rows, but holding them back during a migration keeps the row-count verification clean. |

## Data

**Owns no tables.** There are no migrations in this package — the snapshot
lives in memory and is persisted through the host's `Deps.Cache` seam, because
where a cache belongs is the host's decision (a table, Redis, a file) and not
this plugin's.

That has a consequence worth knowing before you deploy split processes, and
`Provision` makes the split explicit: the **collector** is registered only on
`worker` (or `all`), the **views** only on `web` (or `all`). The snapshot the
views read is the in-memory one, and there is no read-back seam from the cache
yet — so on a split deployment the page says *No snapshot yet* indefinitely,
while a single-process site is correct within a minute of boot. A real gap,
stated rather than left to be discovered.

## Dependencies

Core: `Scheduler`, `Errors`, the view registry, and the extension registry.

| Seam | Why the host supplies it |
|---|---|
| `Deps.Cache(ctx, []pluginapi.Stat) error` | Where the collected snapshot is persisted. Required. The host owns storage; this plugin owns collection. |

`SetDeps` must be called before `core.Boot`. `Provision` fails loudly without
it rather than degrading to a plugin that collects and silently discards.

## Hooks & callbacks

**Consumes** `pluginapi.StatContributor` — every extension registered under the
`stats:` prefix. Two methods: `StatsName()` for a stable label and
`Stats(ctx)` for the numbers.

**A failing contributor does not cost the rest.** The collection loop reports
the error through `core.Errors` and carries on, because a site with twelve
contributors should not lose its whole stats page to one plugin's locked table.
The failure is recorded rather than swallowed — an invisible failure here looks
exactly like a plugin with nothing to report.

Publishes nothing. Declares no events.

## Lifecycle

`Provision` requires `Deps.Cache`, registers the job and the two views, and
resolves nothing from the registry — contributor lookup happens **on each run**,
not at Provision, because all Provisions run before any Start and a sibling
that registers later would otherwise be invisible forever.

`Start` installs the run loop — one minute after boot, hourly thereafter — and
returns immediately on a web-only process, where there is no job to run.
`Stop` is a no-op.

**Every path out of `run` ends the job.** A run marked in flight that never
finishes never runs again on the loop and holds the shutdown drain open. The
nil-context guard sits *before* `SetRunning` for the same reason, and a cache
failure ends the run through `SetError` rather than leaving it hanging.

## Files

```
plugin.go       lifecycle, the collection job, the snapshot
views.go        the page and the widget, and their inline templates
deps.go         the one host seam
plugin_test.go  the job invariants and collection behaviour
```

## Testing

`go test ./stats/` — no database needed.

Covered: every path out of `run()` ends the job (the rule five plugins state in
comments and nothing enforced until now); the nil-context guard sits before
`SetRunning`; one failing contributor does not cost the others and *is*
reported; a cancelled context stops before the next contributor; a cache
failure still publishes the in-memory snapshot, so a down cache costs the
persisted copy and not the page; and `snapshot()` is safe under `-race` against
a concurrent run.

**Not covered:** the two views' templates are not executed in a test, so a
field renamed in the view model would truncate the page mid-render with a 200
and nothing would fail. That is the checklist's §14 template rule and this
plugin does not meet it yet. The split-process gap above is a known design
hole, not a test gap.
