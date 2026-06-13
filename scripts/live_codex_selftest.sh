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
summary="${CAPD_LIVE_SUMMARY:-}"
repair_plan="${CAPD_LIVE_REPAIR_PLAN:-}"
evidence_dir="${CAPD_LIVE_EVIDENCE_DIR:-}"
evidence_manifest=""
evidence_report=""
evidence_health=""
evidence_accounts=""
evidence_route=""
evidence_probe=""
evidence_doctor=""
evidence_smoke=""
evidence_accounts_check=""
bin_owned=0

if [ -n "$evidence_dir" ]; then
	evidence_manifest="$evidence_dir/manifest.json"
	evidence_report="$evidence_dir/report.html"
fi

export CAPD_HOST="$host"
export CAPD_PORT="$port"
export CAPD_SECRET_BACKEND="$backend"

daemon_pid=""
daemon_mode="existing"

json_escape() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

evidence_manifest_path() {
	path="$1"
	if [ -z "$path" ]; then
		printf ''
		return 0
	fi
	prefix="${evidence_dir%/}/"
	rel="${path#"$prefix"}"
	if [ "$rel" != "$path" ]; then
		printf '%s' "$rel"
		return 0
	fi
	printf '%s' "$path"
}

write_summary() {
	if [ -z "$summary" ]; then
		return 0
	fi
	status="$(json_escape "$1")"
	stage="$(json_escape "$2")"
	detail="$(json_escape "${3:-}")"
	checked_at="$(json_escape "$(date -u '+%Y-%m-%dT%H:%M:%SZ')")"
	backend_json="$(json_escape "$backend")"
	host_json="$(json_escape "$host")"
	port_json="$(json_escape "$port")"
	daemon_mode_json="$(json_escape "$daemon_mode")"
	log_json="$(json_escape "$log")"
	bin_json="$(json_escape "$bin")"
	repair_plan_json="$(json_escape "$repair_plan")"
	evidence_dir_json="$(json_escape "$evidence_dir")"
	evidence_manifest_json="$(json_escape "$evidence_manifest")"
	evidence_report_json="$(json_escape "$evidence_report")"
	evidence_route_json="$(json_escape "$evidence_route")"
	evidence_probe_json="$(json_escape "$evidence_probe")"
	evidence_doctor_json="$(json_escape "$evidence_doctor")"
	diagnose_json="$(json_escape "$diagnose_secretstore")"
	run_prompt_json="$(json_escape "$run_prompt")"
	{
		printf '{\n'
		printf '  "summaryVersion": 1,\n'
		printf '  "status": "%s",\n' "$status"
		printf '  "stage": "%s",\n' "$stage"
		printf '  "detail": "%s",\n' "$detail"
		printf '  "checkedAt": "%s",\n' "$checked_at"
		printf '  "backend": "%s",\n' "$backend_json"
		printf '  "host": "%s",\n' "$host_json"
		printf '  "port": "%s",\n' "$port_json"
		printf '  "daemonMode": "%s",\n' "$daemon_mode_json"
		printf '  "logPath": "%s",\n' "$log_json"
		printf '  "bin": "%s",\n' "$bin_json"
		printf '  "repairPlanPath": "%s",\n' "$repair_plan_json"
		printf '  "evidenceDir": "%s",\n' "$evidence_dir_json"
		printf '  "evidenceManifestPath": "%s",\n' "$evidence_manifest_json"
		printf '  "evidenceReportPath": "%s",\n' "$evidence_report_json"
		printf '  "routeEvidencePath": "%s",\n' "$evidence_route_json"
		printf '  "probeEvidencePath": "%s",\n' "$evidence_probe_json"
		printf '  "doctorEvidencePath": "%s",\n' "$evidence_doctor_json"
		printf '  "diagnoseSecretStore": "%s",\n' "$diagnose_json"
		printf '  "runPrompt": "%s"\n' "$run_prompt_json"
		printf '}\n'
	} >"$summary" || echo "warning: failed to write live summary to $summary" >&2
}

