# Security

## Reporting a vulnerability

Please report privately through GitHub's
[security advisories](https://github.com/The-Loon-Clan/loon-plugins/security/advisories/new)
rather than opening a public issue.

Include what you did, what happened, and what you expected. A proof of concept
helps but is not required — a clear description of the flaw is enough to act on.

This is a small project without a paid security team, so expect a first reply
within a week rather than within hours.

## What this software is

**Forty-nine plugins for the [loon](https://github.com/The-Loon-Clan/loon)
framework**, each a module a host imports and boots. A plugin owns a Postgres
schema or a set of narrow ports, mounts routes, and registers jobs.

That shapes where the risk is. A plugin runs **inside the host's process, with
the host's database credentials**, so there is no sandbox: the isolation is a
per-plugin schema and a `search_path`, which is a tidiness boundary rather than
a security one. A plugin that wants the host's tables can reach them. Install
plugins you have read.

The interesting failures are therefore not "plugin escapes its schema" but the
ordinary web ones, repeated 49 times, where **one plugin forgetting is the whole
of the vulnerability**. Most of what follows is about making forgetting
detectable.

## What is defended, and how

**SQL injection.** Statements must be constants. `scripts/lint-sql` fails the
build on SQL assembled by concatenation or formatting, which is how
parameterisation is actually lost. Exceptions need `// sqllint:allow <reason>`.

**CSRF.** Every state-changing POST form carries a token, minted by the host and
reached through one seam (`pluginapi.CSRFTokenName`). An audit in the host repo
(`make resources`) fails on a POST form with no hidden token field.

That audit exists because **58 tokenless POST forms across 9 plugins** were
found at once, by accident, while building something else. Every one of them was
written by somebody who knew about CSRF; the token simply was not reachable from
a plugin template without knowing the seam's name. A guard that can be forgotten
silently will be.

**Ownership confusion.** User id `0` means both "nobody is signed in" and the
reserved system id, so `record.UserID == viewerID` is true for an **anonymous**
viewer whenever the record's owner is 0. `pluginapi/ownership.go` states the
rule and `OwnedBy` / `VisibleTo` implement it; `scripts/audit-sentinels` finds
comparisons that do not use them.

This is not hypothetical. It was found four separate times in two days —
including a comment delete that an anonymous caller could aim at any comment,
and a playlist that leaked to anybody while a correctly-guarded check sat two
lines above it.

**Outbound SSRF.** Plugins do not fetch member-supplied URLs themselves. Every
outbound request goes through `core.HTTPClient`, whose `SafeFetch` refuses
private ranges, loopback, link-local and cloud metadata at dial time. There are
no bare `http.Get` calls in this repository.

See loon's `SECURITY.md` for what that guard does and does not promise —
notably that it does not restrict the scheme, and that when an egress proxy is
configured the IP block necessarily does not apply.

**Secrets in errors.** `pluginapi.RedactSecrets` and `RedactURLError` strip
credentials from an error before it reaches a log or a page. A `*url.Error`
carries the URL it failed on, and an API key in a query string travels with it;
the redacting version preserves `Op` and the wrapped cause so `errors.Is` and
`errors.As` still work.

**Member-facing text.** Sentences live in templates, not in Go, and a handler
redirects with a code. The security-relevant half is that a page therefore
**cannot echo arbitrary query text back to the viewer** — the words are the
template's. `html/template` escaping made that a display concern rather than an
injection one, but a URL that can put chosen words on a page is still a
phishing primitive.

## Known limitations

Stated because a security document that lists only strengths is not useful.

- **No sandbox between plugins.** Named above and worth repeating: schema
  separation is not a trust boundary. A hostile plugin is a compromised host.
- **The CSRF audit checks templates, not handlers.** It proves a form carries a
  token. It does not prove the handler verifies one, and a route that skips
  verification passes.
- **The sentinel audit is baselined.** Existing comparisons it cannot prove safe
  are recorded rather than fixed, so the count going to zero is the goal and not
  the current state.
- **Integration tests skip without a DSN.** On a laptop that is right; it does
  mean the storage-layer tests — the ones that catch a struct tag that stopped
  matching its column — only really run where a Postgres is provided.
- **`anidbscraper` is an unfinished demonstration.** Parts of it are stubs with
  extraction notes, including one describing a fetch that must move to the
  SSRF-safe client when the real body lands. Do not deploy it as-is.
- **No signed releases and no tagged versions.** Consumers pin by commit. Worth
  having; not there yet.
