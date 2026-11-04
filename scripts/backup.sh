#!/usr/bin/env bash
# Back up a running OpenShelf compose stack: a pg_dump (custom format) and a tarball of the data volume.
# Usage: scripts/backup.sh [output-dir]   (default ./backups/openshelf-<UTC timestamp>)
set -euo pipefail
source "$(dirname "$0")/lib.sh"

out=${1:-./backups/openshelf-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$out"

log "dumping database to $out/db.dump"
dc exec -T postgres pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc >"$out/db.dump.partial"
mv "$out/db.dump.partial" "$out/db.dump"

log "archiving data volume to $out/data.tar.gz"
dc run --rm --no-deps -T toolbox tar -C /data -czf - . >"$out/data.tar.gz.partial"
mv "$out/data.tar.gz.partial" "$out/data.tar.gz"

log "backup complete: $out"
