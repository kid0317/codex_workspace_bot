#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
SETUP="$ROOT/scripts/macos_native_setup.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -x $SETUP ]] || fail "macos_native_setup.sh is missing or not executable"
[[ $(sed -n '2p' "$SETUP") == 'set +x' ]] || fail "setup must disable xtrace before reading secrets"

help_output=$($SETUP --help)
[[ $help_output == *'--check'* ]] || fail "help must document the preflight-only mode"
[[ $help_output == *'MySQL'* ]] || fail "help must explain MySQL initialization"
[[ $help_output == *'Workspace'* ]] || fail "help must explain Workspace registration"

bash -n "$SETUP"

if rg -n -- '--secret[[:space:]]+"?\$' "$SETUP" >/dev/null; then
  fail "setup must not pass the Feishu secret as a command-line argument"
fi
rg -q -- '--secret-stdin' "$SETUP" || fail "setup must pass the Feishu secret through appctl stdin"
! rg -n '(^|[;[:space:]])(source|\.)[[:space:]].*ENV_FILE|eval[[:space:]]' "$SETUP" || fail "setup must never source or eval .env"
rg -q 'cmd/safedotenv' "$SETUP" || fail "setup must use the shared safe dotenv loader"
rg -q -- 'appctl upsert' "$SETUP" || fail "idempotent first-time setup must use explicit appctl upsert"

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

# Full fake setup proves explicit upsert remains idempotent and dotenv is data-only.
fixture="$tmp_dir/full"
mkdir -p "$fixture/root/scripts" "$fixture/root/cmd/safedotenv" "$fixture/root/cmd/appctl" \
  "$fixture/root/runtime" "$fixture/root/bin" "$fixture/root/mysql/bin" \
  "$fixture/package/workspace" "$fixture/package/user/.codex-runtime/home" "$fixture/home"
cp "$SETUP" "$fixture/root/scripts/macos_native_setup.sh"
chmod +x "$fixture/root/scripts/macos_native_setup.sh"
: >"$fixture/root/cmd/safedotenv/main.go"
: >"$fixture/root/cmd/appctl/main.go"
printf '%s\n' 'server:' '  listen_addr: 127.0.0.1:9191' >"$fixture/root/config.yaml.template"
printf '%s\n' 'model = "setup-default-model"' 'model_reasoning_effort = "high"' >"$fixture/package/user/.codex-runtime/home/config.toml"
go build -o "$fixture/real-safedotenv" "$ROOT/cmd/safedotenv"

cat >"$fixture/fake-appctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ ${1:-} == upsert ]] || exit 80
shift
name= app_id= workspace= model= effort= enabled=
previous=
for argument in "$@"; do
  case "$previous" in
    --name) name=$argument ;;
    --app-id) app_id=$argument ;;
    --workspace-dir) workspace=$argument ;;
    --model) model=$argument ;;
    --effort) effort=$argument ;;
  esac
  [[ $argument != --enabled=* ]] || enabled=${argument#--enabled=}
  previous=$argument
done
secret=$(cat)
[[ -n $secret && " $* " == *' --secret-stdin '* ]] || exit 81
unset secret
awk -F '\t' -v name="$name" '$1 != name { print }' ./setup-app-state.tsv 2>/dev/null >./setup-app-state.next || true
printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$app_id" "$workspace" "$model" "$effort" "$enabled" >>./setup-app-state.next
mv ./setup-app-state.next ./setup-app-state.tsv
printf 'UPSERT %s\n' "$*" >>./setup-app-calls.log
EOF
cat >"$fixture/root/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ ${1:-} == build ]] || exit 82
output=
target=
previous=
for argument in "$@"; do
  [[ $previous != -o ]] || output=$argument
  case "$argument" in ./cmd/safedotenv|./cmd/appctl) target=$argument ;; esac
  previous=$argument
done
case "$target" in
  ./cmd/safedotenv) cp "${FAKE_REAL_SAFE:?}" "$output" ;;
  ./cmd/appctl) cp "${FAKE_APPCTL:?}" "$output" ;;
  *) exit 83 ;;
