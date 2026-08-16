#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space

read -r -p "Workspace 名称（英文、数字、短横线）: " app_name
[[ "$app_name" =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]] || { echo "名称格式不对。" >&2; exit 2; }
read -r -p "Workspace 文件夹完整路径: " source_dir
test -d "$source_dir" || { echo "找不到这个文件夹。" >&2; exit 2; }
read -r -p "飞书 App ID: " feishu_app_id
read -r -s -p "飞书 App Secret（输入时不会显示）: " feishu_secret
echo
[[ "$feishu_secret" != *$'\n'* && "$feishu_secret" != *$'\r'* && -n "$feishu_secret" ]] || { echo "Secret 不能为空或包含换行。" >&2; exit 2; }
default_model="$(sed -n 's/^DEFAULT_MODEL=//p' "$space_root/.env")"
read -r -p "模型 [${default_model}]: " model
model="${model:-$default_model}"
[[ "$model" =~ ^[A-Za-z0-9._:/-]+$ ]] || { echo "模型名称格式不对。" >&2; exit 2; }

target="$space_root/apps/$app_name/workspace"
if test -e "$target"; then
  read -r -p "同名 Workspace 已存在，覆盖运行副本吗？[y/N] " answer
  [[ "$answer" =~ ^[Yy]$ ]] || exit 0
fi
mkdir -p "$target"
cp -a "$source_dir/." "$target/"

secret_file="$space_root/.secrets/.bootstrap-${app_name}-$$"
cleanup() { rm -f "$secret_file"; }
trap cleanup EXIT INT TERM
umask 077
printf '%s\n' "$feishu_secret" > "$secret_file"
unset feishu_secret

compose up -d mysql
compose run --rm --no-deps \
  -v "$secret_file:/run/secrets/feishu:ro" \
  bot /usr/local/bin/secure-appctl \
  --config /space/config/bot.yaml \
  --name "$app_name" \
  --app-id "$feishu_app_id" \
  --secret-file /run/secrets/feishu \
  --workspace-dir "/space/apps/$app_name/workspace" \
  --model "$model" \
  --effort high
cleanup
trap - EXIT INT TERM
echo "Workspace 已加入。运行 ./start.sh 即可启动全部服务。"

