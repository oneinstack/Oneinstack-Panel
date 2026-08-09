#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  echo "Usage: $0 PUBLISH_DIR OUTPUT_ARCHIVE" >&2
}

if [[ $# -ne 2 ]]; then
  usage
  exit 2
fi

publish_dir="$1"
output_archive="$2"
expected_repository="${EXPECTED_WEB_REPOSITORY:-oneinstack/Oneinstack-Panel-Web}"
archive="${publish_dir}/app.zip"
checksum="${publish_dir}/app.zip.sha256"
source_file="${publish_dir}/SOURCE_SHA"
build_info="${publish_dir}/build-info.json"

for required in "${archive}" "${checksum}" "${source_file}" "${build_info}"; do
  test -s "${required}" || {
    echo "Missing Web Publish artifact: ${required}" >&2
    exit 1
  }
done

source_sha="$(tr -d '[:space:]' <"${source_file}")"
if [[ ! "${source_sha}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Web Publish SOURCE_SHA is invalid" >&2
  exit 1
fi
metadata_sha="$(jq --exit-status --raw-output '.sourceSha | select(type == "string")' "${build_info}")"
if [[ "${metadata_sha}" != "${source_sha}" ]]; then
  echo "Web Publish provenance does not match SOURCE_SHA" >&2
  exit 1
fi
metadata_repository="$(jq --exit-status --raw-output '.repository | select(type == "string")' "${build_info}")"
if [[ "${metadata_repository}" != "${expected_repository}" ]]; then
  echo "Unexpected Web Publish repository: ${metadata_repository}" >&2
  exit 1
fi
if [[ "$(jq --exit-status --raw-output '.schemaVersion' "${build_info}")" != "1" ]]; then
  echo "Unsupported Web Publish metadata schema" >&2
  exit 1
fi

read -r expected_checksum checksum_name checksum_extra <"${checksum}"
if [[ ! "${expected_checksum}" =~ ^[0-9a-f]{64}$ || "${checksum_name}" != "app.zip" || -n "${checksum_extra:-}" ]]; then
  echo "Web Publish checksum file must contain exactly one app.zip SHA-256 entry" >&2
  exit 1
fi
actual_checksum="$(sha256sum "${archive}" | awk '{print $1}')"
if [[ "${actual_checksum}" != "${expected_checksum}" ]]; then
  echo "Web Publish app.zip checksum mismatch" >&2
  exit 1
fi
unzip -t "${archive}" >/dev/null

has_index=false
while IFS= read -r entry; do
  case "${entry}" in
    index.html) has_index=true ;;
  esac
  case "/${entry}/" in
    *'/../'*|*'/./'*|'//'*)
      echo "Unsafe path in Web Publish archive: ${entry}" >&2
      exit 1
      ;;
  esac
  if [[ "${entry}" == /* || "${entry}" == *'\'* ]]; then
    echo "Unsafe path in Web Publish archive: ${entry}" >&2
    exit 1
  fi
done < <(unzip -Z1 "${archive}")
if [[ "${has_index}" != true ]]; then
  echo "Web Publish archive does not contain index.html" >&2
  exit 1
fi

mkdir -p "$(dirname "${output_archive}")"
install -m 0644 "${archive}" "${output_archive}"
printf '%s\n' "${source_sha}"
