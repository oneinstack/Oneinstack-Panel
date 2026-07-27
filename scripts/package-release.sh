#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 VERSION [OUTPUT_DIR] [TARGET]"
  echo "TARGET defaults to both linux-amd64 and linux-arm64."
}

if [[ $# -lt 1 || $# -gt 3 ]]; then
  usage >&2
  exit 2
fi

version="$1"
output_dir="${2:-packages}"
requested_target="${3:-}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "${script_dir}/.." && pwd)"

if [[ ! "$version" =~ ^[0-9A-Za-z._+-]+$ ]]; then
  echo "Invalid release version: $version" >&2
  exit 2
fi

if [[ -n "$requested_target" ]]; then
  targets=("$requested_target")
else
  targets=("linux-amd64" "linux-arm64")
fi

case "$output_dir" in
  /*) ;;
  *) output_dir="${project_dir}/${output_dir}" ;;
esac
mkdir -p "$output_dir"

temporary_base="${TMPDIR:-/tmp}"
temporary_base="${temporary_base%/}"
temporary_dir="$(mktemp -d "${temporary_base}/oneinstack-package.XXXXXX")"
cleanup() {
  case "$temporary_dir" in
    "${temporary_base}"/oneinstack-package.*) rm -rf -- "$temporary_dir" ;;
  esac
}
trap cleanup EXIT

for target in "${targets[@]}"; do
  case "$target" in
    linux-amd64|linux-arm64) ;;
    *)
      echo "Unsupported release target: $target" >&2
      exit 2
      ;;
  esac

  binary="${project_dir}/dist/one-${target}"
  if [[ ! -x "$binary" ]]; then
    echo "Missing executable: $binary (run make build-all first)" >&2
    exit 1
  fi

  package_name="one-${target}-${version}"
  package_root="${temporary_dir}/${package_name}"
  mkdir -p "$package_root"

  install -m 0755 "$binary" "${package_root}/one"
  install -m 0644 "${project_dir}/config.yaml" "${package_root}/config.yaml"
  install -m 0644 "${project_dir}/README.md" "${package_root}/README.md"
  install -m 0644 "${project_dir}/README-zh.md" "${package_root}/README-zh.md"
  install -m 0644 "${project_dir}/BUILD.md" "${package_root}/BUILD.md"
  install -m 0644 "${project_dir}/LICENSE" "${package_root}/LICENSE"
  mkdir -p "${package_root}/script-registry"
  cp -R "${project_dir}/script-registry/bundled" "${package_root}/script-registry/bundled"
  for installer in install.sh install-cent.sh install-ubuntu.sh; do
    install -m 0755 "${project_dir}/${installer}" "${package_root}/${installer}"
  done

  archive="${output_dir}/${package_name}.tar.gz"
  tar -C "$temporary_dir" -czf "$archive" "$package_name"

  (
    cd "$output_dir"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "${package_name}.tar.gz" > "${package_name}.tar.gz.sha256"
    else
      shasum -a 256 "${package_name}.tar.gz" > "${package_name}.tar.gz.sha256"
    fi
  )
  echo "Created ${archive}"
done
