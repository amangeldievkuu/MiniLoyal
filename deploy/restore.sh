#!/bin/sh
# Restore a dump made by backup.sh into the compose database. Stops the app services first so
# nothing writes during the restore, then starts them again.
#   deploy/restore.sh backups/miniloyal-20261109-030000.dump
set -eu

DUMP="${1:?usage: deploy/restore.sh backups/NAME.dump}"
[ -f "$DUMP" ] || { echo "restore: $DUMP not found" >&2; exit 1; }
cd "$(dirname "$0")/.."

echo "restore: stopping api and worker"
docker compose --profile app stop api worker >/dev/null

echo "restore: restoring $DUMP (existing objects are replaced)"
docker compose exec -T db sh -c 'pg_restore --clean --if-exists --no-owner --dbname="$POSTGRES_DB" --username="$POSTGRES_USER"' < "$DUMP"

echo "restore: starting api and worker"
docker compose --profile app start api worker >/dev/null
echo "restore: done"
