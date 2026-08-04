# dbmaint plugin

Database-maintenance worker jobs, extracted from the host's `pkg/services`.
Four scheduled jobs keep Postgres lean and honest:

| Job (`/admin/jobs` name) | Cadence | Online? | What it does |
| --- | --- | --- | --- |
| **Repack NZBs (online)** | weekly | yes | `pg_repack -t <tables> -x` — rebuilds tables + indexes with a copy-and-swap, brief lock only. The active reclaim path. |
| **Reindex (online)** | monthly | yes | `REINDEX INDEX CONCURRENTLY` on the largest used indexes not covered by pg_repack's `-x`. |
| **Verify Indexes** | weekly | yes | `bt_index_check(heapallindexed => true)` over collatable btree indexes. Finds corruption that raises no error and breaks no constraint. Read-only (AccessShareLock). |
| **Vacuum NZBs** | weekly | **no** — maintenance window | `VACUUM FULL nzbs`. Takes an AccessExclusiveLock, so the site shows a maintenance page. **Paused by default** — pg_repack replaced it; kept as a manual-trigger fallback for hosts without pg_repack. |

All four are worker-only (`Processes: ["worker"]`), off-peak-gated
(`MarkOffPeak`), and admin-tunable via `DeclareConfig` knobs (tables, disk
safety multiplier, timeouts, index size/count thresholds). Each holds its own
mutex so a manual `/admin/jobs` trigger can't race the scheduled loop.

## Dependencies (`SetDeps`, worker process, before `core.Boot`)

The heavy table ops + disk probe are host seams; the scheduling and config
machinery come from `loon/schedule`. The host injects:

- **`Diag`** — `GetTableTotalSize`, `IsPGExtensionInstalled`, `GetIndexUsage`,
  `ReindexIndexConcurrently`, `DropInvalidIndexes`, `ListBtreeIndexes`,
  `VerifyIndex`. Most are primitive; the two slice-returning methods
  (`GetIndexUsage`, `ListBtreeIndexes`) need a small host-side conversion into
  the plugin's own `IndexUsage` / `BtreeIndex` types.
- **`StatCache`** — persists each run's duration for the next run's ETA.
- **`Nzbs`** — `VacuumFullNzbs` (VACUUM FULL only).
- **`Maintenance`** — the host maintenance-mode gate (`Begin`/`End`);
  `middleware.Global` satisfies it. VACUUM FULL only.
- **`ConfigStore`** — `schedule.JobConfigStore` (the host JobRun repo) backing
  the admin knobs.
- **`FreeDisk`** — `func(ctx) (int64, error)` free bytes on the working volume;
  the host wraps gopsutil so this module stays dependency-light. Fail-soft: an
  error skips the disk pre-flight rather than blocking the run.
- **`Repack`** — the pg_repack CLI connection target (host/port/user/pass/db).

The `pg_repack` binary + extension are checked at runtime; if absent the Repack
job logs a clear install hint and skips (no error). **Verify Indexes** does the
same for the `amcheck` extension (`CREATE EXTENSION amcheck;` — it ships with
Postgres but is not installed by default).

## Why Verify Indexes exists

Index corruption from a collation change is **silent**. A glibc or ICU upgrade
alters how text sorts; every existing text index is then subtly mis-ordered but
remains internally consistent, so it passes a structural check, raises no error,
and simply stops finding rows.

- On a **unique** index the damage eventually announces itself, badly: an
  `ON CONFLICT` consults the index, misses the duplicate it should have found,
  and the importer inserts it again. The symptom looks like an application bug.
- On a **non-unique** index nothing announces it at all — queries quietly return
  fewer rows than they should, forever.

Both happened on the ameNZB production database. The unique case surfaced as
duplicate usernames and `banned_paths`; the sweep that followed found two more
non-unique indexes that had been returning wrong results with nothing anywhere
reporting a problem. That is the argument for a schedule rather than a
suspicion.

Two settings carry the weight:

- **`heap_all_indexed`** (default on) is what makes the check meaningful. A
  collation-damaged index is well-formed and merely incomplete, so only a
  heap-against-index comparison finds it. With this off the job runs much faster
  and cannot detect what it exists for.
- **`collatable_only`** (default on) restricts the sweep to indexes whose
  ordering depends on a collation, using `pg_index.indcollation`. Indexes on
  integers, timestamps or bytea cannot be damaged that way. Turn it **off** for
  a full audit after a hardware fault, disk error, or unclean shutdown.

Cost scales with the **table**, not the index: each check reads the whole heap
once, so `max_table_mb` and `per_index_timeout_sec` bound the run together. A
timeout is reported as SKIPPED, never as corruption — false alarms would land on
the largest tables first, and an alert that cries wolf about the busiest table
every week is one nobody reads when it finally tells the truth.

Corruption is **reported, not repaired**, by default. `auto_reindex` opts into
rebuilding corrupt non-unique indexes; unique ones are never auto-rebuilt, since
a broken unique index has usually already let through the duplicates that would
make the rebuild fail. Findings go to `core.Errors` as well as the job log,
because the log ring is in-memory and every deploy clears it — the lesson the
Reindex job learned after ~80 silent monthly failures.

## Notes

- The off-peak / interval-override / CPU / panic hooks are installed **globally
  by the host** in `cmd/main.go`, so the plugin calls the bare
  `schedule.ServiceLoop` (no per-call hooks).
- Owns no tables and ships no migrations — it only reads catalog metadata and
  runs maintenance statements against host-owned tables.
