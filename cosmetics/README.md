# cosmetics plugin

Sells what a member **looks like**: name effects, avatar frames, profile
backgrounds, and a custom title.

Every other reward on a loon site pays in NUMBERS — points, ratio, a rank you
climb — and a number is invisible to everybody except its owner. A cosmetic is
the other kind: it costs the site nothing, grants no advantage, and is the only
reward anybody else can see. That is the whole appeal, and why every tracker
with a shop sells one.

**Deliberately absent:** uploaded images. Nothing here lets a member supply a
file or a URL — the sixteen effects are a fixed list and cannot be made to say
or show anything. The one exception is the custom title, which is text somebody
typed, and it is the only part with a staff queue.

## The catalogue is a CONTRACT, not plugin-private

A cosmetic is two halves in two repositories: **this plugin sells and records
it, the HOST draws it**, and drawing it means CSS in the host's stylesheet and a
class on the host's own components. A slug they disagree about fails silently —
the sale settles, the class lands on the name, no rule matches, and the member's
name looks exactly as it did. They paid for that.

So the slug list lives in `pluginapi/cosmetics.go` where both sides import it,
and the reference host carries two tests that hold the seam:
`TestEveryEffectHasCSS` walks the catalogue against `components.css`, and
`TestAnimatedEffectsRespectReducedMotion` checks every moving effect is named
under `prefers-reduced-motion`.

## Four slots, and a slot is a PLACE

| Slot | What carries the class | Catalogue |
|---|---|---|
| `name` | the username, wherever it is drawn | 8 text effects |
| `title` | the member's own approved words | the same 8 |
| `avatar` | a ring around the picture | 4 frames |
| `profile` | the ground behind the profile card | 4 washes |

Name and title share a catalogue on purpose: an aura is an aura whether it sits
on a username or the line under it, and somebody who owns one should be able to
put it on either without buying it twice. You can wear a gold aura on your name
and a rainbow on your title.

Two flags travel with every entry, and both exist because of a way this could
have gone wrong quietly:

- **`Tinted`** — the effect brings its own colour and REPLACES whatever was
  there. A rank can tint a username (`pluginapi.Badge.TitleColor`), and an
  effect that always painted itself gold would silently delete an earned staff
  colour. The untinted ones work in `currentColor` and compose.
- **`Animated`** — it moves, and therefore does nothing under
  `prefers-reduced-motion`. Unsaid, a still preview on such a machine reads as a
  broken purchase.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `GET /p/cosmetics` | member | Every slot, its whole catalogue, each entry drawn as it will look with YOUR name in it. |
| `POST /p/cosmetics/equip` | member | Empty slug takes it off. Slot and slug are both checked against the contract here, and OWNERSHIP inside the statement. |
| `POST /p/cosmetics/title` | member | Submits words. Publishes nothing. |
| `GET /admin/p/titles` | mod | The queue, oldest first. |
| `POST /admin/p/titles/review` | mod | Approve, or turn down with a reason the member reads. |

Store item types: `cosmetic` (an effect — pinned by `reward_ref`, or a chooser
when blank) and `custom_title` (the right to propose one).

No background jobs. A dated unlock lapses by being asked, which is why the
renderer's query joins back to `cosmetic_owned` rather than sweeping a table.

## Data

Owns `cosmetics.cosmetic_owned`, `cosmetics.cosmetic_equipped`,
`cosmetics.cosmetic_titles`.

**Owning and wearing are separate tables**, because they are separate facts and
a site that conflates them has no answer to "I bought three, let me switch".
`cosmetic_equipped` is keyed on `(user_id, slot)`, so equipping is an upsert and
there is no way to end up wearing two.

**Titles keep the proposed and the published words in different columns.** One
column meant a member who changed one word of an approved title watched it
vanish from every page until a moderator got round to them — changing your title
was punished and leaving it alone was not. Submitting touches only the proposal;
approving copies it across; a refusal leaves what is up exactly where it was.

`slug` is not a foreign key to anything. The catalogue is code in both
repositories, because half of an effect is CSS, and a copy in the database would
be a third place to disagree.

**Not here:** points. The ledger is `core.Points` and the store debits before
`Grant` is called; this plugin never charges.

## Dependencies

