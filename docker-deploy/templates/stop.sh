#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space
compose stop
echo "服务已停止，Workspace、配置和数据库都保留。"

