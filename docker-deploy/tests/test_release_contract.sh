#!/usr/bin/env bash
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

required=(
  image/Dockerfile
  image/Dockerfile.dockerignore
  templates/compose.yaml
  templates/config/bot.yaml
  templates/start.sh
  templates/stop.sh
  templates/status.sh
  templates/logs.sh
  templates/manage.sh
  templates/update.sh
  templates/uninstall.sh
  templates/start.ps1
  templates/stop.ps1
  templates/status.ps1
  templates/logs.ps1
  templates/manage.ps1
  templates/update.ps1
  templates/uninstall.ps1
  release/release-manifest.json
  publish.sh
)

for relative in "${required[@]}"; do
  test -f "$deploy_dir/$relative" || {
    echo "missing release file: $relative" >&2
    exit 1
  }
done

compose="$deploy_dir/templates/compose.yaml"
for service in mysql bot codex provider-proxy; do
  grep -Eq "^  ${service}:" "$compose" || {
    echo "missing Compose service: $service" >&2
    exit 1
  }
done
grep -Fq '127.0.0.1:${BOT_HOST_PORT}:8080' "$compose"
grep -Fq 'stop_grace_period: 45s' "$compose"
if grep -Eq '(system/runtime\.env|docker\.sock)' "$compose"; then
  echo "forbidden secret mount or Docker socket in Compose" >&2
  exit 1
fi

codex_block="$(sed -n '/^  codex:/,/^  bot:/p' "$compose")"
if grep -q 'env_file:' <<<"$codex_block"; then
  echo "codex service must not load a secret env file" >&2
  exit 1
fi

dockerfile="$deploy_dir/image/Dockerfile"
grep -Eq '^USER [^0]' "$dockerfile"
grep -Fq 'ARG CODEX_VERSION=0.147.0' "$dockerfile"
grep -Fq '@openai/codex@${CODEX_VERSION}' "$dockerfile"
if grep -Eq '^COPY[[:space:]]+\.[[:space:]]' "$dockerfile"; then
  echo "Dockerfile must use explicit COPY sources" >&2
  exit 1
fi

for script in "$deploy_dir"/templates/*.sh "$deploy_dir/install.sh" "$deploy_dir/publish.sh"; do
  bash -n "$script"
done
grep -Fq 'build-platform' "$deploy_dir/publish.sh"
grep -Fq 'create-manifest' "$deploy_dir/publish.sh"
grep -Fq -- '--provenance=false' "$deploy_dir/publish.sh"
if grep -Eqi '(AccessKey|SecretKey)=' "$deploy_dir/publish.sh"; then
  echo "publisher must not contain registry credentials" >&2
  exit 1
fi
grep -Fq 'https://dashscope.aliyuncs.com/compatible-mode/v1' "$deploy_dir/install.sh"
grep -Fq 'https://dashscope.aliyuncs.com/compatible-mode/v1' "$deploy_dir/install.ps1"
grep -Fq 'SetAccessRuleProtection($true, $false)' "$deploy_dir/install.ps1"
if rg -q 'dashscope\.aliyuncs\.com/api/v2/apps' "$deploy_dir"; then
  echo "obsolete Bailian Base URL found" >&2
  exit 1
fi

echo "docker release contract: PASS"
