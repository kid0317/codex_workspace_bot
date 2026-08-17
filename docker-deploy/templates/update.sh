#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space
umask 077

manifest_source="${RELEASE_MANIFEST_URL:-}"
check_only=false
while (($#)); do
  case "$1" in
    --manifest) manifest_source="${2:?--manifest 后需要路径或 URL}"; shift 2;;
    --check) check_only=true; shift;;
    *) echo "用法：./update.sh [--check] [--manifest 路径或URL]" >&2; exit 2;;
  esac
done
test -n "$manifest_source" || { echo "请用 --manifest 指定 release-manifest.json。" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "更新需要 jq。macOS 可先运行 brew install jq。" >&2; exit 1; }

lock_dir="$space_root/system/update.lock"
mkdir "$lock_dir" 2>/dev/null || { echo "另一个安装或更新正在运行。" >&2; exit 1; }
tmp_dir="$(mktemp -d)"
cleanup() { rm -rf "$tmp_dir"; rmdir "$lock_dir" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

manifest="$tmp_dir/release-manifest.json"
if [[ "$manifest_source" =~ ^https?:// ]]; then
  curl --fail --location --silent --show-error "$manifest_source" -o "$manifest"
  curl --fail --location --silent --show-error "${manifest_source}.sha256" -o "$manifest.sha256"
else
  cp "$manifest_source" "$manifest"
  checksum_source="${manifest_source}.sha256"
  test -f "$checksum_source" || { echo "缺少校验文件：$checksum_source" >&2; exit 1; }
  cp "$checksum_source" "$manifest.sha256"
fi
expected="$(awk '{print $1}' "$manifest.sha256")"
actual="$(sha256_file "$manifest")"
test "$expected" = "$actual" || { echo "发行清单校验失败。" >&2; exit 1; }

version="$(jq -er '.version' "$manifest")"
image="$(jq -er '.image.repository' "$manifest")"
digest="$(jq -er '.image.digest | select(startswith("sha256:"))' "$manifest")"
min_installer="$(jq -er '.minimum_installer_version' "$manifest")"
echo "可用版本：$version"
echo "镜像摘要：$digest"
echo "最低安装器版本：$min_installer"
$check_only && exit 0

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="$space_root/system/backups/$timestamp"
mkdir -p "$backup_dir"
cp "$space_root/.env" "$space_root/space.lock.json" "$manifest" "$backup_dir/"
compose up -d --wait mysql
compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --no-tablespaces -u"$MYSQL_USER" "$MYSQL_DATABASE"' > "$backup_dir/mysql.sql"
test -s "$backup_dir/mysql.sql" || { echo "数据库备份失败。" >&2; exit 1; }

old_env="$tmp_dir/old.env"
cp "$space_root/.env" "$old_env"
candidate="${image}@${digest}"
sed "s|^BOT_IMAGE=.*$|BOT_IMAGE=${candidate}|" "$old_env" > "$tmp_dir/new.env"
cp "$tmp_dir/new.env" "$space_root/.env"

rollback() {
  echo "更新验证失败，正在恢复旧镜像和配置……" >&2
  cp "$old_env" "$space_root/.env"
  compose up -d || true
}
trap 'rollback; cleanup' ERR
compose pull
compose config --quiet
compose up -d
wait_ready 180
jq --arg version "$version" --arg digest "$digest" '.version=$version | .image_digest=$digest' \
  "$space_root/space.lock.json" > "$tmp_dir/space.lock.json"
cp "$tmp_dir/space.lock.json" "$space_root/space.lock.json"
trap cleanup EXIT INT TERM
echo "更新完成：$version"
