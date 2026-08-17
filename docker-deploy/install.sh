#!/usr/bin/env bash
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
image_default="crpi-0c1kby082wk3ovcx.cn-hangzhou.personal.cr.aliyuncs.com/codex-workspace/codex-workspace-bot@sha256:5269d0fdfb0c5c061e20cfa402d67fe4910e38c9d5b2fc43952eb912fe2b4e1e"

command -v docker >/dev/null 2>&1 || { echo "没有找到 Docker。请先安装并打开 Docker Desktop。" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker 没有启动。请先打开 Docker Desktop，等鲸鱼图标稳定后再试。" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "需要 Docker Compose v2。" >&2; exit 1; }

default_path="$PWD/codex-space"
read -r -p "把 Space 安装到哪里？[${default_path}] " install_path
install_path="${install_path:-$default_path}"
mkdir -p "$install_path"
if find "$install_path" -mindepth 1 -maxdepth 1 | grep -q .; then
  echo "目标文件夹不是空的。为避免覆盖文件，安装已停止。" >&2
  exit 1
fi

echo "请选择模型服务："
echo "1. 阿里百炼 Responses"
echo "2. DeepSeek Responses"
read -r -p "请输入 1 或 2: " provider_choice
case "$provider_choice" in
  1) provider_kind="bailian-responses"; default_base="https://dashscope.aliyuncs.com/compatible-mode/v1"; default_model="qwen3.7-max";;
  2) provider_kind="deepseek-responses"; default_base="https://api.deepseek.com"; default_model="deepseek-chat";;
  *) echo "只能输入 1 或 2。" >&2; exit 2;;
esac
read -r -p "Base URL [${default_base}]: " provider_base
provider_base="${provider_base:-$default_base}"
[[ "$provider_base" =~ ^https:// ]] || { echo "Base URL 必须以 https:// 开头。" >&2; exit 2; }
read -r -p "模型名称 [${default_model}]: " model
model="${model:-$default_model}"
[[ "$model" =~ ^[A-Za-z0-9._:/-]+$ ]] || { echo "模型名称格式不正确。" >&2; exit 2; }
read -r -s -p "API Key（输入时不会显示）: " provider_key
echo
[[ -n "$provider_key" && "$provider_key" != *$'\n'* && "$provider_key" != *$'\r'* ]] || { echo "API Key 不能为空或包含换行。" >&2; exit 2; }

read -r -p "Bot 本机端口 [8080]: " bot_port
bot_port="${bot_port:-8080}"
[[ "$bot_port" =~ ^[0-9]+$ ]] && ((bot_port >= 1024 && bot_port <= 65535)) || { echo "端口必须是 1024 到 65535 的数字。" >&2; exit 2; }

random_hex() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -hex 32; else od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; fi
}
random_base64_32() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32 | tr -d '\n'
  else
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n' | xxd -r -p | base64 | tr -d '\n'
  fi
}
space_id="space-$(random_hex | cut -c1-16)"
project_name="codex-space-$(printf '%s' "$space_id" | tail -c 9)"
db_root_password="$(random_hex)"
db_password="$(random_hex)"
attachment_key="$(random_base64_32)"
action_key="$(random_base64_32)"

staging="${install_path}.staging.$$"
cleanup() { rm -rf "$staging"; }
trap cleanup EXIT INT TERM
mkdir -p "$staging"
cp -R "$deploy_dir/templates/." "$staging/"
mkdir -p "$staging/.secrets" "$staging/apps" "$staging/attachments" "$staging/logs" \
  "$staging/system/backups" "$staging/system/codex-home" "$staging/system/home" "$staging/system/bot-home"
cp "$deploy_dir/release/release-manifest.json" "$staging/system/release-manifest.json"

umask 077
{
  echo "MYSQL_ROOT_PASSWORD=$db_root_password"
  echo "MYSQL_DATABASE=codex_workspace_bot"
  echo "MYSQL_USER=codex_workspace_bot"
  echo "MYSQL_PASSWORD=$db_password"
} > "$staging/.secrets/mysql.env"
{
  echo "CODEX_WORKSPACE_BOT_DB_PASSWORD=$db_password"
  echo "CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1=$attachment_key"
  echo "CODEX_WORKSPACE_BOT_ACTION_RESULT_KEY_V1=$action_key"
} > "$staging/.secrets/bot.env"
{
  echo "PROVIDER_UPSTREAM_BASE_URL=$provider_base"
  echo "PROVIDER_API_KEY=$provider_key"
  echo "PROVIDER_PROXY_LISTEN=0.0.0.0:8090"
} > "$staging/.secrets/provider.env"
unset provider_key db_root_password db_password attachment_key action_key

local_uid="$(id -u)"
local_gid="$(id -g)"
cat > "$staging/.env" <<EOF
COMPOSE_PROJECT_NAME=$project_name
SPACE_ID=$space_id
BOT_IMAGE=$image_default
MYSQL_IMAGE=mysql:8.4
BOT_HOST_PORT=$bot_port
LOCAL_UID=$local_uid
LOCAL_GID=$local_gid
DEFAULT_MODEL=$model
PROVIDER_KIND=$provider_kind
EOF
sed "s|__MODEL__|$model|g" "$staging/system/codex-home/config.toml" > "$staging/system/codex-home/config.toml.new"
mv "$staging/system/codex-home/config.toml.new" "$staging/system/codex-home/config.toml"
cat > "$staging/space.lock.json" <<EOF
{
  "schema_version": 1,
  "space_id": "$space_id",
  "version": "0.1.0",
  "image_digest": "sha256:5269d0fdfb0c5c061e20cfa402d67fe4910e38c9d5b2fc43952eb912fe2b4e1e",
  "provider_kind": "$provider_kind"
}
EOF
cat > "$staging/.gitignore" <<'EOF'
.env
.secrets/
system/backups/
logs/
attachments/
EOF
chmod 600 "$staging/.env" "$staging/.secrets/"*.env "$staging/space.lock.json" "$staging/system/codex-home/config.toml"
chmod +x "$staging/"*.sh

(cd "$staging" && docker compose config --quiet)
if ! (cd "$staging" && docker compose pull); then
  echo "镜像下载失败。这个 Public 仓库已经通过匿名拉取验证，不需要维护者密码。" >&2
  echo "请先确认 Docker Desktop 正在运行，并检查网络后重试。" >&2
  echo "ACR 个人版使用共享带宽，高峰期可能限速；请稍后重试，不要使用或索要维护者凭据。" >&2
  exit 1
fi
cp -R "$staging/." "$install_path/"
cleanup
trap - EXIT INT TERM

echo "Space 已安装到：$install_path"
read -r -p "现在添加第一个 Workspace 吗？[Y/n] " add_workspace
if [[ ! "$add_workspace" =~ ^[Nn]$ ]]; then
  "$install_path/manage.sh"
fi
read -r -p "现在启动服务吗？[Y/n] " start_now
if [[ ! "$start_now" =~ ^[Nn]$ ]]; then
  "$install_path/start.sh"
else
  echo "以后进入该文件夹，运行 ./start.sh 即可启动。"
fi
