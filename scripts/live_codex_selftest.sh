#!/usr/bin/env sh
set -eu

backend="${LIVE_SECRET_BACKEND:-native}"
prompt="${LIVE_PROMPT:-say ready}"
run_prompt="${LIVE_RUN_PROMPT:-0}"
host="${CAPD_HOST:-127.0.0.1}"
port="${CAPD_PORT:-7777}"
log="${CAPD_LIVE_DAEMON_LOG:-${TMPDIR:-/tmp}/capd-live-daemon-$$.log}"
bin="${CAPD_LIVE_DAEMON_BIN:-${TMPDIR:-/tmp}/capd-live-daemon-$$}"

export CAPD_HOST="$host"
export CAPD_PORT="$port"
export CAPD_SECRET_BACKEND="$backend"

daemon_pid=""

cleanup() {
	if [ -n "$daemon_pid" ]; then
		kill "$daemon_pid" >/dev/null 2>&1 || true
		wait "$daemon_pid" >/dev/null 2>&1 || true
	fi
	if [ -z "${CAPD_LIVE_DAEMON_BIN:-}" ]; then
		rm -f "$bin"
	fi
}
trap cleanup EXIT INT TERM

health() {
	go run ./cmd/capd health --json --require-secret-backend "$backend" >/dev/null 2>&1
}

if health; then
	echo "using existing capd daemon at ${host}:${port} with ${backend} SecretStore"
else
	echo "starting temporary capd daemon at ${host}:${port} with ${backend} SecretStore"
	go build -o "$bin" ./cmd/capd
	"$bin" start --host "$host" --port "$port" --secret-backend "$backend" >"$log" 2>&1 &
	daemon_pid="$!"
	i=0
	while ! health; do
		i=$((i + 1))
		if [ "$i" -ge 40 ]; then
			echo "capd daemon did not become healthy; log: $log" >&2
			tail -n 80 "$log" >&2 || true
			exit 1
		fi
		if ! kill -0 "$daemon_pid" >/dev/null 2>&1; then
			echo "capd daemon exited before becoming healthy; log: $log" >&2
			tail -n 80 "$log" >&2 || true
			exit 1
		fi
		sleep 1
	done
fi

make live-codex-preflight LIVE_SECRET_BACKEND="$backend"

case "$run_prompt" in
	1|true|TRUE|yes|YES)
		echo "running live Codex prompt with quota-aware auto account"
		go run ./cmd/capd run --agent codex --account auto --require-fresh-quota "$prompt"
		;;
esac
