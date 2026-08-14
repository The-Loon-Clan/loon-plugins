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

**api.predb.net** — the only live source.

Not discoverable the obvious way: `predb.net/api-documentation` renders
client-side, so a fetcher sees only the page title, and `/api`, `/api/v1`,
`/api/docs` all return the same SPA shell. The real host is in the page's own
HTML. Plain JSON, no key, 14,242,239 records.

Parameters established by probing, since the documentation is unreadable to a
machine:

| param | effect |
|---|---|
| `q=` | searches release names |
| `limit=` | page size |
| `page=` | 1-based pagination |
| `offset=`, `count=` | **ignored** |

Two warts worth knowing. `offset` is accepted and ignored, so a caller paging
with it re-reads page one forever and looks like it is working. And an unknown
PATH (`/teams`, `/groups`) is also ignored — it returns the default listing
rather than 404, so a typo'd endpoint looks like a successful call returning
the wrong thing. A filtered response omits `results_total`, which is how you
tell a search from a listing.

Rows carry `id, pretime, release, section, files, size, status, reason, group,
genre, url`. There is an RSS feed at `/feed/`.

No published rate limit, which is a reason for MORE restraint rather than less:
an unstated limit is enforced by whoever is annoyed. The client spaces calls 2s
apart.

**predb.ovh** — **dead**. The domain does not resolve (checked 2026-08-14);
only its GitHub Pages documentation survives, which is what made it look alive.
The client written against it has been deleted rather than left pointing at
nothing.

**IRC `#PreNNTmux`** on irc.synirc.net (6667, or 6697/7001 TLS) — what
newznab-tmux scrapes, so this is the parity path. `parse.go` reads its announce
format, taken from `IRCScraper::processChannelMessages` rather than invented:

```
NEW: [DT: 2026-08-14 05:12:33] [TT: Some.Release-GROUP] [SC: PRE]      [CT: TV/HD] [RQ: 12345:alt.binaries.teevee] [SZ: 1.4 GB] [FL: 24F] [FN: ...]
```

`NEW`, `UPD` and `NUK` all carry it. A nuked release is still a real release and
still de-obfuscates, so a nuke is recorded as an attribute rather than a
deletion, and `UNNUKED` clears the flag rather than dropping the row.

## What is actually on there, and what that means for us

Measured 2026-08-14 over the 1,000 most recent releases:

| section | share |
|---|---|
| MP3-WEB + FLAC-WEB | **49%** |
| TV + film (x264/x265) | ~22% |
| XXX-HD | 9% |
| **ANiME** | **2%** |
| ebook, abook, games, docu | ~5% |

Top groups: ENRiCH, XTC_iNT, PTC, VSEX, WRB, AVTOMAT, ZzZz, CiN_INT, URANiME,
ENViED, AZR, BB.

**None of them overlap with ours.** Our top crawled groups are iVy, FraMeSToR,
Tyrell, NTb, FLUX, ViSTA, PrimeFix, PoF, Telly, SiQ, DUDU, Saon — P2P and
web-rip groups, which do not pre and therefore have no record here.

Measured hit rate against our own catalogue: **0 of 15** randomly sampled
crawled titles matched. The mechanism works — searching `Harley.Quinn.S02E08`
returns three real scene releases — but not OUR copy, which is `-SiQ`. Same
episode, different release, and a PreDB only knows the scene one.

So this is a LOOKUP for the occasional release that happens to be scene, not a
pipeline that will name our catalogue. Anything built on it should be
on-demand: ask about one release when something else has already suggested it
might pay, rather than sweeping. A bulk job here would spend requests on a
free service to learn nothing.

For de-obfuscation on THIS corpus the better routes are the NFO (already
extracted, and a scene NFO carries the release name in plain text) and PAR2
hash16k donor matching, which works because it matches our own releases against
each other and does not care whether anything ever pre'd.

## Status

Built and tested: the IRC announce parser and the api.predb.net client.

Not built: storage, the ingest job, and the lookup capability the usenet plugin
would consume. The transport for live ingest — WebSocket or IRC — is still an
open choice; the WebSocket needs no IRC connection, no nick, and no channel
etiquette, while IRC is what newznab does and therefore what parity means.
