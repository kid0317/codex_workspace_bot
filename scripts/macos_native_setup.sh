#!/usr/bin/env bash
set +x
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
ENV_FILE="$ROOT/.env"
CONFIG_FILE="$ROOT/config.yaml"
CONFIG_TEMPLATE="$ROOT/config.yaml.template"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/macos_native_setup.sh          Interactive native macOS setup
  ./scripts/macos_native_setup.sh --check  Preflight only; do not change files or MySQL
  ./scripts/macos_native_setup.sh --help

The interactive setup checks Git, Go, Codex and Homebrew MySQL 8.4, initializes
the local database, writes private runtime settings to .env, and registers one
Feishu App plus its Workspace through appctl. It defaults Homebrew and Go to
mainland-China download mirrors and does not use Docker or Ubuntu.
EOF
}

fail() {
  printf 'macos_native_setup: %s\n' "$*" >&2
  exit 1
}

require_macos() {
  [[ $(uname -s) == Darwin ]] || fail "this setup only supports macOS"
}

configure_mainland_network_defaults() {
  # Homebrew may otherwise run `brew update` before install and wait on GitHub.
  # Keep user-supplied mirror choices, but make the zero-config path work on a
  # mainland-China network.
  export HOMEBREW_NO_AUTO_UPDATE=1
  export HOMEBREW_NO_ENV_HINTS=1
  export HOMEBREW_API_DOMAIN="${HOMEBREW_API_DOMAIN:-https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles/api}"
  export HOMEBREW_BOTTLE_DOMAIN="${HOMEBREW_BOTTLE_DOMAIN:-https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles}"
  export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
}

ask_yes_no() {
  local prompt=$1 default=${2:-y} answer
  if [[ $default == y ]]; then
    read -r -p "$prompt [Y/n] " answer
    answer=${answer:-y}
  else
    read -r -p "$prompt [y/N] " answer
    answer=${answer:-n}
  fi
  [[ $answer == y || $answer == Y || $answer == yes || $answer == YES ]]
}

expand_home() {
  case "$1" in
    "~") printf '%s\n' "$HOME" ;;
    "~/"*) printf '%s/%s\n' "$HOME" "${1#~/}" ;;
    "~"*) fail "不支持 ~其他用户 形式，请输入完整路径" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

canonical_dir() {
  local path
  path=$(expand_home "$1")
  [[ -d $path ]] || fail "目录不存在：$path"
  (cd -- "$path" && pwd -P)
}

mysql84_installed_locally() {
  [[ -x /opt/homebrew/opt/mysql@8.4/bin/mysql ]] ||
    [[ -x /usr/local/opt/mysql@8.4/bin/mysql ]]
}

command_summary() {
  local failed=0
  printf 'macOS: %s\n' "$(sw_vers -productVersion 2>/dev/null || true)"
  for command_name in git go codex brew openssl curl; do
    if command -v "$command_name" >/dev/null 2>&1; then
      printf '  [OK] %s\n' "$command_name"
    else
      printf '  [缺少] %s\n' "$command_name"
      failed=1
    fi
  done
  # Keep --check fully local and bounded. Homebrew may auto-update or wait on
  # network state even for `brew list`, so only inspect its standard symlinks.
  if mysql84_installed_locally; then
    printf '  [OK] mysql@8.4\n'
  else
    printf '  [未发现] mysql@8.4（初始化时再由 Homebrew 确认）\n'
    failed=1
  fi
  return "$failed"
}

upsert_env() {
  local key=$1 value=$2 temporary quoted
  temporary=$(mktemp "$ROOT/.env.XXXXXX")
  if [[ -f $ENV_FILE ]]; then
    awk -v prefix="$key=" 'index($0, prefix) != 1 { print }' "$ENV_FILE" >"$temporary"
  fi
  printf -v quoted '%q' "$value"
  printf '%s=%s\n' "$key" "$quoted" >>"$temporary"
  chmod 0600 "$temporary"
  mv -f "$temporary" "$ENV_FILE"
}

