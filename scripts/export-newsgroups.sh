#!/usr/bin/env bash
# Export a curated newsgroup pack from a running indexer, in the exact format of
# usenet/seed/newsgroups.tsv.
#
# The point: an install that has actually been crawling knows which groups carry
# real content. Curating that list by hand is guesswork; exporting it is not.
#
# Only ACTIVE group names are exported. Nothing installation-specific goes out —
# no watermarks, no article numbers, no coverage. Group NAMES are portable
# across providers (every backbone carries the same names), which is what makes
# a pack shareable at all; article numbers are the per-backbone part and are
# deliberately excluded.
#
# Usage:
#   ./export-newsgroups.sh --dsn "postgres://user:pass@host:5432/db?sslmode=disable" \
#                          [--pack anime] [--schema public] [--table newsgroups]
#
#   # append straight onto the seed file
#   ./export-newsgroups.sh --dsn "$DSN" --pack anime >> ../usenet/seed/newsgroups.tsv
#
# Prod note: prod keeps newsgroups in `public`; the plugin keeps them in the
# `usenet` schema. Pass --schema accordingly.
#
# Requires: psql on PATH, or run it through a container, e.g.
#   docker run --rm -e PGPASSWORD="$PGPASSWORD" postgres:16-alpine \
#     psql -h HOST -U USER -d DB -At -F $'\t' -c "<the query below>"
set -euo pipefail

DSN=""
PACK="anime"
SCHEMA="public"
TABLE="newsgroups"

while [ $# -gt 0 ]; do
    case "$1" in
        --dsn)    DSN="$2";    shift 2 ;;
        --pack)   PACK="$2";   shift 2 ;;
        --schema) SCHEMA="$2"; shift 2 ;;
        --table)  TABLE="$2";  shift 2 ;;
        -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

if [ -z "$DSN" ]; then
    echo "error: --dsn is required" >&2
    exit 2
fi
case "$PACK" in
    anime|movies|tv|music|books|misc) ;;
    *) echo "error: --pack must be one of anime|movies|tv|music|books|misc" >&2; exit 2 ;;
esac

# -A unaligned, -t tuples-only, -F tab: emits exactly `name<TAB>pack<TAB>notes`.
# The notes column is left empty for a human to fill in; it is not derived.
psql "$DSN" -At -F $'\t' <<SQL
SELECT name, '${PACK}', ''
  FROM ${SCHEMA}.${TABLE}
 WHERE active = TRUE
 ORDER BY name;
SQL
