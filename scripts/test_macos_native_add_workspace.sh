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
  mkdir -p "$fixture/root/scripts" "$fixture/root/cmd/appctl" "$fixture/root/cmd/receivercheck" "$fixture/root/cmd/safedotenv" "$fixture/root/bin" \
    "$fixture/root/runtime" "$fixture/root/workspace-one" "$fixture/root/workspace-two" \
    "$fixture/root/runtime home 中文" "$fixture/root/user data 中文" "$fixture/root/mounted-user" \
    "$fixture/home/.ssh" "$fixture/home/.codex"
  cp "$SCRIPT" "$fixture/root/scripts/macos_native_add_workspace.sh"
  chmod +x "$fixture/root/scripts/macos_native_add_workspace.sh"
  : >"$fixture/root/cmd/appctl/main.go"
  : >"$fixture/root/cmd/receivercheck/main.go"
  : >"$fixture/root/cmd/safedotenv/main.go"
  printf '%s\n' \
    'CODEX_WORKSPACE_BOT_DB_PASSWORD=test-db-password' \
    "CODEX_HOME=${fixture// /\\ }/root/runtime\ home\ 中文" \
    "USER_DIR=${fixture// /\\ }/root/user\ data\ 中文" \
    "AIPM_MOUNT_USER_DIR=${fixture// /\\ }/root/mounted-user" >"$fixture/root/.env"
  printf '%s\n' 'model = "provider-default-model"' 'model_reasoning_effort = "medium"' \
    >"$fixture/root/runtime home 中文/config.toml"
  printf '%s\n' 'server:' '  listen_addr: 127.0.0.1:9191' 'database:' '  password_env: CODEX_WORKSPACE_BOT_DB_PASSWORD' >"$fixture/root/config.yaml"
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
  case "$argument" in ./cmd/appctl|./cmd/receivercheck|./cmd/safedotenv) target=$argument ;; esac
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
    [[ ! -f $FAKE_STATE ]] || awk -F '\t' 'BEGIN { OFS="\t" } { print $1, $2, $3, $4 }' "$FAKE_STATE"
    ;;
  create)
    printf 'CREATE %s\n' "$*" >>"${FAKE_CALLS:?}"
    create_call=0
    [[ ! -f ${FAKE_CREATE_COUNTER:?} ]] || read -r create_call <"$FAKE_CREATE_COUNTER"
    create_call=$((create_call + 1))
    printf '%s\n' "$create_call" >"$FAKE_CREATE_COUNTER"
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
    if [[ ${FAKE_CREATE_FAIL_ON:-0} -eq $create_call ]]; then
      exit "${FAKE_CREATE_FAIL_STATUS:-19}"
    fi
    if [[ ${FAKE_CREATE_RACE:-0} -eq 1 ]]; then
      printf '%s\t%s\t%s\ttrue\t%s\n' "$app_name" "cli_competing" "/competing/workspace" "internal-$app_name" >>"${FAKE_STATE:?}"
      exit 17
    fi
    [[ ${FAKE_CREATE_STATUS:-0} -eq 0 ]] || exit "$FAKE_CREATE_STATUS"
    if [[ ${FAKE_READBACK_MISMATCH:-0} -eq 1 ]]; then
      workspace=/wrong/readback/workspace
    fi
    printf '%s\t%s\t%s\t%s\t%s\n' "$app_name" "$app_id" "$workspace" "$enabled" "internal-$app_name" >>"${FAKE_STATE:?}"
    printf 'updated fake-app\n'
    ;;
  receiver-ids)
    printf 'RECEIVER_IDS %s\n' "$*" >>"${FAKE_CALLS:?}"
    [[ ! -f ${FAKE_STATE:?} ]] || awk -F '\t' '$4 == "true" { if ($5 != "") print $5; else print "internal-existing-" NR }' "$FAKE_STATE"
    ;;
  *) exit 97 ;;
esac
APPCTL_EOF
elif [[ $target == ./cmd/receivercheck ]]; then
  cat >"$output" <<'RECEIVER_EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == --config && ${3:-} == --print-base-url ]]; then
  printf 'http://127.0.0.1:9191\n'
  exit 0
fi
expected_ids=
previous=
for argument in "$@"; do
  [[ $previous == --expected-ids ]] && expected_ids=$argument
  previous=$argument
