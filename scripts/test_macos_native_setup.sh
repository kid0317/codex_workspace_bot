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

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "%s|%s|%s|%s|%s|%s\\n" \' \
  '  "${HOMEBREW_NO_AUTO_UPDATE:-}" \' \
  '  "${HOMEBREW_NO_ENV_HINTS:-}" \' \
  '  "${HOMEBREW_API_DOMAIN:-}" \' \
  '  "${HOMEBREW_BOTTLE_DOMAIN:-}" \' \
  '  "${GOPROXY:-}" \' \
  '  "$*" >>"${BREW_ENV_LOG:?}"' \
  'if [[ ${1:-} == list ]]; then exit 1; fi' \
  'if [[ ${1:-} == install ]]; then exit 73; fi' \
  'exit 0' >"$tmp_dir/bin/brew"
chmod +x "$tmp_dir/bin/brew"

set +e
printf '\n' | \
  BREW_ENV_LOG="$tmp_dir/brew-env.log" \
  HOME="$tmp_dir/home" \
  PATH="$tmp_dir/bin:$PATH" \
  "$SETUP" >"$tmp_dir/setup.out" 2>"$tmp_dir/setup.err"
setup_status=${PIPESTATUS[1]}
set -e

[[ $setup_status -eq 73 ]] || fail "dependency test must reach the fake brew install (status $setup_status)"
[[ $(wc -l <"$tmp_dir/brew-env.log") -eq 2 ]] || fail "expected brew list and brew install calls"
while IFS='|' read -r no_update no_hints api_domain bottle_domain go_proxy brew_args; do
  [[ $no_update == 1 ]] || fail "every brew call must disable auto-update"
  [[ $no_hints == 1 ]] || fail "every brew call must hide network-irrelevant hints"
  [[ $api_domain == https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles/api ]] || fail "every brew call must use the mainland API mirror"
  [[ $bottle_domain == https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles ]] || fail "every brew call must use the mainland bottle mirror"
  [[ $go_proxy == https://goproxy.cn,direct ]] || fail "setup must default Go downloads to the mainland proxy"
  [[ -n $brew_args ]] || fail "brew invocation must be recorded"
done <"$tmp_dir/brew-env.log"

printf 'ok: macOS native setup command and secret-handling contract\n'
