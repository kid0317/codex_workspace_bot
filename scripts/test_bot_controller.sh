#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CONTROLLER="$ROOT/bot_controller.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -x "$CONTROLLER" ]] || fail "bot_controller.sh is missing or not executable"

set +e
usage_output=$("$CONTROLLER" 2>&1)
usage_status=$?
unknown_output=$("$CONTROLLER" unknown 2>&1)
unknown_status=$?
set -e

[[ $usage_status -eq 2 ]] || fail "no-argument status=$usage_status, want 2"
[[ $unknown_status -eq 2 ]] || fail "unknown-command status=$unknown_status, want 2"
[[ "$usage_output" == *'start|restart|stop|build'* ]] || fail "usage text is missing supported commands"
[[ "$unknown_output" == *'start|restart|stop|build'* ]] || fail "unknown-command output is missing usage"

bash -n "$CONTROLLER"
printf 'ok: bot_controller command contract\n'