write_evidence_manifest() {
	if [ -z "$evidence_dir" ]; then
		return 0
	fi
	prepare_evidence_dir || return $?
	status="$(json_escape "$1")"
	stage="$(json_escape "$2")"
	detail="$(json_escape "${3:-}")"
	checked_at="$(json_escape "$(date -u '+%Y-%m-%dT%H:%M:%SZ')")"
	backend_json="$(json_escape "$backend")"
	host_json="$(json_escape "$host")"
	port_json="$(json_escape "$port")"
	daemon_mode_json="$(json_escape "$daemon_mode")"
	health_json="$(json_escape "$(evidence_manifest_path "$evidence_health")")"
	accounts_json="$(json_escape "$(evidence_manifest_path "$evidence_accounts")")"
	route_json="$(json_escape "$(evidence_manifest_path "$evidence_route")")"
	probe_json="$(json_escape "$(evidence_manifest_path "$evidence_probe")")"
	doctor_json="$(json_escape "$(evidence_manifest_path "$evidence_doctor")")"
	smoke_json="$(json_escape "$(evidence_manifest_path "$evidence_smoke")")"
	accounts_check_json="$(json_escape "$(evidence_manifest_path "$evidence_accounts_check")")"
	{
		printf '{\n'
		printf '  "manifestVersion": 1,\n'
		printf '  "status": "%s",\n' "$status"
		printf '  "stage": "%s",\n' "$stage"
		printf '  "detail": "%s",\n' "$detail"
		printf '  "checkedAt": "%s",\n' "$checked_at"
		printf '  "backend": "%s",\n' "$backend_json"
		printf '  "host": "%s",\n' "$host_json"
		printf '  "port": "%s",\n' "$port_json"
		printf '  "daemonMode": "%s",\n' "$daemon_mode_json"
		printf '  "artifacts": {\n'
		printf '    "health": "%s",\n' "$health_json"
		printf '    "accountsList": "%s",\n' "$accounts_json"
		printf '    "agentsRoute": "%s",\n' "$route_json"
		printf '    "probeData": "%s",\n' "$probe_json"
		printf '    "doctor": "%s",\n' "$doctor_json"
		printf '    "accountsSmoke": "%s",\n' "$smoke_json"
		printf '    "accountsCheck": "%s"\n' "$accounts_check_json"
		printf '  }\n'
		printf '}\n'
	} >"$evidence_manifest"
}

prepare_evidence_dir() {
	if [ -z "$evidence_dir" ]; then
		return 0
	fi
	mkdir -p "$evidence_dir" || {
		echo "failed to create live evidence directory: $evidence_dir" >&2
		return 1
	}
}

capture_evidence() {
	name="$1"
	shift
	if [ -z "$evidence_dir" ]; then
		"$@"
		return $?
	fi
	path="$evidence_dir/$name"
	if "$@" >"$path"; then
		cat "$path"
		return 0
	fi
	status=$?
	cat "$path" || true
	return "$status"
}

write_repair_plan() {
	if [ -z "$repair_plan" ]; then
		return 0
	fi
	if "$bin" doctor --prompt-free --json --fail --require-secret-backend "$backend" --timeout 2m >"$repair_plan" 2>/dev/null; then
		return 0
	fi
	if [ -s "$repair_plan" ]; then
		return 0
	fi
	echo "warning: failed to write live repair plan to $repair_plan" >&2
}

write_success_evidence() {
	if [ -z "$evidence_dir" ]; then
		return 0
	fi
	prepare_evidence_dir || return $?
	evidence_route="$evidence_dir/agents-route.json"
	evidence_probe="$evidence_dir/probe-data-readiness.json"
	evidence_doctor="$evidence_dir/doctor-prompt-free.json"
	evidence_manifest="$evidence_dir/manifest.json"
	evidence_report="$evidence_dir/report.html"
	"$bin" agents route --account auto --require-fresh-quota --json >"$evidence_route" || return $?
	"$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail >"$evidence_probe" || return $?
	"$bin" doctor --prompt-free --json --fail --require-secret-backend "$backend" --timeout 2m >"$evidence_doctor" || return $?
}

verify_success_evidence() {
	if [ -z "$evidence_dir" ]; then
		return 0
	fi
	"$bin" probe evidence --manifest "$evidence_manifest" --html "$evidence_report" --fail
}

write_failure_evidence_report() {
	if [ -z "$evidence_dir" ]; then
		return 0
	fi
	"$bin" probe evidence --manifest "$evidence_manifest" --html "$evidence_report"
}

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

write_summary "running" "initializing" "live Codex selftest starting"

if [ -z "${CAPD_LIVE_DAEMON_BIN:-}" ]; then
	go build -o "$bin" ./cmd/capd
	bin_owned=1
fi

write_summary "running" "daemon-health" "checking daemon health"

health() {
	"$bin" health --json --require-secret-backend "$backend" >/dev/null 2>&1
}

health_any_backend() {
	"$bin" health --json >/dev/null 2>&1
}

if health; then
	daemon_mode="existing"
	echo "using existing capd daemon at ${host}:${port} with ${backend} SecretStore"
elif health_any_backend; then
	echo "capd daemon is running at ${host}:${port}, but not with ${backend} SecretStore" >&2
	"$bin" health --json >&2 || true
	echo "restart it with: capd start --secret-backend $backend" >&2
	write_repair_plan
	write_summary "failed" "secret-backend" "daemon is running with a different SecretStore backend"
	exit 1
