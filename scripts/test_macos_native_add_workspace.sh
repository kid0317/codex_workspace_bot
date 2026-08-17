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
  mkdir -p "$fixture/root/scripts" "$fixture/root/cmd/appctl" "$fixture/root/cmd/receivercheck" "$fixture/root/bin" \
    "$fixture/root/runtime" "$fixture/root/workspace-one" "$fixture/root/workspace-two" \
    "$fixture/root/runtime-home" "$fixture/home/.ssh" "$fixture/home/.codex"
  cp "$SCRIPT" "$fixture/root/scripts/macos_native_add_workspace.sh"
  chmod +x "$fixture/root/scripts/macos_native_add_workspace.sh"
  : >"$fixture/root/cmd/appctl/main.go"
  : >"$fixture/root/cmd/receivercheck/main.go"
  printf '%s\n' \
    'CODEX_WORKSPACE_BOT_DB_PASSWORD=test-db-password' \
    "CODEX_HOME=$fixture/root/runtime-home" >"$fixture/root/.env"
  printf '%s\n' 'model = "provider-default-model"' 'model_reasoning_effort = "medium"' \
    >"$fixture/root/runtime-home/config.toml"
  printf '%s\n' 'database:' '  password_env: CODEX_WORKSPACE_BOT_DB_PASSWORD' >"$fixture/root/config.yaml"
  : >"$fixture/calls.log"

  cat >"$fixture/root/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Darwin\n'
EOF

  cat >"$fixture/root/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'GO %s\n' "$*" >>"${FAKE_CALLS:?}"
[[ ${1:-} == build ]] || exit 93
output=
target=
previous=
for argument in "$@"; do
  [[ $previous == -o ]] && output=$argument
  case "$argument" in ./cmd/appctl|./cmd/receivercheck) target=$argument ;; esac
  previous=$argument
done
[[ -n $output && -n $target ]] || exit 94
if [[ $target == ./cmd/appctl ]]; then
  cat >"$output" <<'APPCTL_EOF'
#!/usr/bin/env bash
set -euo pipefail
command_name=${1:-}
shift || true
case "$command_name" in
  list)
    printf 'LIST %s\n' "$*" >>"${FAKE_CALLS:?}"
    list_call=0
    [[ ! -f ${FAKE_LIST_COUNTER:?} ]] || read -r list_call <"$FAKE_LIST_COUNTER"
    list_call=$((list_call + 1))
    printf '%s\n' "$list_call" >"$FAKE_LIST_COUNTER"
    [[ ${FAKE_LIST_STATUS:-0} -eq 0 ]] || exit "$FAKE_LIST_STATUS"
    if (( list_call > 1 )) && [[ ${FAKE_POST_LIST_STATUS:-0} -ne 0 ]]; then
      exit "$FAKE_POST_LIST_STATUS"
    fi
    if [[ ! -e ${FAKE_STATE:?}.initialized ]]; then
      [[ -z ${FAKE_APP_LIST:-} ]] || printf '%b' "$FAKE_APP_LIST" >"$FAKE_STATE"
      : >"$FAKE_STATE.initialized"
    fi
    [[ ! -f $FAKE_STATE ]] || cat "$FAKE_STATE"
    ;;
  create)
    printf 'CREATE %s\n' "$*" >>"${FAKE_CALLS:?}"
    secret_stdin=false
    app_name=
    app_id=
    workspace=
    enabled=
    previous=
    for argument in "$@"; do
      case "$previous" in
        --name) app_name=$argument ;;
        --app-id) app_id=$argument ;;
        --workspace-dir) workspace=$argument ;;
      esac
      case "$argument" in
        --enabled=*) enabled=${argument#--enabled=} ;;
        --secret-stdin) secret_stdin=true ;;
      esac
      previous=$argument
    done
    [[ $secret_stdin == true ]] || exit 91
    secret_value=$(cat)
    [[ -n $secret_value ]] || exit 92
    printf 'SECRET_STDIN_LENGTH=%s\n' "${#secret_value}" >>"${FAKE_CALLS:?}"
    ps -o command= -p "$$" >>"${FAKE_PS_LOG:?}"
    if env | grep -q '^CODEX_WORKSPACE_BOT_NEW_APP_SECRET='; then
      printf 'SECRET_ENV_VISIBLE_TO_APPCTL\n' >>"${FAKE_CALLS:?}"
      exit 96
    fi
    unset secret_value
    if [[ ${FAKE_CREATE_RACE:-0} -eq 1 ]]; then
      printf '%s\t%s\t%s\ttrue\n' "$app_name" "cli_competing" "/competing/workspace" >>"${FAKE_STATE:?}"
      exit 17
    fi
    [[ ${FAKE_CREATE_STATUS:-0} -eq 0 ]] || exit "$FAKE_CREATE_STATUS"
    if [[ ${FAKE_READBACK_MISMATCH:-0} -eq 1 ]]; then
      workspace=/wrong/readback/workspace
    fi
    printf '%s\t%s\t%s\t%s\n' "$app_name" "$app_id" "$workspace" "$enabled" >>"${FAKE_STATE:?}"
    printf 'updated fake-app\n'
    ;;
  *) exit 97 ;;
