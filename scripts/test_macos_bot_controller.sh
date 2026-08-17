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

printf 'ok: macOS launchd controller contract\n'
