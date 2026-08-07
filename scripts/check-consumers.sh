#!/bin/bash
# Build everything that consumes this repo, before committing a change to it.
#
#     bash scripts/check-consumers.sh
#
# WHY THIS EXISTS. Every consumer resolves loon-plugins with
# `replace => ../loon-plugins`, so they compile against this WORKING TREE --
# not a tag, not a pushed commit, not even a commit. There is no version
# boundary anywhere. An edit saved here is in their next `go build` instantly,
# and when it breaks them the failure surfaces in THEIR terminal, about code
# they did not touch.
#
# That matters more than usual right now because two people work these repos at
# once: one on the demo host, one on prod. On 2026-08-05 a grep across
# indexer-site and loon-plugins concluded that plugins/backups was dead
# scaffolding and it was seconds from deletion -- loon-demo-site/main.go
# imports it. A grep of the repo you are standing in cannot answer "is this
# used", and neither can a conversation. Compiling the consumers can.
#
# Run it before you commit here. It is the cheapest possible substitute for CI
# (which currently only runs the SQL lint) and for asking the other person what
# they are in the middle of.
set -uo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd)"
SIBLINGS="$(dirname "$HERE")"

# Ordered cheapest-first, so a syntax error here fails in seconds rather than
# after two full consumer builds.
TARGETS=(
    "loon-plugins|$HERE|go build ./..."
    "loon-plugins tests|$HERE|go test ./... -count=1"
    "loon-demo-site|$SIBLINGS/loon-demo-site|go build ./..."
    "indexer-site|$SIBLINGS/Indexer/indexer-site|go build ./..."
    # `go vet` and not just `go build`, because build ignores _test.go
    # files. A consumer's test FAKE implements our interfaces, so widening
    # one breaks it -- and that break is invisible to a build. It happened
    # on 2026-08-07: AdminStore gained a method, every consumer reported
    # "ok", and the host's fakeRewardsAdmin had been broken for two
    # commits. vet type-checks tests, which is the cheapest way to see it.
    # Compile only -- running those tests needs databases these consumers
    # do not have here.
    "loon-demo-site tests|$SIBLINGS/loon-demo-site|go vet ./..."
    "indexer-site tests|$SIBLINGS/Indexer/indexer-site|go vet ./..."
)

fails=()
echo "Building everything that compiles against $HERE"
echo "======================================================================"
for t in "${TARGETS[@]}"; do
    name="${t%%|*}"; rest="${t#*|}"; dir="${rest%%|*}"; cmd="${rest#*|}"
    if [ ! -d "$dir" ]; then
        # A missing sibling is not a failure: not every checkout has all four.
        printf '  %-20s SKIP (no %s)\n' "$name" "$dir"
        continue
    fi
    printf '  %-20s ' "$name"
    if out="$(cd "$dir" && eval "$cmd" 2>&1)"; then
        echo "ok"
    else
        echo "FAILED"
        fails+=("$name")
        # First few lines only: the first error is the cause and the rest is
        # usually consequence.
        printf '%s\n' "$out" | head -12 | sed 's/^/      /'
    fi
done

echo
if [ ${#fails[@]} -ne 0 ]; then
    echo "BROKEN: ${fails[*]}"
    echo
    echo "If a CONSUMER broke and loon-plugins itself did not, you changed a"
    echo "contract someone else depends on. Fix it here, or update them in the"
    echo "same change -- do not commit and let it surface in their session."
    exit 1
fi
echo "All consumers build. Safe to commit."