esac
APPCTL_EOF
else
  cat >"$output" <<'RECEIVER_EOF'
#!/usr/bin/env bash
set -euo pipefail
expected=
previous=
for argument in "$@"; do
  [[ $previous == --expected ]] && expected=$argument
  previous=$argument
done
payload=$(cat)
printf 'RECEIVERCHECK %s\n' "$*" >>"${FAKE_CALLS:?}"
[[ -n $expected ]] || exit 81
states=$(printf '%s\n' "$payload" | grep -Eo '"state":"[^"]+"' || true)
count=$(printf '%s\n' "$states" | awk 'NF { n++ } END { print n+0 }')
connected=$(printf '%s\n' "$states" | grep -Ec '"connected"$' || true)
[[ $count -eq $expected && $connected -eq $expected ]] || exit 1
RECEIVER_EOF
fi
chmod 0700 "$output"
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

  cat >"$fixture/root/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'CURL %s\n' "$*" >>"${FAKE_CALLS:?}"
[[ ${FAKE_CURL_TIMEOUT:-0} -eq 0 ]] || exit 28
url=${*: -1}
case "$url" in
  */healthz)
    printf 'ok\n'
    ;;
  */readyz)
    ready_call=0
    [[ ! -f ${FAKE_READY_COUNTER:?} ]] || read -r ready_call <"$FAKE_READY_COUNTER"
    ready_call=$((ready_call + 1))
    printf '%s\n' "$ready_call" >"$FAKE_READY_COUNTER"
    enabled_count=$(awk -F '\t' '$4 == "true" { count++ } END { print count+0 }' "${FAKE_STATE:?}")
    shown_count=$enabled_count
    state=connected
    case "${FAKE_READY_MODE:-connected}" in
      reconnecting-once)
        (( ready_call == 1 )) && state=reconnecting
        ;;
      short-once)
        (( ready_call == 1 && shown_count > 0 )) && shown_count=$((shown_count - 1))
        ;;
      never)
        state=reconnecting
        ;;
    esac
    printf '{"receivers":{'
    index=1
    while (( index <= shown_count )); do
      (( index == 1 )) || printf ','
      printf '"app-%s":{"state":"%s"}' "$index" "$state"
      index=$((index + 1))
    done
    printf '},"observability":"disabled"}\n'
    ;;
  *) exit 95 ;;
esac
EOF

  cat >"$fixture/root/bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$fixture/root/bin/uname" "$fixture/root/bin/go" "$fixture/root/bin/curl" \
    "$fixture/root/bin/sleep" "$fixture/root/macos_bot_controller.sh"
}

run_fixture() {
  local fixture=$1 input=$2
  shift 2
  set +e
  printf '%b' "$input" | env \
    HOME="$fixture/home" \
    PATH="$fixture/root/bin:/usr/bin:/bin" \
    FAKE_CALLS="$fixture/calls.log" \
    FAKE_PS_LOG="$fixture/ps.log" \
    FAKE_STATE="$fixture/apps.tsv" \
    FAKE_LIST_COUNTER="$fixture/list-counter" \
    FAKE_READY_COUNTER="$fixture/ready-counter" \
    "$@" \
    "$fixture/root/scripts/macos_native_add_workspace.sh" \
    >"$fixture/stdout" 2>"$fixture/stderr"
  RUN_STATUS=$?
  set -e
}

