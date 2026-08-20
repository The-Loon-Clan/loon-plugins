# mediainfo plugin

What somebody who actually downloaded a release can tell everybody who has not:
**MediaInfo, chapters and screenshots**.

## Why this is contributed and not derived

A Usenet index holds pointers to articles, not the bytes. Nothing on the site
can open a file and read its bitrate, and pretending otherwise is how a feature
ends up fabricating.

The host already reports everything the NZB's own file list **proves** —
container, subtitle files and their languages, recovery share, how much of the
download is not the film — and that is derived and therefore true. Bitrate,
audio tracks, muxed subtitle tracks and chapters are simply not in an NZB. The
only honest way to have them is for a member holding the file to say so, and for
the page to be clear that is what it is.

**So the two are drawn as separate panels on purpose.** One says "read from the
NZB's own file list"; this one carries an author and a date on every report and
a footnote naming what cannot be checked from here. A reader must never have to
guess whether a figure is a fact or a stranger's claim, and one panel holding
both would guarantee they do.

**Deliberately absent:** any verdict. Nothing here marks a release good or bad,
ranks two copies, or feeds health. Two members describing one release is
information — a re-encode and the original differ — and collapsing that into a
score would throw away the disagreement that made it useful.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `POST /p/mediainfo/post` | member | Paste. An empty box withdraws yours, which is what clearing a field and pressing save means. A parse that recognises nothing is refused rather than stored. |
| `POST /p/mediainfo/remove` | member / mod | Author withdraws; a moderator withholds anybody's. The check is inside the UPDATE. |
| `POST /p/mediainfo/shot` | member | Fetches through the host's intake. Four per member per release. |
| `POST /p/mediainfo/unshot` | member / mod | As above. |
| Widget `mediainfo` | public read | Placed in `release-main`. Anonymous readers see everything — the value of a contributed report is that it helps somebody choose before they have an account. |

No background jobs.

## Data

Owns `mediainfo.reports` and `mediainfo.shots`.

**One report per member per release.** Two members describing the same release
is useful; one member posting six is spam. A listing summary takes the newest,
since it has one line and the most recent is least likely to describe a file
that has since been replaced.

`release_id` is **not** a foreign key. Releases live in the usenet plugin's
schema and age out with retention; a hard reference would either block that
cleanup or delete somebody's contribution with it. A row pointing at a gone
release is simply never rendered.

The **raw paste is kept alongside the parse**, because a parser that improves
should be able to re-read what it was given, and a member disputing what was
rendered needs the original to point at. The parse is JSONB: this is a report
ABOUT a file, not a table of columns this plugin defines.

`shots.source_url` is kept for attribution and for a moderator checking a
source, and is **never** rendered as an `<img src>` — see below.

## Dependencies

| Needs | Why |
|---|---|
| `core.Storage.SchemaDB` | Its own schema. |
| `core.Auth` | Contributions carry an author. |
| `core.Users` (Start) | Names for the reports on a page, resolved in one call. Absent: every report renders as "a member". |
| `pluginapi.ImageIntake` (`media.intake`, Start) | **Screenshots only.** Absent: reports still work and the screenshot field is not offered — better than a field that fails on submit. |
| `pluginapi.CSRFTokenFunc` | Every form. |
| `core.WidgetItem` | Which release the page is about. Refuses any kind that is not `release`. |

Bounds, all constants in `plugin.go`: `rawMax` 64 KB, `shotsPerMember` 4,
`maxLines` 2000 inside the parser.

## Screenshots are fetched, never hotlinked

A page that renders a remote image sends every one of its readers to a third
party on load — handing that host a log of who reads what, and leaving it free
to swap the picture for anything afterwards. So the file is pulled once and
served from this site.

**This plugin never makes the request itself.** It hands the URL to
`pluginapi.ImageIntake`, and the reason is the whole of that seam's doc:
fetching a URL somebody typed is a request the SERVER makes, from inside the
network, to an address the poster chose. The address rules, the redirect
re-check, the size cap and the content sniffing are the host's to get right
once.

**External services:** none of this plugin's own choosing. It fetches exactly
the URL a member supplies, through the host, and stores the result locally.
Nothing is ever sent outward.

## Hooks & callbacks

- **Consumes:** `media.intake`, `csrf.token`, `core.Users`, `core.WidgetItem`.
- **Publishes:** nothing yet. `SummariesFor` exists and is the obvious seam for
  a listing that wants "HEVC at 10.4 Mb/s" beside a row; it has no contract
  because nothing consumes it yet, and inventing one before there is a second
  side is how the bare-string tier in SEAMS.md grows.
- **Widget:** `mediainfo`, region `release-main`.

## Lifecycle

`Provision` opens the schema, mounts four POST routes and registers the widget.
`Start` resolves `core.Users` and `media.intake` — in Start because every
Provision runs before any Start, and a sibling's capability is not there yet at
Provision.

## The parser

`parse.go` reads MediaInfo's text output. **It never returns an error**,
deliberately: the input is a paste from a member on a form, and it will arrive
truncated, from six different MediaInfo versions, with its leading spaces eaten
by a chat client, and occasionally as something that is not MediaInfo at all. It
extracts what it recognises and the caller decides whether that was enough
(`Report.Meaningful`). A parser that rejected a paste would send somebody away
to reformat text they did not write.

It also does **not interpret**. `"12.4 Mb/s"` is stored as `"12.4 Mb/s"` rather
than as a number of bits, because converting is asserting something about a file
this site has never seen — and the whole reason the data is contributed is that
nothing here can check it.

Two decisions that look like details and are not:

- It splits on `" : "` rather than `":"`, because a Menu line is
  `00:00:00.000 : en:Chapter 01` and the first colon is inside the timestamp.
- A heading is **any line that is not a pair**, rather than a match against
  known section names — MediaInfo localises them, and a parser that knew only
  "Video" would silently drop every track for a member running it in German.

## Files

```
plugin.go     metadata, routes, the widget, removal handlers
parse.go      MediaInfo text -> Report (tracks, fields, chapters)
parse_test.go
store.go      reports and shots
views.go      the widget, post, shot, backTo
migrations/
templates/    mediainfo_widget.html
```

## Testing

`parse_test.go` covers the parser, which is the load-bearing half: every track
found and labelled, fields in MediaInfo's own order, chapters split on the right
colon, a chapter title that legitimately contains a colon, the summary line,
junk input never failing, non-English headings kept, pairs before any heading,
a stated-empty field surviving, and a long paste bounded.

**Verified by hand against a running site:** the six SSRF and URL refusals
(loopback, `169.254.169.254`, a private range, plain http, credentials in the
URL, `javascript:`); one real fetch stored locally under a content hash; a junk
paste refused without wiping the good report already there; one report per
member with an edit not becoming a second row; and a member's forged attempt to
remove somebody else's report writing nothing while staff could.

**Known gaps.** The store has no tests — `SummariesFor`'s `DISTINCT ON` and the
`(user_id = $2 OR $3)` author check are both verified only by the transcript
above. Nothing tests the widget's view model, including the rule that a withheld
report stays visible to staff and to nobody else.
