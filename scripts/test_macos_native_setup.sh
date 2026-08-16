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

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/bin" "$tmp_dir/home"

printf '#!/usr/bin/env bash\nprintf "Darwin\\n"\n' >"$tmp_dir/bin/uname"
printf '#!/usr/bin/env bash\nprintf "14.6\\n"\n' >"$tmp_dir/bin/sw_vers"
printf '#!/usr/bin/env bash\nprintf called >"${BREW_CALL_MARKER:?}"\nexit 73\n' >"$tmp_dir/bin/brew"
chmod +x "$tmp_dir/bin/uname" "$tmp_dir/bin/sw_vers" "$tmp_dir/bin/brew"

BREW_CALL_MARKER="$tmp_dir/brew-called" \
  HOME="$tmp_dir/home" \
  PATH="$tmp_dir/bin:$PATH" \
  "$SETUP" --check >"$tmp_dir/check.out" 2>"$tmp_dir/check.err"

[[ ! -e $tmp_dir/brew-called ]] || fail "--check must not execute Homebrew"

printf 'ok: macOS native setup command and secret-handling contract\n'
