# catalog plugin

The content taxonomy. It owns the standard Newznab category tree — Console,
Movies, Audio, PC, TV, XXX, Books, Other — decides which top-level categories
an admin has enabled, and works out which category a release belongs in from
its group and title.

It publishes one capability that indexer plugins read for their Newznab caps
and their categorisation, and contributes a section to the host's
`/admin/settings`.

**"List everything, pick what to index."** The tree is complete and static; the
table records what an admin turned *off*. That direction matters: a new
category added to the taxonomy is enabled by default rather than invisible
until somebody notices, and a site that has never touched the setting behaves
like a site with the standard tree.

**Deliberately absent:** metadata scraping. Enrichment — posters, summaries,
external ids — is a separate pluggable `MetadataSource` layer that sits on top;
see the `scraper` plugin and `SCRAPER-ARCHITECTURE.md`. This plugin answers
*what kind of thing is this*, and nothing about what it is.

Also absent: per-subcategory enabling. The toggle is top-level only.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| View `catalog` — "Categories" | admin | `SlotAdminSettings`, so it appears inside the host's settings page rather than as a page of its own. One action, `toggle`. |

No routes, no jobs, no widgets. `Processes: ["web", "worker"]` — the worker
needs the capability for categorising what it indexes, the web process for the
settings section and covers. Any flavour.

## Data

Owns two tables in the `catalog` schema:

| Table | Holds |
|---|---|
| `category_disabled` | One row per top-level category an admin switched **off**. Absence means enabled. |
| `catalog_entry` | Identified works — kind, external namespace + id, title, normalised title, cover URL. |

`catalog_entry` is what the metadata layer fills and what release covers are
resolved through. Its `norm_title` exists so matching does not have to
re-normalise on every lookup.

## Dependencies

Core: `Storage.SchemaDB`, the view registry, the extension registry.

`deps.go` carries the host seams this plugin needs for cover resolution; see
the file for the current set.

## Hooks & callbacks

**Publishes** `pluginapi.CatalogName`. The service is the whole contract:

| Method | Answers |
|---|---|
| `All`, `Enabled`, `IsEnabled` | the tree, and what an admin left on |
| `Categorize(group, title)` | which category a release belongs in |
| `Name(id)` | the display name for a category id |
| `Upsert(entry)` | record an identified work |
| `SetReleaseCover`, `ReleaseCover`, `ReleaseCovers` | cover art, with a **batch** read because a listing page wants fifty at once |

Declares no events.

### Categorisation is a pile of heuristics, and says so

`categorize` reads the newsgroup first and falls back to the title, and
`categories.go` is a long list of rules — fansub numbering, CRC tags,
resolution tiers, token matching that will not fire on a substring inside
another word. It is not a classifier and does not pretend to be; it is the set
of patterns that hold on real Usenet subjects, and it is tested as such.

The safe direction is **Other**: an unrecognised release lands somewhere
harmless rather than being asserted into a category it does not belong in.

## Lifecycle

`Provision` opens the schema DB, builds the service, registers the capability,
and registers the settings view. `Start` and `Stop` are no-ops.

## Files

```
plugin.go         lifecycle, registration
service.go        the published capability
categories.go     the taxonomy and every categorisation heuristic
store.go          Store, PGStore
views.go          the admin settings section and its toggle
deps.go           host seams
templates/
migrations/
categories_test.go  the heuristics, on real subjects
store_test.go
```

## Testing

`go test ./catalog/` — no database needed.

Covered: the categorisation rules against real-shaped subjects, including the
cases they are easy to get wrong — a token appearing inside another word, anime
fansub numbering versus ordinary episode numbering, and the resolution tiers.

**Not covered:** the store's SQL, so "which categories does `Enabled` actually
return" is exercised only through the double; the settings template is not
executed in a test; and nothing verifies the `catalog_entry` upsert's conflict
behaviour against a real Postgres, which is the one place a wrong constraint
would silently create duplicate works.
