#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
config_path="${CONFIG:-$repo_root/config.yaml}"
debug="${DEBUG:-false}"
debug_token="${DEBUG_TOKEN:-}"

if [[ ! -f "$config_path" ]]; then
  cp "$repo_root/config.yaml.template" "$config_path"
fi

if [[ "$debug" == "true" ]]; then
  if [[ -z "$debug_token" ]]; then
    debug_token="$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(24))
PY
)"
  fi
  python3 - "$config_path" "$debug_token" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
token = sys.argv[2]
text = path.read_text()
text = text.replace("debug_enabled: false", "debug_enabled: true")
text = text.replace("EXAMPLE_DEBUG_TOKEN_DO_NOT_USE", token)
path.write_text(text)
PY
  export DEBUG_TOKEN="$debug_token"
  printf 'Debug API enabled on local bind. Use X-Debug-Token: %s\n' "$debug_token"
fi

cd "$repo_root"
go run ./cmd/server "$config_path"
