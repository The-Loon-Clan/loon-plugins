# polls plugin

Ask the members a question and count the answers. Staff use polls for rule
changes and category decisions; members use them for arguments; neither is well
served by a forum thread, which tells you who is loudest rather than what people
think.

**A poll is never the destination.** It belongs on the front page during a rule
change, in the sidebar of the forum it concerns, in the body of the news post
arguing for it — so there is no `/polls` page and no template anywhere names a
poll. The whole plugin is ONE widget that takes a poll's name as its
per-placement setting. Two placements of that widget are two different polls,
and neither needed a code change.

**Deliberately absent:** multiple choice, anonymous voting, and paying for a
vote. The first is a real gap and is a value in one column plus a different tally
query; the second would remove the one-vote-each guarantee that makes the number
mean anything; the third is covered under *Economy* below.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `POST /p/polls/vote` | member | Casting and changing are the same act — see the primary key on `poll_votes`. Refuses a closed poll here and not only in the template: the ballot somebody had open when it closed is still a working form. |
| `GET /admin/p/polls` | admin | Write a poll, see the ones written, and copy the shortcode that places one. |
| `POST /admin/p/polls/create` | admin | Slug derived from the question when left blank. |
| `POST /admin/p/polls/close` | admin | Closes, or reopens. Reopening does **not** clear a deadline that already passed. |
| `POST /admin/p/polls/delete` | admin | Asks for the poll's name typed out. Closing keeps the answers; deleting throws away what people said, and a dialog gets dismissed by reflex. |
| Widget `poll` | public read | Placeable in any region, or `[widget poll <name>]` in a page body. Anonymous readers see the question and, where the policy allows, the tally. |

No background jobs. A poll closes by being asked whether it has, which is why
`Poll.Closed(now)` takes the time rather than reading a flag.

## Data

Owns `polls.polls`, `polls.poll_options`, `polls.poll_votes`.

The slug is how a placement names a poll and is why polls are placeable at all:
the widget's per-placement config is a slug, so the same widget in a sidebar and
in a page body is two different polls. An id would work and would be unreadable
in a shortcode.

`poll_votes` is **one row per member per poll**, which is what makes changing
your mind an UPDATE. The primary key enforces it — a count that has to
deduplicate is a count somebody will eventually forget to deduplicate.

Two ways to end a poll, kept as two columns because they are different facts:
`closes_at` is a plan made when it opened, `closed_at` is somebody deciding it
is over. Keeping both means "closed early" and "ran its course" stay tellable
apart.

**Not here:** the tally is not cached anywhere. It is a `count(*)` over an
indexed column on a table with one row per voter, and a cached copy would be a
second source for a number the page already has.

## Dependencies

| Needs | Why |
|---|---|
| `core.Storage.SchemaDB` | Its own schema. |
| `core.Auth` | Voting needs to know who you are — that is what makes it one vote each rather than one per click. |
| `core.Router` | One POST route. |
| `pluginapi.CSRFTokenFunc` (`csrf.token`) | Every form. Resolved per request through `pluginapi.CSRFToken`. |

No config keys. Nothing to tune: the per-poll settings are per-poll.

## Economy

**Voting pays nothing, deliberately.** Every points-bearing action on a loon
site is one somebody could usefully do MORE of. A poll wants considered answers
from people who care about the question, and paying for them buys the opposite —
a room full of members clicking the first option to collect. The reward for
voting is the result.

## Events

**Declares** `polls.voted`, a countable member event.

Countable is unusually safe here: a member gets one vote per poll and cannot
create polls, so the ceiling is set by staff rather than by the member. That is
the same property that makes dailyreward's claim countable, arrived at from a
different direction.

**The payload does not carry the option.** The results policy exists to control
exactly when a tally becomes visible, and an event stream carrying each member's
choice would route around all of it and turn a ballot into a public record. That
a member voted is the notable fact; what they chose is the poll's business. A
test asserts it by reflection, so adding an `Option` field fails rather than
passing a hand-written list nobody updated.

## Hooks & callbacks

- **Consumes:** `csrf.token`.
- **Publishes:** nothing. A poll's result is for the page it is on; a plugin
  that wanted to gate on one would want a contract, and none has yet.
- **Views:** `SlotAdminPage` (`polls`), plus the `poll` widget.

## Lifecycle

`Provision` opens the schema, parses templates, mounts the vote route, registers
the admin view and the widget. `Start` and `Stop` do nothing — there are no
sibling capabilities to resolve, which is why the import is the whole wiring.

Migrations run before `Provision` through the host's plugin-migration runner.

## Results policies

The one real editorial decision, and a boolean cannot hold it:

| `results` | When the tally is readable |
|---|---|
| `after_vote` | Once you have committed to an answer. **The default**, because a running tally you can see before answering moves how you answer. |
| `always` | A temperature check where the tally IS the point. |
| `on_close` | Nobody sees anything until it is over — for a vote where a two-to-nothing lead in the first hour would read as a settled question. |

A closed poll shows its results under all three, including to somebody who never
voted: the reason to withhold stopped applying when the last vote was cast.

## Files

```
plugin.go     metadata, routes, the admin view, the widget registration
store.go      the Store interface, Poll/Option, Closed(), slugify
store_pg.go   Postgres
views.go      the widget, showResults, the vote handler
admin.go      the admin page and its three actions
templates/    poll_widget.html, poll_missing.html, polls_admin.html
```

## Testing

**Covered by hand against a running site**, not by unit tests, and that is the
honest state of it: vote-then-change leaving one row; an option id from another
poll writing nothing; a vote after close being refused in the handler; each of
the three results policies from a voter's and a non-voter's view; a closed poll
with no votes; and `back=https://evil.example/` bouncing to `/`.

**Known gaps.** No Go tests at all. `groupEpisodes`-style pure functions here —
`showResults`, `percent`, `slugify`, `Poll.Closed` — are exactly the shape that
should have them, and the results-policy matrix is a table test waiting to be
written. The store is unmocked, so the ownership and closed-poll refusals are
verified only by the transcript above.

## Placing one

Write the poll at **Admin → Polls**, then either drop the **Poll** widget in a
region and type the poll's name in its setting, or paste the shortcode into any
editable page:

```
[widget poll rule-change]
```

An unknown name renders nothing for members and a note for admins — a typo that
silently renders nothing is otherwise indistinguishable from a poll nobody has
written yet, and the person who can fix it is exactly the one who should be told
the difference.
