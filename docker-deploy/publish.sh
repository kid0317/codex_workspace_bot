#!/usr/bin/env bash
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$deploy_dir/.." && pwd)"
registry_image="${ACR_IMAGE:-crpi-0c1kby082wk3ovcx.cn-hangzhou.personal.cr.aliyuncs.com/codex-workspace/codex-workspace-bot}"

usage() {
  cat >&2 <<'EOF'
用法：
  ./docker-deploy/publish.sh build-platform VERSION linux/amd64
  ./docker-deploy/publish.sh build-platform VERSION linux/arm64
  ./docker-deploy/publish.sh create-manifest VERSION

ACR 临时登录令牌有效期有限。请在每条命令前重新 docker login，先分别
推送两个架构，再创建总 manifest。脚本不读取、保存或打印阿里云 AccessKey。
EOF
  exit 2
}

action="${1:-}"
version="${2:-}"
test -n "$action" && test -n "$version" || usage
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || { echo "VERSION 格式不正确。" >&2; exit 2; }
command -v docker >/dev/null 2>&1 || { echo "需要 Docker。" >&2; exit 1; }
docker buildx version >/dev/null

case "$action" in
  build-platform)
    platform="${3:-}"
    case "$platform" in
      linux/amd64) architecture=amd64;;
      linux/arm64) architecture=arm64;;
      *) usage;;
    esac
    test "$#" -eq 3 || usage
    tmp_dir="$(mktemp -d)"
    cleanup() { rm -rf "$tmp_dir"; }
    trap cleanup EXIT INT TERM
    git -C "$repo_root" archive HEAD | tar -x -C "$tmp_dir"
    revision="$(git -C "$repo_root" rev-parse HEAD)"
    tag="${registry_image}:${version}-${architecture}"
    docker buildx build \
      --platform "$platform" \
      --file "$tmp_dir/docker-deploy/image/Dockerfile" \
      --build-arg VERSION="$version" \
      --build-arg VCS_REF="$revision" \
      --tag "$tag" \
      --provenance=false \
      --sbom=false \
      --push "$tmp_dir"
    docker buildx imagetools inspect "$tag"
    echo "架构镜像发布完成：$tag"
    ;;
  create-manifest)
    test "$#" -eq 2 || usage
    command -v jq >/dev/null 2>&1 || { echo "需要 jq。" >&2; exit 1; }
    tag="${registry_image}:${version}"
    docker buildx imagetools create \
      --tag "$tag" \
      "${registry_image}:${version}-amd64" \
      "${registry_image}:${version}-arm64"
    inspect_output="$(docker buildx imagetools inspect "$tag")"
    printf '%s\n' "$inspect_output"
    digest="$(awk '$1 == "Digest:" { print $2; exit }' <<<"$inspect_output")"
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "无法读取总 manifest digest。" >&2; exit 1; }
    tmp_dir="$(mktemp -d)"
    cleanup() { rm -rf "$tmp_dir"; }
    trap cleanup EXIT INT TERM
    manifest="$deploy_dir/release/release-manifest.json"
    jq --arg version "$version" --arg digest "$digest" '.version=$version | .image.digest=$digest' "$manifest" > "$tmp_dir/manifest.json"
    cp "$tmp_dir/manifest.json" "$manifest"
    (cd "$deploy_dir/release" && sha256sum release-manifest.json > release-manifest.json.sha256)
    echo "多架构发行完成：$tag"
    echo "digest：$digest"
    ;;
  *) usage;;
esac
