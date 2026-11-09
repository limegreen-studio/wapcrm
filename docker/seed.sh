#!/bin/sh
# Copies the database of a running Postgres container into this stack's db
# service. Everything already in the target database is replaced.
# Usage: make seed CONTAINER=<postgres container id or name>
set -eu

source_container=${1:?usage: seed.sh <postgres container id or name>}
engine=${CONTAINER_ENGINE:-docker}
compose="$engine compose"

target_container=$($compose ps -q db)
if [ -z "$target_container" ]; then
  echo "db service is not running. Start it first: $compose up -d --build" >&2
  exit 1
fi
if [ "$($engine inspect -f '{{.Id}}' "$source_container")" = "$($engine inspect -f '{{.Id}}' "$target_container")" ]; then
  echo "$source_container is this stack's own db. Pass the container to copy from." >&2
  exit 1
fi

# Dump to a file first, so a failed dump never touches the target.
dump=$(mktemp)
trap 'rm -f "$dump"; $compose start app >/dev/null' EXIT

echo "Dumping $source_container..."
$engine exec "$source_container" sh -c 'pg_dump -U "$POSTGRES_USER" --clean --if-exists --no-owner --no-acl "${POSTGRES_DB:-$POSTGRES_USER}"' >"$dump"

echo "Restoring..."
$compose stop app
$compose exec -T -e PGOPTIONS='-c client_min_messages=warning' db \
  sh -c 'psql -q -v ON_ERROR_STOP=1 --single-transaction -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <"$dump" >/dev/null

# Cached rows point at the replaced database.
$compose exec -T redis redis-cli FLUSHALL >/dev/null

echo "Seed complete. Restarting app."