esac
EOF
cat >"$fixture/root/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Darwin\n'
EOF
cat >"$fixture/root/bin/codex" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$fixture/root/bin/brew" <<EOF
#!/usr/bin/env bash
case "\${1:-} \${2:-}" in
  'list --versions') exit 0 ;;
  '--prefix mysql@8.4') printf '%s\n' '$fixture/root/mysql' ;;
  'services start') exit 0 ;;
  *) exit 0 ;;
esac
EOF
cat >"$fixture/root/mysql/bin/mysqladmin" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$fixture/root/mysql/bin/mysql" <<'EOF'
#!/usr/bin/env bash
for argument in "$@"; do
  [[ $argument != -e ]] || exit 0
done
cat >/dev/null || true
exit 0
EOF
cat >"$fixture/root/macos_bot_controller.sh" <<'EOF'
#!/usr/bin/env bash
printf 'CONTROLLER %s\n' "$*" >>./setup-controller-calls.log
EOF
chmod +x "$fixture/fake-appctl" "$fixture/root/bin/go" "$fixture/root/bin/uname" "$fixture/root/bin/codex" \
  "$fixture/root/bin/brew" "$fixture/root/mysql/bin/mysqladmin" "$fixture/root/mysql/bin/mysql" \
  "$fixture/root/macos_bot_controller.sh"

run_full_setup() {
  local secret=$1
  set +e
  printf '%s\n' "$fixture/package" setup-app cli_setup123 "$secret" '' '' n | env -i \
    HOME="$fixture/home" PATH="$fixture/root/bin:/usr/bin:/bin" \
    FAKE_REAL_SAFE="$fixture/real-safedotenv" FAKE_APPCTL="$fixture/fake-appctl" \
    "$fixture/root/scripts/macos_native_setup.sh" >"$fixture/setup.out" 2>"$fixture/setup.err"
  FULL_SETUP_STATUS=${PIPESTATUS[1]}
  set -e
}

run_full_setup 'setup-first-secret-90817'
[[ $FULL_SETUP_STATUS -eq 0 ]] || fail "first full fake setup failed"
run_full_setup 'setup-second-secret-90817'
[[ $FULL_SETUP_STATUS -eq 0 ]] || fail "second full fake setup failed"
[[ $(grep -c '^UPSERT ' "$fixture/root/setup-app-calls.log") -eq 2 ]] || fail "setup must execute explicit upsert twice"
[[ $(wc -l <"$fixture/root/setup-app-state.tsv") -eq 1 ]] || fail "two same-name setup runs must leave one fake record"
grep -Fq $'setup-app\tcli_setup123' "$fixture/root/setup-app-state.tsv" || fail "setup upsert record is not exact"
! grep -R -Fq -- 'setup-first-secret-90817' "$fixture/root" "$fixture/setup.out" "$fixture/setup.err" || fail "first setup secret leaked"
! grep -R -Fq -- 'setup-second-secret-90817' "$fixture/root" "$fixture/setup.out" "$fixture/setup.err" || fail "second setup secret leaked"

printf '%s\n' 'set -x' \
  'CODEX_WORKSPACE_BOT_DB_PASSWORD=setup-db-sentinel-90817' \
  'OPENAI_API_KEY=setup-provider-sentinel-90817' \
  'CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1=setup-crypto-sentinel-90817' >"$fixture/root/.env"
run_full_setup 'unread-new-secret-90817'
[[ $FULL_SETUP_STATUS -ne 0 ]] || fail "setup must reject executable dotenv content"
for sentinel in setup-db-sentinel-90817 setup-provider-sentinel-90817 setup-crypto-sentinel-90817 unread-new-secret-90817; do
  ! grep -Fq -- "$sentinel" "$fixture/setup.out" "$fixture/setup.err" "$fixture/root/setup-app-calls.log" || fail "setup rejection leaked a sentinel"
done

printf 'ok: macOS native setup command and secret-handling contract\n'
