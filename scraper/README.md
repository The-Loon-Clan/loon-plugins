# scraper

Fills in what a release is *about* — title, year, summary, poster, genres —
by asking third-party metadata services.

**This plugin sends data off the site.** That is its entire job, and it is the
first thing an operator should know about it, so the disclosure is at the top
rather than in a footnote.

---

## What leaves this site

For a release the site cannot already identify, the scraper sends a **cleaned
release title** — and, for television, a season and episode number — to one
external service per domain. It receives metadata and an image URL back.

The release title comes from the Usenet subject, so treat it as public
information that already existed on a public network. What is *new* is that
this site's server contacts a third party, which tells that third party:

- this site's **IP address** (or the egress proxy's, if one is configured),
- **what is being looked up**, and roughly when,
- a `User-Agent` of `loon-scraper/1.0` or, for the sources that follow
  Wikimedia's policy, one naming this project.

It does **not** send member names, member IPs, search queries typed by members,
download activity, or anything from the tracker.

No source is contacted for a release the catalog has already matched.

---

## The services

| Source | Endpoint | Credential | Domain |
|---|---|---|---|
| TMDB | `api.themoviedb.org` | `TMDB_API_KEY` | movie, tv |
| TVmaze | `api.tvmaze.com` | none | tv (fallback) |
| Wikipedia / Wikidata | `en.wikipedia.org`, `www.wikidata.org` | none | movie (fallback) |
| AniList | `graphql.anilist.co` | none | anime (fallback) |
| AniDB | `api.anidb.net:9001` | `ANIDB_CLIENT` | anime |
| Open Library | `openlibrary.org` | none | book |
| ThePornDB | `api.theporndb.net` | `TPDB_API_KEY` | xxx |

Image URLs come back pointing at each service's CDN — `image.tmdb.org`,
`static.tvmaze.com`, `cdn-eu.anidb.net`, `covers.openlibrary.org`,
`upload.wikimedia.org`. Whether those are fetched server-side or handed to a
member's browser is the **host's** decision, not this plugin's; a host that
renders a remote URL directly sends every reader to that CDN on page load. See
`pluginapi.ImageIntake` for the seam that fetches and stores locally instead.

A source with no credential configured returns `nil` from its constructor and
is simply never registered, so an unconfigured site quietly does less rather
than failing. Open Library needs no credential and is therefore the one source
a fresh checkout actually exercises.

### AniDB talks in cleartext

`api.anidb.net:9001` is **plain HTTP** — AniDB publishes no HTTPS endpoint for
its HTTP API — and the request carries `client=<ANIDB_CLIENT>` in the query
string. So on that one source, both the lookup and the client name are visible
to anything on the path between this server and AniDB.

Nothing here can fix that; it is a property of the upstream. It is written
down because "we use HTTPS" is otherwise a reasonable thing to assume, and an
operator choosing whether to configure `ANIDB_CLIENT` should know.

---

## Credentials

**Credentials go in headers where the upstream allows it, and are redacted from
errors where it does not.**

That is not decoration. `net/http` embeds the full URL in the `*url.Error` it
returns from any transport failure, so the obvious

```go
return fmt.Errorf("tmdb request: %w", err)
```

writes the operator's API key into this site's `error_logs` table on a single
DNS blip — visible on an admin page, with nothing about the failure hinting
that a credential went with it. Every outbound error here goes through
`pluginapi.RedactURLError`, and `pluginapi/httperr_test.go` reproduces the leak
against a real client to prove the guard still bites.

- **ThePornDB** authenticates with `Authorization: Bearer`. Nothing in the URL.
- **TMDB** accepts a **v4 read access token** as a bearer header, and that is
  used automatically when the configured value is one (a JWT). A **v3
  `api_key`** has no header form and must travel in the query string, so it
  does — redacted from errors. Migrating to a v4 token is the better setting.
- **AniDB** requires `client=` in the query and offers no alternative.

---

## Configuration

All optional. Set none and the keyless sources still cover television, film,
anime and books.

```
TMDB_API_KEY=...     # movie + tv. A v4 read access token is preferred.
ANIDB_CLIENT=...     # anime. Registered client name; see the cleartext note.
TPDB_API_KEY=...     # xxx.
```

Each source's `New` takes a `baseURL` that defaults to the public API; tests
point it at an `httptest` server, and an operator can point it at a mirror.

To switch the whole thing off, do not wire it: the host's
`wireMetadataSources` is the only thing that registers these sources, and a
site with none registered simply shows what the NZB itself said.

---

## Chains, not either/or

The catalog registry accepts one source per domain, which used to make this an
either/or choice at boot: TMDB when a key was set, TVmaze and Wikipedia when it
was not. The fallbacks were registered *instead of* the primary, so a title
TMDB had never heard of was simply not found — there was nothing behind it.

A **chain** presents several sources to the registry as one, so the
one-per-domain rule holds and the fallback happens inside. It also *confirms* a
hit before accepting it, which is the step whose absence is invisible: a source
that answered is not a source that was right, and a wrong poster looks exactly
like a right one.

See `internal/catalogchain` in the host, and `docs/METADATA-METHODS.md`.

---

## HTTP

Every source uses `httpclient.NewAPI()` — one pooled transport shared across
all of them, rather than seven bespoke `&http.Client{}` values. `core`'s
`HTTPClientService` contract states that raw `&http.Client{}` is forbidden in
plugin code, and `pkg/httpclient` exists because the codebase once had 21
places each building their own with timeouts from 5s to 60s.

Not `SafeFetch`: that carries an SSRF dial guard for URLs a **member** supplied.
These endpoints are fixed and operator-configured, and the guard would refuse
the loopback address the tests point at. A member-supplied URL must never be
fetched here — that is `pluginapi.ImageIntake`'s job.
