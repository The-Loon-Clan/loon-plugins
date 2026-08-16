<p align="center">
  <img src="img/logo.png" alt="loon" width="180">
</p>

<h1 align="center">loon-plugins</h1>

<p align="center">A collection of plugins for the <a href="https://github.com/The-Loon-Clan/loon">loon</a> framework.</p>

---

**New or changed plugins are held to [CHECKLIST.md](CHECKLIST.md)** — contract,
security, i18n-readiness, admin surface, jobs, docs, tests, and how each item
is verified.

Each plugin is a self-describing module a loon host imports and boots. The job
machinery (`RegisterJob`, `RunLoop`, off-peak gating, the `/admin/jobs` view)
lives in loon's `schedule` package, so a plugin inherits the scheduler for free —
what a plugin declares is its **data surface**: its own Postgres schema, or a set
of narrow ports the host injects.

## Plugins

| Plugin | What it is |
|---|---|
| **`usenet`** | A full Usenet indexer — multi-provider fleet with per-backbone crawl state, multi-host coordination (leases + term assignment), pluggable staging (Postgres or Redis), a data-driven 24-rule junk engine + operator blacklist, NZB assembly, and health checking. Runs standalone or, in host-sink mode, hands assembled releases to a host's NZB domain (the `ReleaseSink`/`ReleaseHealthStore` seams). Owns its `usenet` schema; NNTP via `loon/nntp`. |
| **`scraper`** | Generic metadata scraper — shared jobs over a registry of pluggable `catalog.MetadataSource` modules (anidb, tmdb, mangadex, …). |
| **`catalog`** | Newznab category-tree taxonomy — the content classification a host and the usenet plugin categorise releases against. |
| **`backup`** | Single-job site backup: `pg_dump` + static dirs into one archive, with a free-disk pre-flight. |
| **`backups`** | Capability-hook backup — runs every plugin's `Backupable` hook into one archive. |
| **`dbmaint`** | Database maintenance jobs — `pg_repack`, reindex, and vacuum, off-peak-gated. |
| **`stats`** | Collects every plugin's `StatContributor` hook into a cached site-stats snapshot. |
| **`dailyreward`** | A home-page daily-points card (points sink demo). |
| **`pointstore`** | A points sink / profile-flair store. |
| **`anidbscraper`** | Mechanics demo — AniDB as a standalone host-data worker plugin (injected ports + `SetDeps`). |
| **`pluginapi`** | Neutral capability contracts both sides import (never each other): `UsenetIndex`/`UsenetAdmin`, the `ReleaseSink`/`ReleaseHealthStore` seams, `CatalogSink`/`Fillable`, `Backupable`/`StatContributor`, host-data ports. |

## Plugin archetypes

| Archetype | Owns a schema? | Data access | Example |
|---|---|---|---|
| **Self-contained** | Yes (`Metadata.Migrations`) | `core.Storage.SchemaDB` | **`usenet`** |
| **Host-data worker** | No | narrow ports injected via `SetDeps` | **`anidbscraper`** |
| **Capability hook** | No | reads peers off the extension registry | **`backups`, `stats`** |

## How a host consumes a plugin

1. In the host's `go.mod` (sibling-checkout `replace` until loon tags releases):

   ```
   require github.com/the-loon-clan/loon-plugins v0.0.0-...
   replace github.com/the-loon-clan/loon-plugins => ../loon-plugins
   ```

2. Import the plugin — its `init()` self-registers. Self-contained plugins need
   only a blank import; host-data plugins also call `SetDeps(...)` before
   `core.Boot`:

   ```go
   import _ "github.com/the-loon-clan/loon-plugins/usenet"   // self-contained
   ```

3. Docker: a `replace` pointing outside the build context needs a BuildKit
   build-context (`--build-context loonplugins=../loon-plugins` +
   `COPY --from=loonplugins`) — the same three-way contract loon uses.

See [loon-demo-site](https://github.com/The-Loon-Clan/loon-demo-site) for a working host
that wires `usenet`, `scraper`, `catalog`, `backups`, `stats`, `dailyreward`, and `pointstore`.

## Development

```
go build ./...
go vet ./...
go test ./...
```

loon is resolved as `../loon`; keep it checked out beside this repo.

## License

MIT — see [LICENSE](LICENSE).
