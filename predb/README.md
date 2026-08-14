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
| **Look up one release** | predb.ovh `GET /?q=` | searches ~7.7M records we would otherwise have to hold |
| **Record what is releasing now** | predb.ovh `GET /ws`, or IRC `#PreNNTmux` | same data, different transport |
| **Mirror the whole database** | *neither* | see below |

**A full local mirror is not on offer.** predb.ovh is "hard limited at 1000"
rows by its Sphinx backend, so offset paging cannot walk 7.7M records — and a
caller that believes it received everything is the failure that matters. What
we keep locally is what we saw live plus what we asked about; the rest stays
remote and is queried when a specific release needs a name.

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

**predb.net** — assessed 2026-08-14 and **not used**. It is a client-routed
SPA: every path, including `/api` and `/api/docs`, returns the same HTML shell,
and no API documentation exists on it. Scraping the UI would be fragile and
impolite while a documented API exists elsewhere.

## Status

Built and tested: the IRC announce parser and the predb.ovh client.

Not built: storage, the ingest job, and the lookup capability the usenet plugin
would consume. The transport for live ingest — WebSocket or IRC — is still an
open choice; the WebSocket needs no IRC connection, no nick, and no channel
etiquette, while IRC is what newznab does and therefore what parity means.
