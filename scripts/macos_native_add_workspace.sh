#!/usr/bin/env bash
set +x
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
ENV_FILE="$ROOT/.env"
CONFIG_FILE="$ROOT/config.yaml"
APPCTL_MAIN="$ROOT/cmd/appctl/main.go"
CONTROLLER="$ROOT/macos_bot_controller.sh"
APP_LIST=
CANCEL_STATUS=20
DEFAULT_MODEL=gpt-5.6-terra
DEFAULT_EFFORT=high

usage() {
  cat <<'EOF'
用法：
  ./scripts/macos_native_add_workspace.sh
  ./scripts/macos_native_add_workspace.sh --help

给已经完成原生初始化的 macOS Codex Workspace Bot 继续添加飞书 App 和
Workspace。脚本会先检查现有安装和 MySQL，再逐项询问；不会安装依赖或初始化数据库。
EOF
}

fail() {
  printf '添加 Workspace：%s\n' "$*" >&2
  exit 1
}

ask_yes_no() {
  local prompt=$1 default=${2:-n} answer
  if [[ $default == y ]]; then
    read -r -p "$prompt [Y/n] " answer || fail "输入已结束"
    answer=${answer:-y}
  else
    read -r -p "$prompt [y/N] " answer || fail "输入已结束"
    answer=${answer:-n}
  fi
  case "$answer" in
    y|Y|yes|YES) return 0 ;;
    n|N|no|NO) return 1 ;;
    *) fail "请输入 y 或 n" ;;
  esac
}

require_existing_install() {
  [[ $(uname -s) == Darwin ]] || fail "这个入口只用于 macOS 原生安装"
  [[ -f $ENV_FILE ]] || fail "没有找到 .env；请先运行 ./scripts/macos_native_setup.sh 完成原生初始化"
  [[ -f $CONFIG_FILE ]] || fail "没有找到 config.yaml；请先完成 macOS 原生初始化"
  [[ -f $APPCTL_MAIN ]] || fail "没有找到 cmd/appctl；当前安装不完整"
  [[ -x $CONTROLLER ]] || fail "没有找到可执行的 macos_bot_controller.sh；当前安装不完整"
  command -v go >/dev/null 2>&1 || fail "没有找到 Go；请先修复原生安装"
  command -v curl >/dev/null 2>&1 || fail "没有找到 curl；请先修复原生安装"

  set -a
  # .env is the existing native install's private runtime environment.
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a

  if [[ -n ${CODEX_HOME:-} && -f $CODEX_HOME/config.toml ]]; then
    DEFAULT_MODEL=$(awk -F'"' '/^model[[:space:]]*=/ { print $2; exit }' "$CODEX_HOME/config.toml")
    DEFAULT_EFFORT=$(awk -F'"' '/^model_reasoning_effort[[:space:]]*=/ { print $2; exit }' "$CODEX_HOME/config.toml")
  fi
  [[ $DEFAULT_MODEL =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || DEFAULT_MODEL=gpt-5.6-terra
  case "$DEFAULT_EFFORT" in
    low|medium|high|xhigh|max|ultra) ;;
    *) DEFAULT_EFFORT=high ;;
  esac

  if ! APP_LIST=$(
    cd "$ROOT"
    go run ./cmd/appctl list --config ./config.yaml
  ); then
    fail "无法读取 MySQL 中的 App 列表；请先确认 MySQL 已启动、.env 和 config.yaml 正确"
  fi
  printf '现有 macOS 原生安装和 MySQL 检查通过。\n'
}

name_exists() {
  local wanted=$1 existing_name existing_id ignored wanted_folded existing_folded
  wanted_folded=$(printf '%s' "$wanted" | tr '[:upper:]' '[:lower:]')
  while IFS=$'\t' read -r existing_name existing_id ignored; do
    existing_folded=$(printf '%s' "$existing_name" | tr '[:upper:]' '[:lower:]')
    [[ $existing_folded == "$wanted_folded" ]] && return 0
  done <<<"$APP_LIST"
  return 1
}