[[ -x $SCRIPT ]] || fail "macos_native_add_workspace.sh is missing or not executable"
bash -n "$SCRIPT"
[[ $(sed -n '2p' "$SCRIPT") == 'set +x' ]] || fail "the script must disable xtrace before handling secrets"
help_output=$($SCRIPT --help)
[[ $help_output == *'macOS'* && $help_output == *'Workspace'* ]] || fail "help must describe macOS Workspace onboarding"
if rg -n -- '--secret([=[:space:]]|$)' "$SCRIPT" >/dev/null; then
  fail "the script must never pass a secret value through --secret"
fi
rg -q -- '--secret-stdin' "$SCRIPT" || fail "the script must use appctl --secret-stdin"
! rg -q -- '--secret-env' "$SCRIPT" || fail "the add-Workspace script must not export the App Secret"
rg -q -- 'go build.*cmd/appctl|go build' "$SCRIPT" || fail "the script must build a private appctl before reading the secret"
rg -q -- 'macos_bot_controller\.sh[" ]+restart' "$SCRIPT" || fail "the script must reuse the macOS controller restart command"

TEST_TMP=$(mktemp -d)
trap 'rm -rf "$TEST_TMP"' EXIT

# Single enabled Workspace.
fixture="$TEST_TMP/single"
new_fixture "$fixture"
run_fixture "$fixture" "single-app\n$fixture/root/workspace-one\ncli_single123\nsuper-secret-single\ngpt-5.6-sol\nhigh\ny\nn\n"
if [[ $RUN_STATUS -ne 0 ]]; then
  sed -n '1,160p' "$fixture/stdout" >&2
  sed -n '1,160p' "$fixture/stderr" >&2
  sed -n '1,160p' "$fixture/calls.log" >&2
  fail "single add failed with status $RUN_STATUS"
fi
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 1 ]] || fail "single add must create once"
[[ $(grep -c '^CONTROLLER restart' "$fixture/calls.log") -eq 1 ]] || fail "single add must restart once"
assert_contains "$fixture/calls.log" '--enabled=true'
assert_contains "$fixture/calls.log" '--secret-stdin'
assert_contains "$fixture/stdout" '已经登记并生效'
assert_contains "$fixture/calls.log" 'http://127.0.0.1:8080/healthz'
assert_contains "$fixture/calls.log" 'http://127.0.0.1:8080/readyz'
[[ -z $(find "$fixture/root/runtime" -mindepth 1 -maxdepth 1 -print -quit) ]] || fail "private helper binaries must be cleaned after exit"
while IFS= read -r curl_call; do
  [[ $curl_call == *'--connect-timeout 2'* ]] || fail "curl must bound connect time"
  [[ $curl_call == *'--max-time 5'* ]] || fail "curl must bound total time"
done < <(grep '^CURL ' "$fixture/calls.log")

# Empty model/effort input inherits the already initialized Codex runtime config.
fixture="$TEST_TMP/config-defaults"
new_fixture "$fixture"
run_fixture "$fixture" "defaults-app\n$fixture/root/workspace-one\ncli_defaults123\ndefaults-secret\n\n\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "runtime config defaults scenario failed"
assert_contains "$fixture/calls.log" '--model provider-default-model'
assert_contains "$fixture/calls.log" '--effort medium'

# Continuous mode adds two independent App/Workspace pairs.
fixture="$TEST_TMP/two"
new_fixture "$fixture"
run_fixture "$fixture" "first-app\n$fixture/root/workspace-one\ncli_first123\nfirst-secret\ngpt-5.6-sol\nhigh\ny\ny\nsecond-app\n$fixture/root/workspace-two\ncli_second456\nsecond-secret\ngpt-5.6-terra\nmedium\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "continuous add failed with status $RUN_STATUS"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 2 ]] || fail "continuous add must create twice"
[[ $(grep -c '^CONTROLLER restart' "$fixture/calls.log") -eq 2 ]] || fail "continuous add must restart twice"
assert_contains "$fixture/calls.log" '--name first-app --app-id cli_first123'
assert_contains "$fixture/calls.log" '--name second-app --app-id cli_second456'
[[ $(awk -F '\t' 'NF { seen[$1 FS $2 FS $3]++ } END { for (key in seen) if (seen[key] != 1) exit 1; print length(seen) }' "$fixture/apps.tsv") -eq 2 ]] || fail "continuous add must leave two unique exact records"

