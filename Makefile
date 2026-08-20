# loon-plugins
#
# Everything runs the Go toolchain in a container (scripts/go.sh explains why).
# `make help` lists the targets.

GO ?= bash scripts/go.sh

# The throwaway Postgres `itest` starts. Port 5598 rather than 5432 so it cannot
# be confused with a development database, and one digit from the host repo's
# 5599 so both can be up at once.
ITEST_DB_PORT ?= 5598
ITEST_DB_NAME ?= loon_plugins_test

.PHONY: help test itest vet fmt sqllint check

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## test: the unit suite. Integration tests SKIP without LOON_TEST_DSN — see itest.
test:
	$(GO) test ./...

## itest: the tests that need a real Postgres, against a disposable one.
##
## NEVER a development database: these create and DROP schemas.
##
## Two mistakes are deliberately not repeated here, both learned in the host
## repo's copy of this target:
##
##   - it must not end in `|| true`. That was there so the cleanup below always
##     ran, and it also meant a FAILING integration test left the target
##     reporting success — which makes it a demonstration rather than a check.
##     The status is kept, the cleanup still runs, and the target exits with it.
##   - it must not name a subset with `-run`. A hardcoded list means an
##     integration test added later never runs and nobody is told. `./...` with
##     the tag is the whole of it, and a new file is picked up by existing.
##
## The `integration` build tag is what separates these from the unit suite;
## without it the files are not compiled at all.
itest:
	@bash scripts/itest.sh

## vet: go vet, including the integration-tagged files
vet:
	$(GO) vet ./...
	$(GO) vet -tags integration ./...

## fmt: report anything gofmt would change (an error, not a suggestion)
##
## ON WINDOWS THIS OVER-REPORTS, and it is not worth chasing. git is configured
## with core.autocrlf=true here, so every file git checked out has CRLF endings
## in the working tree while the repo itself stores LF — and gofmt, reading the
## working tree, wants to rewrite all of them. Files written directly by an
## editor or a script are LF and come back clean, which is why the list looks
## arbitrary. CI runs on Linux against the LF copies and is the answer that
## counts; `git diff` is the local one, since it compares normalised content.
fmt:
	@out=$$($(GO) gofmt -l .); \
	 if [ -n "$$out" ]; then echo "gofmt would change:"; echo "$$out"; exit 1; fi; \
	 echo "gofmt: clean"

## sqllint: the constant-only-SQL guard (scripts/lint-sql)
sqllint:
	$(GO) run ./scripts/lint-sql ./...

## sentinels: ownership checks a zero viewer id could satisfy
##
## User id 0 is both "nobody is signed in" and the reserved system id, so
## `record.UserID == viewerID` is true for an anonymous viewer whenever the
## record's owner is 0. Found four times by accident in two days before this
## existed. pluginapi/ownership.go states the rule; this finds where it is
## missing. Baselined like sqllint.
sentinels:
	$(GO) run ./scripts/audit-sentinels ./...

## check: everything CI runs except itest
check: vet sqllint sentinels test
