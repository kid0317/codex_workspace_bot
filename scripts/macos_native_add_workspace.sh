#!/usr/bin/env bash
set +x
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
OS_HOME=${HOME:?HOME is required}
ENV_FILE="$ROOT/.env"
CONFIG_FILE="$ROOT/config.yaml"
APPCTL_MAIN="$ROOT/cmd/appctl/main.go"
RECEIVERCHECK_MAIN="$ROOT/cmd/receivercheck/main.go"
SAFEDOTENV_MAIN="$ROOT/cmd/safedotenv/main.go"
CONTROLLER="$ROOT/macos_bot_controller.sh"
PRIVATE_TOOL_DIR=
APPCTL_BIN=
RECEIVERCHECK_BIN=
SAFEDOTENV_BIN=
APP_LIST=
BASE_URL=
SENSITIVE_DIRS=()
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

cleanup_private_tools() {
  [[ -n ${PRIVATE_TOOL_DIR:-} ]] || return 0
  [[ -z ${APPCTL_BIN:-} ]] || rm -f -- "$APPCTL_BIN"
  [[ -z ${RECEIVERCHECK_BIN:-} ]] || rm -f -- "$RECEIVERCHECK_BIN"
  [[ -z ${SAFEDOTENV_BIN:-} ]] || rm -f -- "$SAFEDOTENV_BIN"
  rmdir -- "$PRIVATE_TOOL_DIR" 2>/dev/null || true
}

build_private_tools() {
  mkdir -p "$ROOT/runtime"
  PRIVATE_TOOL_DIR=$(mktemp -d "$ROOT/runtime/.add-workspace.XXXXXX")
  chmod 0700 "$PRIVATE_TOOL_DIR"
  APPCTL_BIN="$PRIVATE_TOOL_DIR/appctl"
  RECEIVERCHECK_BIN="$PRIVATE_TOOL_DIR/receivercheck"
  SAFEDOTENV_BIN="$PRIVATE_TOOL_DIR/safedotenv"
  trap cleanup_private_tools EXIT
  (
    cd "$ROOT"
    go build -o "$APPCTL_BIN" ./cmd/appctl
    go build -o "$RECEIVERCHECK_BIN" ./cmd/receivercheck
    go build -o "$SAFEDOTENV_BIN" ./cmd/safedotenv
  ) || fail "无法编译私有管理工具；请先执行 go build ./... 查看错误"
  chmod 0700 "$APPCTL_BIN" "$RECEIVERCHECK_BIN" "$SAFEDOTENV_BIN"
}

dotenv_get() {
  "$SAFEDOTENV_BIN" get --file "$ENV_FILE" --key "$1" --allow-missing
}