| Needs | Why |
|---|---|
| `core.Storage.SchemaDB` | Its own schema. |
| `core.Auth` | A cosmetic belongs to an account. |
| `core.Users` (Start) | The renderer keys on USERNAME — the templates that draw a member have a name and nothing else — so ids have to become names. Absent: effects are recorded and nothing renders, logged at boot. |
| `pluginapi.CSRFTokenFunc` | Every form. |
| A host that draws the classes | See the contract section. Absent: every purchase succeeds and nothing changes. |

## Events

**Declares** `cosmetics.unlocked` and `cosmetics.equipped`, and the pair is the
clearest example in this repo of what the `Countable` flag is for.

**Unlocked is countable.** It costs points or an admin's decision, so the count
measures something that was spent and a member cannot manufacture more.

**Equipped is not.** Putting an effect on is free and unlimited — a member can
toggle it all afternoon, so a total measures fiddling with a dropdown. It is
still worth *announcing*, because a subscriber may want to react to what
somebody is wearing. That gap between "worth hearing" and "worth totalling" is
the whole distinction.

Both are the ON leg only; taking an effect off announces nothing, and what
somebody is wearing now is a question the store answers directly rather than
something to reconstruct from a stream.

## Hooks & callbacks

- **Publishes:** `cosmetics.effects` (`pluginapi.CosmeticResolver`) —
  username → what they wear, and username → their approved title.
- **Publishes:** `store.itemtype.cosmetic`, `store.itemtype.custom_title`.
- **Consumes:** `csrf.token`.
- **Views:** `SlotSitePage` (`cosmetics`), `SlotAdminPage` (`titles`).

The cache in front of the resolver lives in `pluginapi`, not in the host, because
the host is not the only place a member is drawn — the comments plugin renders
its own authors. One staleness window and one query load rather than one per
caller. Five seconds; a member sees their own page immediately because it renders
from the database.

## Lifecycle

`Provision` registers the resolver and both store types, mounts the member page
and the queue. `Start` resolves `core.Users` — in Start rather than Provision
because every Provision runs before any Start, and asking earlier is how a
lookup comes back absent for a service that is perfectly present.

## The title queue

The only part of this plugin that publishes words somebody typed, which makes it
the highest-leverage user-supplied text on a site per character — it appears
beside their name on every page they appear on.

The shop sells the RIGHT to propose one; staff pass the words. Editing an
approved title returns it to the queue, because otherwise the queue is
bypassable in two moves: submit something harmless, get it passed, edit it into
anything. Rejections require a reason and the member reads it — a title turned
down with no explanation is one they send straight back.

`cleanTitle` is **not moderation and does not pretend to be**. No character
check substitutes for a person reading it. What it removes is the tricks that
are about the RENDERING rather than the words: control characters, the
bidirectional overrides that let text escape its own element and reorder the
username drawn beside it, combining marks stacked deep enough to paint over the
row above, and whitespace runs that make a short title occupy a tall one.

## Files

```
plugin.go     metadata, registration, the resolver
store.go      owned/equipped, the extend-never-truncate unlock
titles.go     the titles half, and cleanTitle
itemtype.go   the effect store type
titleitem.go  the custom-title store type
views.go      the member page, equip, submit
queue.go      the staff queue
templates/    cosmetics_page.html, cosmetics_queue.html
```

## Testing

**Verified by hand against a running site, and by screenshot** — which for this
plugin is not a shortcut but the only method that works: three separate CSS
faults shipped with every class applied, every rule matching, and the
catalogue-vs-stylesheet test passing.

Covered: an effect bought and worn in each of the four slots; a frame refused on
a username and an aura on an avatar; buying the same 30-day item twice giving 60
days rather than 30; auto-equip on the first purchase only; a title pending and
not public, approved and public, edited with the old one still up, and the edit
turned down with the old one still up; a rejection with no reason refused; and
bidi overrides, control characters and folded newlines stripped from a
submission.

**Known gaps.** No Go tests in this package. `cleanTitle` is pure, fully
specified, and security-adjacent — it is the first thing that should get a table
test. `Unlock`'s extend-never-truncate arithmetic is verified only by two
purchases and a `psql` query. The two tests that do exist for this feature live
in the reference host, because what they check is the host's stylesheet.
