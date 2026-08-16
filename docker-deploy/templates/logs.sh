#!/usr/bin/env bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_space
service="${1:-bot}"
case "$service" in mysql|bot|codex|provider-proxy) ;; *) echo "服务只能是 mysql、bot、codex 或 provider-proxy。" >&2; exit 2;; esac
compose logs --tail 200 "$service"

