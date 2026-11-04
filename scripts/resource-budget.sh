#!/usr/bin/env bash
# Checks the ADR 0001 idle budget: starts a throwaway stack, seeds it, exercises a few pages,
# waits idle, then asserts app RSS < MAX_RSS_MB and CPU < MAX_CPU_PCT.
# Env: COMPOSE_PROJECT_NAME (default openshelf-budget), PORT (default 18081), IDLE_SECONDS (60),
#      SAMPLE_SECONDS (20), MAX_RSS_MB (60), MAX_CPU_PCT (1), OPENSHELF_IMAGE, SKIP_BUILD.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

idle=${IDLE_SECONDS:-60} sample=${SAMPLE_SECONDS:-20}
max_rss=${MAX_RSS_MB:-60} max_cpu=${MAX_CPU_PCT:-1}

throwaway_stack "${COMPOSE_PROJECT_NAME:-openshelf-budget}" "${PORT:-18081}"
seed

log "warming up"
jar=$work/cookies
curl -fsS -o /dev/null -c "$jar" -d username=alice -d password=password "$base_url/login"
for p in / /library /library/game /journal /lists /stats /offline /sw.js /manifest.webmanifest; do
	curl -sS -o /dev/null -b "$jar" "$base_url$p"
done

log "idling for ${idle}s"
sleep "$idle"

cid=$(dc ps -q app)
pid=$(docker inspect -f '{{.State.Pid}}' "$cid")
if [ -r "/proc/$pid/stat" ] && [ -r "/proc/$pid/status" ]; then
	ticks() { awk '{print $14 + $15}' "/proc/$pid/stat"; }
	t0=$(ticks)
	sleep "$sample"
	t1=$(ticks)
	hz=$(getconf CLK_TCK)
	cpu=$(awk -v d=$((t1 - t0)) -v hz="$hz" -v s="$sample" 'BEGIN {printf "%.2f", d / hz / s * 100}')
	rss=$(awk '/^VmRSS:/ {printf "%.1f", $2 / 1024}' "/proc/$pid/status")
	method="/proc/$pid over ${sample}s"
else
	read -r mem cpu < <(docker stats --no-stream --format '{{.MemUsage}} {{.CPUPerc}}' "$cid" | awk '{print $1, $4}')
	cpu=${cpu%\%}
	rss=$(awk -v m="$mem" 'BEGIN {
		v = m + 0; u = m; sub(/^[0-9.]+/, "", u)
		f = (u == "GiB") ? 1024 : (u == "KiB") ? 1 / 1024 : (u == "B") ? 1 / 1048576 : 1
		printf "%.1f", v * f }')
	method="docker stats"
fi

size=$(docker image ls "$OPENSHELF_IMAGE" --format '{{.Size}}' | head -1)
log "docker stats: $(docker stats --no-stream --format '{{.MemUsage}} {{.CPUPerc}}' "$cid")"
printf 'idle RSS: %s MB (limit %s)\nidle CPU: %s%% (limit %s%%)\nimage size: %s\nmeasured with %s\n' \
	"$rss" "$max_rss" "$cpu" "$max_cpu" "$size" "$method"

awk -v r="$rss" -v mr="$max_rss" -v c="$cpu" -v mc="$max_cpu" 'BEGIN {exit !(r < mr && c < mc)}' ||
	die "resource budget exceeded"
log "resource budget OK"
