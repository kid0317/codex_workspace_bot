#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
SCRIPT="$REPO_ROOT/scripts/macos_native_add_workspace.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local file=$1 expected=$2
  grep -Fq -- "$expected" "$file" || fail "$file does not contain: $expected"
}

assert_no_create_or_restart() {
  local fixture=$1
  ! grep -q '^CREATE ' "$fixture/calls.log" 2>/dev/null || fail "unexpected appctl create call"
  ! grep -q '^CONTROLLER restart' "$fixture/calls.log" 2>/dev/null || fail "unexpected controller restart"
}

new_fixture() {
  local fixture=$1
  mkdir -p "$fixture/root/scripts" "$fixture/root/cmd/appctl" "$fixture/root/bin" \
    "$fixture/root/workspace-one" "$fixture/root/workspace-two" "$fixture/home"
  cp "$SCRIPT" "$fixture/root/scripts/macos_native_add_workspace.sh"
  chmod +x "$fixture/root/scripts/macos_native_add_workspace.sh"
  : >"$fixture/root/cmd/appctl/main.go"
  printf '%s\n' 'CODEX_WORKSPACE_BOT_DB_PASSWORD=test-db-password' >"$fixture/root/.env"
  printf '%s\n' 'database:' '  password_env: CODEX_WORKSPACE_BOT_DB_PASSWORD' >"$fixture/root/config.yaml"
  : >"$fixture/calls.log"

  cat >"$fixture/root/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Darwin\n'
EOF

  cat >"$fixture/root/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *' ./cmd/appctl list '*)
    printf 'LIST %s\n' "$*" >>"${FAKE_CALLS:?}"
    [[ ${FAKE_LIST_STATUS:-0} -eq 0 ]] || exit "$FAKE_LIST_STATUS"
    [[ -z ${FAKE_APP_LIST:-} ]] || printf '%b' "$FAKE_APP_LIST"
    ;;
  *' ./cmd/appctl create '*)
    printf 'CREATE %s\n' "$*" >>"${FAKE_CALLS:?}"
    secret_env=
    previous=
    for argument in "$@"; do
      if [[ $previous == --secret-env ]]; then secret_env=$argument; fi
      previous=$argument
    done
    [[ -n $secret_env ]] || exit 91
    [[ -n ${!secret_env:-} ]] || exit 92
    printf 'SECRET_ENV=%s SECRET_LENGTH=%s\n' "$secret_env" "${#!secret_env}" >>"${FAKE_CALLS:?}"
    ps -o command= -p "$$" >>"${FAKE_PS_LOG:?}"
    [[ ${FAKE_CREATE_STATUS:-0} -eq 0 ]] || exit "$FAKE_CREATE_STATUS"
    printf 'updated fake-app\n'
    ;;
  *)
    printf 'UNEXPECTED_GO %s\n' "$*" >>"${FAKE_CALLS:?}"
    exit 93
    ;;
esac
EOF

  cat >"$fixture/root/macos_bot_controller.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'CONTROLLER %s\n' "$*" >>"${FAKE_CALLS:?}"
if env | grep -q '^CODEX_WORKSPACE_BOT_NEW_APP_SECRET='; then
  printf 'SECRET_VISIBLE_TO_CONTROLLER\n' >>"${FAKE_CALLS:?}"
  exit 94
fi
[[ ${FAKE_RESTART_STATUS:-0} -eq 0 ]] || exit "$FAKE_RESTART_STATUS"
printf 'service ready\n'
EOF
  chmod +x "$fixture/root/bin/uname" "$fixture/root/bin/go" "$fixture/root/macos_bot_controller.sh"
}

run_fixture() {
  local fixture=$1 input=$2
  shift 2
  set +e
  printf '%s' "$input" | env \
    HOME="$fixture/home" \
    PATH="$fixture/root/bin:/usr/bin:/bin" \
    FAKE_CALLS="$fixture/calls.log" \
    FAKE_PS_LOG="$fixture/ps.log" \
    "$@" \
    "$fixture/root/scripts/macos_native_add_workspace.sh" \
    >"$fixture/stdout" 2>"$fixture/stderr"
  RUN_STATUS=$?
  set -e
}

[[ -x $SCRIPT ]] || fail "macos_native_add_workspace.sh is missing or not executable"
bash -n "$SCRIPT"
help_output=$($SCRIPT --help)
[[ $help_output == *'macOS'* && $help_output == *'Workspace'* ]] || fail "help must describe macOS Workspace onboarding"
if rg -n -- '--secret([=[:space:]]|$)' "$SCRIPT" >/dev/null; then
  fail "the script must never pass a secret value through --secret"
fi
rg -q -- '--secret-env' "$SCRIPT" || fail "the script must use appctl --secret-env"
rg -q -- 'macos_bot_controller\.sh[" ]+restart' "$SCRIPT" || fail "the script must reuse the macOS controller restart command"

TEST_TMP=$(mktemp -d)
trap 'rm -rf "$TEST_TMP"' EXIT

# Single Workspace, explicitly disabled.
fixture="$TEST_TMP/single"
new_fixture "$fixture"
run_fixture "$fixture" "single-app\n$fixture/root/workspace-one\ncli_single123\nsuper-secret-single\ngpt-5.6-sol\nhigh\nn\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "single add failed with status $RUN_STATUS"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 1 ]] || fail "single add must create once"
[[ $(grep -c '^CONTROLLER restart' "$fixture/calls.log") -eq 1 ]] || fail "single add must restart once"
assert_contains "$fixture/calls.log" '--enabled=false'
assert_contains "$fixture/stdout" '已经登记并生效'

