#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "Usage: $0 VERSION PACKAGE_DIR [HTTPS_BASE_URL]" >&2
  exit 2
fi

version="$1"
package_dir="$2"
base_url="${3:-https://mirrors.oneinstack.com/oneinstack}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! "$version" =~ ^[0-9A-Za-z._+-]+$ ]]; then
  echo "Invalid release version: ${version}" >&2
  exit 2
fi

case "$package_dir" in
  /*) ;;
  *) package_dir="$(cd "$package_dir" && pwd)" ;;
esac
[[ -d "$package_dir" ]] || {
  echo "Package directory not found: ${package_dir}" >&2
  exit 1
}

base_url="${base_url%/}"
[[ "$base_url" == https://* && "$base_url" != *'"'* && "$base_url" != *"'"* &&
  "$base_url" != *\\* &&
  "$base_url" != *[[:space:]]* ]] || {
  echo "HTTPS base URL is required: ${base_url}" >&2
  exit 2
}

sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

file_size() {
  wc -c <"$1" | tr -d '[:space:]'
}

artifact_sha_amd64=""
artifact_size_amd64=""
artifact_file_amd64=""
artifact_sha_arm64=""
artifact_size_arm64=""
artifact_file_arm64=""
for arch in amd64 arm64; do
  file_name="one-linux-${arch}-${version}.tar.gz"
  archive="${package_dir}/${file_name}"
  [[ -f "$archive" && -s "$archive" ]] || {
    echo "Release archive not found or empty: ${archive}" >&2
    exit 1
  }
  "${script_dir}/verify-release.sh" "$archive"
  case "$arch" in
    amd64)
      artifact_file_amd64="$file_name"
      artifact_sha_amd64="$(sha256_file "$archive")"
      artifact_size_amd64="$(file_size "$archive")"
      ;;
    arm64)
      artifact_file_arm64="$file_name"
      artifact_sha_arm64="$(sha256_file "$archive")"
      artifact_size_arm64="$(file_size "$archive")"
      ;;
  esac
done

output="${package_dir}/install-manifest.json"
temporary="$(mktemp "${package_dir}/.install-manifest.XXXXXX")"
cleanup() {
  rm -f -- "$temporary"
}
trap cleanup EXIT

{
  printf '{\n'
  printf '  "schemaVersion": 1,\n'
  printf '  "version": "%s",\n' "$version"
  printf '  "platform": "linux",\n'
  printf '  "artifacts": {\n'
  write_artifact() {
    local arch="$1"
    local file_name="$2"
    local sha256="$3"
    local size="$4"
    printf '    "linux-%s": {\n' "$arch"
    printf '      "fileName": "%s",\n' "$file_name"
    printf '      "url": "%s/%s",\n' "$base_url" "$file_name"
    printf '      "checksumUrl": "%s/%s.sha256",\n' "$base_url" "$file_name"
    printf '      "sha256": "%s",\n' "$sha256"
    printf '      "size": %s\n' "$size"
    printf '    }'
  }
  write_artifact amd64 "$artifact_file_amd64" "$artifact_sha_amd64" "$artifact_size_amd64"
  printf ',\n'
  write_artifact arm64 "$artifact_file_arm64" "$artifact_sha_arm64" "$artifact_size_arm64"
  printf '\n'
  printf '  }\n'
  printf '}\n'
} >"$temporary"
chmod 0644 "$temporary"
mv -f -- "$temporary" "$output"
trap - EXIT
echo "Created ${output}"
