#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space
compose ps
port="$(sed -n 's/^BOT_HOST_PORT=//p' "$space_root/.env")"
if curl --fail --silent "http://127.0.0.1:${port}/readyz"; then
  echo
  echo "状态：ready"
elif compose ps --status running --services | grep -q .; then
  echo "状态：degraded（容器在运行，但 Bot 尚未就绪）"
  exit 2
else
  echo "状态：stopped"
  exit 3
fi

