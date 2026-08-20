# pointstore plugin

Owns **flair** — a small coloured badge on a member's public profile — and
sells it through the points store. The counterpart to points earned elsewhere:
a currency with nothing to spend it on is a number, not an economy.

**It no longer has a shop.** This plugin used to register `/p/store`, its own
three-item storefront, which sat in the site nav one menu away from the points
store and read as a second one. Flair is a points-store **item** now
(`store.RewardFlair`), bought where invites and ranks are bought. What stayed
here is what is genuinely this plugin's: the catalogue, the equip rule, the
profile widget, and the granter the store calls.

That split is the point of the `FlairGranter` seam. The store knows how to take
points and refund them; it does not know what flair is. This knows what flair
is and never touches a balance.

**Deliberately absent:** an admin UI for the catalogue. The three flairs are a
`var` in `views.go`, which is honest for a demo and is the first thing a real
site would want to change. A definition editor is the pattern to follow — see
the four the host already ships.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| View `flair` | public | `SlotUserWidget` on a profile. Renders **nothing** when the subject has no flair, so the card is omitted rather than shown empty. Keyed on `core.ViewSubject` — the profile's owner, not the viewer. |

No routes of its own, no jobs, no widgets in the site regions.
`Processes: ["web"]`, any flavour.

## Data

Owns `pointstore.user_flair`: one row per member, `user_id` primary key.

**One flair per member, by the primary key.** Equipping is an upsert that
replaces, which has been this plugin's rule since it had its own shop — a new
purchase takes the slot rather than accumulating. `bought_at` moves with it.

There is no ownership history: buying Legend after VIP leaves no trace of VIP.
That is a deliberate simplification and a real limit — a member cannot switch
back to something they already paid for without paying again.

## Dependencies

Core: `Storage.SchemaDB`, the view registry, the extension registry.

No `SetDeps` seam — this plugin needs nothing from the host beyond core.

Points are **not** touched here. `core.Points.Deduct` is the store's job; see
*Hooks* for why that separation is load-bearing.

## Hooks & callbacks

**Publishes** `pluginapi.FlairGranter` under `flair.granter`.

`EquipFlair(ctx, userID, flairID) (string, error)` is **grant-only**: the store
has already debited the member and will refund if this returns an error. Two
consequences the code comments spell out:

- An unknown flair id is an **error, never a guess**. An admin typo in an
  item's `reward_ref` must fail the purchase — which refunds — rather than
  equipping something the member did not buy.
- The returned string is the display name, which the store shows back to the
  member. It is not an id and callers should not parse it.

Declares no events. Consumes nothing.

## Lifecycle

`Provision` opens the schema DB, parses the templates, registers the granter,
and registers the profile view. `Start` and `Stop` are no-ops.

Migrations run through the host's runner with `search_path` scoped to the
`pointstore` schema.

## Files

```
plugin.go       lifecycle; registers the granter and the profile view
views.go        the flair catalogue, EquipFlair, the profile widget render
store.go        Store, PGStore — Flair and SetFlair
templates/      flair.html
migrations/     001_init.sql
store_test.go
```

## Testing

`go test ./pointstore/` — no database needed.

Covered: the store's behaviour through the in-memory double, and the catalogue
lookup.

**Not covered, and worth knowing:** `EquipFlair`'s refund contract is only
exercised from the store side, so nothing here proves that an unknown id
returns an error *before* `SetFlair` runs. The upsert-replaces rule is a
property of the primary key and is not verified against a real Postgres —
`pluginapi/pgtest` is the harness for that, and it is a short test somebody
should write. `flair.html` is not executed in a test.