# Continuous mode adds two independent App/Workspace pairs.
fixture="$TEST_TMP/two"
new_fixture "$fixture"
run_fixture "$fixture" "first-app\n$fixture/root/workspace-one\ncli_first123\nfirst-secret\ngpt-5.6-sol\nhigh\ny\ny\ny\nsecond-app\n$fixture/root/workspace-two\ncli_second456\nsecond-secret\ngpt-5.6-terra\nmedium\ny\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "continuous add failed with status $RUN_STATUS"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 2 ]] || fail "continuous add must create twice"
[[ $(grep -c '^CONTROLLER restart' "$fixture/calls.log") -eq 2 ]] || fail "continuous add must restart twice"

# Final confirmation cancellation is a successful no-op.
fixture="$TEST_TMP/cancel"
new_fixture "$fixture"
run_fixture "$fixture" "cancelled-app\n$fixture/root/workspace-one\ncli_cancel123\ncancel-secret\ngpt-5.6-sol\nhigh\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "user cancellation should exit successfully"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stdout" '已取消'

# Invalid input is rejected before create.
fixture="$TEST_TMP/invalid-name"
new_fixture "$fixture"
run_fixture "$fixture" 'bad name\n'
[[ $RUN_STATUS -ne 0 ]] || fail "invalid name must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '名称'

fixture="$TEST_TMP/relative-workspace"
new_fixture "$fixture"
run_fixture "$fixture" 'relative-app\nrelative/path\n'
[[ $RUN_STATUS -ne 0 ]] || fail "relative Workspace must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '绝对路径'

fixture="$TEST_TMP/missing-workspace"
new_fixture "$fixture"
run_fixture "$fixture" 'missing-app\n/definitely/not/a/workspace\n'
[[ $RUN_STATUS -ne 0 ]] || fail "missing Workspace must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '不存在'

fixture="$TEST_TMP/invalid-app-id"
new_fixture "$fixture"
run_fixture "$fixture" "id-app\n$fixture/root/workspace-one\nnot_cli_123\n"
[[ $RUN_STATUS -ne 0 ]] || fail "invalid App ID must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'cli_'

# Existing names and App IDs are refused without ever invoking create.
fixture="$TEST_TMP/duplicate-name"
new_fixture "$fixture"
run_fixture "$fixture" 'old-app\n' FAKE_APP_LIST="old-app\\tcli_old123\\t/old/workspace\\ttrue\\n"
[[ $RUN_STATUS -ne 0 ]] || fail "duplicate name must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '名称已经存在'

fixture="$TEST_TMP/duplicate-id"
new_fixture "$fixture"
run_fixture "$fixture" "new-app\n$fixture/root/workspace-one\ncli_old123\n" FAKE_APP_LIST="old-app\\tcli_old123\\t/old/workspace\\ttrue\\n"
[[ $RUN_STATUS -ne 0 ]] || fail "duplicate App ID must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'App ID 已经存在'

# The secret reaches appctl by environment name only and disappears before restart.
fixture="$TEST_TMP/secret"
new_fixture "$fixture"
secret_value='do-not-leak-this-secret-90817'
run_fixture "$fixture" "secret-app\n$fixture/root/workspace-one\ncli_secret123\n$secret_value\ngpt-5.6-sol\nxhigh\ny\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "secret-handling scenario failed"
assert_contains "$fixture/calls.log" 'SECRET_ENV=CODEX_WORKSPACE_BOT_NEW_APP_SECRET'
! grep -R -Fq -- "$secret_value" "$fixture" || fail "secret leaked to output, command logs, ps snapshot, or a temporary file"
! grep -q 'SECRET_VISIBLE_TO_CONTROLLER' "$fixture/calls.log" || fail "secret remained exported during restart"

# appctl failure does not restart or claim success.
fixture="$TEST_TMP/create-failure"
new_fixture "$fixture"
run_fixture "$fixture" "failed-app\n$fixture/root/workspace-one\ncli_failed123\nfailed-secret\ngpt-5.6-sol\nhigh\ny\ny\n" FAKE_CREATE_STATUS=47
[[ $RUN_STATUS -ne 0 ]] || fail "appctl failure must fail the script"
! grep -q '^CONTROLLER restart' "$fixture/calls.log" || fail "appctl failure must not restart"
assert_contains "$fixture/stderr" '登记失败'

# A restart failure preserves the registration and gives an exact recovery command.
fixture="$TEST_TMP/restart-failure"
new_fixture "$fixture"
run_fixture "$fixture" "restart-app\n$fixture/root/workspace-one\ncli_restart123\nrestart-secret\ngpt-5.6-sol\nhigh\ny\ny\n" FAKE_RESTART_STATUS=48
[[ $RUN_STATUS -ne 0 ]] || fail "restart failure must fail the script"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 1 ]] || fail "restart failure happens after one successful registration"
assert_contains "$fixture/stderr" '已登记但未生效'
assert_contains "$fixture/stderr" './macos_bot_controller.sh restart'

# The installed-state checks fail clearly and do not touch appctl create.
fixture="$TEST_TMP/missing-env"
new_fixture "$fixture"
rm "$fixture/root/.env"
run_fixture "$fixture" ''
[[ $RUN_STATUS -ne 0 ]] || fail "missing .env must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '.env'

fixture="$TEST_TMP/mysql-unavailable"
new_fixture "$fixture"
run_fixture "$fixture" '' FAKE_LIST_STATUS=46
[[ $RUN_STATUS -ne 0 ]] || fail "unavailable MySQL/appctl list must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'MySQL'

printf 'ok: macOS native multi-Workspace interactive onboarding\n'
