# backup plugin

Keeps a site's irreplaceable bytes recoverable. It does three jobs on three
clocks: it **indexes** every persistent asset directory daily (size, content
hash, per-class totals), it **dumps the database** weekly into that same asset
tree, and it serves both as **content-addressed packs** an off-site puller
fetches over the host's ops listener.

The direction is deliberate: the backup box PULLS. Production never holds a
second copy of its own data and never holds a write credential to the backup.
The older weekly **archive** job (zip the assets + `pg_dump` into a dated folder
on local disk) still exists for small installs, but on a site whose assets are
3.5× the database it cannot run — it stages a full second copy on the volume it
is protecting — and the pack pipeline is what replaced it.

## Surface

One admin page (`SlotAdminPage`, slug `backup`, filed under Operations) and no
routes of its own. The page answers, in this order: **is anything actually
taking the backup** (the acks), what a puller would be handed, per-class
coverage against the previous generation, published database dumps, index
history, and suspect files.

The pack server is published as a capability (`pluginapi.BackupPacksName`) and
the **host** mounts it wherever its own authentication already lives — on this
site, the tailnet-only worker ops listener:

```
GET  /ops/backup/manifest            generation + pack list (id, class, bytes)
GET  /ops/backup/pack/{id}?gen=N     the pack, Accept-Ranges + Range for resume
POST /ops/backup/ack                 a puller reports holding a generation
```

The ack is the only write. Without it this plugin can report what is
*available* to pull and nothing about whether anything pulls it — and a backup
that stopped a month ago looks exactly like one that ran last night.

Jobs (all worker-side, all visible in `/admin/jobs`):

| Job | Interval | What it does |
|---|---|---|
| **Backup Index** | daily | Walks the classes, stats everything, hashes what changed plus 1-in-8 of the rest, seals a generation. |
| **Backup Database** | weekly | `pg_dump -Fd -j4` into a dated directory inside the `db-dumps` class. |
| **Backup** | weekly | The legacy archive: zips classes + `pg_dump` into `<BackupDir>/YYYY-MM-DD_HHMMSS/`. |

Process kinds (`Metadata.Processes`): `web`, `worker`. Provision gates on
`c.Process`: the page registers web-side, the jobs and the pack capability
worker-side. Without that gate the web process would register three jobs it
never runs — a Run button and a `next_run` nothing honours — and `Start` would
launch a second set of loops.

## Data

Owns the **`backup` Postgres schema** (migrations `001`–`002`):

- `generations` — one row per index pass: `started_at`, `sealed_at`, file/byte
  totals, how many were rehashed, and an error. An **unsealed** row is a pass
  that did not finish; nothing downstream may treat it as authoritative.
- `files` — the inventory: path, class, `sha256`, size, `mtime_ns`, `ctime_ns`,
  `inode`, `first_gen`/`last_gen`, `hashed_at`. `ctime_ns` and `inode` are in
  the stat gate on purpose — userspace cannot set `ctime` backward, so a clock
  step cannot hide a change.
- `class_stats` — per-generation, per-class file and byte totals. This is what
  the shrink gate compares against.
- `suspect` — files that looked wrong (a size change with no mtime change, a
  torn write), with a first/last-seen and a count, so a recurring one is
  visible rather than a one-line log that scrolled away.
- `acks` — one row per (generation, source): what a puller reported taking
  away, and the only record in this schema of anything that left the building
  (migration `002`).

Reads no other schema. Writes files only under the class directories and
`DBDumpDir`.

## Dependencies

Core services consumed: **Storage** (`SchemaDB("backup")`), **Scheduler** (the
three jobs), **Logger**, **Errors**, and the extension registry (`Register`).
No Router: the host mounts the HTTP surface.

Store: **self-contained** — builds its own `PGStore` at Provision.

Host seams (`SetDeps`, once in the worker, before `core.Boot`):

```go
backup.SetDeps(backup.Deps{
    DB:        backup.PGConn{Host: …, Port: …, User: …, Password: …, DBName: …},
    Config:    settingsService,   // the four Get… methods on ConfigStore
    Classes:   backupClasses(),   // slug, dir, order, regenerable, rotates
    Root:      "",                // "" = process working directory
    BackupDir: "backups",         // legacy archive target — bind mount
    DBDumpDir: "db-dumps",        // pg_dump target — bind mount AND a class
    FreeDisk:  func(ctx) (int64, error) { … },
    DBSize:    func(ctx) (int64, error) { … },
})
```

`FreeDisk`/`DBSize` are closures so this module needs neither gopsutil nor a DB
driver. `Config` stays a host seam rather than becoming `loon/schedule`
job-config vars: those knobs already live in the host's admin surface, and
moving them would migrate live operator settings rather than extract a job.

