#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_HOST="127.0.0.1"
BACKEND_PORT="8787"
FRONTEND_HOST="127.0.0.1"
FRONTEND_PORT="3000"
DATA_DIR="$ROOT/.loom-demo"
BINARY="$ROOT/bin/loom"

for command in go bun python3; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'Missing required command: %s\n' "$command" >&2
		exit 1
	fi
done

if [[ ! -d "$ROOT/web/node_modules" ]]; then
	printf 'Frontend dependencies are missing. Run `cd web && bun install` first.\n' >&2
	exit 1
fi

python3 "$ROOT/scripts/check_ports.py" "$BACKEND_PORT" "$FRONTEND_PORT"

mkdir -p "$ROOT/bin" "$DATA_DIR"
printf 'Building Loom backend...\n'
(cd "$ROOT" && go build -o "$BINARY" ./cmd/loom)

backend_pid=""
frontend_pid=""

stop_child() {
	local pid="$1"
	[[ -n "$pid" ]] || return 0
	if ! kill -0 "$pid" 2>/dev/null; then
		wait "$pid" 2>/dev/null || true
		return 0
	fi
	kill -TERM "$pid" 2>/dev/null || true
	for _ in {1..30}; do
		if ! kill -0 "$pid" 2>/dev/null; then
			wait "$pid" 2>/dev/null || true
			return 0
		fi
		sleep 0.1
	done
	kill -KILL "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
}

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	stop_child "$frontend_pid"
	stop_child "$backend_pid"
	exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"$BINARY" --demo --listen "$BACKEND_HOST:$BACKEND_PORT" --data-dir "$DATA_DIR" &
backend_pid=$!

if ! python3 "$ROOT/scripts/wait_http.py" "http://$BACKEND_HOST:$BACKEND_PORT/api/health"; then
	printf 'Backend did not become ready at http://%s:%s.\n' "$BACKEND_HOST" "$BACKEND_PORT" >&2
	exit 1
fi
if ! kill -0 "$backend_pid" 2>/dev/null; then
	printf 'Backend exited before readiness was confirmed.\n' >&2
	exit 1
fi

(
	cd "$ROOT/web"
	exec bun ./node_modules/vite/bin/vite.js dev --host "$FRONTEND_HOST" --port "$FRONTEND_PORT" --strictPort
) &
frontend_pid=$!

if ! python3 "$ROOT/scripts/wait_http.py" "http://$FRONTEND_HOST:$FRONTEND_PORT"; then
	printf 'Frontend did not become ready at http://%s:%s.\n' "$FRONTEND_HOST" "$FRONTEND_PORT" >&2
	exit 1
fi

printf '\nLoom demo is ready:\n  Dashboard: http://%s:%s\n  API:       http://%s:%s/api/health\n  Data:      %s\n\nPress Ctrl-C to stop both processes.\n' \
	"$FRONTEND_HOST" "$FRONTEND_PORT" "$BACKEND_HOST" "$BACKEND_PORT" "$DATA_DIR"

while true; do
	if ! kill -0 "$backend_pid" 2>/dev/null; then
		printf 'Backend exited unexpectedly.\n' >&2
		exit 1
	fi
	if ! kill -0 "$frontend_pid" 2>/dev/null; then
		printf 'Frontend exited unexpectedly.\n' >&2
		exit 1
	fi
	sleep 1
done
