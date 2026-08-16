#!/usr/bin/env bash
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$deploy_dir/.." && pwd)"
registry_image="${ACR_IMAGE:-crpi-0c1kby082wk3ovcx.cn-hangzhou.personal.cr.aliyuncs.com/codex-workspace/codex-workspace-bot}"
version="${1:-}"
test -n "$version" || { echo "用法：./docker-deploy/publish.sh VERSION" >&2; exit 2; }
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || { echo "VERSION 格式不正确。" >&2; exit 2; }
command -v docker >/dev/null 2>&1 || { echo "需要 Docker。" >&2; exit 1; }
docker buildx version >/dev/null
tmp_dir="$(mktemp -d)"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT INT TERM
git -C "$repo_root" archive HEAD | tar -x -C "$tmp_dir"
revision="$(git -C "$repo_root" rev-parse HEAD)"
tag="${registry_image}:${version}"

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --file "$tmp_dir/docker-deploy/image/Dockerfile" \
  --build-arg VERSION="$version" \
  --build-arg VCS_REF="$revision" \
  --tag "$tag" \
  --provenance=mode=max \
  --sbom=true \
  --metadata-file "$tmp_dir/build-metadata.json" \
  --push "$tmp_dir"

digest="$(jq -er '."containerimage.digest" | select(startswith("sha256:"))' "$tmp_dir/build-metadata.json")"
manifest="$deploy_dir/release/release-manifest.json"
jq --arg version "$version" --arg digest "$digest" '.version=$version | .image.digest=$digest' "$manifest" > "$tmp_dir/manifest.json"
cp "$tmp_dir/manifest.json" "$manifest"
(cd "$deploy_dir/release" && sha256sum release-manifest.json > release-manifest.json.sha256)
docker buildx imagetools inspect "$tag"
echo "发布完成：$tag"
echo "digest：$digest"