**`BackupDir` and `DBDumpDir` must be bind mounts.** Left on a container's
overlay they are wiped on every recreate, which turns the job into theatre — it
runs, logs success, and protects nothing. `DBDumpDir` has the sharper edge: on
the overlay a dump would be indexed, packed, pulled, and then vanish on the next
deploy while the array's manifest still claimed to hold it.

**`DBDumpDir` must also be registered as an `AssetClass`** (with `Rotates:
true`). Writing the dump is only half of getting the database into the backup;
the half that matters is the index walking it into a generation like any other
file.

## Hooks & Callbacks

- Host hooks SET: (none).
- Extensions PUBLISHED: `pluginapi.BackupPacksName` — `Manifest(ctx)`,
  `WritePack(ctx, w, gen, id, skip)` and `Ack(ctx, a)`. Registered in
  `Provision`, not `Start`, because the host wires its routes immediately after
  `Boot`. Worker-side only.
- Extensions CONSUMED: (none).

## Lifecycle

**Provision** refuses to boot without `Config`, `BackupDir`, or a database name
— a backup plugin that starts up misconfigured and logs success is the failure
mode this whole package exists to prevent. It builds the store, registers the
three jobs, and publishes the pack capability.

**Start** launches one loop per job from `loops()`, which is declared as data so
a test can assert that every REGISTERED job is also SCHEDULED. That invariant
was broken once and stayed broken silently: the index job had a Run button and
an interval but no loop, so it only ran when someone pressed the button and the
inventory quietly stopped advancing.

**Stop** is a no-op.

## Four behaviours worth knowing

**The shrink gate.** A missing bind mount is indistinguishable from an empty
class: the boot check creates the directory, the walk finds nothing, the
generation seals with zero files, and retention ages out the only copy — every
step green. So a class that loses more than 10% of its files against the last
sealed generation makes the pass refuse to seal. Classes marked `Rotates`
(the database dump) are exempt from the percentage and checked only for the
signature that matters: had files, now has none.

**Hashing is the exception, not the rule.** The corpus is ~420k immutable
images; the stat gate does the work, and 1-in-8 of the inventory is re-read
per run regardless — full coverage roughly weekly, which is what catches bit
rot and a torn write whose mtime never moved again.

**The database is a full dump, always.** Over 31 days `nzbs` took 588k inserts
against 193.6M updates, and it has no `updated_at` — so a row-id incremental
cannot see updates at all and would restore a database missing every edit and
resurrecting every deletion. The same arithmetic kills WAL archiving. What makes
the full dump cheap is honest instead: `--exclude-table-data` for request logs,
caches and job history (~3.9 GB here), schema still dumped, zero correctness
risk. Dumps publish to a dated directory via a dot-prefixed staging name the
index skips, so a half-written dump can never be packed and served as a backup.

**Packs are streamed, not stored.** A pack is assembled into the response as its
members are read, so production stores no second copy. The pack id is the
fingerprint of its member list, and the puller verifies length and every CRC
before renaming into place — a `.zip` at its final path is complete and verified
by construction.

## Files

- `plugin.go` — registration, the three jobs, `loops()`, the pack capability.
- `deps.go` — the host seams and `ConfigStore`.
- `index_job.go` — the daily pass: shrink gate, rehash fraction, logging.
- `inventory.go` — the walk, the stat gate, hashing, `detectShrink`, class order.
- `inventory_store.go` — the `backup` schema queries.
- `manifest.go` — generation → pack list, and the served manifest.
- `pack.go` — pack composition and the STORED-zip writer.
- `dbdump.go` — `pg_dump -Fd`, atomic dated publish, local retention.
- `jobs.go` — the legacy archive job (zip + dump + prune).
- `views.go` + `templates/backup.html` — the admin page.
- `stat_unix.go` / `stat_other.go` — `ctime`/`inode` per platform.

## Testing

Unit-tested: the shrink gate (collapse caught, churn ignored, new classes
ignored, rotating classes exempt except on collapse), class ordering, the
archive mode matrix, prune (keeps newest N, ignores foreign folders), the
zip writer against a golden file, manifest composition, the dump's retention
listing and stamp ordering, the identifier gate on the exclusion setting, and
the every-registered-job-is-scheduled invariant.

The suite runs under `TZ=UTC`, `America/Los_Angeles` and `Asia/Tokyo`, because
`stampFormat` carries no zone: `Format` writes local time, so the guard must
`ParseInLocation`.

Needs integration (live DB): the inventory store and a full index pass —
`inventory_integration_test.go`, which wants a throwaway Postgres.

Not covered by tests: `pg_dump` itself. The dump job's shell-out is exercised
only in production, so its failure path is built to be loud (bounded stderr
capture into the job log and the error sink) rather than clever.