# Final confirmation cancellation is a successful no-op.
fixture="$TEST_TMP/cancel"
new_fixture "$fixture"
run_fixture "$fixture" "cancelled-app\n$fixture/root/workspace-one\ncli_cancel123\ncancel-secret\ngpt-5.6-sol\nhigh\nn\n"
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

fixture="$TEST_TMP/control-workspace"
new_fixture "$fixture"
run_fixture "$fixture" 'control-app\n/absolute/path\twith-control\n'
[[ $RUN_STATUS -ne 0 ]] || fail "Workspace with control characters must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '控制字符'

for dangerous_path in / "$fixture/home" "$fixture/home/.ssh" "$fixture/home/.codex" /System; do
  fixture="$TEST_TMP/danger-$(printf '%s' "$dangerous_path" | tr '/.' '__')"
  new_fixture "$fixture"
  # Use the fixture's own HOME for HOME-sensitive cases.
  case "$dangerous_path" in
    */home|*/home/.ssh|*/home/.codex) rejected_path="$fixture/home${dangerous_path#*\/home}" ;;
    *) rejected_path=$dangerous_path ;;
  esac
  run_fixture "$fixture" "danger-app\n$rejected_path\n"
  [[ $RUN_STATUS -ne 0 ]] || fail "dangerous Workspace $rejected_path must fail"
  assert_no_create_or_restart "$fixture"
  assert_contains "$fixture/stderr" '危险目录'
done

fixture="$TEST_TMP/invalid-app-id"
new_fixture "$fixture"
run_fixture "$fixture" "id-app\n$fixture/root/workspace-one\nnot_cli_123\n"
[[ $RUN_STATUS -ne 0 ]] || fail "invalid App ID must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'cli_'

# Existing names and App IDs are refused without ever invoking create.
fixture="$TEST_TMP/duplicate-name"
new_fixture "$fixture"
run_fixture "$fixture" 'old-app\n' FAKE_APP_LIST="OLD-APP\\tcli_old123\\t/old/workspace\\ttrue\\n"
[[ $RUN_STATUS -ne 0 ]] || fail "duplicate name must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '名称已经存在'

fixture="$TEST_TMP/duplicate-id"
new_fixture "$fixture"
run_fixture "$fixture" "new-app\n$fixture/root/workspace-one\ncli_old123\n" FAKE_APP_LIST="old-app\\tcli_OLD123\\t/old/workspace\\ttrue\\n"
[[ $RUN_STATUS -ne 0 ]] || fail "duplicate App ID must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'App ID 已经存在'

# The secret reaches the final private appctl only through stdin.
fixture="$TEST_TMP/secret"
new_fixture "$fixture"
secret_value='do-not-leak-this-secret-90817'
run_fixture "$fixture" "secret-app\n$fixture/root/workspace-one\ncli_secret123\n$secret_value\ngpt-5.6-sol\nxhigh\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "secret-handling scenario failed"
assert_contains "$fixture/calls.log" 'SECRET_STDIN_LENGTH='
! grep -R -Fq -- "$secret_value" "$fixture" || fail "secret leaked to output, command logs, ps snapshot, or a temporary file"
! grep -q 'SECRET_VISIBLE_TO_CONTROLLER' "$fixture/calls.log" || fail "secret remained exported during restart"
! grep -q 'SECRET_ENV_VISIBLE_TO_APPCTL' "$fixture/calls.log" || fail "secret reached appctl through the environment"

# A hostile .env may enable xtrace; success and failure still must not reveal the newly entered secret.
for create_status in 0 19; do
  fixture="$TEST_TMP/env-xtrace-$create_status"
  new_fixture "$fixture"
  printf '%s\n' 'set -x' >>"$fixture/root/.env"
  secret_value="xtrace-secret-$create_status-90817"
  if [[ $create_status -eq 0 ]]; then
    run_fixture "$fixture" "xtrace-app\n$fixture/root/workspace-one\ncli_xtrace123\n$secret_value\ngpt-5.6-sol\nhigh\ny\nn\n"
    [[ $RUN_STATUS -eq 0 ]] || fail ".env set -x success case failed"
  else
    run_fixture "$fixture" "xtrace-fail-app\n$fixture/root/workspace-one\ncli_xtracefail123\n$secret_value\ngpt-5.6-sol\nhigh\ny\n" FAKE_CREATE_STATUS=$create_status
    [[ $RUN_STATUS -ne 0 ]] || fail ".env set -x failure case must fail"
  fi
  ! grep -R -Fq -- "$secret_value" "$fixture" || fail "secret leaked while .env enabled xtrace (status $create_status)"
