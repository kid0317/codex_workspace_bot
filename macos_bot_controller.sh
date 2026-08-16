#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
RUNTIME_DIR="$ROOT/runtime"
BINARY="$RUNTIME_DIR/codex_workspace_bot"
RUNNER="$RUNTIME_DIR/macos_run.sh"
CONFIG="$ROOT/config.yaml"
ENV_FILE="$ROOT/.env"
LOG_DIR="$ROOT/logs"
LABEL="com.kid0317.codex-workspace-bot"
DOMAIN="gui/$(id -u)"
LAUNCH_AGENTS_DIR="${CODEX_WORKSPACE_BOT_LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents}"
PLIST="$LAUNCH_AGENTS_DIR/$LABEL.plist"
STOP_TIMEOUT_SECONDS=45
START_TIMEOUT_SECONDS=30

usage() {
  printf 'Usage: %s {start|restart|stop|build|status|logs}\n' "$(basename "$0")" >&2
}

fail() {
  printf 'macos_bot_controller: %s\n' "$*" >&2
  exit 1
}

require_macos() {
  [[ $(uname -s) == Darwin ]] || fail "this controller only supports macOS"
}

require_start_files() {
  [[ -f $CONFIG ]] || fail "missing config.yaml; run ./scripts/macos_native_setup.sh first"
  [[ -f $ENV_FILE ]] || fail "missing .env; run ./scripts/macos_native_setup.sh first"
  [[ -x $BINARY ]] || fail "missing executable $BINARY; run ./macos_bot_controller.sh build first"
}

is_loaded() {
  launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1
}

xml_escape() {
  local value=$1
  value=${value//&/&amp;}
  value=${value//</&lt;}
  value=${value//>/&gt;}
  value=${value//\"/&quot;}
  value=${value//\'/&apos;}
  printf '%s' "$value"
}

build() {
  require_macos
  mkdir -p "$RUNTIME_DIR"
  local temporary
  temporary=$(mktemp "$RUNTIME_DIR/.codex_workspace_bot.XXXXXX")
  trap 'rm -f "$temporary"' RETURN
  go build -o "$temporary" ./cmd/server
  chmod 0755 "$temporary"
  mv -f "$temporary" "$BINARY"
  trap - RETURN
  printf 'built %s\n' "$BINARY"
}

write_runner() {
  local root_quoted
  printf -v root_quoted '%q' "$ROOT"
  {
    printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
    printf 'cd %s\n' "$root_quoted"
    printf '%s\n' 'set -a' '. ./.env' 'set +a'
    printf '%s\n' 'export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:${PATH:-}"'
    printf '%s\n' 'exec ./runtime/codex_workspace_bot -config ./config.yaml'
  } >"$RUNNER"
  chmod 0700 "$RUNNER"
}

write_plist() {
  local runner_xml root_xml stdout_xml stderr_xml
  runner_xml=$(xml_escape "$RUNNER")
  root_xml=$(xml_escape "$ROOT")
  stdout_xml=$(xml_escape "$LOG_DIR/macos-stdout.log")
  stderr_xml=$(xml_escape "$LOG_DIR/macos-stderr.log")
  mkdir -p "$LAUNCH_AGENTS_DIR" "$LOG_DIR"
  {
    printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>'
    printf '%s\n' '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
    printf '%s\n' '<plist version="1.0"><dict>'
    printf '%s\n' '  <key>Label</key>' "  <string>$LABEL</string>"
    printf '%s\n' '  <key>ProgramArguments</key><array>' "    <string>$runner_xml</string>" '  </array>'
    printf '%s\n' '  <key>WorkingDirectory</key>' "  <string>$root_xml</string>"
    printf '%s\n' '  <key>RunAtLoad</key><true/>'
    printf '%s\n' '  <key>KeepAlive</key><false/>'
    printf '%s\n' '  <key>ProcessType</key><string>Background</string>'
    printf '%s\n' '  <key>ThrottleInterval</key><integer>10</integer>'
    printf '%s\n' '  <key>StandardOutPath</key>' "  <string>$stdout_xml</string>"
    printf '%s\n' '  <key>StandardErrorPath</key>' "  <string>$stderr_xml</string>"
    printf '%s\n' '</dict></plist>'
  } >"$PLIST"
  chmod 0600 "$PLIST"
  plutil -lint "$PLIST" >/dev/null
}

show_recent_logs() {
  [[ -f $LOG_DIR/macos-stdout.log ]] && tail -n 40 "$LOG_DIR/macos-stdout.log" || true
  [[ -f $LOG_DIR/macos-stderr.log ]] && tail -n 80 "$LOG_DIR/macos-stderr.log" >&2 || true
}

wait_for_ready() {
  local deadline=$((SECONDS + START_TIMEOUT_SECONDS)) ready
  while (( SECONDS < deadline )); do
    if curl --fail --silent --show-error http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
      ready=$(curl --fail --silent --show-error http://127.0.0.1:8080/readyz 2>/dev/null || true)
      if [[ -n $ready ]] && ! grep -Eq '"state":"(connecting|disconnected|failed)"' <<<"$ready"; then
        printf 'service ready\n'
        return 0
      fi
    fi
    sleep 1
  done
  show_recent_logs
  fail "service did not become ready within ${START_TIMEOUT_SECONDS}s"
}

start() {
  require_macos
  require_start_files
  if is_loaded; then
    fail "$LABEL is already loaded; use restart"
  fi
  write_runner
  write_plist
  launchctl bootstrap "$DOMAIN" "$PLIST"
  wait_for_ready
}

stop() {
  require_macos
  if ! is_loaded; then
    printf 'service is not running\n'
    return 0
  fi
  launchctl bootout "$DOMAIN/$LABEL"
  local deadline=$((SECONDS + STOP_TIMEOUT_SECONDS))
  while (( SECONDS < deadline )); do
    is_loaded || {
      printf 'service stopped\n'
      return 0
    }
    sleep 1
  done
  fail "service did not stop within ${STOP_TIMEOUT_SECONDS}s"
}

status() {
  require_macos
  if is_loaded; then
    printf 'launchd: loaded\n'
  else
    printf 'launchd: not loaded\n'
  fi
  if curl --fail --silent http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    printf 'healthz: ok\n'
    curl --fail --silent --show-error http://127.0.0.1:8080/readyz
    printf '\n'
  else
    printf 'healthz: unavailable\n'
  fi
}

case "${1:-}" in
  build)
    build
    ;;
  start)
    start
    ;;
  stop)
    stop
    ;;
  restart)
    stop
    start
    ;;
  status)
    status
    ;;
  logs)
    require_macos
    show_recent_logs
    ;;
  *)
    usage
    exit 2
    ;;
esac
