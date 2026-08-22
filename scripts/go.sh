#!/usr/bin/env bash
# Run the Go toolchain in a container, never on the host.
#
#   scripts/go.sh test ./...
#   scripts/go.sh vet ./...
#   scripts/go.sh test -tags integration ./cosmetics/ -v
#   scripts/go.sh gofmt -l .        <- the one non-`go` command, see below
#
# Why
# ---
# `go build` and `go test` write executables into the working tree and into a
# cache, and a Windows anti-virus treats freshly produced unsigned binaries as
# exactly what they look like. The symptoms are not obvious errors — they are a
# toolchain that reports `no such tool "compile"` because the compiler was
# quarantined between two commands.
#
# In a container nothing lands on the host filesystem except the source that was
# already there. It also pins the Go version to the one go.mod asks for rather
# than whatever the host happens to have, which is why CI does the same.
#
# THE PARENT DIRECTORY IS THE MOUNT, not this repo. go.mod carries
# `replace github.com/the-loon-clan/loon => ../loon`, so a container that could
# only see loon-plugins would fail to resolve the module graph before it
# compiled a line. This is the difference from the host repo's copy of this
# script, which has no sibling to reach.
set -euo pipefail

IMAGE="${GO_IMAGE:-golang:1.26}"
CACHE_VOL="loon-gomod"
BUILD_VOL="loon-gobuild"

docker volume create "$CACHE_VOL" >/dev/null
docker volume create "$BUILD_VOL" >/dev/null

# MSYS_NO_PATHCONV: git-bash on Windows rewrites /src into a Windows path before
# docker sees it, and the mount silently lands somewhere useless.
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PARENT_DIR="$(dirname "$REPO_DIR")"
REPO_NAME="$(basename "$REPO_DIR")"

# gofmt is not a `go` subcommand, and formatting has to be checkable in the same
# container as everything else — `go fmt` WRITES, which is not what a check
# wants. So this one word is passed through as the binary rather than as an
# argument to `go`.
BIN=go
if [ "${1:-}" = "gofmt" ]; then
  BIN=gofmt
  shift
fi

MSYS_NO_PATHCONV=1 exec docker run --rm \
  -v "$PARENT_DIR":/src \
  -v "$CACHE_VOL":/go/pkg/mod \
  -v "$BUILD_VOL":/root/.cache/go-build \
  -w "/src/$REPO_NAME" \
  -e GOWORK=off \
  -e GOFLAGS="-buildvcs=false -mod=mod" \
  `# Forwarded, not inherited. A container gets none of the host's environment` \
  `# unless it is named here, so LOON_TEST_DSN=... scripts/go.sh test would set` \
  `# the variable in the SHELL and not in the process that reads it — and the` \
  `# integration tests would skip while looking like they ran.` \
  -e LOON_TEST_DSN \
  `# The integration tests that predate pluginapi/pgtest each read their own` \
  `# variable. itest.sh sets them all to the same DSN, and they have to cross` \
  `# into the container or those tests skip while appearing to have run.` \
  -e ACHIEVEMENTS_TEST_DSN \
  -e BACKUP_TEST_DSN \
  -e EVENTS_TEST_DSN \
  -e NEWS_TEST_DSN \
  -e RANKS_TEST_DSN \
  -e REWARDS_TEST_DSN \
  -e TICKETS_TEST_DSN \
  -e TRACKER_TEST_DSN \
  -e USENET_TEST_DSN \
  -e INDEXER_TEST_DB_DSN \
  -e REDIS_TEST_ADDR \
  -e CI \
  `# The forum preview dumper writes each page as a standalone file` \
  `# so it can be screenshotted. Those five templates render on the` \
  `# RenderPage contract only, which no host wires yet, so looking` \
  `# at them is otherwise impossible.` \
  -e FORUM_PREVIEW_DIR \
  -e FORUM_PREVIEW_CSS \
  -e FORUM_PREVIEW_SPRITE \
  `# --network host so the container can reach the throwaway Postgres that` \
  `# 'make itest' publishes on the host.` \
  --network host \
  "$IMAGE" "$BIN" "$@"
