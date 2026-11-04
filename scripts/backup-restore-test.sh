#!/usr/bin/env bash
# Round-trips seeded data through backup.sh and restore.sh on a throwaway stack.
# Env: COMPOSE_PROJECT_NAME (default openshelf-brtest), PORT (default 18080), OPENSHELF_IMAGE, SKIP_BUILD.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

throwaway_stack "${COMPOSE_PROJECT_NAME:-openshelf-brtest}" "${PORT:-18080}"
seed

dc run --rm --no-deps -T toolbox sh -c \
	'mkdir -p /data/covers && echo restore-marker >/data/covers/marker.txt && chown -R 65532:65532 /data/covers'

# River tables churn while the app runs, so compare every other table.
counts() {
	psql_q <<-'SQL'
		SELECT format('SELECT %L || '' '' || count(*) FROM public.%I', table_name, table_name)
		FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name NOT LIKE 'river\_%'
		ORDER BY table_name \gexec
	SQL
}

counts >"$work/before.txt"
for t in users media_items library_entries; do
	n=$(awk -v t="$t" '$1 == t {print $2}' "$work/before.txt")
	[ "${n:-0}" -gt 0 ] || die "seed produced no rows in $t"
done
log "seeded $(awk '{s += $2} END {print s}' "$work/before.txt") rows across $(wc -l <"$work/before.txt") tables"

"$scripts_dir/backup.sh" "$work/backup"
[ -s "$work/backup/db.dump" ] || die "empty db.dump"

log "wiping stack (containers and volumes)"
dc down -v
dc up -d --wait postgres

"$scripts_dir/restore.sh" -y "$work/backup"

counts >"$work/after.txt"
if ! diff -u "$work/before.txt" "$work/after.txt"; then
	die "row counts differ after restore"
fi
marker=$(dc run --rm --no-deps -T toolbox cat /data/covers/marker.txt)
[ "$marker" = restore-marker ] || die "data volume not restored"
owner=$(dc run --rm --no-deps -T toolbox stat -c %u /data/covers/marker.txt)
[ "$owner" = 65532 ] || die "data volume ownership not preserved (uid $owner)"

curl -fsS "$base_url/readyz" >/dev/null || die "app not ready after restore"
code=$(curl -s -o /dev/null -w '%{http_code}' -d username=alice -d password=password "$base_url/login")
[ "$code" = 303 ] || die "login as seeded user failed after restore (status $code)"

log "backup/restore round trip OK"
