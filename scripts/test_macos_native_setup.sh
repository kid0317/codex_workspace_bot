#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
SETUP="$ROOT/scripts/macos_native_setup.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -x $SETUP ]] || fail "macos_native_setup.sh is missing or not executable"

help_output=$($SETUP --help)
[[ $help_output == *'--check'* ]] || fail "help must document the preflight-only mode"
[[ $help_output == *'MySQL'* ]] || fail "help must explain MySQL initialization"
[[ $help_output == *'Workspace'* ]] || fail "help must explain Workspace registration"

bash -n "$SETUP"

if rg -n -- '--secret[[:space:]]+"?\$' "$SETUP" >/dev/null; then
  fail "setup must not pass the Feishu secret as a command-line argument"
fi
rg -q -- '--secret-env' "$SETUP" || fail "setup must pass the Feishu secret through appctl --secret-env"

printf 'ok: macOS native setup command and secret-handling contract\n'
