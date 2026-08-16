#!/usr/bin/env bash
set -euo pipefail

space_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

require_space() {
  test -f "$space_root/space.lock.json" || { echo "这不是安装器管理的 Space：缺少 space.lock.json" >&2; exit 1; }
  test -f "$space_root/compose.yaml" || { echo "Space 缺少 compose.yaml" >&2; exit 1; }
  command -v docker >/dev/null 2>&1 || { echo "没有找到 Docker，请先安装并启动 Docker Desktop。" >&2; exit 1; }
  docker info >/dev/null 2>&1 || { echo "Docker 还没有启动，请打开 Docker Desktop。" >&2; exit 1; }
}

compose() {
  docker compose --project-directory "$space_root" -f "$space_root/compose.yaml" "$@"
}

wait_ready() {
  local port timeout_seconds elapsed
  port="$(sed -n 's/^BOT_HOST_PORT=//p' "$space_root/.env")"
  timeout_seconds="${1:-120}"
  elapsed=0
  while (( elapsed < timeout_seconds )); do
    if curl --fail --silent "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  return 1
}

space_id() {
  sed -n 's/.*"space_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$space_root/space.lock.json" | head -n 1
}

