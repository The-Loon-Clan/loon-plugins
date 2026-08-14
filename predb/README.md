# predb — scene release names, for de-obfuscation

A PreDB is a record of scene release names captured as they are **announced**.
It is the cheap answer to obfuscated postings: matching a scrambled subject
against a known real name costs nothing on the wire, works on RAR'd and
passworded content where the yEnc header only gives you `abc123.part01.rar`,
and needs no article body at all.

That is why this exists as its own track rather than waiting on body fetching
(`Indexer/docs/BODY-FETCH.md` §4): for de-obfuscation specifically, a PreDB
beats yEnc name recovery on cost, coverage and reliability.

## The three jobs, and which source serves each

Not one source. They are good at different things, and pretending otherwise is
how you end up with an importer that cannot import.

| job | source | why |
|---|---|---|
| **Look up one release** | `api.predb.net/?q=` | searches 14.2M records we would otherwise have to hold |
| **Record what is releasing now** | `api.predb.net/?limit=` polled, or IRC `#PreNNTmux` | newest-first; the IRC channel is the parity path |
| **Mirror the whole database** | *nothing* | see below |

**A full local mirror is not worth taking.** predb.net pages fine, but 14.2M
records at 100 a page is 142,000 requests against a free service — the limit
is manners, not the API. predb.ovh cannot do it at all: it is hard limited to
1000 rows by its Sphinx backend, so offset paging silently stops and a caller
that believes it received everything is the failure that matters.

What we keep locally is what we saw live plus what we asked about; the rest
stays remote and is queried when a specific release needs a name.

## Sources assessed

**predb.ovh** — https://predbdotovh.github.io/pre-api/. Documented, no auth.
`GET /` (30 req/60s, 60s cache), `GET /live` (2/20s, uncached), `GET /ws`
(WebSocket, actions `insert|update|delete|nuke|unnuke|modnuke|delpre|undelpre`),
`GET /rss`, `GET /teams`, `GET /stats`. Rows carry
`id, name, team, cat, genre, url, size, files, preAt, nuke`. `api.go` speaks
this, with a 2s floor between calls — the documented limit allows more, but a
lookup path that occasionally bursts is what gets an IP banned from a free
service somebody runs as a favour.

**IRC `#PreNNTmux`** on irc.synirc.net (6667, or 6697/7001 TLS) — what
newznab-tmux scrapes, so this is the parity path. `parse.go` reads its announce
format, taken from `IRCScraper::processChannelMessages` rather than invented:

```
NEW: [DT: 2026-08-14 05:12:33] [TT: Some.Release-GROUP] [SC: PRE] \
     [CT: TV/HD] [RQ: 12345:alt.binaries.teevee] [SZ: 1.4 GB] [FL: 24F] [FN: ...]
```

`NEW`, `UPD` and `NUK` all carry it. A nuked release is still a real release and
still de-obfuscates, so a nuke is recorded as an attribute rather than a
deletion, and `UNNUKED` clears the flag rather than dropping the row.

**api.predb.net** — the PRIMARY source, and the best of the three.

Not discoverable the obvious way: `predb.net/api-documentation` renders
client-side, so a fetcher sees only the page title, and `/api`, `/api/v1`,
`/api/docs` all return the same SPA shell. The real host is in the page's own
HTML. It serves plain JSON, needs no key, and holds **14,242,239** records
against predb.ovh's 7,751,280.

Parameters established by probing, since the documentation is unreadable to a
machine:

| param | effect |
|---|---|
| `q=` | searches release names |
| `limit=` | page size |
| `page=` | 1-based pagination |
| `offset=`, `count=` | **ignored** |

`offset` being accepted and ignored is worth the warning in the code: a caller
paging with it re-reads page one forever and looks like it is working. A
filtered response also omits `results_total`, which is how you tell a search
from a listing.

Rows carry `id, pretime, release, section, files, size, status, reason, group,
genre, url` — `section` (MP3-WEB, APPS-0DAY, TV-HD…) and the `status`/`reason`
nuke pair are both things predb.ovh does not give as cleanly. There is also an
RSS feed at `api.predb.net/feed/`.

No published rate limit, which is a reason for MORE restraint rather than less:
an unstated limit is enforced by whoever is annoyed. The client spaces calls 2s
apart, same as the .ovh one.

## Status

Built and tested: the IRC announce parser, the api.predb.net client (primary)
and the predb.ovh client (secondary).

Not built: storage, the ingest job, and the lookup capability the usenet plugin
would consume. The transport for live ingest — WebSocket or IRC — is still an
open choice; the WebSocket needs no IRC connection, no nick, and no channel
etiquette, while IRC is what newznab does and therefore what parity means.
