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
grep -Fq '$attachmentKey = New-RandomBase64' "$deploy_dir/install.ps1"
grep -Fq '$actionKey = New-RandomBase64' "$deploy_dir/install.ps1"
grep -Fq '已经通过匿名拉取验证' "$deploy_dir/install.sh"
grep -Fq '已经通过匿名拉取验证' "$deploy_dir/install.ps1"
if rg -q '(Public.*要求登录|不支持匿名拉取)' "$deploy_dir/install.sh" "$deploy_dir/install.ps1"; then
  echo "installer contains a disproven ACR authentication claim" >&2
  exit 1
fi
release_repository="$(jq -er '.image.repository' "$deploy_dir/release/release-manifest.json")"
release_digest="$(jq -er '.image.digest | select(test("^sha256:[0-9a-f]{64}$"))' "$deploy_dir/release/release-manifest.json")"
release_image="${release_repository}@${release_digest}"
grep -Fq "image_default=\"${release_image}\"" "$deploy_dir/install.sh"
grep -Fq "\$imageDefault = \"${release_image}\"" "$deploy_dir/install.ps1"
grep -Fq "\"image_digest\": \"${release_digest}\"" "$deploy_dir/install.sh"
grep -Fq "image_digest = \"${release_digest}\"" "$deploy_dir/install.ps1"
grep -Fq 'Get-ChildItem -LiteralPath $sourceDir -Force | Copy-Item' "$deploy_dir/templates/manage.ps1"
if grep -Fq 'Copy-Item -LiteralPath (Join-Path $sourceDir "*")' "$deploy_dir/templates/manage.ps1"; then
  echo "PowerShell must not use a wildcard with LiteralPath" >&2
  exit 1
fi
if rg -q 'dashscope\.aliyuncs\.com/api/v2/apps' "$deploy_dir"; then
  echo "obsolete Bailian Base URL found" >&2
  exit 1
fi

# Run the Linux installer with a fake Docker CLI and prove that every generated
# Bot encryption key matches the runtime contract: standard Base64 for exactly
# 32 decoded bytes. This catches installers that accidentally emit hex strings.
fake_bin="$(mktemp -d)"
fake_space="$(mktemp -d)"
cleanup_installer_probe() { rm -rf "$fake_bin" "$fake_space"; }
trap cleanup_installer_probe EXIT INT TERM
cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$fake_bin/docker"
printf '%s\n' \
  "$fake_space" \
  '1' \
  '' \
  '' \
  'contract-test-provider-key' \
  '18089' \
  'n' \
  'n' | PATH="$fake_bin:$PATH" bash "$deploy_dir/install.sh" >/dev/null
for key_name in CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1 CODEX_WORKSPACE_BOT_ACTION_RESULT_KEY_V1; do
  encoded="$(sed -n "s/^${key_name}=//p" "$fake_space/.secrets/bot.env")"
  decoded_bytes="$(printf '%s' "$encoded" | base64 --decode 2>/dev/null | wc -c)"
  if [[ "$decoded_bytes" -ne 32 ]]; then
    echo "$key_name must decode from Base64 to exactly 32 bytes; got $decoded_bytes" >&2
    exit 1
  fi
done
cleanup_installer_probe
trap - EXIT INT TERM

echo "docker release contract: PASS"
