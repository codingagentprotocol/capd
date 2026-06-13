#!/usr/bin/env sh
set -eu

backend="${LIVE_SECRET_BACKEND:-native}"
prompt="${LIVE_PROMPT:-say ready}"
run_prompt="${LIVE_RUN_PROMPT:-0}"
diagnose_secretstore="${LIVE_DIAGNOSE_SECRETSTORE:-0}"
host="${CAPD_HOST:-127.0.0.1}"
port="${CAPD_PORT:-7777}"
log="${CAPD_LIVE_DAEMON_LOG:-${TMPDIR:-/tmp}/capd-live-daemon-$$.log}"
bin="${CAPD_LIVE_DAEMON_BIN:-${TMPDIR:-/tmp}/capd-live-daemon-$$}"
bin_owned=0

export CAPD_HOST="$host"
export CAPD_PORT="$port"
export CAPD_SECRET_BACKEND="$backend"

daemon_pid=""

cleanup() {
	if [ -n "$daemon_pid" ]; then
		kill "$daemon_pid" >/dev/null 2>&1 || true
		wait "$daemon_pid" >/dev/null 2>&1 || true
	fi
	if [ "$bin_owned" -eq 1 ]; then
		rm -f "$bin"
	fi
}
trap cleanup EXIT INT TERM

if [ -z "${CAPD_LIVE_DAEMON_BIN:-}" ]; then
	go build -o "$bin" ./cmd/capd
	bin_owned=1
fi

health() {
	"$bin" health --json --require-secret-backend "$backend" >/dev/null 2>&1
}

health_any_backend() {
	"$bin" health --json >/dev/null 2>&1
}

if health; then
	echo "using existing capd daemon at ${host}:${port} with ${backend} SecretStore"
elif health_any_backend; then
	echo "capd daemon is running at ${host}:${port}, but not with ${backend} SecretStore" >&2
	"$bin" health --json >&2 || true
	echo "restart it with: capd start --secret-backend $backend" >&2
	exit 1
else
	echo "starting temporary capd daemon at ${host}:${port} with ${backend} SecretStore"
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

if ! make live-codex-preflight LIVE_SECRET_BACKEND="$backend" CAPD_BIN="$bin"; then
	echo "live-codex-preflight failed; safe diagnostics follow" >&2
	echo "readiness gaps to resolve: >=2 imported Codex accounts, fresh quota for auto-route/all accounts, ${backend} SecretStore, and daemon/Web readiness" >&2
	"$bin" health --json --require-secret-backend "$backend" || true
	"$bin" accounts --secret-backend "$backend" codex list --json || true
	"$bin" agents route --account auto --require-fresh-quota --json || true
	"$bin" accounts --secret-backend "$backend" codex smoke --json --require-multiple --require-secret-backend "$backend" --timeout 2m || true
	case "$diagnose_secretstore" in
		1|true|TRUE|yes|YES)
			"$bin" doctor --json --fail --verify-secretstore --require-secret-backend "$backend" --timeout 2m || true
			"$bin" accounts check --json --readiness --require-secret-backend "$backend" --timeout 2m || true
			if health; then
				"$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail || true
			fi
			;;
	esac
	exit 1
fi

case "$run_prompt" in
	1|true|TRUE|yes|YES)
		echo "running live Codex prompt with quota-aware auto account"
		"$bin" run --agent codex --account auto --require-fresh-quota "$prompt"
		;;
esac