done
payload=$(cat)
printf 'RECEIVERCHECK %s\n' "$*" >>"${FAKE_CALLS:?}"
[[ -n $expected_ids ]] || exit 81
IFS=, read -r -a ids <<<"$expected_ids"
[[ $(printf '%s\n' "$payload" | grep -Eo '"state":"connected"' | wc -l | tr -d ' ') -eq ${#ids[@]} ]] || exit 1
for id in "${ids[@]}"; do
  [[ $payload == *\"$id\":\{\"state\":\"connected\"\}* ]] || exit 1
done
RECEIVER_EOF
else
  cat >"$output" <<'DOTENV_EOF'
#!/usr/bin/env bash
set -euo pipefail
command_name=${1:-}
shift || true
file=
key=
while (( $# > 0 )); do
  case "$1" in
    --file) file=$2; shift 2 ;;
    --key) key=$2; shift 2 ;;
    --allow-missing) shift ;;
    --) shift; break ;;
    *) break ;;
  esac
done
DOTENV_NAMES=()
DOTENV_VALUES=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ -z $line || $line == \#* ]] && continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] || exit 64
  name=${BASH_REMATCH[1]}
  value=${BASH_REMATCH[2]}
  case "$name" in
    CODEX_WORKSPACE_BOT_*|CODEX_HOME|USER_DIR|AIPM_MOUNT_WORKSPACE_DIR|AIPM_MOUNT_USER_DIR|AIPM_STATE|SANDBOX_STATE|OPENAI_*|DASHSCOPE_*|LANGFUSE_*) ;;
    *) exit 64 ;;
  esac
  [[ $value != *'$('* && $value != *'`'* ]] || exit 64
  value=${value//\\ / }
  value=${value//\\\$/\$}
  DOTENV_NAMES+=("$name")
  DOTENV_VALUES+=("$value")
done <"$file"
case "$command_name" in
  validate) exit 0 ;;
  get)
    for ((i=0; i<${#DOTENV_NAMES[@]}; i++)); do
      [[ ${DOTENV_NAMES[i]} == "$key" ]] && { printf '%s' "${DOTENV_VALUES[i]}"; exit 0; }
    done
    exit 0
    ;;
  exec)
    for ((i=0; i<${#DOTENV_NAMES[@]}; i++)); do export "${DOTENV_NAMES[i]}=${DOTENV_VALUES[i]}"; done
    exec "$@"
    ;;
  *) exit 64 ;;
esac
DOTENV_EOF
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
    mapfile -t enabled_ids < <(awk -F '\t' '$4 == "true" { if ($5 != "") print $5; else print "internal-existing-" NR }' "${FAKE_STATE:?}")
    shown_count=${#enabled_ids[@]}
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
      receiver_id=${enabled_ids[index-1]}
      [[ ${FAKE_READY_MODE:-} != wrong-same-count ]] || receiver_id="wrong-$index"
      printf '"%s":{"state":"%s"}' "$receiver_id" "$state"
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
    FAKE_CREATE_COUNTER="$fixture/create-counter" \
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
! rg -n '(^|[;[:space:]])(source|\.)[[:space:]].*ENV_FILE|eval[[:space:]]' "$SCRIPT" || fail "the script must never source or eval .env"
rg -q 'cmd/safedotenv' "$SCRIPT" || fail "the script must build the shared safe dotenv loader"
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
assert_contains "$fixture/calls.log" 'http://127.0.0.1:9191/healthz'
assert_contains "$fixture/calls.log" 'http://127.0.0.1:9191/readyz'
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

fixture="$TEST_TMP/config-missing"
new_fixture "$fixture"
rm "$fixture/root/runtime home 中文/config.toml"
run_fixture "$fixture" "fallback-app\n$fixture/root/workspace-one\ncli_fallback123\nfallback-secret\n\n\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "missing runtime config must use safe defaults"
assert_contains "$fixture/calls.log" '--model gpt-5.6-terra'
assert_contains "$fixture/calls.log" '--effort high'

fixture="$TEST_TMP/config-invalid"
new_fixture "$fixture"
printf '%s\n' 'model = "$(bad-model)"' 'model_reasoning_effort = "dangerous"' >"$fixture/root/runtime home 中文/config.toml"
run_fixture "$fixture" "invalid-default-app\n$fixture/root/workspace-one\ncli_invaliddefault123\ninvalid-default-secret\n\n\ny\nn\n"
[[ $RUN_STATUS -eq 0 ]] || fail "invalid runtime config must use safe defaults"
assert_contains "$fixture/calls.log" '--model gpt-5.6-terra'
assert_contains "$fixture/calls.log" '--effort high'

# Continuous mode adds two independent App/Workspace pairs.
fixture="$TEST_TMP/two"
new_fixture "$fixture"
run_fixture "$fixture" "first-app\n$fixture/root/workspace-one\ncli_first123\nfirst-secret\ngpt-5.6-sol\nhigh\ny\ny\nsecond-app\n$fixture/root/workspace-two\ncli_second456\nsecond-secret\ngpt-5.6-terra\nmedium\ny\nn\n"
if [[ $RUN_STATUS -ne 0 ]]; then
  sed -n '1,200p' "$fixture/stdout" >&2
  sed -n '1,200p' "$fixture/stderr" >&2
  sed -n '1,200p' "$fixture/calls.log" >&2
  fail "continuous add failed with status $RUN_STATUS"
fi
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 2 ]] || fail "continuous add must create twice"
[[ $(grep -c '^CONTROLLER restart' "$fixture/calls.log") -eq 2 ]] || fail "continuous add must restart twice"
assert_contains "$fixture/calls.log" '--name first-app --app-id cli_first123'
assert_contains "$fixture/calls.log" '--name second-app --app-id cli_second456'
assert_contains "$fixture/calls.log" "--workspace-dir $fixture/root/workspace-one --model gpt-5.6-sol --effort high --enabled=true"
assert_contains "$fixture/calls.log" "--workspace-dir $fixture/root/workspace-two --model gpt-5.6-terra --effort medium --enabled=true"
[[ $(awk -F '\t' 'NF { seen[$1 FS $2 FS $3]++ } END { for (key in seen) if (seen[key] != 1) exit 1; print length(seen) }' "$fixture/apps.tsv") -eq 2 ]] || fail "continuous add must leave two unique exact records"

fixture="$TEST_TMP/two-second-create-fails"
new_fixture "$fixture"
run_fixture "$fixture" "first-ok\n$fixture/root/workspace-one\ncli_firstok123\nfirst-ok-secret\ngpt-5.6-sol\nhigh\ny\ny\nsecond-fails\n$fixture/root/workspace-two\ncli_secondfails456\nsecond-fail-secret\ngpt-5.6-terra\nmedium\ny\n" FAKE_CREATE_FAIL_ON=2
[[ $RUN_STATUS -ne 0 ]] || fail "a second create failure must stop continuous mode"
[[ $(grep -c '^CREATE ' "$fixture/calls.log") -eq 2 ]] || fail "second create failure must make exactly two create attempts"
[[ $(grep -c '^CONTROLLER restart' "$fixture/calls.log") -eq 1 ]] || fail "second create failure must not trigger a second restart"
[[ $(wc -l <"$fixture/apps.tsv") -eq 1 ]] || fail "second create failure must preserve only the first record"

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

for dynamic_path in "$fixture/root/runtime home 中文" "$fixture/root/user data 中文" "$fixture/root/mounted-user" "$fixture/root"; do
  fixture="$TEST_TMP/dynamic-danger-$(printf '%s' "$dynamic_path" | cksum | awk '{print $1}')"
  new_fixture "$fixture"
  case "$dynamic_path" in
    */runtime\ home\ 中文) rejected_path="$fixture/root/runtime home 中文" ;;
    */user\ data\ 中文) rejected_path="$fixture/root/user data 中文" ;;
    */mounted-user) rejected_path="$fixture/root/mounted-user" ;;
    *) rejected_path="$fixture/root" ;;
  esac
  run_fixture "$fixture" "dynamic-danger-app\n$rejected_path\n"
  [[ $RUN_STATUS -ne 0 ]] || fail "dynamic sensitive Workspace $rejected_path must fail"
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