done

# appctl failure does not restart or claim success.
fixture="$TEST_TMP/create-failure"
new_fixture "$fixture"
run_fixture "$fixture" "failed-app\n$fixture/root/workspace-one\ncli_failed123\nfailed-secret\ngpt-5.6-sol\nhigh\ny\n" FAKE_CREATE_STATUS=2
[[ $RUN_STATUS -ne 0 ]] || fail "appctl failure must fail the script"
! grep -q '^CONTROLLER restart' "$fixture/calls.log" || fail "appctl failure must not restart"
assert_contains "$fixture/stderr" '登记失败'

# A concurrent insert after list must make atomic create fail without overwriting or restarting.
fixture="$TEST_TMP/create-race"
new_fixture "$fixture"
run_fixture "$fixture" "race-app\n$fixture/root/workspace-one\ncli_race123\nrace-secret\ngpt-5.6-sol\nhigh\ny\n" FAKE_CREATE_RACE=1
[[ $RUN_STATUS -ne 0 ]] || fail "list/create race must fail atomically"
! grep -q '^CONTROLLER restart' "$fixture/calls.log" || fail "race failure must not restart"
assert_contains "$fixture/apps.tsv" $'race-app\tcli_competing\t/competing/workspace\ttrue'
! grep -q 'cli_race123' "$fixture/apps.tsv" || fail "race failure overwrote the competing record"

# A restart failure preserves the registration and gives an exact recovery command.
fixture="$TEST_TMP/restart-failure"
new_fixture "$fixture"
run_fixture "$fixture" "restart-app\n$fixture/root/workspace-one\ncli_restart123\nrestart-secret\ngpt-5.6-sol\nhigh\ny\n" FAKE_RESTART_STATUS=48
[[ $RUN_STATUS -ne 0 ]] || fail "restart failure must fail the script"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 1 ]] || fail "restart failure happens after one successful registration"
assert_contains "$fixture/stderr" '已登记但未生效'
assert_contains "$fixture/stderr" '服务状态未知'
assert_contains "$fixture/stderr" './macos_bot_controller.sh status'
assert_contains "$fixture/stderr" './macos_bot_controller.sh logs'
assert_contains "$fixture/stderr" './macos_bot_controller.sh restart'

# A successful create must be read back exactly before restart or a success claim.
fixture="$TEST_TMP/readback-mismatch"
new_fixture "$fixture"
run_fixture "$fixture" "readback-app\n$fixture/root/workspace-one\ncli_readback123\nreadback-secret\ngpt-5.6-sol\nhigh\ny\n" FAKE_READBACK_MISMATCH=1
[[ $RUN_STATUS -ne 0 ]] || fail "mismatched create readback must fail"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 1 ]] || fail "readback mismatch follows one reported create"
! grep -q '^CONTROLLER restart' "$fixture/calls.log" || fail "readback mismatch must not restart"
assert_contains "$fixture/stderr" '登记状态需人工核对'
! grep -q '已经登记并生效' "$fixture/stdout" || fail "readback mismatch must not claim success"

fixture="$TEST_TMP/post-list-failure"
new_fixture "$fixture"
run_fixture "$fixture" "post-list-app\n$fixture/root/workspace-one\ncli_postlist123\npost-list-secret\ngpt-5.6-sol\nhigh\ny\n" FAKE_POST_LIST_STATUS=44
[[ $RUN_STATUS -ne 0 ]] || fail "post-create list failure must fail"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 1 ]] || fail "post-create list failure follows one create"
! grep -q '^CONTROLLER restart' "$fixture/calls.log" || fail "post-create list failure must not restart"
assert_contains "$fixture/stderr" '登记状态需人工核对'