app_id_exists() {
  local wanted=$1 existing_name existing_id ignored wanted_folded existing_folded
  wanted_folded=$(printf '%s' "$wanted" | tr '[:upper:]' '[:lower:]')
  while IFS=$'\t' read -r existing_name existing_id ignored; do
    existing_folded=$(printf '%s' "$existing_id" | tr '[:upper:]' '[:lower:]')
    [[ $existing_folded == "$wanted_folded" ]] && return 0
  done <<<"$APP_LIST"
  return 1
}

refresh_app_list() {
  local refreshed
  if ! refreshed=$(
    cd "$ROOT"
    go run ./cmd/appctl list --config ./config.yaml
  ); then
    return 1
  fi
  APP_LIST=$refreshed
}

created_app_matches() {
  local wanted_name=$1 wanted_id=$2 wanted_workspace=$3 existing_name existing_id existing_workspace existing_enabled
  while IFS=$'\t' read -r existing_name existing_id existing_workspace existing_enabled; do
    if [[ $existing_name == "$wanted_name" && $existing_id == "$wanted_id" && \
      $existing_workspace == "$wanted_workspace" && $existing_enabled == true ]]; then
      return 0
    fi
  done <<<"$APP_LIST"
  return 1
}

enabled_app_count() {
  local existing_name existing_id existing_workspace existing_enabled count=0
  while IFS=$'\t' read -r existing_name existing_id existing_workspace existing_enabled; do
    [[ $existing_enabled == true ]] && count=$((count + 1))
  done <<<"$APP_LIST"
  printf '%s\n' "$count"
}