else
	daemon_mode="temporary"
	echo "starting temporary capd daemon at ${host}:${port} with ${backend} SecretStore"
	"$bin" start --host "$host" --port "$port" --secret-backend "$backend" >"$log" 2>&1 &
	daemon_pid="$!"
	i=0
	while ! health; do
		i=$((i + 1))
		if [ "$i" -ge 40 ]; then
			echo "capd daemon did not become healthy; log: $log" >&2
			tail -n 80 "$log" >&2 || true
			write_repair_plan
			write_summary "failed" "daemon-health" "temporary daemon did not become healthy"
			exit 1
		fi
		if ! kill -0 "$daemon_pid" >/dev/null 2>&1; then
			echo "capd daemon exited before becoming healthy; log: $log" >&2
			tail -n 80 "$log" >&2 || true
			write_repair_plan
			write_summary "failed" "daemon-start" "temporary daemon exited before becoming healthy"
			exit 1
		fi
		sleep 1
	done
fi

write_summary "running" "live-codex-preflight" "running live Codex preflight"

if ! make live-codex-preflight LIVE_SECRET_BACKEND="$backend" CAPD_BIN="$bin"; then
	echo "live-codex-preflight failed; safe diagnostics follow" >&2
	echo "readiness gaps to resolve: >=2 imported Codex accounts, fresh quota for auto-route/all accounts, ${backend} SecretStore, and daemon/Web readiness" >&2
	prepare_evidence_dir || true
	write_repair_plan
	write_summary "failed" "live-codex-preflight" "readiness gaps: accounts, quota, SecretStore, or daemon/Web readiness"
	if [ -n "$evidence_dir" ]; then
		evidence_health="$evidence_dir/health.json"
		evidence_accounts="$evidence_dir/accounts-list.json"
		evidence_smoke="$evidence_dir/accounts-smoke.json"
		evidence_manifest="$evidence_dir/manifest.json"
		evidence_report="$evidence_dir/report.html"
	fi
	capture_evidence "health.json" "$bin" health --json --require-secret-backend "$backend" || true
	capture_evidence "accounts-list.json" "$bin" accounts --secret-backend "$backend" codex list --json || true
	if [ -n "$evidence_dir" ]; then
		evidence_route="$evidence_dir/agents-route.json"
		evidence_probe="$evidence_dir/probe-data-prompt-free.json"
	fi
	capture_evidence "agents-route.json" "$bin" agents route --account auto --require-fresh-quota --json || true
	capture_evidence "probe-data-prompt-free.json" "$bin" probe data --json --timeout 2m || true
	capture_evidence "accounts-smoke.json" "$bin" accounts --secret-backend "$backend" codex smoke --json --require-multiple --require-secret-backend "$backend" --timeout 2m || true
	case "$diagnose_secretstore" in
		1|true|TRUE|yes|YES)
			if [ -n "$evidence_dir" ]; then
				evidence_doctor="$evidence_dir/doctor-secretstore.json"
				evidence_accounts_check="$evidence_dir/accounts-check-readiness.json"
			fi
			capture_evidence "doctor-secretstore.json" "$bin" doctor --json --fail --verify-secretstore --require-secret-backend "$backend" --timeout 2m || true
			capture_evidence "accounts-check-readiness.json" "$bin" accounts check --json --readiness --require-secret-backend "$backend" --timeout 2m || true
			if health; then
				if [ -n "$evidence_dir" ]; then
					evidence_probe="$evidence_dir/probe-data-readiness.json"
				fi
				capture_evidence "probe-data-readiness.json" "$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail || true
			fi
			;;
	esac
	write_evidence_manifest "failed" "live-codex-preflight" "readiness gaps: accounts, quota, SecretStore, or daemon/Web readiness" || true
	write_failure_evidence_report || true
	write_summary "failed" "live-codex-preflight" "readiness gaps: accounts, quota, SecretStore, or daemon/Web readiness"
	exit 1
fi

case "$run_prompt" in
	1|true|TRUE|yes|YES)
		echo "running live Codex prompt with quota-aware auto account"
		write_summary "running" "live-prompt" "running quota-aware Codex prompt"
		if ! "$bin" run --agent codex --account auto --require-fresh-quota "$prompt"; then
			write_repair_plan
			write_summary "failed" "live-prompt" "quota-aware Codex prompt failed"
			exit 1
		fi
		;;
esac

write_summary "running" "evidence" "writing live Codex evidence artifacts"
if ! write_success_evidence; then
	write_repair_plan
	write_summary "failed" "evidence" "failed to write live Codex evidence artifacts"
	exit 1
fi
if ! write_evidence_manifest "passed" "complete" "live Codex selftest completed"; then
	write_repair_plan
	write_summary "failed" "evidence" "failed to write live Codex evidence manifest"
	exit 1
fi
if ! verify_success_evidence; then
	write_repair_plan
	write_summary "failed" "evidence" "failed to validate live Codex evidence manifest"
	exit 1
fi
write_summary "passed" "complete" "live Codex selftest completed"
