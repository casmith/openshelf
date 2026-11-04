#!/usr/bin/env bash
# Restore a backup made by backup.sh. This REPLACES the current database and data volume.
# Usage: scripts/restore.sh [-y] [--no-start] <backup-dir>
set -euo pipefail
source "$(dirname "$0")/lib.sh"

yes= start=1
while [ $# -gt 0 ]; do
	case $1 in
	-y | --yes) yes=1 ;;
	--no-start) start= ;;
	-*) die "unknown flag $1" ;;
	*) break ;;
	esac
	shift
done
in=${1:-}
[ -n "$in" ] || die "usage: scripts/restore.sh [-y] [--no-start] <backup-dir>"
[ -f "$in/db.dump" ] || die "$in/db.dump not found"

if [ -z "$yes" ]; then
	read -r -p "This replaces the OpenShelf database and data volume with $in. Continue? [y/N] " answer
	[[ $answer == [yY]* ]] || die "aborted"
fi

log "stopping app"
dc stop app >/dev/null 2>&1 || true
dc up -d --wait postgres

log "recreating database $DB_NAME"
dc exec -T postgres psql -U "$DB_USER" -d postgres -v ON_ERROR_STOP=1 -q \
	-c "DROP DATABASE IF EXISTS \"$DB_NAME\" WITH (FORCE)" \
	-c "CREATE DATABASE \"$DB_NAME\" OWNER \"$DB_USER\""

log "restoring $in/db.dump"
dc exec -T postgres pg_restore -U "$DB_USER" -d "$DB_NAME" --no-owner --exit-on-error <"$in/db.dump"

if [ -f "$in/data.tar.gz" ]; then
	log "restoring data volume from $in/data.tar.gz"
	dc run --rm --no-deps -T toolbox sh -c 'find /data -mindepth 1 -delete && tar -C /data -xzpf -' <"$in/data.tar.gz"
else
	log "no data.tar.gz in $in; leaving the data volume as is"
fi

if [ -n "$start" ]; then
	log "starting app"
	dc up -d --wait app
fi
log "restore complete"