random_hex() {
  openssl rand -hex "$1"
}

random_base64() {
  openssl rand -base64 "$1" | tr -d '\n'
}

ensure_dependencies() {
  command -v brew >/dev/null 2>&1 || fail "请先从 https://brew.sh/ 安装 Homebrew 官方 .pkg，再重新运行本脚本"

  local packages=()
  command -v git >/dev/null 2>&1 || packages+=(git)
  command -v go >/dev/null 2>&1 || packages+=(go)
  brew list --versions mysql@8.4 >/dev/null 2>&1 || packages+=(mysql@8.4)
  if (( ${#packages[@]} > 0 )); then
    printf '需要通过 Homebrew 安装：%s\n' "${packages[*]}"
    ask_yes_no "现在安装这些依赖吗？" y || fail "依赖未安装，初始化已停止"
    brew install "${packages[@]}"
  fi
  command -v codex >/dev/null 2>&1 || fail "没有找到 Codex；请先完成教程第 1 步"
}

mysql_paths() {
  local prefix
  prefix=$(brew --prefix mysql@8.4)
  MYSQL_BIN="$prefix/bin/mysql"
  MYSQLADMIN_BIN="$prefix/bin/mysqladmin"
  [[ -x $MYSQL_BIN && -x $MYSQLADMIN_BIN ]] || fail "没有找到 mysql@8.4 客户端"
}

start_mysql() {
  brew services start mysql@8.4 >/dev/null
  local attempts=30
  while (( attempts > 0 )); do
    if "$MYSQLADMIN_BIN" --protocol=socket -u root ping >/dev/null 2>&1; then
      printf 'MySQL 已启动。\n'
      return 0
    fi
    sleep 1
    attempts=$((attempts - 1))
  done
  fail "MySQL 30 秒内没有就绪；请执行 brew services info mysql@8.4 查看原因"
}

mysql_root_args() {
  MYSQL_AUTH_ARGS=(--protocol=socket -u root)
  if "$MYSQL_BIN" "${MYSQL_AUTH_ARGS[@]}" -e 'SELECT 1' >/dev/null 2>&1; then
    return 0
  fi

  local root_password escaped
  printf '请输入你为本机 MySQL root 设置的密码：'
  read -r -s root_password
  printf '\n'
  [[ -n $root_password ]] || fail "MySQL root 密码为空且免密登录失败"
  MYSQL_AUTH_FILE=$(mktemp "$ROOT/.mysql-auth.XXXXXX")
  chmod 0600 "$MYSQL_AUTH_FILE"
  escaped=${root_password//\\/\\\\}
  escaped=${escaped//\"/\\\"}
  {
    printf '[client]\n'
    printf 'user=root\n'
    printf 'password="%s"\n' "$escaped"
  } >"$MYSQL_AUTH_FILE"
  unset root_password escaped
  MYSQL_AUTH_ARGS=(--defaults-extra-file="$MYSQL_AUTH_FILE" --protocol=socket)
  "$MYSQL_BIN" "${MYSQL_AUTH_ARGS[@]}" -e 'SELECT 1' >/dev/null 2>&1 || fail "MySQL root 登录失败"
}

initialize_database() {
  local db_password=$1
  [[ $db_password =~ ^[A-Za-z0-9._+/=-]+$ ]] || fail "数据库密码包含初始化器不支持的字符，请删除 .env 后重新生成"
  "$MYSQL_BIN" "${MYSQL_AUTH_ARGS[@]}" <<SQL
CREATE DATABASE IF NOT EXISTS codex_workspace_bot CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS 'codex_workspace_bot'@'localhost' IDENTIFIED BY '$db_password';
CREATE USER IF NOT EXISTS 'codex_workspace_bot'@'127.0.0.1' IDENTIFIED BY '$db_password';
ALTER USER 'codex_workspace_bot'@'localhost' IDENTIFIED BY '$db_password';
ALTER USER 'codex_workspace_bot'@'127.0.0.1' IDENTIFIED BY '$db_password';
GRANT ALL PRIVILEGES ON codex_workspace_bot.* TO 'codex_workspace_bot'@'localhost';
GRANT ALL PRIVILEGES ON codex_workspace_bot.* TO 'codex_workspace_bot'@'127.0.0.1';
FLUSH PRIVILEGES;
SQL
  printf '数据库和本机应用账号已就绪。\n'
}

existing_or_random() {
  local name=$1 kind=$2 current=
  if [[ -f $ENV_FILE ]]; then
    set -a
    . "$ENV_FILE"
    set +x
    set +a
    eval "current=\${$name:-}"
  fi
  if [[ -n $current ]]; then
    printf '%s' "$current"
  elif [[ $kind == hex ]]; then
    random_hex 24
  else
    random_base64 32
  fi
}

write_runtime_environment() {
  local workspace_dir=$1 user_dir=$2 runtime_home=$3 db_password=$4
  upsert_env CODEX_WORKSPACE_BOT_DB_PASSWORD "$db_password"
  upsert_env CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1 "$(existing_or_random CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1 base64)"
  upsert_env CODEX_WORKSPACE_BOT_ACTION_RESULT_KEY_V1 "$(existing_or_random CODEX_WORKSPACE_BOT_ACTION_RESULT_KEY_V1 base64)"
  upsert_env CODEX_WORKSPACE_BOT_SCHEDULE_PAYLOAD_KEY_V1 "$(existing_or_random CODEX_WORKSPACE_BOT_SCHEDULE_PAYLOAD_KEY_V1 base64)"
  upsert_env CODEX_WORKSPACE_BOT_SCHEDULE_OWNER_HMAC_KEY_V1 "$(existing_or_random CODEX_WORKSPACE_BOT_SCHEDULE_OWNER_HMAC_KEY_V1 base64)"
  upsert_env CODEX_HOME "$runtime_home"
  upsert_env USER_DIR "$user_dir"
  upsert_env AIPM_MOUNT_WORKSPACE_DIR "$workspace_dir"
  upsert_env AIPM_MOUNT_USER_DIR "$user_dir"
  upsert_env AIPM_STATE use
  upsert_env SANDBOX_STATE use

  if [[ -n ${OPENAI_API_KEY:-} ]] && ask_yes_no "检测到当前终端已有 OPENAI_API_KEY，是否安全写入 Bot 的 .env？" y; then
    upsert_env OPENAI_API_KEY "$OPENAI_API_KEY"
  elif grep -Eq 'env_key[[:space:]]*=[[:space:]]*"OPENAI_API_KEY"' "$runtime_home/config.toml" && ! grep -q '^OPENAI_API_KEY=' "$ENV_FILE"; then
    local model_key
    printf '当前模型配置需要 OPENAI_API_KEY。请粘贴 Key：'
    read -r -s model_key
    printf '\n'
    [[ -n $model_key ]] || fail "模型 Key 不能为空"
    upsert_env OPENAI_API_KEY "$model_key"
    unset model_key
  fi
}

register_app() {
  local workspace_dir=$1 runtime_home=$2 app_name app_id app_secret model effort
  read -r -p 'Bot 名称 [aipm-assistant]：' app_name
  app_name=${app_name:-aipm-assistant}
  [[ $app_name =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || fail "Bot 名称只能使用字母、数字、点、下划线和短横线"

  read -r -p '飞书 App ID（cli_ 开头）：' app_id
  [[ $app_id == cli_* ]] || fail "飞书 App ID 应以 cli_ 开头"
  printf '飞书 App Secret（输入时不显示）：'
  read -r -s app_secret
  printf '\n'
  [[ -n $app_secret ]] || fail "飞书 App Secret 不能为空"

  model=$(awk -F'"' '/^model[[:space:]]*=/ {print $2; exit}' "$runtime_home/config.toml")
  model=${model:-gpt-5.6-terra}
  read -r -p "模型名称 [$model]：" chosen_model
  model=${chosen_model:-$model}

  effort=$(awk -F'"' '/^model_reasoning_effort[[:space:]]*=/ {print $2; exit}' "$runtime_home/config.toml")
  effort=${effort:-high}
  read -r -p "推理强度 [$effort]：" chosen_effort
  effort=${chosen_effort:-$effort}

  set -a
  . "$ENV_FILE"
  set +x
  set +a
  export AIPM_FEISHU_APP_SECRET="$app_secret"
  (
    cd "$ROOT"
    go run ./cmd/appctl upsert \
      --config ./config.yaml \
      --name "$app_name" \
      --app-id "$app_id" \
      --secret-env AIPM_FEISHU_APP_SECRET \
      --workspace-dir "$workspace_dir" \
      --model "$model" \
      --effort "$effort" \
      --enabled=true
  )
  unset AIPM_FEISHU_APP_SECRET app_secret
  printf '飞书 App 与 Workspace 已写入本机 MySQL。\n'
}

preflight_only() {
  require_macos
  command_summary || true
  [[ -f $ENV_FILE ]] && printf '  [OK] .env\n' || printf '  [未初始化] .env\n'
  [[ -f $CONFIG_FILE ]] && printf '  [OK] config.yaml\n' || printf '  [未初始化] config.yaml\n'
  exit 0
}

case "${1:-}" in
  --help|-h)
    usage
    exit 0
    ;;
  --check)
    preflight_only
    ;;
  "")
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

require_macos
configure_mainland_network_defaults
printf '\n=== Codex Workspace Bot：macOS 原生初始化 ===\n'
printf '已启用国内网络默认配置：Homebrew 清华镜像（跳过自动更新），Go goproxy.cn。\n'
ensure_dependencies
mysql_paths
start_mysql

[[ -f $CONFIG_FILE ]] || cp "$CONFIG_TEMPLATE" "$CONFIG_FILE"
chmod 0600 "$CONFIG_FILE"

default_package="$HOME/Documents/aipm-assistant"
read -r -p "AI PM 运行包目录 [$default_package]：" package_root
package_root=${package_root:-$default_package}
package_root=$(canonical_dir "$package_root")
workspace_dir=$(canonical_dir "$package_root/workspace")
user_dir=$(canonical_dir "$package_root/user")
runtime_home=$(canonical_dir "$user_dir/.codex-runtime/home")
[[ -f $runtime_home/config.toml ]] || fail "没有找到 $runtime_home/config.toml，请先完成教程第 3 步"

db_password=$(existing_or_random CODEX_WORKSPACE_BOT_DB_PASSWORD hex)
MYSQL_AUTH_FILE=
trap '[[ -n ${MYSQL_AUTH_FILE:-} ]] && rm -f "$MYSQL_AUTH_FILE"' EXIT
mysql_root_args
initialize_database "$db_password"
write_runtime_environment "$workspace_dir" "$user_dir" "$runtime_home" "$db_password"
unset db_password

register_app "$workspace_dir" "$runtime_home"

(
  cd "$ROOT"
  ./macos_bot_controller.sh build
)

if ask_yes_no "现在启动 Bot 并检查飞书长连接吗？" y; then
  (
    cd "$ROOT"
    ./macos_bot_controller.sh start
    ./macos_bot_controller.sh status
  )
fi

printf '\n初始化完成。以后进入 %s 后使用：\n' "$ROOT"
printf '  ./macos_bot_controller.sh status\n'
printf '  ./macos_bot_controller.sh restart\n'
printf '  ./macos_bot_controller.sh logs\n'
