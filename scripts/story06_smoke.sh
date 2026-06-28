#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
evidence_dir="${1:-$repo_root/docs/evidence/story06/latest}"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/story06-smoke.XXXXXX")"
server_pid=""
port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"

cleanup() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT

rm -rf "$evidence_dir"
mkdir -p "$evidence_dir" "$work_dir/workspaces/demo-assistant"
config="$work_dir/config.yaml"
sed "s#workspace_dir: ./workspaces/demo-assistant#workspace_dir: $work_dir/workspaces/demo-assistant#" "$repo_root/config.yaml.template" > "$config"
sed -i "s/port: 8080/port: $port/" "$config"
sed -i "s/debug_enabled: false/debug_enabled: true/" "$config"
sed -i "s#temp_dir: ./workspaces/demo-assistant/tmp/attachments#temp_dir: $work_dir/workspaces/demo-assistant/tmp/attachments#" "$config"
debug_token="story06-smoke-token"
sed -i "s/EXAMPLE_DEBUG_TOKEN_DO_NOT_USE/$debug_token/" "$config"

(
  cd "$repo_root"
  go build -o "$work_dir/codex_workspace_bot" ./cmd/server
)

"$work_dir/codex_workspace_bot" "$config" > "$evidence_dir/server.log" 2>&1 &
server_pid=$!

for _ in $(seq 1 50); do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    echo "server exited before health check" >&2
    exit 1
  fi
  if curl -fsS "http://127.0.0.1:$port/health" > "$evidence_dir/health.json" 2>>"$evidence_dir/curl.err"; then
    break
  fi
  sleep 0.1
done
test -s "$evidence_dir/health.json"

curl -fsS -X POST "http://127.0.0.1:$port/debug/dispatch" \
  -H 'Content-Type: application/json' \
  -H "X-Debug-Token: $debug_token" \
  -d '{"app_id":"demo-assistant","chat_id":"oc_demo","sender_id":"ou_demo","message_id":"manual-1","text":"hello"}' \
  > "$evidence_dir/debug-dispatch.json"

curl -fsS -X POST "http://127.0.0.1:$port/debug/task/run" \
  -H 'Content-Type: application/json' \
  -H "X-Debug-Token: $debug_token" \
  -d '{"app_id":"demo-assistant","task":{"id":"demo-assistant/manual","target_type":"p2p","target_id":"ou_demo","send_output":true,"prompt":"manual task","enabled":true}}' \
  > "$evidence_dir/task-run.json"

sqlite3 "$work_dir/workspaces/demo-assistant/bot.db" \
  'select role, content from messages order by created_at;' \
  > "$evidence_dir/sqlite-messages.txt"
sqlite3 "$work_dir/workspaces/demo-assistant/bot.db" \
  'select status, error_kind from turns order by created_at;' \
  > "$evidence_dir/sqlite-turns.txt"
grep -q 'user|hello' "$evidence_dir/sqlite-messages.txt"
grep -q 'assistant|hello' "$evidence_dir/sqlite-messages.txt"
grep -q 'completed|' "$evidence_dir/sqlite-turns.txt"

kill "$server_pid"
wait "$server_pid" 2>/dev/null || true
server_pid=""

disabled_config="$work_dir/config-disabled.yaml"
sed 's/debug_enabled: true/debug_enabled: false/' "$config" > "$disabled_config"
"$work_dir/codex_workspace_bot" "$disabled_config" >> "$evidence_dir/server.log" 2>&1 &
server_pid=$!
sleep 0.5
status="$(curl -s -o "$evidence_dir/debug-disabled-body.txt" -w '%{http_code}' -X POST "http://127.0.0.1:$port/debug/dispatch" -H 'Content-Type: application/json' -d '{}')"
printf 'debug disabled status=%s\n' "$status" > "$evidence_dir/debug-disabled.txt"
test "$status" = "404"

(
  cd "$repo_root"
  git status --ignored --short
) > "$evidence_dir/git-status-ignored.txt"

printf 'Story 06 smoke evidence written to %s\n' "$evidence_dir"
