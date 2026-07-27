#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd -- "${script_dir}/.." && pwd)"
archive_path="${1:-${project_dir}/../Oneinstack-Panel-Web/version/app-1.0.0.zip}"
target_path="${project_dir}/webui/app.zip"

if [[ ! -f "${archive_path}" ]]; then
    echo "frontend archive not found: ${archive_path}" >&2
    exit 1
fi
if ! unzip -tq "${archive_path}" >/dev/null; then
    echo "frontend archive is invalid: ${archive_path}" >&2
    exit 1
fi
if ! unzip -l "${archive_path}" index.html | grep -q 'index.html'; then
    echo "frontend archive does not contain index.html: ${archive_path}" >&2
    exit 1
fi

temporary_path="$(mktemp "${target_path}.tmp.XXXXXX")"
trap 'rm -f -- "${temporary_path}"' EXIT
cp -- "${archive_path}" "${temporary_path}"
chmod 0644 "${temporary_path}"
mv -f -- "${temporary_path}" "${target_path}"
trap - EXIT

echo "embedded frontend updated: ${target_path}"