# Strict activation waits through a reconnecting receiver.
fixture="$TEST_TMP/reconnecting"
new_fixture "$fixture"
run_fixture "$fixture" "reconnect-app\n$fixture/root/workspace-one\ncli_reconnect123\nreconnect-secret\ngpt-5.6-sol\nhigh\ny\nn\n" FAKE_READY_MODE=reconnecting-once
[[ $RUN_STATUS -eq 0 ]] || fail "reconnecting receiver should become connected"
[[ $(<"$fixture/ready-counter") -ge 2 ]] || fail "strict activation must wait past reconnecting"
assert_contains "$fixture/stdout" '已经登记并生效'

# Strict activation also waits until receiver count equals enabled App count.
fixture="$TEST_TMP/receiver-count"
new_fixture "$fixture"
run_fixture "$fixture" "count-app\n$fixture/root/workspace-two\ncli_count123\ncount-secret\ngpt-5.6-sol\nhigh\ny\nn\n" \
  FAKE_APP_LIST="existing-app\\tcli_existing123\\t$fixture/root/workspace-one\\ttrue\\n" \
  FAKE_READY_MODE=short-once
[[ $RUN_STATUS -eq 0 ]] || fail "receiver count should eventually match enabled App count"
[[ $(<"$fixture/ready-counter") -ge 2 ]] || fail "strict activation must wait past a short receiver map"
assert_contains "$fixture/stdout" '已经登记并生效'

# Controller success is insufficient when strict receiver activation times out.
fixture="$TEST_TMP/activation-timeout"
new_fixture "$fixture"
run_fixture "$fixture" "timeout-app\n$fixture/root/workspace-one\ncli_timeout123\ntimeout-secret\ngpt-5.6-sol\nhigh\ny\n" \
  FAKE_READY_MODE=never CODEX_WORKSPACE_BOT_ADD_READY_ATTEMPTS=3
[[ $RUN_STATUS -ne 0 ]] || fail "strict activation timeout must fail"
assert_contains "$fixture/stderr" '已登记但未生效'
assert_contains "$fixture/stderr" './macos_bot_controller.sh status'
assert_contains "$fixture/stderr" './macos_bot_controller.sh logs'
assert_contains "$fixture/stderr" './macos_bot_controller.sh restart'
! grep -q '已经登记并生效' "$fixture/stdout" || fail "activation timeout must not claim success"

# curl timeout is bounded by flags and the polling attempt count.
fixture="$TEST_TMP/curl-timeout"
new_fixture "$fixture"
run_fixture "$fixture" "curl-timeout-app\n$fixture/root/workspace-one\ncli_curltimeout123\ncurl-timeout-secret\ngpt-5.6-sol\nhigh\ny\n" \
  FAKE_CURL_TIMEOUT=1 CODEX_WORKSPACE_BOT_ADD_READY_ATTEMPTS=2
[[ $RUN_STATUS -ne 0 ]] || fail "curl timeout must end in bounded activation failure"
[[ $(grep -c '^CURL ' "$fixture/calls.log") -eq 4 ]] || fail "two attempts must make exactly four bounded curl calls"
while IFS= read -r curl_call; do
  [[ $curl_call == *'--connect-timeout 2'* && $curl_call == *'--max-time 5'* ]] || fail "every curl call must be bounded"
done < <(grep '^CURL ' "$fixture/calls.log")

# The installed-state checks fail clearly and do not touch appctl create.
fixture="$TEST_TMP/missing-env"
new_fixture "$fixture"
rm "$fixture/root/.env"
run_fixture "$fixture" ''
[[ $RUN_STATUS -ne 0 ]] || fail "missing .env must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" '.env'

fixture="$TEST_TMP/missing-config"
new_fixture "$fixture"
rm "$fixture/root/config.yaml"
run_fixture "$fixture" ''
[[ $RUN_STATUS -ne 0 ]] || fail "missing config.yaml must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'config.yaml'

fixture="$TEST_TMP/missing-appctl"
new_fixture "$fixture"
rm "$fixture/root/cmd/appctl/main.go"
run_fixture "$fixture" ''
[[ $RUN_STATUS -ne 0 ]] || fail "missing appctl must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'appctl'

fixture="$TEST_TMP/mysql-unavailable"
new_fixture "$fixture"
run_fixture "$fixture" '' FAKE_LIST_STATUS=46
[[ $RUN_STATUS -ne 0 ]] || fail "unavailable MySQL/appctl list must fail"
assert_no_create_or_restart "$fixture"
assert_contains "$fixture/stderr" 'MySQL'

printf 'ok: macOS native multi-Workspace interactive onboarding\n'
