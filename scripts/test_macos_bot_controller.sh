#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CONTROLLER="$ROOT/macos_bot_controller.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -x $CONTROLLER ]] || fail "macos_bot_controller.sh is missing or not executable"

set +e
usage_output=$($CONTROLLER 2>&1)
usage_status=$?
set -e

[[ $usage_status -eq 2 ]] || fail "no-argument status=$usage_status, want 2"
[[ $usage_output == *'start|restart|stop|build|status|logs'* ]] || fail "usage is missing native controller commands"

bash -n "$CONTROLLER"

if rg -n '/proc|systemctl|systemd-run|journalctl' "$CONTROLLER" >/dev/null; then
  fail "macOS controller must not depend on Linux process or service APIs"
fi
rg -q 'launchctl' "$CONTROLLER" || fail "macOS controller must use launchd for lifecycle management"
! rg -n '(^|[;[:space:]])(source|\.)[[:space:]].env|eval[[:space:]]' "$CONTROLLER" || fail "controller and runner must never source or eval .env"
rg -q 'safedotenv' "$CONTROLLER" || fail "runtime runner must use the shared safe dotenv loader"
rg -q 'receivercheck' "$CONTROLLER" || fail "controller readiness must use the structured receiver checker"

curl_lines=$(rg 'curl .*healthz|curl .*readyz' "$CONTROLLER" || true)
[[ -n $curl_lines ]] || fail "controller must probe health endpoints"
while IFS= read -r line; do
  [[ $line == *'--connect-timeout'* && $line == *'--max-time'* ]] || fail "every controller curl must be bounded: $line"
done <<<"$curl_lines"

test_tmp=$(mktemp -d)
trap 'rm -rf "$test_tmp"' EXIT
fixture="$test_tmp/fixture"
mkdir -p "$fixture/runtime" "$fixture/bin" "$fixture/home/Library/LaunchAgents"
cp "$CONTROLLER" "$fixture/macos_bot_controller.sh"
chmod +x "$fixture/macos_bot_controller.sh"
printf '%s\n' 'server:' '  listen_addr: 127.0.0.1:9191' >"$fixture/config.yaml"
printf '%s\n' 'CODEX_WORKSPACE_BOT_DB_PASSWORD=db-sentinel' >"$fixture/.env"
for binary in codex_workspace_bot appctl safedotenv receivercheck; do
  : >"$fixture/runtime/$binary"
  chmod +x "$fixture/runtime/$binary"
done

cat >"$fixture/runtime/receivercheck" <<'EOF'
#!/usr/bin/env bash
if [[ ${1:-} == --config && ${3:-} == --print-base-url ]]; then
  printf 'http://127.0.0.1:9191\n'
  exit 0
fi
exit 1
EOF
cat >"$fixture/runtime/safedotenv" <<'EOF'
#!/usr/bin/env bash
while (( $# > 0 )); do
  [[ $1 == -- ]] && { shift; break; }
  shift
done
exec "$@"
EOF
cat >"$fixture/runtime/appctl" <<'EOF'
#!/usr/bin/env bash
printf 'internal-a\n'
EOF
cat >"$fixture/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Darwin\n'
EOF
cat >"$fixture/bin/plutil" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$fixture/bin/launchctl" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  print) [[ -f ${FAKE_LAUNCH_STATE:?} ]] ;;
  bootout) rm -f "${FAKE_LAUNCH_STATE:?}" ;;
  bootstrap) : >"${FAKE_LAUNCH_STATE:?}" ;;
  *) exit 1 ;;
esac
EOF
cat >"$fixture/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf 'CURL %s\n' "$*" >>"${FAKE_CALLS:?}"
[[ " $* " == *' --connect-timeout 2 '* ]] || exit 90
[[ " $* " == *' --max-time 5 '* ]] || exit 91
[[ ${FAKE_CURL_MODE:-ok} == ok ]] || exit 28
case "${*: -1}" in
  */healthz) printf 'ok\n' ;;
  */readyz) printf '{"receivers":{"internal-a":{"state":"connected"}}}\n' ;;
  *) exit 92 ;;
esac
EOF
chmod +x "$fixture/runtime/receivercheck" "$fixture/runtime/safedotenv" "$fixture/runtime/appctl" \
  "$fixture/bin/uname" "$fixture/bin/plutil" "$fixture/bin/launchctl" "$fixture/bin/curl"

