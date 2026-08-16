#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space
compose config --quiet
compose up -d
if wait_ready 180; then
  echo "启动成功。"
  "${space_root}/status.sh"
else
  echo "服务没有在 3 分钟内就绪，请运行 ./logs.sh 查看原因。" >&2
  compose ps
  exit 1
fi

