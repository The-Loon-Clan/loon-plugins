#!/usr/bin/env bash
# Run the integration tests against a disposable Postgres.
#
#   scripts/itest.sh                    # the whole tagged suite
#   scripts/itest.sh ./cosmetics/ -v    # a subset, arguments passed to go test
#
# NEVER point these at a development database. They CREATE and DROP schemas.
#
# The logic lives here rather than in the Makefile so there is one copy: this
# box has no `make` at all, CI does, and a target that only works in one of
# those places is a check that quietly stops being run.
#
# Two mistakes are deliberately not repeated, both learned from the host repo's
# equivalent target:
#
#   - it must not end in `|| true`. That was there so the teardown always ran,
#     and it also meant a FAILING test left the script reporting success — which
#     makes it a demonstration rather than a check. The status is captured, the
#     teardown still runs, and the script exits with the test's result.
#   - it must not name a subset with `-run`. A hardcoded list means an
#     integration test added later never runs and nobody is told. The default
#     here is `./...`, so a new file is picked up by existing.
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"

CONTAINER="${ITEST_CONTAINER:-loon-plugins-itestdb}"
# 5598 rather than 5432 so this cannot be mistaken for a development database,
# and one digit from the host repo's 5599 so both can be up at once.
PORT="${ITEST_DB_PORT:-5598}"
DBNAME="${ITEST_DB_NAME:-loon_plugins_test}"

cleanup() { docker rm -fv "$CONTAINER" >/dev/null 2>&1 || true; }
cleanup
trap cleanup EXIT

echo "starting postgres on :$PORT"
if ! docker run -d --name "$CONTAINER" \
      -e POSTGRES_USER=demo -e POSTGRES_PASSWORD=demo -e POSTGRES_DB="$DBNAME" \
      -p "$PORT":5432 postgres:16-alpine >/dev/null; then
  echo "could not start the test database" >&2
  exit 2
fi

printf 'waiting for it to accept connections'
ready=""
for _ in $(seq 1 45); do
  if docker exec "$CONTAINER" pg_isready -U demo -q 2>/dev/null; then ready=1; break; fi
  printf '.'
  sleep 1
done
echo
if [ -z "$ready" ]; then
  echo "postgres never became ready; its log follows" >&2
  docker logs "$CONTAINER" >&2 || true
  exit 2
fi

# Default to the whole tagged suite. See the note above on why this is not a
# hardcoded list of packages.
if [ "$#" -eq 0 ]; then
  set -- ./...
fi

export LOON_TEST_DSN="postgres://demo:demo@localhost:$PORT/$DBNAME?sslmode=disable"

# The thirty-one integration tests that predate pluginapi/pgtest each read their
# OWN variable. Exporting the same DSN under every one of those names lights
# them all up here without editing thirty-one files that several people own —
# and without which `make itest` would run three tests and skip the rest while
# reporting success, which is the failure mode this whole exercise is about.
#
# pgtest.legacyEnv lists the same names; if one is added there, add it here.
LEGACY_DSN_VARS="
  ACHIEVEMENTS_TEST_DSN
  BACKUP_TEST_DSN
  EVENTS_TEST_DSN
  NEWS_TEST_DSN
  RANKS_TEST_DSN
  REWARDS_TEST_DSN
  TICKETS_TEST_DSN
  TRACKER_TEST_DSN
  USENET_TEST_DSN
  INDEXER_TEST_DB_DSN
"
for legacy in $LEGACY_DSN_VARS; do
  export "$legacy=$LOON_TEST_DSN"
done
bash "$REPO_DIR/scripts/go.sh" test -tags integration -count=1 "$@"
status=$?

exit $status
