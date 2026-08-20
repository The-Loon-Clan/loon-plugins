# pluginapi

**Not a plugin.** This is the contract package — the declared seams plugins use
to reach each other and the host without importing each other.

It is the answer to the question a plugin author asks first and used to answer
by grepping: *does this already exist?* Read this for the **mechanism**;
[SEAMS.md](../SEAMS.md) is the **catalogue** of what exists, and
[CHECKLIST.md](../CHECKLIST.md) §1 is what a plugin must satisfy to use it.

## The mechanism, in full

loon's extension registry *is* the hook bus. `core.Register(name, svc)`,
`core.Lookup(name)`, `core.ExtensionNames()` — that is the whole of it, and it
needs no core change to add a contract.

What this package adds is a **typed convention on top**, so call sites are not
stringly-typed:

```go
// The contract — an interface, a Name constant, and nothing else.
const StatsName = "stats:"

type StatContributor interface {
    StatsName() string
    Stats(ctx context.Context) ([]Stat, error)
}
```

A plugin **participates** by implementing the interface and registering itself
in `Provision`. Another plugin **collects** by prefix-scanning. Neither imports
the other; both import this.

```go
// in some plugin's Provision
pluginapi.RegisterStats(c, p)

// in the stats plugin's job
for _, sc := range pluginapi.StatContributors(c) { … }
```

**Register in `Provision`, look up in `Start`.** All `Provision`s run before
any `Start`, so a sibling resolved at Provision time may not have registered
yet. Getting this backwards produces a plugin that silently does nothing — the
quietest failure this architecture has.

## Two shapes: a name, or a prefix

| Shape | For | Example |
|---|---|---|
| `…Name` | **one** provider answers | `CatalogName = "catalog.taxonomy"` |
| `…Prefix` | **many** contribute and all are collected | `MetricSourcePrefix = "metrics.source."` |

There are **62** declared contracts as of 20 Aug 2026 — recount rather than
trusting that, exactly as SEAMS.md says, because it drifted from 41 to 52
without anyone noticing:

```
grep -rhoE '[A-Za-z]+(Name|Prefix)\s+=\s+"[^"]+"' pluginapi/*.go \
  --exclude='*_test.go' | sort -u
```

### Never hand-roll a prefix scan

Use `Contributions[T]`, which returns contributors in sorted key order and
skips anything registered under the prefix that does not satisfy `T`:

```go
for _, c := range pluginapi.Contributions[MetricSource](c, MetricSourcePrefix) {
    …
}
```

A hand-rolled loop over `ExtensionNames()` is how the ordering stops being
deterministic and how a wrongly-typed registration becomes a panic instead of
a skip.

### "Can another plugin append to this?"

Ask it of every closed set. A list of two things that only the host may extend
becomes a prefix the day a plugin needs a third — and `store.itemtype.*` is the
worked example: the store's item types were a closed enum until charity needed
one, and turning it into a prefix opened the catalogue to every plugin rather
than to one more.

## The other tier, and why it is empty

A **bare-string convention** is a key agreed between one host and one plugin,
typed as a raw func, declared nowhere. Nothing lists them, so a plugin author
cannot find one — and the tenth plugin needing a CSRF token invents an eleventh
key, or ships without one and every form 403s. That happened: 58 tokenless
forms across nine plugins.

All of them are collapsed into declared contracts now and the tier is empty of
live seams. **Nothing new should add to it.** If two components need to agree
on a key, the agreement belongs in a file in this package.

## What is here that is not a contract

Four things, and they are here because every plugin needs them and getting them
wrong is quiet:

| File | Why |
|---|---|
| `ownership.go` | `OwnedBy` / `VisibleTo`. **User id 0 is never an owner** — it is both "nobody is signed in" and the reserved system id, and a bare `record.UserID == viewerID` hands an anonymous viewer anything owned by 0. Found four times by accident in two days. |
| `httperr.go` | `RedactURLError`. `net/http` embeds the full URL in every transport error, so `fmt.Errorf("%w", err)` writes a query-string API key into `error_logs`. |
| `contributions.go` | The prefix-scan helpers above. |
| `pgtest/` | A scratch schema, the plugin's own migrations, one `LOON_TEST_DSN`. See its package doc. |

## Adding a contract

1. **Look first.** SEAMS.md, then this directory. The point of the package is
   that the answer is findable.
2. One file per contract, named after it. A package doc comment at the top
   saying what it is *for* and what the alternative would have cost —
   `imageintake.go` is the model.
3. A `…Name` or `…Prefix` constant, the interface or func type, and a
   collector if it is a prefix.
4. Add it to SEAMS.md's catalogue in the same commit. The count drifting by
   eleven is what that document exists to prevent.
5. Keep it **narrow**. A contract is the smallest thing that works: a plugin
   owns knowing that it wants a picture, and the host owns the HTTP client, the
   egress policy, the address rules, the size cap and the file store.

## Testing

`go test ./pluginapi/...` — no database for the package itself; `pgtest` is the
harness for the ones that need one.

Covered: the prefix-scan helpers, ownership, redaction (against a real client,
so the leak it guards is reproduced rather than assumed), multipliers,
rank stats, store items, the AI surface, and usenet coverage.

**Not covered:** most contracts are interfaces with no behaviour to test — they
are checked by the compiler at both ends, which is most of the argument for
declaring them here rather than agreeing a string.
