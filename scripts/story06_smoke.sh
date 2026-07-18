#!/usr/bin/env bash
set -euo pipefail

# S06 local MySQL smoke. It never uses the service schema: a fresh temporary
# database receives the production migration runner, then is removed on exit.
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
container=${S06_MYSQL_CONTAINER:-codex_workspace_bot-mysql-1}
env_file=${S06_ENV_FILE:-"$root/.env"}

if [[ ! -f "$env_file" ]]; then
  echo "S06 smoke: missing local environment file" >&2
  exit 2
fi
set -a
. "$env_file"
set +a

database="codex_workspace_bot_s06_smoke_${RANDOM}${RANDOM}"
app_user=${MYSQL_USER:-codex_workspace_bot}
config=$(mktemp)
mysql_root() {
  docker exec -e MYSQL_PWD="$MYSQL_ROOT_PASSWORD" "$container" mysql -uroot --protocol=tcp "$@"
}
cleanup() {
  rm -f "$config"
	mysql_root -Nse "REVOKE SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER, CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, EVENT, TRIGGER ON $database.* FROM '$app_user'@'%'" >/dev/null 2>&1 || true
  mysql_root -Nse "DROP DATABASE IF EXISTS $database" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mysql_root -Nse "CREATE DATABASE $database CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci" >/dev/null
mysql_root -Nse "GRANT ALL PRIVILEGES ON $database.* TO '$app_user'@'%'" >/dev/null
sed "s/^  name: .*/  name: \"$database\"/" "$root/config.yaml" >"$config"

(cd "$root" && go run ./cmd/appctl list --config "$config") >/dev/null

# The integration test shares only this disposable schema. It exercises real
# InnoDB claim locking; credentials remain in the inherited environment and
# are never printed.
(
  cd "$root"
  S06_MYSQL_DSN="${MYSQL_USER}:${CODEX_WORKSPACE_BOT_DB_PASSWORD}@tcp(127.0.0.1:3306)/${database}?parseTime=true" \
    go test ./internal/schedule -run TestS06MySQLClaimRaceAndRestartEvidence -count=1
) >/dev/null

tables=$(mysql_root -Nse "SELECT table_name FROM information_schema.tables WHERE table_schema = '$database' AND table_name IN ('scheduled_tasks', 'scheduled_task_runs', 'scheduled_task_deliveries', 'scheduled_task_tool_calls', 'scheduled_script_definitions') ORDER BY table_name" 2>/dev/null)
expected="scheduled_script_definitions
scheduled_task_deliveries
scheduled_task_runs
scheduled_task_tool_calls
scheduled_tasks"
if [[ "$tables" != "$expected" ]]; then
  echo "S06 smoke: migration 005 schedule table set is incomplete" >&2
  exit 1
fi
version=$(mysql_root -D "$database" -Nse "SELECT COUNT(*) FROM schema_migrations WHERE version='005_s06_scheduled_tasks.sql' AND checksum IS NOT NULL" 2>/dev/null)
if [[ "$version" != "1" ]]; then
  echo "S06 smoke: migration 005 checksum record is missing" >&2
  exit 1
fi
echo "S06 MySQL migration smoke passed (temporary schema only)."