run_appctl() {
  "$SAFEDOTENV_BIN" exec --file "$ENV_FILE" -- "$APPCTL_BIN" "$@"
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
  [[ -f $RECEIVERCHECK_MAIN ]] || fail "没有找到 cmd/receivercheck；当前安装不完整"
  [[ -f $SAFEDOTENV_MAIN ]] || fail "没有找到 cmd/safedotenv；当前安装不完整"
  [[ -x $CONTROLLER ]] || fail "没有找到可执行的 macos_bot_controller.sh；当前安装不完整"
  command -v go >/dev/null 2>&1 || fail "没有找到 Go；请先修复原生安装"
  command -v curl >/dev/null 2>&1 || fail "没有找到 curl；请先修复原生安装"

  build_private_tools
  "$SAFEDOTENV_BIN" validate --file "$ENV_FILE" || fail ".env 包含不受支持或可能执行的内容；请先修复，脚本没有读取其中的秘密"

  local codex_home user_dir mount_user_dir mount_workspace_dir sensitive
  codex_home=$(dotenv_get CODEX_HOME)
  user_dir=$(dotenv_get USER_DIR)
  mount_user_dir=$(dotenv_get AIPM_MOUNT_USER_DIR)
  mount_workspace_dir=$(dotenv_get AIPM_MOUNT_WORKSPACE_DIR)
  for sensitive in "$codex_home" "$user_dir" "$mount_user_dir" "$mount_workspace_dir"; do
    [[ -n $sensitive && $sensitive == /* ]] && SENSITIVE_DIRS+=("$sensitive")
  done

  if [[ -n $codex_home && -f $codex_home/config.toml ]]; then
    DEFAULT_MODEL=$(awk -F'"' '/^model[[:space:]]*=/ { print $2; exit }' "$codex_home/config.toml")
    DEFAULT_EFFORT=$(awk -F'"' '/^model_reasoning_effort[[:space:]]*=/ { print $2; exit }' "$codex_home/config.toml")
  fi
  [[ $DEFAULT_MODEL =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || DEFAULT_MODEL=gpt-5.6-terra
  case "$DEFAULT_EFFORT" in
    low|medium|high|xhigh|max|ultra) ;;
    *) DEFAULT_EFFORT=high ;;
  esac

  BASE_URL=$("$RECEIVERCHECK_BIN" --config "$CONFIG_FILE" --print-base-url) || fail "config.yaml 的 server.listen_addr 必须是本机 loopback 地址和合法端口"
  if ! APP_LIST=$(
    cd "$ROOT"
    run_appctl list --config ./config.yaml
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
    run_appctl list --config ./config.yaml
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

enabled_receiver_ids() {
  local output id joined=
  output=$(cd "$ROOT" && run_appctl receiver-ids --config ./config.yaml) || return 1
  while IFS= read -r id; do
    [[ -n $id && $id != *,* ]] || return 1
    if [[ -n $joined ]]; then
      joined="$joined,$id"
    else
      joined=$id
    fi
  done <<<"$output"
  [[ -n $joined ]] || return 1
  printf '%s\n' "$joined"
}

strict_receivers_connected() {
  local expected_ids=$1 attempts=${CODEX_WORKSPACE_BOT_ADD_READY_ATTEMPTS:-30}
  local health ready
  [[ $attempts =~ ^[1-9][0-9]*$ ]] || attempts=30
  while (( attempts > 0 )); do
    health=$(curl --fail --silent --show-error --connect-timeout 2 --max-time 5 "$BASE_URL/healthz" 2>/dev/null || true)
    ready=$(curl --fail --silent --show-error --connect-timeout 2 --max-time 5 "$BASE_URL/readyz" 2>/dev/null || true)
    if [[ $health == ok ]] && printf '%s' "$ready" | "$RECEIVERCHECK_BIN" --expected-ids "$expected_ids" >/dev/null 2>&1; then
      return 0
    fi
    attempts=$((attempts - 1))
    (( attempts > 0 )) && sleep 1
  done
  return 1
}

dangerous_workspace() {
  local path=$1 sensitive
  case "$path" in
    /|"$OS_HOME"|"$OS_HOME"/.ssh|"$OS_HOME"/.ssh/*|"$OS_HOME"/.codex|"$OS_HOME"/.codex/*|\
      /System|/System/*|/Library|/Library/*|/Applications|/Applications/*|\
      /usr|/usr/*|/bin|/bin/*|/sbin|/sbin/*|/etc|/etc/*|/var|/var/*|/private|/private/*)
      return 0
      ;;
  esac
  for sensitive in "${SENSITIVE_DIRS[@]}"; do
    case "$path/" in
      "$sensitive/"* ) return 0 ;;
    esac
    case "$sensitive/" in
      "$path/"* ) return 0 ;;
    esac
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
  dangerous_workspace "$workspace_dir" && fail "Workspace 不能使用 HOME、HOME 下的密钥目录、根目录或系统危险目录：$workspace_dir"
  [[ -d $workspace_dir ]] || fail "Workspace 目录不存在：$workspace_dir"
  workspace_dir=$(cd -- "$workspace_dir" && pwd -P)
  dangerous_workspace "$workspace_dir" && fail "Workspace 解析后指向危险目录：$workspace_dir"

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

  create_status=0
  printf '%s' "$app_secret" | (
    cd "$ROOT"
    run_appctl create \
      --config ./config.yaml \
      --name "$app_name" \
      --app-id "$app_id" \
      --secret-stdin \
      --workspace-dir "$workspace_dir" \
      --model "$model" \
      --effort "$effort" \
      --enabled=true
  ) || create_status=$?
  unset app_secret

  if (( create_status != 0 )); then
    printf '添加 Workspace：appctl 登记失败；Bot 没有重启，原有 App 继续运行。\n' >&2
    return "$create_status"
  fi

  if ! refresh_app_list || ! created_app_matches "$app_name" "$app_id" "$workspace_dir"; then
    printf '添加 Workspace：appctl 已报告成功，但 MySQL 回读不一致，登记状态需人工核对；Bot 尚未重启。\n' >&2
    printf '请进入 %s 执行：go run ./cmd/appctl list --config ./config.yaml\n' "$ROOT" >&2
    return 1
  fi
  expected_receivers=$(enabled_receiver_ids) || {
    printf '添加 Workspace：无法回读 enabled App 的 receiver ID，登记状态需人工核对；Bot 尚未重启。\n' >&2
    return 1
  }

  if ! (
    cd "$ROOT"
    ./macos_bot_controller.sh restart
  ); then
    printf '添加 Workspace：已登记但未生效，当前服务状态未知。数据库记录已保留，没有伪装回滚。\n' >&2
    printf '请进入 %s 后依次检查或重试：\n' "$ROOT" >&2
    printf '  ./macos_bot_controller.sh status\n' >&2
    printf '  ./macos_bot_controller.sh logs\n' >&2
    printf '  ./macos_bot_controller.sh restart\n' >&2
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
