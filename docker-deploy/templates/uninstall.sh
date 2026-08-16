#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space

purge=false
delete_backups=false
for arg in "$@"; do
  case "$arg" in
    --purge) purge=true;;
    --delete-backups) delete_backups=true;;
    *) echo "用法：./uninstall.sh [--purge] [--delete-backups]" >&2; exit 2;;
  esac
done

if ! $purge; then
  compose down
  echo "服务和网络已卸载，Space、数据库 volume、Workspace 与 Secret 均保留。"
  exit 0
fi

expected="$(space_id)"
test -n "$expected" || { echo "Space ID 无效，拒绝删除。" >&2; exit 1; }
echo "即将永久删除此 Space 的数据库 volume 和安装器生成的数据。"
read -r -p "请输入 Space ID ${expected} 进行确认: " typed
test "$typed" = "$expected" || { echo "Space ID 不一致，已取消。" >&2; exit 1; }

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$space_root/system/backups/$timestamp"
if compose ps --status running --services | grep -qx mysql; then
  compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump -u"$MYSQL_USER" "$MYSQL_DATABASE"' > "$space_root/system/backups/$timestamp/mysql.sql"
fi
compose down -v

for generated in compose.yaml .env start.sh stop.sh status.sh logs.sh manage.sh update.sh uninstall.sh start.ps1 stop.ps1 status.ps1 logs.ps1 manage.ps1 update.ps1 uninstall.ps1 lib.sh; do
  rm -f "$space_root/$generated"
done
rm -rf "$space_root/config" "$space_root/.secrets" "$space_root/system/codex-home" "$space_root/system/home" "$space_root/system/bot-home" "$space_root/logs" "$space_root/attachments"
if $delete_backups; then
  rm -rf "$space_root/system/backups"
fi
rm -f "$space_root/space.lock.json"
echo "受管运行环境已删除。apps/ 和默认备份仍保留；你可以检查后手工删除整个 Space 文件夹。"