HOME="$fixture/home" PATH="$fixture/bin:/usr/bin:/bin" FAKE_CALLS="$fixture/calls.log" \
  FAKE_LAUNCH_STATE="$fixture/launch.state" "$fixture/macos_bot_controller.sh" status >"$fixture/status.out"
grep -Fq 'http://127.0.0.1:9191/healthz' "$fixture/calls.log" || fail "status must use configured port"
grep -Fq 'http://127.0.0.1:9191/readyz' "$fixture/calls.log" || fail "status must probe configured readyz"

: >"$fixture/launch.state"
set +e
timeout 4 env HOME="$fixture/home" PATH="$fixture/bin:/usr/bin:/bin" FAKE_CALLS="$fixture/calls.log" \
  FAKE_LAUNCH_STATE="$fixture/launch.state" FAKE_CURL_MODE=timeout CODEX_WORKSPACE_BOT_START_TIMEOUT_SECONDS=1 \
  "$fixture/macos_bot_controller.sh" restart >"$fixture/restart.out" 2>"$fixture/restart.err"
restart_status=$?
set -e
[[ $restart_status -ne 0 && $restart_status -ne 124 ]] || fail "restart with no HTTP response must fail within its bound"
grep -Fq 'service did not become ready within 1s' "$fixture/restart.err" || fail "bounded restart must report timeout"
! rg -n '(^|[;[:space:]])(source|\.)[[:space:]].env' "$fixture/runtime/macos_run.sh" || fail "generated runner must not source dotenv"
grep -Fq 'safedotenv exec --file ./.env' "$fixture/runtime/macos_run.sh" || fail "generated runner must use safe dotenv exec"

# Execute the generated runner with the real data-only loader on both valid and
# hostile dotenv files. Parent-only secrets must not reach the server process.
go build -o "$fixture/runtime/safedotenv" "$ROOT/cmd/safedotenv"
cat >"$fixture/runtime/codex_workspace_bot" <<EOF
#!/usr/bin/env bash
[[ -n \${CODEX_WORKSPACE_BOT_DB_PASSWORD:-} ]] || exit 71
[[ -z \${PARENT_ONLY_SECRET:-} ]] || exit 72
: >'$fixture/server-called'
EOF
chmod +x "$fixture/runtime/codex_workspace_bot"
printf '%s\n' \
  'CODEX_WORKSPACE_BOT_DB_PASSWORD=runner-db-sentinel-90817' \
  'OPENAI_API_KEY=runner-provider-sentinel-90817' \
  'CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1=runner-crypto-sentinel-90817' >"$fixture/.env"
PARENT_ONLY_SECRET=must-not-reach-server "$fixture/runtime/macos_run.sh" >"$fixture/runner.out" 2>"$fixture/runner.err"
[[ -f $fixture/server-called ]] || fail "valid dotenv runner did not reach the server"
for sentinel in runner-db-sentinel-90817 runner-provider-sentinel-90817 runner-crypto-sentinel-90817 must-not-reach-server; do
  ! grep -Fq -- "$sentinel" "$fixture/runner.out" "$fixture/runner.err" || fail "runner success leaked a sentinel"
done

rm "$fixture/server-called"
printf '%s\n' 'set -x' \
  'CODEX_WORKSPACE_BOT_DB_PASSWORD=runner-bad-db-sentinel-90817' \
  'OPENAI_API_KEY=runner-bad-provider-sentinel-90817' \
  'CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1=runner-bad-crypto-sentinel-90817' >"$fixture/.env"
set +e
PARENT_ONLY_SECRET=must-not-reach-server "$fixture/runtime/macos_run.sh" >"$fixture/runner.out" 2>"$fixture/runner.err"
runner_status=$?
set -e
[[ $runner_status -ne 0 && ! -e $fixture/server-called ]] || fail "hostile dotenv must be rejected before server execution"
for sentinel in runner-bad-db-sentinel-90817 runner-bad-provider-sentinel-90817 runner-bad-crypto-sentinel-90817 must-not-reach-server; do
  ! grep -Fq -- "$sentinel" "$fixture/runner.out" "$fixture/runner.err" || fail "runner rejection leaked a sentinel"
done

printf 'ok: macOS launchd controller contract\n'