# Executable dotenv content is rejected as data before prompts and never leaks sentinels.
for create_status in 0 19; do
  fixture="$TEST_TMP/env-xtrace-$create_status"
  new_fixture "$fixture"
  printf '%s\n' 'set -x' \
    'CODEX_WORKSPACE_BOT_DB_PASSWORD=dotenv-db-sentinel-90817' \
    'OPENAI_API_KEY=dotenv-provider-sentinel-90817' \
    'CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1=dotenv-crypto-sentinel-90817' >>"$fixture/root/.env"
  secret_value="xtrace-secret-$create_status-90817"
  run_fixture "$fixture" "xtrace-app\n$fixture/root/workspace-one\ncli_xtrace123\n$secret_value\ngpt-5.6-sol\nhigh\ny\nn\n" FAKE_CREATE_STATUS=$create_status
  [[ $RUN_STATUS -ne 0 ]] || fail ".env set -x must be rejected"
  assert_no_create_or_restart "$fixture"
  ! grep -R -Fq -- "$secret_value" "$fixture" || fail "secret leaked while .env enabled xtrace (status $create_status)"
  for sentinel in dotenv-db-sentinel-90817 dotenv-provider-sentinel-90817 dotenv-crypto-sentinel-90817; do
    ! grep -R -Fq -- "$sentinel" "$fixture/stdout" "$fixture/stderr" "$fixture/calls.log" "$fixture/root/runtime" || fail "dotenv sentinel leaked"
  done
done

for payload in 'printf() { :; }' "trap 'printf leaked' DEBUG" 'CODEX_WORKSPACE_BOT_DB_PASSWORD=$(printf leaked)' 'PATH=/attacker/bin' 'HOME=/attacker/home' 'BASH_ENV=/tmp/evil'; do
  fixture="$TEST_TMP/hostile-dotenv-$(printf '%s' "$payload" | cksum | awk '{print $1}')"
  new_fixture "$fixture"
  printf '%s\n' "$payload" >>"$fixture/root/.env"
  run_fixture "$fixture" ''
  [[ $RUN_STATUS -ne 0 ]] || fail "hostile dotenv content must be rejected: $payload"
  assert_no_create_or_restart "$fixture"
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

# A same-size map with the wrong internal receiver ID must not be accepted.
fixture="$TEST_TMP/wrong-receiver-id"
new_fixture "$fixture"
run_fixture "$fixture" "wrong-id-app\n$fixture/root/workspace-one\ncli_wrongid123\nwrong-id-secret\ngpt-5.6-sol\nhigh\ny\n" \
  FAKE_READY_MODE=wrong-same-count CODEX_WORKSPACE_BOT_ADD_READY_ATTEMPTS=2
[[ $RUN_STATUS -ne 0 ]] || fail "wrong same-count receiver IDs must fail activation"
assert_contains "$fixture/stderr" '已登记但未生效'

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
