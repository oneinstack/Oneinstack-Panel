#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
software_version="${2:-}"
os_id="${3:-}"
os_version="${4:-}"
architecture="${5:-}"
source_file="${6:-}"
package_version=1.0.7
artifact_name=adminer-6.1.0.php
source_sha256=95bf24b510b41904446f720f4f1212c9e28b1d523f3df44259480d7a12ea181e

usage() {
  printf 'Usage: %s OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_FILE\n' "${BASH_SOURCE[0]}" >&2
  exit 64
}
[[ -n "${output_dir}" && -n "${software_version}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_file}" ]] || usage
[[ "${software_version}" == 6.1.0 && ( "${architecture}" == amd64 || "${architecture}" == arm64 ) ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
[[ "${output_dir}" == /* && "${output_dir}" != */../* && "${output_dir}" != */./* && "${output_dir}" != */.. && "${output_dir}" != */. ]] || usage
output_parent="$(dirname -- "${output_dir}")"
[[ -d "${output_parent}" ]] || { printf 'Bundle output parent does not exist: %s\n' "${output_parent}" >&2; exit 64; }
output_dir="$(cd -- "${output_parent}" && pwd -P)/$(basename -- "${output_dir}")"
case "${output_dir}" in /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*) printf 'refusing unsafe Bundle output: %s\n' "${output_dir}" >&2; exit 64 ;; esac
case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|ol:8|ol:9|ol:10|centos:7|centos:8|centos:9|centos:10|fedora:40|fedora:41|fedora:42|fedora:43|fedora:44|amzn:2023|sles:15|sles:16|opensuse-leap:15.6|opensuse-leap:16.0|opensuse:15.6|opensuse:16.0) ;;
  *) printf 'unsupported Adminer Bundle platform: %s %s\n' "${os_id}" "${os_version}" >&2; exit 64 ;;
esac
[[ -f "${source_file}" && ! -L "${source_file}" ]] || { printf 'Adminer source file is missing or is a symlink\n' >&2; exit 66; }
[[ "$(sha256sum "${source_file}" | awk '{print $1}')" == "${source_sha256}" ]] || { printf 'Adminer source SHA-256 mismatch\n' >&2; exit 65; }
head -c 5 "${source_file}" | grep -q '^<?php' || { printf 'Adminer source is not PHP\n' >&2; exit 65; }

rm -rf -- "${output_dir}"
install -d -m 0750 -- "${output_dir}/scripts" "${output_dir}/artifacts/${architecture}"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
for file in "${script_dir}"/*.sh; do install -m 0755 -- "${file}" "${output_dir}/scripts/${file##*/}"; done
install -m 0644 -- "${source_file}" "${output_dir}/artifacts/${architecture}/${artifact_name}"
bundle_id="adminer-${package_version}-${software_version}-${os_id}-${os_version}-${architecture}"
cat >"${output_dir}/bundle-info" <<EOF
component=adminer
package-version=${package_version}
software-version=${software_version}
os-id=${os_id}
os-version=${os_version}
architecture=${architecture}
bundle-id=${bundle_id}
EOF
chmod 0644 "${output_dir}/bundle-info"
inventory_candidate="$(mktemp)"
trap 'rm -f -- "${inventory_candidate}"' EXIT
(
  cd "${output_dir}"
  find . -type f ! -name files.sha256 -print0 |
    LC_ALL=C sort -z |
    xargs -0 sha256sum >"${inventory_candidate}"
)
mv -f -- "${inventory_candidate}" "${output_dir}/files.sha256"
trap - EXIT
inventory_digest="$(sha256sum "${output_dir}/files.sha256" | awk '{print $1}')"
chmod 0644 "${output_dir}/files.sha256"
printf 'Bundle ID: %s\nInventory SHA-256: %s\nPath: %s\n' "${bundle_id}" "${inventory_digest}" "${output_dir}"
