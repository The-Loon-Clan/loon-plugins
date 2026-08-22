# Handover — `agent`, `ranks`, `requests`

Everything below is in a plugin another workstream owns, so it was measured and
left rather than fixed. **Measured 22 Aug 2026.**

Every entry names the command that reports it. That is deliberate: a handover
listing findings goes stale the moment somebody fixes one, and nobody can tell
which half is still true. A handover naming the check stays honest, because you
run it.

None of this blocks anyone. Each item is at a recorded baseline, so `make check`
is green with all of it outstanding — the baselines are what stop it *growing*.

---

## `requests`

### 45 classes no stylesheet defines

```
python scripts/audit_css.py          # in loon-demo-site, siblings checked out
```

Reports `requests 45, baseline 45`. The largest remaining gap in the tree; the
next is `roadmap` at 31. Mostly Bootstrap names this host defines nowhere —
`alert-dismissible`, `alert-info`, `bg-opacity-25`, `border-info`,
`breadcrumb`, `btn-close` — so each renders as though the class were absent,
which is indistinguishable from being styled to look plain.

The forum went 144 → 9 by writing the plugin its own stylesheet
(`forum/templates/forum_styles.html`) rather than converting its markup to the
host's vocabulary. That is the pattern to copy: a plugin cannot know a host's
class names, and 45 names it invented are 45 the host was never asked for.

### 6 Bootstrap controls that do nothing

```
python scripts/audit_bootstrap.py
```

All in `requests/templates/community_requests.html`: five
`data-bs-dismiss="alert"` close buttons and one `data-bs-toggle="collapse"`.
This host ships neither Bootstrap's CSS nor its JS — `bootstrap.min.css` is 790
bytes and says so in its own first line — so the collapse panel is permanently
open and none of the six buttons does anything.

Twelve of these were converted across five other plugins on 22 Aug: `<details>`
for a disclosure, `<dialog>` for a real modal. CHECKLIST §8 names the remedy.

### 2 classes JavaScript applies that nothing styles

```
python scripts/audit_css.py          # the "unstyled JS states" line
```

`bg-opacity-50` and `text-white`, toggled in `community_requests.html`. The
element gets the class, no rule matches it, and the selection highlight does
not highlight. They are in that check's exclusion list with this note; remove
the entry when the classes are styled or the toggle is dropped.

### 9 member-facing sentences built in Go

```
python scripts/audit_resources.py ../loon-plugins    # in loon-demo-site
```

All in `requests/handlers.go`, and the single largest contributor to the
tree's ratchet of 26. CHECKLIST §10: a user-visible string belongs in a
template, so the translation seam lands as a mechanical wrap rather than a
rewrite.

---

## `ranks`

### ~~All 63 remaining accessibility findings on the site~~ — DONE

Fixed on 22 Aug 2026 in `9e9303b`, not by this workstream. It had not
moved, the fix pattern was already established twice over in `donations`
and `forum`, and the site's a11y baseline is now empty — so leaving it
listed would have meant leaving the whole site's a11y debt at 63 to
protect a boundary.

What was done, in case it needs revisiting: the nine controls in each
rank edit row are named for their ROW — `aria-label="Name for Trusted"`
— because every row's visible label says the same word, and for/id would
have fixed the association without fixing the ambiguity. The create form
got for/id instead, having no ambiguity. The table got a visually-hidden
caption. Nothing else in `ranks` was touched.

```
python scripts/audit_a11y.py         # in loon-demo-site, stack running
```

Now reports 0 findings across 133 pages.

---

## All three: `Metadata.Flavours`

```
python scripts/audit_flavours.py
```

`agent`, `ranks` and `requests` are the only three of 51 that do not declare
it. CHECKLIST §1 makes it a **MUST**, and GRADES had all three marked as
passing that section until 22 Aug — the grade is corrected there now.

The check itself was blind to three more until the same day: it read `plugin.go`
alone, and `games`, `magic` and `medals` keep their `Metadata` in a file named
after themselves, so a bare `continue` skipped them. All three were also
undeclared — the check was silent about the plugins it was most needed for.
Those three are declared now (`games` and `medals` any, `magic` tracker-only,
since free leech and ratio buffs are statements about a swarm); yours are what
is left.

`FlavourAny` is the right answer for anything that does not care what the site
indexes, and saying it is the whole point: an empty field and "belongs to both"
behave identically, so only a declaration tells them apart.

---

## Checked and NOT a finding

`gofmt` reports seven files under `agent/` and `requests/` as needing
formatting. **They do not.** The difference is CRLF line endings in the Windows
working tree; the committed content is LF and formats clean. CHECKLIST's own
`make fmt` note warns about this over-report. An earlier draft of this handover
listed it as debt, which it is not.
