#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out_dir="${OUT_DIR:-$repo_root/dist}"
binary_name="${BINARY_NAME:-codex_workspace_bot}"

cd "$repo_root"

mkdir -p "$out_dir"

if [[ -n "$(gofmt -l .)" ]]; then
  gofmt -l .
  echo "gofmt found unformatted files" >&2
  exit 1
fi

go test ./...
go vet ./...
go build -o "$out_dir/$binary_name" ./cmd/server

printf 'Built %s\n' "$out_dir/$binary_name"
