# Contributing to loon-plugins

Two documents govern this repository and this one does not repeat them:

- **[CHECKLIST.md](CHECKLIST.md)** — what a new or changed plugin is held to,
  item by item, and how each is verified. Read it before opening a PR, not
  after.
- **[SEAMS.md](SEAMS.md)** — every shared system a plugin can reach for, and
  what is still duplicated because nothing was there to reach for. It answers
  the question every author asks first and currently answers by grepping:
  *does this already exist?*

What follows is the mechanics.

## Running it

```sh
make help      # the targets
make check     # what CI runs: vet, sqllint, sentinels, test
make test      # just the unit suite
make itest     # the tests that need a real Postgres, against a disposable one
```

The Go toolchain runs **in a container**, via `scripts/go.sh`. On Windows an
anti-virus quarantines freshly built unsigned binaries, and the symptom is not
an obvious error — it is a toolchain reporting `no such tool "compile"` because
the compiler disappeared between two commands. The script's comment has the
detail.

**`scripts/go.sh` mounts the PARENT directory**, not this repo. `go.mod` carries
`replace github.com/the-loon-clan/loon => ../loon`, so a container that could
only see this repo would fail to resolve the module graph before compiling a
line. Keep `loon` checked out beside this one.

`make itest` starts a throwaway Postgres on port 5598 and drops it afterwards.
**Never point the integration tests at a development database** — they create
and DROP schemas.

## The checks, and what each one exists for

`make check` is four things, and none of them is style for its own sake:

**`vet`** — including the integration-tagged files, which are not compiled at
all without the tag and so rot silently.

**`sqllint`** — SQL must be a constant. A statement assembled by concatenation
or formatting is how parameterisation is actually lost. Exceptions need
`// sqllint:allow <reason>`.

**`sentinels`** — user id `0` is both "nobody is signed in" and the reserved
system id, so `record.UserID == viewerID` is true for an *anonymous* viewer
whenever the record's owner is 0. That was found four times by accident in two
days before this audit existed. `pluginapi/ownership.go` states the rule;
`OwnedBy` and `VisibleTo` are the answer.

**`test`** — the unit suite. Integration tests skip without a DSN, which is
right for a laptop and wrong for CI.

Two more run from the host repo (`make resources` there, because it needs both
trees): every POST form carries a CSRF token, and **no member-facing sentence
is built in Go**. The second is a ratchet with a recorded baseline — lower it
in the same commit that converts one.

## Where words live

A user-visible string belongs in a **template**, not in Go (CHECKLIST §10). A
handler redirects with a **code** — `?err=toopoor` — and the template maps the
code to a sentence.

This is not tidiness. A sentence assembled from fragments cannot be translated
at all: `"The " + name + " medal is yours"` has to be *rewritten* for any
language whose word order differs. Where a name or a number belongs in a
sentence, pass it as its own query parameter and let the template interpolate.

If a plugin hands text to **another plugin's surface** — the store shows the
buyer whatever a `Grant` returns — render it from your own templates and hand
over finished text. The other plugin cannot map a code it has no vocabulary
for. `games/messages.go` is the worked example.

## Ownership and shared trees

This repository is edited by more than one workstream at a time. **Stage
explicit paths — `git add <paths>` — and never `git add -A`.** Commit promptly
rather than sitting on a dirty tree.

## Security

Do not open a public issue for a vulnerability. [SECURITY.md](SECURITY.md) has
the process, and is worth reading anyway for what the shared guards do and do
not promise.
