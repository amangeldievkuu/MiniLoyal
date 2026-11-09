#!/bin/sh
# PostgreSQL backups for the docker compose deployment (the "backup" service): a pg_dump when the
# service starts, then every BACKUP_INTERVAL_HOURS, into /backups (./backups on the host). Dumps
# older than BACKUP_KEEP_DAYS are deleted. Restore with deploy/restore.sh.
#
# Each dump is written under a temporary name and renamed when complete, so a file named *.dump is
# always a finished one. Dumps hold member data and credential ciphertext: owner-readable only.
set -eu

: "${PGHOST:=db}" "${PGUSER:?}" "${PGDATABASE:?}" "${PGPASSWORD:?}"
INTERVAL_HOURS="${BACKUP_INTERVAL_HOURS:-24}"
KEEP_DAYS="${BACKUP_KEEP_DAYS:-14}"
DIR=/backups
umask 077

backup() {
  name="$PGDATABASE-$(date -u +%Y%m%d-%H%M%S).dump"
  # Custom format: compressed, and pg_restore can restore all of it or single tables.
  if pg_dump --format=custom --no-owner --file="$DIR/.$name.partial"; then
    mv "$DIR/.$name.partial" "$DIR/$name"
    echo "backup: wrote $name ($(du -h "$DIR/$name" | cut -f1))"
  else
    rm -f "$DIR/.$name.partial"
    echo "backup: pg_dump failed" >&2
    return 1
  fi
  # Old dumps go only after a new one succeeded, so a broken database never empties the folder.
  find "$DIR" -maxdepth 1 -name "$PGDATABASE-*.dump" -mtime +"$KEEP_DAYS" ! -name "$name" \
    -print -delete | sed 's/^/backup: deleted /'
}

# One run (docker compose run --rm backup now), or a dump now and then on a schedule.
if [ "${1:-}" = now ]; then
  backup
  exit
fi
while true; do
  # A failed dump is retried in an hour rather than at the next interval.
  if backup; then sleep "$((INTERVAL_HOURS * 3600))"; else sleep 3600; fi
done
