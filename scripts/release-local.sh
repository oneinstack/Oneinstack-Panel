#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat >&2 <<'EOF'
用法: release-local.sh v1.0.48

环境变量：
  WEB_REF=origin/main            用于构建前端 ZIP 的 Git 提交或引用
  WEB_ALLOW_DIRTY=1              将当前前端未提交改动打入测试包
  RELEASE_ALLOW_DIRTY_PANEL=1    明确允许把 Panel 的未提交业务代码打入发布包
EOF
}

if [[ $# -ne 1 ]]; then
  usage
  exit 2
fi

release_version="$1"
panel_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
web_repo="${panel_dir}/../Oneinstack-Panel-Web"

if [[ ! "${release_version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]]; then
  echo "发布版本必须是 vX.Y.Z 格式：${release_version}" >&2
  exit 2
fi
if [[ ! -d "${web_repo}/.git" ]]; then
  echo "前端仓库不存在：${web_repo}" >&2
  exit 1
fi

web_dirty_paths=()
web_dirty=0
if [[ "${WEB_ALLOW_DIRTY:-}" == "1" ]]; then
  web_dirty=1
  web_ref="$(git -C "${web_repo}" rev-parse HEAD)"
  if [[ -n "${WEB_REF:-}" ]]; then
    requested_web_ref="$(git -C "${web_repo}" rev-parse --verify --quiet "${WEB_REF}^{commit}")" || {
      echo "前端引用不存在：${WEB_REF}" >&2
      exit 1
    }
    if [[ "${requested_web_ref}" != "${web_ref}" ]]; then
      echo "WEB_ALLOW_DIRTY=1 只能基于当前前端 HEAD 打包，WEB_REF=${WEB_REF} 与当前 HEAD 不一致。" >&2
      echo "请先切换前端分支/提交，或先提交改动后使用 WEB_REF 打正式包。" >&2
      exit 1
    fi
  fi
  while IFS= read -r path; do
    [[ -z "${path}" ]] && continue
    web_dirty_paths+=("${path}")
  done < <(
    {
      git -C "${web_repo}" diff --name-only HEAD
      git -C "${web_repo}" ls-files --others --exclude-standard
    } | LC_ALL=C sort -u
  )
else
  web_ref="${WEB_REF:-origin/main}"
fi

panel_sha="$(git -C "${panel_dir}" rev-parse HEAD)"
panel_dirty_paths=()
while IFS= read -r path; do
  [[ -z "${path}" || "${path}" == "webui/app.zip" ]] && continue
  panel_dirty_paths+=("${path}")
done < <(
  {
    git -C "${panel_dir}" diff --name-only
    git -C "${panel_dir}" diff --cached --name-only
    git -C "${panel_dir}" ls-files --others --exclude-standard
  } | LC_ALL=C sort -u
)
if (( ${#panel_dirty_paths[@]} > 0 )) && [[ "${RELEASE_ALLOW_DIRTY_PANEL:-}" != "1" ]]; then
  echo "Panel 工作区含有未提交的业务改动，拒绝生成不可追溯发布包：" >&2
  printf '  %s\n' "${panel_dirty_paths[@]}" >&2
  echo "确认需要将这些改动打包时，请显式设置 RELEASE_ALLOW_DIRTY_PANEL=1。" >&2
  exit 1
fi
if (( ${#panel_dirty_paths[@]} > 0 )); then
  echo "警告：正在按 RELEASE_ALLOW_DIRTY_PANEL=1 打包未提交的 Panel 改动。" >&2
fi

temp_root="$(mktemp -d "${TMPDIR:-/tmp}/one-panel-release.XXXXXX")"
web_dir="${temp_root}/web"
web_archive_dir="${temp_root}/web-archive"

cleanup() {
  local exit_code=$?
  git -C "${web_repo}" worktree remove --force "${web_dir}" >/dev/null 2>&1 || true
  rm -rf -- "${temp_root}"
  exit "${exit_code}"
}
trap cleanup EXIT

verify_web_archive() {
  local archive="$1"
  rm -rf -- "${web_archive_dir}"
  mkdir -p "${web_archive_dir}"
  unzip -tq "${archive}" >/dev/null
  unzip -q "${archive}" -d "${web_archive_dir}"
  node - "${web_archive_dir}" <<'NODE'
const fs = require('node:fs')
const path = require('node:path')

const root = process.argv[2]
const files = new Set()
const collectFiles = (directory) => {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const fullPath = path.join(directory, entry.name)
    if (entry.isDirectory()) {
      collectFiles(fullPath)
      continue
    }
    files.add(path.relative(root, fullPath).split(path.sep).join('/'))
  }
}

collectFiles(root)
if (!files.has('index.html')) {
  throw new Error('Web archive does not contain index.html')
}

const missing = new Set()
const pattern = /\b(?:import|from)\s*\(?["']([^"']+\.(?:js|css)(?:[?#][^"']*)?)["']/g
for (const file of files) {
  if (!file.endsWith('.js')) continue
  const source = fs.readFileSync(path.join(root, file), 'utf8')
  for (const match of source.matchAll(pattern)) {
    const reference = match[1]
    if (!reference.startsWith('.')) continue
    const target = path.posix.normalize(
      path.posix.join(path.posix.dirname(file), reference.replace(/[?#].*$/, ''))
    )
    if (!files.has(target)) missing.add(`${file} -> ${target}`)
  }
}

if (missing.size) {
  throw new Error(`Web archive has missing lazy-load assets:\n${[...missing].sort().join('\n')}`)
}
NODE
}

if (( web_dirty )); then
  echo "警告：正在按 WEB_ALLOW_DIRTY=1 构建包含未提交前端改动的测试包。" >&2
  if (( ${#web_dirty_paths[@]} == 0 )); then
    echo "提示：当前前端工作区没有未提交改动，仍将以当前 HEAD 构建。" >&2
  else
    printf '  %s\n' "${web_dirty_paths[@]}" >&2
  fi
else
  echo "更新前端代码..."
  git -C "${web_repo}" fetch --prune origin
  if ! git -C "${web_repo}" rev-parse --verify --quiet "${web_ref}^{commit}" >/dev/null; then
    echo "前端引用不存在：${web_ref}" >&2
    exit 1
  fi
fi

echo "创建干净前端工作区..."
git -C "${web_repo}" worktree add --detach "${web_dir}" "${web_ref}"

if (( web_dirty && ${#web_dirty_paths[@]} > 0 )); then
  web_dirty_patch="${temp_root}/web-dirty.patch"
  git -C "${web_repo}" diff --binary HEAD > "${web_dirty_patch}"
  if [[ -s "${web_dirty_patch}" ]]; then
    git -C "${web_dir}" apply --binary --whitespace=nowarn "${web_dirty_patch}"
  fi

  while IFS= read -r -d '' path; do
    mkdir -p "${web_dir}/$(dirname -- "${path}")"
    cp -p "${web_repo}/${path}" "${web_dir}/${path}"
  done < <(git -C "${web_repo}" ls-files --others --exclude-standard -z)
fi

echo "安装前端依赖..."
(
  cd "${web_dir}"
  npm ci
)

web_sha="$(git -C "${web_dir}" rev-parse HEAD)"
web_version="${web_sha}"
if (( web_dirty )); then
  web_version="${web_sha}-dirty"
fi

echo "构建并同步前端..."
make -C "${panel_dir}" build-ui \
  WEB_DIR="${web_dir}" \
  WEB_ARCHIVE="${web_dir}/version/app-1.0.0.zip"

echo "校验前端懒加载资源闭包..."
verify_web_archive "${web_dir}/version/app-1.0.0.zip"

web_hash="$(shasum -a 256 "${web_dir}/version/app-1.0.0.zip" | awk '{print $1}')"
panel_hash="$(shasum -a 256 "${panel_dir}/webui/app.zip" | awk '{print $1}')"

if [[ "${web_hash}" != "${panel_hash}" ]]; then
  echo "前端 ZIP 校验失败：" >&2
  echo "Web:   ${web_hash}" >&2
  echo "Panel: ${panel_hash}" >&2
  exit 1
fi

echo "前端提交: ${web_sha}"
if (( web_dirty )); then
  echo "前端工作区: 含未提交改动（仅测试包，不可复现）"
fi
echo "前端 ZIP: ${web_hash}"
echo "Panel 提交: ${panel_sha}"
echo "开始生成 Panel ${release_version}..."

COPYFILE_DISABLE=1 make -C "${panel_dir}" release \
  VERSION="${release_version}" \
  COMMIT_HASH="${panel_sha}" \
  WEB_VERSION="${web_version}"

echo "发布完成：${release_version}"
