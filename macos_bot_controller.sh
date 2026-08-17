#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
RUNTIME_DIR="$ROOT/runtime"
BINARY="$RUNTIME_DIR/codex_workspace_bot"
SAFEDOTENV_BINARY="$RUNTIME_DIR/safedotenv"
RECEIVERCHECK_BINARY="$RUNTIME_DIR/receivercheck"
APPCTL_BINARY="$RUNTIME_DIR/appctl"
RUNNER="$RUNTIME_DIR/macos_run.sh"
CONFIG="$ROOT/config.yaml"
ENV_FILE="$ROOT/.env"
LOG_DIR="$ROOT/logs"
LABEL="com.kid0317.codex-workspace-bot"
DOMAIN="gui/$(id -u)"
LAUNCH_AGENTS_DIR="${CODEX_WORKSPACE_BOT_LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents}"
PLIST="$LAUNCH_AGENTS_DIR/$LABEL.plist"
STOP_TIMEOUT_SECONDS=${CODEX_WORKSPACE_BOT_STOP_TIMEOUT_SECONDS:-45}
START_TIMEOUT_SECONDS=${CODEX_WORKSPACE_BOT_START_TIMEOUT_SECONDS:-30}
CURL_CONNECT_TIMEOUT_SECONDS=2
CURL_MAX_TIME_SECONDS=5

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
  [[ -x $SAFEDOTENV_BINARY ]] || fail "missing executable $SAFEDOTENV_BINARY; run ./macos_bot_controller.sh build first"
  [[ -x $RECEIVERCHECK_BINARY ]] || fail "missing executable $RECEIVERCHECK_BINARY; run ./macos_bot_controller.sh build first"
  [[ -x $APPCTL_BINARY ]] || fail "missing executable $APPCTL_BINARY; run ./macos_bot_controller.sh build first"
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
  local temporary_server temporary_dotenv temporary_receiver temporary_appctl
  temporary_server=$(mktemp "$RUNTIME_DIR/.codex_workspace_bot.XXXXXX")
  temporary_dotenv=$(mktemp "$RUNTIME_DIR/.safedotenv.XXXXXX")
  temporary_receiver=$(mktemp "$RUNTIME_DIR/.receivercheck.XXXXXX")
  temporary_appctl=$(mktemp "$RUNTIME_DIR/.appctl.XXXXXX")
  trap 'rm -f "$temporary_server" "$temporary_dotenv" "$temporary_receiver" "$temporary_appctl"' RETURN
  go build -o "$temporary_server" ./cmd/server
  go build -o "$temporary_dotenv" ./cmd/safedotenv
  go build -o "$temporary_receiver" ./cmd/receivercheck
  go build -o "$temporary_appctl" ./cmd/appctl
  chmod 0755 "$temporary_server" 0700 "$temporary_dotenv" "$temporary_receiver" "$temporary_appctl"
  mv -f "$temporary_server" "$BINARY"
  mv -f "$temporary_dotenv" "$SAFEDOTENV_BINARY"
  mv -f "$temporary_receiver" "$RECEIVERCHECK_BINARY"
  mv -f "$temporary_appctl" "$APPCTL_BINARY"
  trap - RETURN
  printf 'built %s\n' "$BINARY"
}

write_runner() {
  local root_quoted
  printf -v root_quoted '%q' "$ROOT"
  {
    printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
    printf 'cd %s\n' "$root_quoted"
    printf '%s\n' 'export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"'
    printf '%s\n' 'exec ./runtime/safedotenv exec --file ./.env -- ./runtime/codex_workspace_bot -config ./config.yaml'
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
  [[ $START_TIMEOUT_SECONDS =~ ^[1-9][0-9]*$ ]] || fail "invalid start timeout"
  local deadline=$((SECONDS + START_TIMEOUT_SECONDS)) ready health base_url expected_ids
  base_url=$("$RECEIVERCHECK_BINARY" --config "$CONFIG" --print-base-url) || fail "invalid local server.listen_addr"
  expected_ids=$(enabled_receiver_ids) || fail "could not read enabled App receiver IDs"
  while (( SECONDS < deadline )); do
    health=$(curl --fail --silent --show-error --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time "$CURL_MAX_TIME_SECONDS" "$base_url/healthz" 2>/dev/null || true)
    if [[ $health == ok ]]; then
      ready=$(curl --fail --silent --show-error --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time "$CURL_MAX_TIME_SECONDS" "$base_url/readyz" 2>/dev/null || true)
      if [[ -n $ready ]] && printf '%s' "$ready" | "$RECEIVERCHECK_BINARY" --expected-ids "$expected_ids" >/dev/null 2>&1; then
        printf 'service ready\n'
        return 0
      fi
    fi
    sleep 1
  done
  show_recent_logs
  fail "service did not become ready within ${START_TIMEOUT_SECONDS}s"
}

enabled_receiver_ids() {
  local output id joined=
  output=$(cd "$ROOT" && "$SAFEDOTENV_BINARY" exec --file "$ENV_FILE" -- "$APPCTL_BINARY" receiver-ids --config ./config.yaml) || return 1
  while IFS= read -r id; do
    [[ -n $id && $id != *,* ]] || return 1
    if [[ -n $joined ]]; then
      joined="$joined,$id"
    else
      joined=$id
    fi
  done <<<"$output"
  [[ -n $joined ]] || return 1
  printf '%s\n' "$joined"
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
  require_start_files
  local base_url
  base_url=$("$RECEIVERCHECK_BINARY" --config "$CONFIG" --print-base-url) || fail "invalid local server.listen_addr"
  if is_loaded; then
    printf 'launchd: loaded\n'
  else
    printf 'launchd: not loaded\n'
  fi
  if curl --fail --silent --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time "$CURL_MAX_TIME_SECONDS" "$base_url/healthz" >/dev/null 2>&1; then
    printf 'healthz: ok\n'
    curl --fail --silent --show-error --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time "$CURL_MAX_TIME_SECONDS" "$base_url/readyz"
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