strict_receivers_connected() {
  local expected_count=$1 attempts=${CODEX_WORKSPACE_BOT_ADD_READY_ATTEMPTS:-30}
  local health ready states state_count connected_count
  [[ $attempts =~ ^[1-9][0-9]*$ ]] || attempts=30
  while (( attempts > 0 )); do
    health=$(curl --fail --silent --show-error http://127.0.0.1:8080/healthz 2>/dev/null || true)
    ready=$(curl --fail --silent --show-error http://127.0.0.1:8080/readyz 2>/dev/null || true)
    states=$(printf '%s\n' "$ready" | grep -Eo '"state"[[:space:]]*:[[:space:]]*"[^"]+"' || true)
    state_count=$(printf '%s\n' "$states" | awk 'NF { count++ } END { print count+0 }')
    connected_count=$(printf '%s\n' "$states" | grep -Ec '"connected"$' || true)
    if [[ $health == ok && $ready == *'"receivers"'* && \
      $state_count -eq $expected_count && $connected_count -eq $expected_count ]]; then
      return 0
    fi
    attempts=$((attempts - 1))
    (( attempts > 0 )) && sleep 1
  done
  return 1
}

add_one_workspace() {
  local app_name workspace_dir app_id app_secret model effort create_status expected_receivers

  read -r -p '唯一 App 名称：' app_name || fail "输入已结束"
  [[ $app_name =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || fail "App 名称只能使用字母、数字、点、下划线和短横线，最长 64 个字符"
  name_exists "$app_name" && fail "App 名称已经存在：$app_name；没有写入或覆盖任何配置"

  read -r -p 'Workspace 绝对目录：' workspace_dir || fail "输入已结束"
  [[ $workspace_dir == /* ]] || fail "Workspace 必须填写绝对路径"
  if LC_ALL=C printf '%s' "$workspace_dir" | grep -q '[[:cntrl:]]'; then
    fail "Workspace 路径不能包含 tab、回车或其他控制字符"
  fi
  [[ -d $workspace_dir ]] || fail "Workspace 目录不存在：$workspace_dir"
  workspace_dir=$(cd -- "$workspace_dir" && pwd -P)

  read -r -p '飞书 App ID（cli_ 开头）：' app_id || fail "输入已结束"
  [[ $app_id =~ ^cli_[A-Za-z0-9]+$ ]] || fail "飞书 App ID 格式不正确，应为 cli_ 开头并只包含字母和数字"
  app_id_exists "$app_id" && fail "飞书 App ID 已经存在：$app_id；一个飞书 App ID 只能绑定一个 Workspace"

  printf '飞书 App Secret（输入时不显示）：'
  read -r -s app_secret || fail "输入已结束"
  printf '\n'
  [[ -n $app_secret ]] || fail "飞书 App Secret 不能为空"

  read -r -p "模型名称 [$DEFAULT_MODEL]：" model || fail "输入已结束"
  model=${model:-$DEFAULT_MODEL}
  [[ $model =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || fail "模型名称格式不正确"

  read -r -p "Reasoning effort [$DEFAULT_EFFORT]：" effort || fail "输入已结束"
  effort=${effort:-$DEFAULT_EFFORT}
  case "$effort" in
    low|medium|high|xhigh|max|ultra) ;;
    *) fail "Reasoning effort 请选择 low、medium、high、xhigh、max 或 ultra" ;;
  esac

  printf '\n请确认：\n'
  printf '  App 名称：%s\n' "$app_name"
  printf '  Workspace：%s\n' "$workspace_dir"
  printf '  App ID：%s\n' "$app_id"
  printf '  模型 / effort：%s / %s\n' "$model" "$effort"
  printf '  启用：是（新增后立即启用）\n'
  if ! ask_yes_no "确认登记吗？" n; then
    unset app_secret
    printf '已取消，没有写入 MySQL，也没有重启 Bot。\n'
    return "$CANCEL_STATUS"
  fi

  export CODEX_WORKSPACE_BOT_NEW_APP_SECRET="$app_secret"
  unset app_secret
  create_status=0
  (
    cd "$ROOT"
    go run ./cmd/appctl create \
      --config ./config.yaml \
      --name "$app_name" \
      --app-id "$app_id" \
      --secret-env CODEX_WORKSPACE_BOT_NEW_APP_SECRET \
      --workspace-dir "$workspace_dir" \
      --model "$model" \
      --effort "$effort" \
      --enabled=true
  ) || create_status=$?
  unset CODEX_WORKSPACE_BOT_NEW_APP_SECRET

  if (( create_status != 0 )); then
    printf '添加 Workspace：appctl 登记失败；Bot 没有重启，原有 App 继续运行。\n' >&2
    return "$create_status"
  fi

  if ! refresh_app_list || ! created_app_matches "$app_name" "$app_id" "$workspace_dir"; then
    printf '添加 Workspace：appctl 已报告成功，但 MySQL 回读不一致，登记状态需人工核对；Bot 尚未重启。\n' >&2
    printf '请进入 %s 执行：go run ./cmd/appctl list --config ./config.yaml\n' "$ROOT" >&2
    return 1
  fi
  expected_receivers=$(enabled_app_count)

  if ! (
    cd "$ROOT"
    ./macos_bot_controller.sh restart
  ); then
    printf '添加 Workspace：已登记但未生效。数据库记录已保留，没有伪装回滚。\n' >&2
    printf '请排除问题后进入 %s 并执行：./macos_bot_controller.sh restart\n' "$ROOT" >&2
    return 1
  fi

  if ! strict_receivers_connected "$expected_receivers"; then
    printf '添加 Workspace：已登记但未生效。Bot 已重启，但 receiver 尚未全部 connected；数据库记录已保留。\n' >&2
    printf '请进入 %s 后依次检查或重试：\n' "$ROOT" >&2
    printf '  ./macos_bot_controller.sh status\n' >&2
    printf '  ./macos_bot_controller.sh logs\n' >&2
    printf '  ./macos_bot_controller.sh restart\n' >&2
    return 1
  fi

  printf 'App“%s”已经登记并生效。\n' "$app_name"
  return 0
}

case "${1:-}" in
  --help|-h)
    usage
    exit 0
    ;;
  "") ;;
  *)
    usage >&2
    exit 2
    ;;
esac

require_existing_install
printf '\n=== 添加飞书 App 与 Workspace ===\n'
while true; do
  set +e
  add_one_workspace
  add_status=$?
  set -e
  if (( add_status == CANCEL_STATUS )); then
    exit 0
  elif (( add_status != 0 )); then
    exit "$add_status"
  fi
  ask_yes_no "是否继续添加另一个 Workspace？" n || break
  printf '\n'
done

printf '全部完成。以后修改配置后仍请使用 ./macos_bot_controller.sh restart。\n'
