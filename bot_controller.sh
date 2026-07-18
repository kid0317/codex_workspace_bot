#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
RUNTIME_DIR="$ROOT/runtime"
BINARY="$RUNTIME_DIR/codex_workspace_bot"
CONFIG="$ROOT/config.yaml"
ENV_FILE="$ROOT/.env"
UNIT="codex-workspace-bot.service"
STOP_TIMEOUT_SECONDS=45
START_TIMEOUT_SECONDS=30

usage() {
  printf 'Usage: %s {start|restart|stop|build}\n' "$(basename "$0")" >&2
}

fail() {
  printf 'bot_controller: %s\n' "$*" >&2
  exit 1
}

is_active_unit() {
  systemctl --user is-active --quiet "$UNIT" 2>/dev/null
}

has_persistent_unit() {
  systemctl --user cat "$UNIT" >/dev/null 2>&1
}

is_bot_main_pid() {
  local pid=$1 exe cwd cmd
  [[ $pid =~ ^[0-9]+$ && -r "/proc/$pid/cmdline" ]] || return 1
  exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
  exe=${exe%" (deleted)"}
  case "$exe" in
    "$BINARY"|"$RUNTIME_DIR"/codex_workspace_bot_s[0-9]*) return 0 ;;
  esac
  cwd=$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)
  [[ $cwd == "$ROOT" ]] || return 1
  cmd=$(tr '\0' ' ' <"/proc/$pid/cmdline")
  [[ $cmd =~ (^|[[:space:]])(\./)?runtime/codex_workspace_bot(_s[0-9]+)?([[:space:]]|$) ]]
}

bot_main_pids() {
  local proc pid
  for proc in /proc/[0-9]*; do
    pid=${proc##*/}
    if is_bot_main_pid "$pid"; then
      printf '%s\n' "$pid"
    fi
  done
}

descendant_pids() {
  local parent=$1 child
  while IFS= read -r child; do
    [[ -n $child ]] || continue
    descendant_pids "$child"
    printf '%s\n' "$child"
  done < <(pgrep -P "$parent" 2>/dev/null || true)
}

require_start_files() {
  [[ -f $CONFIG ]] || fail "missing config.yaml"
  [[ -f $ENV_FILE ]] || fail "missing .env"
  [[ -x $BINARY ]] || fail "missing executable $BINARY; run ./bot_controller.sh build first"
}

build() {
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
  journalctl --user-unit="$UNIT" --no-pager -n 40 >&2 || true
  fail "service did not become ready within ${START_TIMEOUT_SECONDS}s"
}

start() {
  require_start_files
  if is_active_unit; then
    fail "$UNIT is already active; use restart"
  fi
  local pids
  pids=$(bot_main_pids)
  if [[ -n $pids ]]; then
    fail "Bot process already exists (PID: ${pids//$'\n'/, }); use restart"
  fi
  if has_persistent_unit; then
    systemctl --user start "$UNIT"
  else
    systemd-run --user --unit=codex-workspace-bot --collect \
      --property="WorkingDirectory=$ROOT" \
      --property=KillMode=mixed \
      --property="TimeoutStopSec=${STOP_TIMEOUT_SECONDS}s" \
      --setenv="PATH=$PATH" \
      /bin/bash -lc 'set -a; . ./.env; set +a; exec ./runtime/codex_workspace_bot -config ./config.yaml'
  fi
  wait_for_ready
}

wait_for_pids_to_exit() {
  local deadline=$((SECONDS + STOP_TIMEOUT_SECONDS)) pid remaining
  local pids=("$@")
  while (( SECONDS < deadline )); do
    remaining=0
    for pid in "${pids[@]}"; do
      if kill -0 "$pid" 2>/dev/null; then
        remaining=1
        break
      fi
    done
    (( remaining == 0 )) && return 0
    sleep 1
  done
  return 1
}

stop() {
  local -a targets=() main_pids=()
  local pid child
  while IFS= read -r pid; do
    [[ -n $pid ]] || continue
    main_pids+=("$pid")
    targets+=("$pid")
    while IFS= read -r child; do
      [[ -n $child ]] && targets+=("$child")
    done < <(descendant_pids "$pid")
  done < <(bot_main_pids)

  if is_active_unit; then
    systemctl --user stop "$UNIT"
  fi

  if (( ${#targets[@]} == 0 )); then
    printf 'service is not running\n'
    return 0
  fi

  for pid in "${targets[@]}"; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  if ! wait_for_pids_to_exit "${targets[@]}"; then
    for pid in "${targets[@]}"; do
      kill -KILL "$pid" 2>/dev/null || true
    done
    sleep 1
  fi
  printf 'service stopped\n'
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
  *)
    usage
    exit 2
    ;;
esac
