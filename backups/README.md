# backups plugin

A weekly job that dumps the database and then runs **every** plugin's
`Backupable` hook, writing each into one archive. A site made of forty plugins
that each own a schema has forty things to back up, and an operator should have
to think about none of them.

**Not to be confused with `backup` (singular)** in this same repo. That is
production's index/pack/pull pipeline — several thousand lines solving a
different problem: assets too large to re-transfer whole, kept on hardware
separate from the box that made them. This one is the generic answer, about a
hundred lines, and the one a demo host wires.

**Registry-driven.** A plugin opts in by implementing `pluginapi.Backupable`
and calling `pluginapi.RegisterBackup(c, self)` in its `Provision`; this plugin
discovers it off the core extension registry in name order. Adding a
backup-capable plugin needs no change here, and there is no list of plugins in
this package.

**Deliberately absent: restore.** This writes an archive and nothing reads one
back. That is not an oversight to be fixed casually — a restore has to decide
what happens to rows created since the dump, and that decision belongs to
whoever is holding the incident, not to a plugin. What is here is the half that
must run unattended; the other half is a person with a shell.

Also absent: retention. Every run writes a fresh set of entries and nothing
prunes old ones, because where the bytes go is the host's `OpenEntry` seam and
so is deciding when they expire.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| Job **Backup** | operator | Worker only — `Processes: ["worker"]`. Weekly, ten minutes after boot. `MarkOffPeak()` so it skips scheduled runs while traffic is above the configured threshold, and `MarkWrites()` so it holds off while the site is read-only. Manual "run now" bypasses the off-peak gate, which is the point of a manual trigger. |

No routes, no views, no widgets. Nothing member-facing at all.

## Data

**Owns no tables.** It reads other plugins' data through their own hooks and
writes bytes through the host's seam. It has no migrations and no schema.

Archive entry names are part of the contract, because a restore script reads
them:

| Entry | From |
|---|---|
| `database.sql` | `Deps.DumpDB` — the host's `pg_dump` |
| `<plugin>.bak` | each `Backupable.BackupName()` |

## Dependencies

Core: `Scheduler`, `Errors`, and the extension registry.

| Seam | Why the host supplies it |
|---|---|
| `Deps.OpenEntry(ctx, name) (io.WriteCloser, error)` | **Required.** Where one named entry's bytes go. The host decides storage — a file in a dated directory, a tar member, an object-store key — and this plugin decides only what the entries are called and what goes in them. |
| `Deps.DumpDB(ctx, w) error` | **Optional.** Writes a logical database dump. Nil skips the `database.sql` entry entirely rather than writing an empty one that would claim the database was captured. |

`SetDeps` must be called before `core.Boot`; `Provision` fails loudly without
`OpenEntry`, because a backup plugin that cannot write anywhere is worse than
no backup plugin — it reports success.

## Hooks & callbacks

**Consumes** `pluginapi.Backupable` — every extension under the `backup:`
prefix, in name order (`ExtensionNames` is sorted, so archive order is stable
between runs).

**One failing hook does not cost the backup.** The loop reports the error
through `core.Errors` and continues to the next plugin. A backup that aborted
on the first error would silently stop covering everything after it in registry
order, for a reason nobody looks at until a restore — which is the worst
possible time to find out.

A partial write is **kept**, not discarded: this plugin cannot tell whether
half a payload is useless or the most that could be had, and the reported error
is what marks the entry suspect.

Publishes nothing. Declares no events.

## Lifecycle

`Provision` checks `Deps.OpenEntry`, registers the job, and installs the manual
trigger. `Start` installs the run loop — ten minutes after boot, weekly
thereafter. `Stop` is a no-op.

Hook discovery happens **on each run**, not at Provision: all Provisions run
before any Start, so a sibling that registers its hook later would otherwise
never be backed up.

**Every path out of `run` ends the job**, and it matters more here than in most
plugins. This job runs *weekly*: a run left marked in flight never runs again
on the loop, so the symptom is a site that quietly stops being backed up, and
nothing says so until somebody needs the backup. The nil-context guard sits
before `SetRunning` for the same reason.

Every entry is closed, including one whose hook failed midway — otherwise the
archive holds an open handle per failing plugin, every week.

## Files

```
plugin.go       lifecycle, the weekly job, writeEntry
deps.go         the two host seams
plugin_test.go  the job invariants and what ends up in the archive
```

## Testing

`go test ./backups/` — no database and no real storage needed; the tests stand
an in-memory archive in for `OpenEntry`.

Covered: every path out of `run()` ends the job across six shapes (nothing to
back up, the ordinary path, a failed dump, a failed hook, an archive that
refuses an entry, a cancelled context); the nil-context guard; one failing hook
does not cost the rest **and is reported**; every entry is closed even when its
hook failed; the partial write is kept; entry naming and order; an unwired
`DumpDB` skips the database entry without reporting an error; and a cancelled
context opens nothing.

**Not covered:** nothing exercises a real `pg_dump` or a real object store —
those are the host's seams and the host's tests. There is no test that a
written archive can be *read back*, because nothing reads one back yet.
