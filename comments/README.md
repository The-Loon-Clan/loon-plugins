# comments plugin

A conversation attached to whatever a page is about — releases today, anything
that declares a subject tomorrow. The most-used social surface on a site of this
kind, and the release page had nowhere to say *is this the good encode*, *this
one is missing subs*, *thanks*.

**Keyed by subject, not by release.** A release on a loon site can also exist as
a torrent on the tracker, and the two have different ids. The comment is about
the RELEASE — the encode, the audio, whether the pack is complete — so keying on
whichever id the page happened to hold would strand the conversation the day
somebody mirrored it. `(subject_kind, subject_id)` is the fix, and it also means
the next thing that wants comments is a new value in one column.

**Deliberately absent:** threading, editing by moderators, and comment counts on
listing rows. The first is a forum's job and this is not one. The second is a
line nobody should cross — a moderator removing a comment is one thing, a
moderator rewriting one under somebody else's name is another, and this offers
no way to. The third is a real gap and needs a seam the host would consume.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `POST /p/comments/post` | member | |
| `POST /p/comments/edit` | author only | Enforced in the statement, not by a read this could race. |
| `POST /p/comments/delete` | author or mod | Withheld, never deleted — a moderator asked why something was removed cannot answer from a row showing nothing. |
| `POST /p/comments/thanks` | member | Toggles. See *Thanks* below. |
| Widget `comments` | public read | Region `release-main`. Anonymous visitors READ; whether they may post is answered per viewer, because a comment section only members can see is one nobody joins for. |

Every write carries `back`, the path the viewer is on, so a handler can return
them without this plugin knowing the host's routes — and refuses anything that
is not a same-site path, because a redirect target taken from a request is an
open redirect the moment it is trusted.

No background jobs.

## Data

Owns `comments.comments` and `comments.comment_thanks`.

A withheld comment keeps its body. Withholding is the caller's job because staff
may see what an ordinary member may not, and a store that stripped the body
would make that impossible without a second query.

**`comment_thanks` rows are never deleted.** Withdrawing sets `withdrawn_at`,
and the award fires only when the row is CREATED — detected through `xmax = 0`
in the `RETURNING` clause, which is Postgres's own answer to "was this an insert
rather than an update" and the only way to tell them apart in one statement.
Deleting on withdrawal would make the button a faucet, and it is the kind of
faucet nobody notices until a balance is absurd.

## Thanks: who earns, and why only one of them

The obvious design pays both parties a little. It is the wrong design: paying
somebody to press thanks is how a site grows thanks-farming rings, and the cap
that would stop it is not on thanks — one per comment already — but on
COMMENTS, which are unlimited. Two accounts can generate as many as they like
and thank each other's.

So **the author earns and the giver does not**. A thanks is a gift rather than a
trade, which is also what it is socially. `thanksAward` is 2: enough that a
helpful member accumulates something over a month of being helpful, small enough
that nobody organises around it.

Refused silently, because the button is not offered in either case and reaching
the handler means a forged post: your own comment (thanking yourself would pay
you for commenting) and a withheld one (which must not still be earning).

## Dependencies

| Needs | Why |
|---|---|
| `core.Storage.SchemaDB` | Its own schema. |
| `core.Auth` | Posting, editing and deleting all need to know who you are. |
| `core.Users` (Start) | Author names, resolved in ONE call — forty comments by six people is six names, and per-row lookup would be forty queries. Absent: every comment renders as "a member". |
| `core.Points` (Start) | Pays a thanked author. Absent: thanks are still recorded and pay nobody, which is a poorer feature but a working one. |
| `pluginapi.CSRFTokenFunc` | Every form. |
| `core.WidgetItem` | Which subject the page is about. |

## Hooks & callbacks

- **Consumes:** `csrf.token`, `core.Users`, `core.Points`, `core.WidgetItem`,
  and `pluginapi.NameClass` for an author's cosmetic name effect.
- **Publishes:** nothing. A comment count belongs on a listing row and would be
  the obvious first contract here.
- **Widget:** `comments`, region `release-main`.

The widget renders **body content only** — no panel of its own. A host frames a
placed widget and titles it, so drawing a card here put this heading underneath
the host's heading, inside the host's box. That was live for a while and is
worth knowing about before writing the next widget; see the rule in SEAMS.md.

## Lifecycle

`Provision` opens the schema, mounts four POST routes and registers the widget.
`Start` resolves `core.Users` and `core.Points`.

## Files

```
plugin.go     metadata, routes, the widget
store.go      Store, PGStore, the comment half
thanks.go     ThanksStore, the toggle, the award
views.go      the widget's view model, backTo
templates/    comments_widget.html
```

## Testing

`comments_test.go` covers the store's ownership rules. Everything else was
**verified by hand against a running site with two accounts**: thank, withdraw,
thank again paying once (123 → 125 → 125 → 125); a self-thank paying nothing
with the button not offered; thanking a removed comment paying nothing; and the
three render states per viewer — the giver sees "given", another member sees the
plain button, an anonymous reader sees the count with no control.

**Known gaps.** No test asserts the withheld-body rule (staff see it, members do
not), which is the one place this plugin decides who reads what. `backTo` is
pure and untested. The thanks toggle's once-only award depends on `xmax = 0`
behaving as documented and nothing pins that.
