#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./start.sh start|stop|restart|status

Environment:
  CONFIG=/path/to/config.yaml
  DEBUG=true
  DEBUG_TOKEN=dev-token
  PID_FILE=/path/to/server.pid
  LOG_FILE=/path/to/server.log
  ERR_LOG_FILE=/path/to/server.log.wf
  BINARY=/path/to/dist/codex_workspace_bot
EOF
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
command="${1:-start}"
config_path="${CONFIG:-$repo_root/config.yaml}"
binary="${BINARY:-$repo_root/dist/codex_workspace_bot}"
pid_file="${PID_FILE:-$repo_root/server.pid}"
log_file="${LOG_FILE:-$repo_root/server.log}"
err_log_file="${ERR_LOG_FILE:-$repo_root/server.log.wf}"
debug="${DEBUG:-false}"
debug_token="${DEBUG_TOKEN:-}"

ensure_config() {
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
}

is_running() {
  [[ -f "$pid_file" ]] || return 1
  local pid
  pid="$(cat "$pid_file")"
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

rotate_logs() {
  local ts
  ts="$(date +%Y%m%d_%H%M%S)"
  [[ -s "$log_file" ]] && mv "$log_file" "$log_file.$ts"
  [[ -s "$err_log_file" ]] && mv "$err_log_file" "$err_log_file.$ts"
  { ls -t "$log_file".* 2>/dev/null || true; } | tail -n +11 | xargs -r rm -f
  { ls -t "$err_log_file".* 2>/dev/null || true; } | tail -n +11 | xargs -r rm -f
}

start_app() {
  ensure_config
  if [[ ! -x "$binary" ]]; then
    echo "Missing executable $binary. Run ./build.sh first." >&2
    exit 1
  fi
  if is_running; then
    echo "codex_workspace_bot is already running with pid $(cat "$pid_file")"
    return
  fi
  mkdir -p "$(dirname "$pid_file")" "$(dirname "$log_file")" "$(dirname "$err_log_file")"
  rotate_logs
  cd "$repo_root"
  nohup "$binary" "$config_path" > "$log_file" 2> "$err_log_file" &
  echo "$!" > "$pid_file"
  echo "Started (pid $(cat "$pid_file"))"
}

stop_app() {
  if ! is_running; then
    rm -f "$pid_file"
    echo "Not running"
    return
  fi
  local pid
  pid="$(cat "$pid_file")"
  kill "$pid"
  for _ in $(seq 1 50); do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$pid_file"
      echo "Stopped (pid $pid)"
      return
    fi
    sleep 0.1
  done
  kill -9 "$pid" 2>/dev/null || true
  rm -f "$pid_file"
  echo "Force killed (pid $pid)"
}

status_app() {
  if is_running; then
    echo "Running (pid $(cat "$pid_file"))"
  else
    rm -f "$pid_file"
    echo "Not running"
    return 1
  fi
}

case "$command" in
  start)
    start_app
    ;;
  stop)
    stop_app
    ;;
  restart)
    stop_app
    sleep 1
    start_app
    ;;
  status)
    status_app
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
