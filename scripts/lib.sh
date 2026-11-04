# Shared helpers for the ops scripts. Source it; don't run it.
# Compose settings come from the standard COMPOSE_FILE / COMPOSE_PROJECT_NAME / COMPOSE_ENV_FILES variables.

scripts_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "$scripts_dir/.." && pwd)
export COMPOSE_FILE=${COMPOSE_FILE:-$repo_dir/deploy/compose.yaml}
export COMPOSE_PROGRESS=${COMPOSE_PROGRESS:-quiet}

DB_USER=${DB_USER:-openshelf}
DB_NAME=${DB_NAME:-openshelf}

dc() { docker compose "$@"; }
log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# throwaway_stack PROJECT PORT: points compose at an isolated project with its own env file
# and removes it (containers and volumes) on exit.
throwaway_stack() {
	export COMPOSE_PROJECT_NAME=$1
	export OPENSHELF_HTTP_PORT=127.0.0.1:$2
	export OPENSHELF_IMAGE=${OPENSHELF_IMAGE:-openshelf:ci}
	work=$(mktemp -d)
	export OPENSHELF_ENV_FILE=$work/env COMPOSE_ENV_FILES=$work/env
	cat >"$work/env" <<-EOF
		POSTGRES_PASSWORD=$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')
		OPENSHELF_SECRET_KEY=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
		OPENSHELF_BASE_URL=http://localhost:$2
	EOF
	base_url=http://127.0.0.1:$2
	trap 'log "tearing down $COMPOSE_PROJECT_NAME"; dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
	if [ -z "${SKIP_BUILD:-}" ]; then
		log "building $OPENSHELF_IMAGE"
		dc build app
	fi
	log "starting stack"
	dc up -d --wait
}

seed() {
	log "seeding demo data"
	dc run --rm --no-deps -T -e OPENSHELF_ENV=development app seed
}

psql_q() { dc exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 -At "$@"; }
