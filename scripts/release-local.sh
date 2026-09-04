#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
  echo "用法: $0 v1.0.48" >&2
  exit 2
fi

release_version="$1"
panel_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
web_repo="${panel_dir}/../Oneinstack-Panel-Web"

temp_root="$(mktemp -d "${TMPDIR:-/tmp}/one-panel-release.XXXXXX")"
web_dir="${temp_root}/web"

cleanup() {
  local exit_code=$?
  git -C "${web_repo}" worktree remove --force "${web_dir}" >/dev/null 2>&1 || true
  rmdir "${temp_root}" 2>/dev/null || true
  exit "${exit_code}"
}
trap cleanup EXIT

echo "更新前端代码..."
git -C "${web_repo}" fetch origin

echo "创建干净前端工作区..."
git -C "${web_repo}" worktree add --detach "${web_dir}" origin/main

echo "安装前端依赖..."
(
  cd "${web_dir}"
  npm ci
)

web_sha="$(git -C "${web_dir}" rev-parse HEAD)"

echo "构建并同步前端..."
make -C "${panel_dir}" build-ui \
  WEB_DIR="${web_dir}" \
  WEB_ARCHIVE="${web_dir}/version/app-1.0.0.zip"

web_hash="$(shasum -a 256 "${web_dir}/version/app-1.0.0.zip" | awk '{print $1}')"
panel_hash="$(shasum -a 256 "${panel_dir}/webui/app.zip" | awk '{print $1}')"

if [[ "${web_hash}" != "${panel_hash}" ]]; then
  echo "前端 ZIP 校验失败：" >&2
  echo "Web:   ${web_hash}" >&2
  echo "Panel: ${panel_hash}" >&2
  exit 1
fi

echo "前端提交: ${web_sha}"
echo "前端 ZIP: ${web_hash}"
echo "开始生成 Panel ${release_version}..."

COPYFILE_DISABLE=1 make -C "${panel_dir}" release \
  VERSION="${release_version}" \
  WEB_VERSION="${web_sha}"

echo "发布完成：${release_version}"
